package main

// 地区筛选：cfdata 只能在扫描时用 trace 返回的 colo（Cloudflare 边缘机房 IATA 码）判定地址归属，
// 因此「按地区优选」＝ 先全量扫描拿到每个地址的 colo，再按地区码过滤，最后只对命中项测速。
// 本文件负责：地区码 -> 机房码集合的映射，以及从 cfdata 的扫描 CSV 中过滤出地址。

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultLocationsURL = "https://www.baipiao.eu.org/cloudflare/locations"

type locationRec struct {
	Iata   string  `json:"iata"`
	Cca2   string  `json:"cca2"`
	Region string  `json:"region"`
	City   string  `json:"city"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

// loadLocations 读取 locations.json：优先用指定路径（cfdata 会下载到工作目录），
// 其次用本工具自己的缓存，都不可用时按上游地址重新下载。
func loadLocations(path, url, cacheDir string) ([]locationRec, string, error) {
	read := func(p string) ([]locationRec, bool) {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, false
		}
		var recs []locationRec
		if err := json.Unmarshal(data, &recs); err != nil || len(recs) == 0 {
			return nil, false
		}
		return recs, true
	}

	if strings.TrimSpace(path) != "" {
		if recs, ok := read(path); ok {
			return recs, path, nil
		}
	}
	if strings.TrimSpace(cacheDir) != "" {
		p := filepath.Join(cacheDir, "locations.json")
		if recs, ok := read(p); ok {
			return recs, p, nil
		}
	}
	if strings.TrimSpace(url) == "" {
		return nil, "", fmt.Errorf("locations.json 不可用且未配置下载地址")
	}

	client := &http.Client{Timeout: 25 * time.Second}
	body, err := fetchURL(client, url)
	if err != nil {
		return nil, "", fmt.Errorf("下载 locations.json 失败: %v", err)
	}
	var recs []locationRec
	if err := json.Unmarshal([]byte(body), &recs); err != nil || len(recs) == 0 {
		return nil, "", fmt.Errorf("解析 locations.json 失败（内容异常）")
	}
	// 下载后优先写回指定路径，cfdata 下次运行会直接复用这份，避免重复下载
	if strings.TrimSpace(path) != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			if err := os.WriteFile(path, []byte(body), 0o644); err == nil {
				return recs, path, nil
			}
		}
	}
	if strings.TrimSpace(cacheDir) != "" {
		if err := os.MkdirAll(cacheDir, 0o755); err == nil {
			p := filepath.Join(cacheDir, "locations.json")
			if err := os.WriteFile(p, []byte(body), 0o644); err == nil {
				return recs, p, nil
			}
		}
	}
	return recs, url, nil
}

func splitRegionCodes(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case ',', '，', ';', '；', '/', '|', ' ', '\t', '\n', '\r':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToUpper(strings.TrimSpace(f))
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// resolveRegions 把用户给的地区码展开成机房码集合：
//   - 2 位国家码（HK、JP、US）→ 该国家下所有机房
//   - 3 位机房码（HKG、NRT、LAX）→ 原样使用
func resolveRegions(codes string, recs []locationRec) (map[string]string, []string, error) {
	iataSet := make(map[string]string, len(recs))   // 机房码 -> 国家码
	cca2Map := make(map[string][]string, len(recs)) // 国家码 -> 机房码列表
	for _, r := range recs {
		iata := strings.ToUpper(strings.TrimSpace(r.Iata))
		if iata == "" {
			continue
		}
		cca2 := strings.ToUpper(strings.TrimSpace(r.Cca2))
		iataSet[iata] = cca2
		if cca2 != "" {
			cca2Map[cca2] = append(cca2Map[cca2], iata)
		}
	}

	targets := make(map[string]string, 16)
	var notes []string
	for _, code := range splitRegionCodes(codes) {
		switch len(code) {
		case 3:
			targets[code] = iataSet[code]
			if _, ok := iataSet[code]; !ok {
				notes = append(notes, fmt.Sprintf("机房码 %s 不在 locations.json 中，按原样匹配（可能是新增机房）", code))
			}
		case 2:
			list := cca2Map[code]
			if len(list) == 0 {
				return nil, nil, fmt.Errorf("地区码 %s 无法识别；可改用 3 位机房码（如 HKG、NRT、LAX）", code)
			}
			sorted := append([]string(nil), list...)
			sort.Strings(sorted)
			for _, iata := range sorted {
				targets[iata] = code
			}
			notes = append(notes, fmt.Sprintf("国家码 %s -> 机房 %s（%d 个）", code, strings.Join(sorted, "/"), len(sorted)))
		default:
			return nil, nil, fmt.Errorf("地区码 %q 格式不对：应为 2 位国家码（HK）或 3 位机房码（HKG）", code)
		}
	}
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("没有解析出任何地区码")
	}
	return targets, notes, nil
}

func mustResolveRegions(codes, locPath, locURL, cacheDir string) (map[string]string, []string) {
	recs, src, err := loadLocations(locPath, locURL, cacheDir)
	if err != nil {
		fatal("%v", err)
	}
	fmt.Fprintf(os.Stderr, "[cfdata-extract] 位置信息: %s（%d 个机房）\n", src, len(recs))
	targets, notes, err := resolveRegions(codes, recs)
	if err != nil {
		fatal("%v", err)
	}
	return targets, notes
}

func normalizeHeader(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "\u3000", "")
	return strings.ToLower(strings.TrimSpace(s))
}

func findColumn(header []string, names ...string) int {
	for _, want := range names {
		w := normalizeHeader(want)
		for i, h := range header {
			if normalizeHeader(h) == w {
				return i
			}
		}
	}
	return -1
}

// runFilterMode 从 cfdata 导出的扫描 CSV 中按机房与出口归属过滤地址，写出 cfdata 可直接消费的清单。
// 两个条件可单独使用，也可同时使用（取交集）：只给 region 时按机房筛，只给 excludeLoc 时按出口地区剔。
func runFilterMode(csvPath, region, excludeLoc, locPath, locURL, cacheDir, outPath string) {
	hasRegion := strings.TrimSpace(region) != ""
	excludeSet, excludeList, err := parseLocCodes(excludeLoc)
	if err != nil {
		fatal("%v", err)
	}
	if !hasRegion && len(excludeSet) == 0 {
		fatal("过滤模式需要 -region 或 -exclude-loc（至少给一个）")
	}

	targets := make(map[string]string, 16)
	if hasRegion {
		var notes []string
		targets, notes = mustResolveRegions(region, locPath, locURL, cacheDir)
		for _, n := range notes {
			fmt.Fprintf(os.Stderr, "[cfdata-extract] %s\n", n)
		}
	}
	if len(excludeList) > 0 {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 排除出口地区（loc）: %s\n", strings.Join(excludeList, ","))
	}

	f, err := os.Open(csvPath)
	if err != nil {
		fatal("打开扫描结果 %s 失败: %v", csvPath, err)
	}
	defer f.Close()

	reader := csv.NewReader(bufio.NewReaderSize(f, 64*1024))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		fatal("读取扫描结果表头失败: %v", err)
	}
	ipCol := findColumn(header, "IP地址", "IP", "ip")
	portCol := findColumn(header, "端口号", "端口", "port")
	dcCol := -1
	if hasRegion {
		dcCol = findColumn(header, "数据中心", "colo", "dc", "datacenter")
	}
	locCol := -1
	if len(excludeSet) > 0 {
		locCol = findColumn(header, "源IP位置", "loc", "location", "country", "国家码", "出口地区")
	}
	if ipCol < 0 || portCol < 0 {
		fatal("扫描结果缺少必要列（IP地址/端口号），当前表头: %s", strings.Join(header, ","))
	}
	if hasRegion && dcCol < 0 {
		fatal("扫描结果缺少「数据中心」列，无法按地区筛选，当前表头: %s", strings.Join(header, ","))
	}
	if len(excludeSet) > 0 && locCol < 0 {
		fatal("扫描结果缺少「源IP位置」列，无法按出口地区排除，当前表头: %s", strings.Join(header, ","))
	}

	var (
		rows     int
		kept     []entry
		seen     = make(map[string]struct{}, 512)
		hit      = make(map[string]int, len(targets))
		all      = make(map[string]int, 64) // 全量扫描里的机房分布，无命中时用于提示
		unknown  int                        // 缺机房信息的行数
		excluded = make(map[string]int, 8)  // 按出口地区统计的排除条数
		locAll   = make(map[string]int, 64) // 全量扫描里的出口地区分布
		locEmpty int                        // 缺出口地区信息的行数
	)
	for {
		rec, err := reader.Read()
		if err != nil {
			break
		}
		rows++
		if len(rec) <= ipCol || len(rec) <= portCol {
			continue
		}
		host := strings.TrimSpace(rec[ipCol])
		portStr := strings.TrimSpace(rec[portCol])
		colo := ""
		if hasRegion {
			colo = strings.ToUpper(strings.TrimSpace(rec[dcCol]))
			if colo == "" {
				unknown++
				continue
			}
			all[colo]++
			if _, ok := targets[colo]; !ok {
				continue
			}
		}
		if len(excludeSet) > 0 {
			loc := ""
			if len(rec) > locCol {
				loc = strings.ToUpper(strings.TrimSpace(rec[locCol]))
			}
			if loc == "" {
				locEmpty++
			} else {
				locAll[loc]++
				if _, ok := excludeSet[loc]; ok {
					excluded[loc]++
					continue
				}
			}
		}
		port, ok := parsePort(portStr)
		if !ok || host == "" {
			continue
		}
		key := host + "|" + portStr
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if hasRegion {
			hit[colo]++
		}
		kept = append(kept, entry{host: host, port: port})
	}

	if err := writeList(outPath, kept); err != nil {
		fatal("写出 %s 失败: %v", outPath, err)
	}

	dist := make([]string, 0, len(hit))
	for colo, n := range hit {
		dist = append(dist, fmt.Sprintf("%s=%d", colo, n))
	}
	sort.Strings(dist)
	fmt.Fprintf(os.Stderr, "[cfdata-extract] 扫描结果 %d 行，命中 %d 条 -> %s\n", rows, len(kept), outPath)
	if len(dist) > 0 {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 机房分布: %s\n", strings.Join(dist, " "))
	}
	if len(excluded) > 0 {
		parts := make([]string, 0, len(excluded))
		total := 0
		for loc, n := range excluded {
			parts = append(parts, fmt.Sprintf("%s=%d", loc, n))
			total += n
		}
		sort.Strings(parts)
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 按出口地区排除 %d 条（%s）\n", total, strings.Join(parts, " "))
	}
	if len(locAll) > 0 && len(kept) == 0 {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 出口地区分布: %s\n", formatCounter(locAll, 12))
	}
	if locEmpty > 0 {
		warn("有 %d 行缺少出口地区信息（loc 为空），无法判定，已保留", locEmpty)
	}
	if unknown > 0 {
		warn("有 %d 行缺少机房信息（colo 为空），已跳过", unknown)
	}
	if len(kept) == 0 && hasRegion && len(all) > 0 {
		warn("没有地址落在筛选的机房内；本次扫描实际出现的机房 Top（可作为地区码参考）: %s", formatCounter(all, 12))
	}
}

// formatCounter 把计数 map 整理成 "K(n) K(n)" 的 Top-N 串：次数降序，同次数按名称升序。
func formatCounter(m map[string]int, top int) string {
	type kv struct {
		k string
		n int
	}
	list := make([]kv, 0, len(m))
	for k, n := range m {
		list = append(list, kv{k, n})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].k < list[j].k
	})
	if top > 0 && len(list) > top {
		list = list[:top]
	}
	parts := make([]string, 0, len(list))
	for _, it := range list {
		parts = append(parts, fmt.Sprintf("%s(%d)", it.k, it.n))
	}
	return strings.Join(parts, " ")
}
