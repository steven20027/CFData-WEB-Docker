package main

// 出口地区（loc）排除
//
// cfdata 的非标模式只能用机房（colo，参数 -nsbdc）过滤，没有按出口归属地过滤的参数，
// 而 loc 在扫描阶段的 trace 里就已经拿到了（tasks_nsb.go: locCode := trace["loc"]），
// 测速发生在其后。所以「先排除某个 loc 的地址再测速」只能由本工具在
// 「阶段 1 扫描」与「阶段 2 测速」之间完成：读扫描 CSV 过滤出待测地址清单。

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// parseLocCodes 解析 -exclude-loc 的取值：逗号/分号/空格分隔的 2 位国家码（如 CN、CN,RU）。
// 返回集合、规范化后的列表（去重、保序、大写）。
func parseLocCodes(s string) (map[string]struct{}, []string, error) {
	set := make(map[string]struct{}, 4)
	list := make([]string, 0, 4)
	for _, code := range splitRegionCodes(s) {
		if len(code) != 2 {
			return nil, nil, fmt.Errorf("出口地区码 %q 格式不对：loc 是 2 位国家码（如 CN）；要按机房筛选请用 -region", code)
		}
		for _, r := range code {
			if r < 'A' || r > 'Z' {
				return nil, nil, fmt.Errorf("出口地区码 %q 不是 2 位字母国家码（如 CN）", code)
			}
		}
		if _, ok := set[code]; ok {
			continue
		}
		set[code] = struct{}{}
		list = append(list, code)
	}
	return set, list, nil
}

// parseDCCodes 解析 -recheck-dc 的取值：逗号/分号/空格分隔的 3 位机房码（如 NRT、NRT,KIX）。
// 返回集合、规范化后的列表（去重、保序、大写）。
func parseDCCodes(s string) (map[string]struct{}, []string, error) {
	set := make(map[string]struct{}, 4)
	list := make([]string, 0, 4)
	for _, code := range splitRegionCodes(s) {
		if len(code) != 3 {
			return nil, nil, fmt.Errorf("机房码 %q 格式不对：这里要 3 位 IATA 机房码（如 NRT），国家码请用 -exclude-loc", code)
		}
		for _, r := range code {
			if r < 'A' || r > 'Z' {
				return nil, nil, fmt.Errorf("机房码 %q 不是 3 位字母码（如 NRT）", code)
			}
		}
		if _, ok := set[code]; ok {
			continue
		}
		set[code] = struct{}{}
		list = append(list, code)
	}
	return set, list, nil
}

// recheckDrop 记录一条结果被终检剔除的原因。
type recheckDrop struct {
	kind byte   // 'l' = 出口地区命中排除清单，'d' = 机房不在允许清单
	code string // 命中的 loc，或阶段 2 实际落到的机房码
}

// runRecheckMode 终检：阶段 2 会重新握手，机房（colo）与出口 IP 都可能与阶段 1 不一致，
// 于是用阶段 2 的完整扫描 CSV 复核结果 txt：
//   - -exclude-loc 命中 → 剔除，保证输出里没有这些出口地区
//   - -recheck-dc 之外的机房 → 剔除，保证 -region 真的生效，而不是阶段 1 命中、阶段 2 漂走
//
// 剔除条数打到标准输出（供入口脚本记录），明细走标准错误。
func runRecheckMode(csvPath, txtPath, excludeLoc, recheckDC string) {
	excludeSet, excludeList, err := parseLocCodes(excludeLoc)
	if err != nil {
		fatal("%v", err)
	}
	dcSet, dcList, err := parseDCCodes(recheckDC)
	if err != nil {
		fatal("%v", err)
	}
	if len(excludeSet) == 0 && len(dcSet) == 0 {
		fatal("终检模式必须指定 -exclude-loc 或 -recheck-dc")
	}
	if strings.TrimSpace(txtPath) == "" {
		fatal("终检模式必须指定 -recheck-txt")
	}

	bad, scanRows, err := collectBad(csvPath, excludeSet, dcSet)
	if err != nil {
		fatal("%v", err)
	}

	data, err := os.ReadFile(txtPath)
	if err != nil {
		fatal("读取结果 %s 失败: %v", txtPath, err)
	}

	var (
		kept    = make([]string, 0, 32)
		dropped int
		byLoc   = make(map[string]int, 4)
		byDC    = make(map[string]int, 4)
	)
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if host, port, ok := splitIPPort(trimmed); ok {
			if hit, ok := bad[ipportKey(host, port)]; ok {
				dropped++
				if hit.kind == 'd' {
					byDC[hit.code]++
				} else {
					byLoc[hit.code]++
				}
				continue
			}
		}
		kept = append(kept, trimmed)
	}

	if dropped > 0 {
		if err := writeLines(txtPath, kept); err != nil {
			fatal("写回 %s 失败: %v", txtPath, err)
		}
	}

	scope := describeRecheckScope(excludeList, dcList)
	var parts []string
	if len(byLoc) > 0 {
		parts = append(parts, "出口地区 "+formatCounter(byLoc, 0))
	}
	if len(byDC) > 0 {
		parts = append(parts, "机房 "+formatCounter(byDC, 0))
	}
	if len(parts) == 0 {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 终检: 扫描结果 %d 行，%s 均无偏差\n", scanRows, scope)
	} else {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 终检: 扫描结果 %d 行，从结果中剔除 %d 条（%s）\n",
			scanRows, dropped, strings.Join(parts, "；"))
	}
	if len(kept) == 0 && dropped > 0 {
		warn("终检后结果为空：没有一条结果符合 %s，请确认机房码/地区码是否写对", scope)
	}
	fmt.Println(dropped)
}

// describeRecheckScope 把终检的两个维度拼成一句人话，用于日志。
func describeRecheckScope(excludeList, dcList []string) string {
	var parts []string
	if len(excludeList) > 0 {
		parts = append(parts, "排除出口地区 "+strings.Join(excludeList, "/"))
	}
	if len(dcList) > 0 {
		parts = append(parts, "限定机房 "+strings.Join(dcList, "/"))
	}
	return strings.Join(parts, "、")
}

// collectBad 从扫描 CSV 中挑出需要从结果里剔除的地址，返回 "ip|port" -> 剔除原因。
// 出口地区命中排除清单的优先记录原因，其次才是机房不在允许清单。
func collectBad(csvPath string, excludeSet, dcSet map[string]struct{}) (map[string]recheckDrop, int, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, 0, fmt.Errorf("打开扫描结果 %s 失败: %v", csvPath, err)
	}
	defer f.Close()

	reader := csv.NewReader(bufio.NewReaderSize(f, 64*1024))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	header, err := reader.Read()
	if err != nil {
		return nil, 0, fmt.Errorf("读取扫描结果表头失败: %v", err)
	}
	ipCol := findColumn(header, "IP地址", "IP", "ip")
	portCol := findColumn(header, "端口号", "端口", "port")
	locCol := findColumn(header, "源IP位置", "loc", "location", "country", "国家码", "出口地区")
	dcCol := findColumn(header, "数据中心", "colo", "dc", "datacenter")
	if ipCol < 0 || portCol < 0 {
		return nil, 0, fmt.Errorf("扫描结果缺少必要列（IP地址/端口号），当前表头: %s", strings.Join(header, ","))
	}
	if len(excludeSet) > 0 && locCol < 0 {
		return nil, 0, fmt.Errorf("扫描结果缺少「源IP位置」列，无法按出口地区复核，当前表头: %s", strings.Join(header, ","))
	}
	if len(dcSet) > 0 && dcCol < 0 {
		return nil, 0, fmt.Errorf("扫描结果缺少「数据中心」列，无法按机房复核，当前表头: %s", strings.Join(header, ","))
	}

	bad := make(map[string]recheckDrop, 64)
	rows := 0
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
		port := strings.TrimSpace(rec[portCol])
		if host == "" || port == "" {
			continue
		}
		key := ipportKey(host, port)

		if len(excludeSet) > 0 && len(rec) > locCol {
			loc := strings.ToUpper(strings.TrimSpace(rec[locCol]))
			if _, hit := excludeSet[loc]; hit {
				bad[key] = recheckDrop{'l', loc}
				continue
			}
		}
		// 机房不在允许清单里：阶段 2 重新握手后可能落到别的机房（实测 NRT→MAA），
		// 这时结果行里的 dc 已经不是用户要的那个机房，必须剔掉。
		// 机房为空的行不动，那是数据缺失，不是漂移。
		if len(dcSet) > 0 && len(rec) > dcCol {
			dc := strings.ToUpper(strings.TrimSpace(rec[dcCol]))
			if dc != "" {
				if _, ok := dcSet[dc]; !ok {
					bad[key] = recheckDrop{'d', dc}
				}
			}
		}
	}
	return bad, rows, nil
}

// ipportKey 统一 ip:port 的比对键：去方括号、端口归一化成十进制。
func ipportKey(host, port string) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if p, ok := parsePort(port); ok {
		return host + "|" + strconv.Itoa(p)
	}
	return host + "|" + strings.TrimSpace(port)
}

// splitIPPort 拆出结果行开头的 ip 与端口。
// 兼容 "1.2.3.4:443#..."、"[2001:db8::1]:443#..." 与不带备注的裸 "ip:port"。
func splitIPPort(line string) (string, string, bool) {
	s := line
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	if strings.HasPrefix(s, "[") {
		j := strings.Index(s, "]")
		if j < 0 {
			return "", "", false
		}
		host := s[1:j]
		rest := strings.TrimPrefix(s[j+1:], ":")
		if host == "" || rest == "" {
			return "", "", false
		}
		return host, rest, true
	}
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// writeLines 用临时文件 + rename 覆盖写，避免中途失败留下半个结果文件。
func writeLines(path string, lines []string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".recheck-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	w := bufio.NewWriterSize(tmp, 64*1024)
	for _, l := range lines {
		if _, err := w.WriteString(l + "\n"); err != nil {
			tmp.Close()
			os.Remove(name)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
