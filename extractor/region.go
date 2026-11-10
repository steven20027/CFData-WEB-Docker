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

// runFilterMode 从 cfdata 导出的扫描 CSV 中按地区过滤地址，写出 cfdata 可直接消费的清单。
func runFilterMode(csvPath, region, locPath, locURL, cacheDir, outPath string) {
	if strings.TrimSpace(region) == "" {
		fatal("过滤模式必须指定 -region")
	}
	targets, notes := mustResolveRegions(region, locPath, locURL, cacheDir)
	for _, n := range notes {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] %s\n", n)
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
	dcCol := findColumn(header, "数据中心", "colo", "dc", "datacenter")
	if ipCol < 0 || portCol < 0 || dcCol < 0 {
		fatal("扫描结果缺少必要列（IP地址/端口号/数据中心），当前表头: %s", strings.Join(header, ","))
	}

	var (
		rows    int
		kept    []entry
		seen    = make(map[string]struct{}, 512)
		hit     = make(map[string]int, len(targets))
		all     = make(map[string]int, 64) // 全量扫描里的机房分布，无命中时用于提示
		unknown int
	)
	for {
		rec, err := reader.Read()
		if err != nil {
			break
		}
		rows++
		if len(rec) <= ipCol || len(rec) <= portCol || len(rec) <= dcCol {
			continue
		}
		colo := strings.ToUpper(strings.TrimSpace(rec[dcCol]))
		host := strings.TrimSpace(rec[ipCol])
		portStr := strings.TrimSpace(rec[portCol])
		if colo == "" {
			unknown++
			continue
		}
		all[colo]++
		if _, ok := targets[colo]; !ok {
			continue
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
		hit[colo]++
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
	fmt.Fprintf(os.Stderr, "[cfdata-extract] 扫描结果 %d 行，命中地区 %d 条 -> %s\n", rows, len(kept), outPath)
	if len(dist) > 0 {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 机房分布: %s\n", strings.Join(dist, " "))
	}
	if unknown > 0 {
		warn("有 %d 行缺少机房信息（colo 为空），已跳过", unknown)
	}
	if len(kept) == 0 {
		warn("目标地区没有任何可用地址；本次扫描实际出现的机房如下（可作为地区码参考）:")
		type kv struct {
			k string
			n int
		}
		list := make([]kv, 0, len(all))
		for k, n := range all {
			list = append(list, kv{k, n})
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].n != list[j].n {
				return list[i].n > list[j].n
			}
			return list[i].k < list[j].k
		})
		if len(list) > 12 {
			list = list[:12]
		}
		parts := make([]string, 0, len(list))
		for _, it := range list {
			parts = append(parts, fmt.Sprintf("%s(%d)", it.k, it.n))
		}
		if len(parts) > 0 {
			fmt.Fprintf(os.Stderr, "[cfdata-extract] 机房分布 Top: %s\n", strings.Join(parts, " "))
		}
	}
}
