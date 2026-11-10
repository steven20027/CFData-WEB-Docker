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

// runRecheckMode 终检：阶段 2 会重新握手，出口 IP 有概率落到另一个地区，
// 于是用阶段 2 的完整扫描 CSV 复核结果 txt，把仍命中排除清单的行剔掉。
// 剔除条数打到标准输出（供入口脚本记录），明细走标准错误。
func runRecheckMode(csvPath, txtPath, excludeLoc string) {
	excludeSet, excludeList, err := parseLocCodes(excludeLoc)
	if err != nil {
		fatal("%v", err)
	}
	if len(excludeSet) == 0 {
		fatal("终检模式必须指定 -exclude-loc")
	}
	if strings.TrimSpace(txtPath) == "" {
		fatal("终检模式必须指定 -recheck-txt")
	}

	bad, scanRows, err := collectExcluded(csvPath, excludeSet)
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
		dist    = make(map[string]int, 4)
	)
	for _, line := range strings.Split(strings.TrimPrefix(string(data), "\ufeff"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if host, port, ok := splitIPPort(trimmed); ok {
			if loc, hit := bad[ipportKey(host, port)]; hit {
				dropped++
				dist[loc]++
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

	detail := formatCounter(dist, 0)
	if detail == "" {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 终检: 扫描结果 %d 行，未发现命中 %s 的地址\n",
			scanRows, strings.Join(excludeList, ","))
	} else {
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 终检: 扫描结果 %d 行，从结果中剔除 %d 条（%s）\n",
			scanRows, dropped, detail)
	}
	if len(kept) == 0 && dropped > 0 {
		warn("终检后结果为空：这批地址的出口地区全部命中排除清单 %s", strings.Join(excludeList, ","))
	}
	fmt.Println(dropped)
}

// collectExcluded 从扫描 CSV 中挑出出口地区命中排除清单的地址，返回 "ip|port" -> loc。
func collectExcluded(csvPath string, excludeSet map[string]struct{}) (map[string]string, int, error) {
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
	if ipCol < 0 || portCol < 0 || locCol < 0 {
		return nil, 0, fmt.Errorf("扫描结果缺少必要列（IP地址/端口号/源IP位置），当前表头: %s", strings.Join(header, ","))
	}

	bad := make(map[string]string, 64)
	rows := 0
	for {
		rec, err := reader.Read()
		if err != nil {
			break
		}
		rows++
		if len(rec) <= ipCol || len(rec) <= portCol || len(rec) <= locCol {
			continue
		}
		loc := strings.ToUpper(strings.TrimSpace(rec[locCol]))
		if loc == "" {
			continue
		}
		if _, ok := excludeSet[loc]; !ok {
			continue
		}
		host := strings.TrimSpace(rec[ipCol])
		port := strings.TrimSpace(rec[portCol])
		if host == "" || port == "" {
			continue
		}
		bad[ipportKey(host, port)] = loc
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
