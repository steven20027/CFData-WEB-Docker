// cfdata-extract 从「地址来源清单」或 bestcf.pages.dev 的优选清单中抓取文本，
// 提取所有 非标地址（ip/域名:端口），把网段还原成具体 IP，去重后写出
// 可用 cfdata 直接消费的 a.txt（每行 "host port"）。
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxBodyBytes   = 8 << 20 // 单个来源最大读取 8MB
	defaultWorkers = 10      // 并发抓取清单数
)

var (
	// [2001:db8::1]:443
	reV6Port = regexp.MustCompile(`\[([0-9A-Fa-f:.]{2,45})\]:(\d{1,5})`)
	// 104.16.0.0/24 或 104.16.0.0/24:443
	reV4CIDR = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3})/(\d{1,2})(?::(\d{1,5}))?`)
	// 2001:db8::/64
	reV6CIDR = regexp.MustCompile(`\b([0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7})/(\d{1,3})\b`)
	// 104.16.1.1:443
	reV4Port = regexp.MustCompile(`\b(\d{1,3}(?:\.\d{1,3}){3}):(\d{1,5})\b`)
	// 域名:端口
	reHostPort = regexp.MustCompile(`(?i)\b((?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}):(\d{1,5})\b`)

	// bestcf 页面解析
	reHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reCopySingle  = regexp.MustCompile(`copy\(\s*this\s*,\s*'([^']+)'\s*\)`)
	reCopyDouble  = regexp.MustCompile(`copy\(\s*this\s*,\s*"([^"]+)"\s*\)`)
	reAnyURL      = regexp.MustCompile(`https?://[^\s"'<>\\]+`)
)

// 非公网网段（这些地址扫描没有意义，来源里的脏数据也主要落在这里）
var reservedV4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("192.88.99.0/24"),
}

type entry struct {
	host string
	port int
}

type stats struct {
	sourcesTotal int
	sourcesOK    int
	sourcesFail  int
	direct       int
	cidrCount    int
	cidrIPs      int
	dupes        int
	filtered     int
	skipped      []string
}

type cacheMeta struct {
	URL       string `json:"url"`
	FetchedAt int64  `json:"fetched_at"`
	URLCount  int    `json:"url_count"`
	Bytes     int    `json:"bytes"`
}

func main() {
	sourcesPath := flag.String("sources", "", "地址来源清单，每行一个 URL 或本地文件路径")
	outPath := flag.String("out", "/tmp/a.txt", "输出文件，每行一个 host port")
	maxCIDR := flag.Int("max-cidr", 4096, "单个网段最大展开 IP 数，0 表示不限制")
	defPort := flag.Int("port", 443, "地址没有端口时使用的端口")
	timeout := flag.Duration("timeout", 20*time.Second, "单个来源的抓取超时")

	bestcf := flag.Bool("bestcf", false, "抓取 bestcf.pages.dev 页面「优选域名 / 优选IP」两部分的全部清单作为默认输入")
	bestcfURL := flag.String("bestcf-url", "https://bestcf.pages.dev/", "bestcf 页面地址")
	bestcfTTL := flag.Duration("bestcf-ttl", 3*time.Hour, "bestcf 缓存有效期，0 表示每次都刷新")
	cacheDir := flag.String("cache-dir", "/data/.cache/bestcf", "bestcf 缓存目录")
	workers := flag.Int("workers", defaultWorkers, "并发抓取清单数量")

	filterCSV := flag.String("filter-csv", "", "扫描结果 CSV；指定后进入地区过滤模式，按 -region 过滤后输出地址清单")
	regionCodes := flag.String("region", "", "地区码，逗号分隔：2 位国家码（HK/JP/US）或 3 位机房码（HKG/NRT/LAX）")
	locationsPath := flag.String("locations", "/data/locations.json", "locations.json 路径，用于国家码到机房码的映射")
	locationsURL := flag.String("locations-url", defaultLocationsURL, "locations.json 缺失时的下载地址")
	flag.Parse()

	if *defPort < 1 || *defPort > 65535 {
		fatal("端口不合法: %d", *defPort)
	}
	if *workers < 1 {
		*workers = 1
	}

	// 地区过滤模式：读 cfdata 扫描得到的 CSV（含「数据中心」列），按地区码过滤出地址
	if strings.TrimSpace(*filterCSV) != "" {
		runFilterMode(*filterCSV, *regionCodes, *locationsPath, *locationsURL, *cacheDir, *outPath)
		return
	}
	// 只给 -region：打印地区码对应的机房列表，便于确认筛选范围
	if strings.TrimSpace(*regionCodes) != "" {
		targets, notes := mustResolveRegions(*regionCodes, *locationsPath, *locationsURL, *cacheDir)
		for _, n := range notes {
			fmt.Fprintln(os.Stderr, "[cfdata-extract] "+n)
		}
		out := make([]string, 0, len(targets))
		for colo := range targets {
			out = append(out, colo)
		}
		fmt.Println(strings.Join(out, " "))
		return
	}

	client := &http.Client{
		Timeout: *timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return fmt.Errorf("重定向次数过多")
			}
			return nil
		},
	}

	st := &stats{}
	seen := make(map[string]struct{}, 4096)
	ordered := make([]entry, 0, 4096)

	add := func(host string, port int, viaCIDR bool) {
		host = strings.Trim(strings.TrimSpace(host), "[]")
		if !isValidHost(host) || port < 1 || port > 65535 {
			return
		}
		if ip, err := netip.ParseAddr(host); err == nil && !isPublicIP(ip) {
			st.filtered++
			return
		}
		key := host + "|" + strconv.Itoa(port)
		if _, ok := seen[key]; ok {
			st.dupes++
			return
		}
		seen[key] = struct{}{}
		ordered = append(ordered, entry{host: host, port: port})
		if !viaCIDR {
			st.direct++
		}
	}

	seenCIDR := make(map[string]struct{}, 64)
	expand := func(raw, src string) {
		if _, ok := seenCIDR[raw]; ok {
			return
		}
		seenCIDR[raw] = struct{}{}
		n, err := expandCIDR(raw, *defPort, *maxCIDR, add)
		switch {
		case err == errTooLarge:
			st.skipped = append(st.skipped, fmt.Sprintf("%s（来自 %s，超过上限 %d）", raw, src, *maxCIDR))
		case err != nil:
			st.skipped = append(st.skipped, fmt.Sprintf("%s（来自 %s，%v）", raw, src, err))
		case n > 0:
			st.cidrCount++
			st.cidrIPs += n
		}
	}

	addSource := func(name, content string) {
		text := normalizeContent(content)
		parseText(text, name, *defPort, add, expand)
		fmt.Fprintf(os.Stderr, "[cfdata-extract] 已解析 %s（%d 字符）\n", name, len(text))
	}

	// ---------- 1. bestcf 默认输入 ----------
	if *bestcf {
		text, fetchedAt, fresh, cached := loadBestCFCache(*cacheDir, *bestcfTTL)
		if cached && fresh {
			fmt.Fprintf(os.Stderr, "[cfdata-extract] 命中 bestcf 缓存 %s（生成于 %s 前），跳过网络拉取\n",
				*cacheDir, humanDuration(time.Since(fetchedAt)))
			st.sourcesTotal++
			st.sourcesOK++
			addSource(*bestcfURL+" [cache]", text)
		} else {
			urls, err := bestcfListURLs(client, *bestcfURL)
			if err != nil {
				if cached {
					warn("拉取 bestcf 页面失败（%v），回退到过期缓存（%s 前）", err, humanDuration(time.Since(fetchedAt)))
					st.sourcesTotal++
					st.sourcesOK++
					addSource(*bestcfURL+" [stale cache]", text)
				} else {
					fatal("拉取 bestcf 页面失败: %v", err)
				}
			} else {
				st.sourcesTotal += len(urls)
				fmt.Fprintf(os.Stderr, "[cfdata-extract] bestcf 页面共 %d 个清单，开始并发抓取（%d 线程）\n", len(urls), *workers)
				texts, okN, failN := fetchMany(client, urls, *workers)
				if okN == 0 {
					if cached {
						warn("bestcf 清单全部抓取失败，回退到过期缓存")
						st.sourcesTotal++
						st.sourcesOK++
						addSource(*bestcfURL+" [stale cache]", text)
					} else {
						fatal("bestcf 清单全部抓取失败，请检查网络")
					}
				} else {
					merged := strings.Join(texts, "\n")
					st.sourcesOK += okN
					st.sourcesFail += failN
					addSource(fmt.Sprintf("%s（%d 个清单）", *bestcfURL, okN), merged)
					if err := writeBestCFCache(*cacheDir, *bestcfURL, merged, len(urls)); err != nil {
						warn("写入 bestcf 缓存失败: %v", err)
					} else {
						fmt.Fprintf(os.Stderr, "[cfdata-extract] bestcf 缓存已更新: %s\n", *cacheDir)
					}
					if failN > 0 {
						warn("bestcf 有 %d 个清单抓取失败（不影响其余清单）", failN)
					}
				}
			}
		}
	}

	// ---------- 2. 附加来源清单 ----------
	if strings.TrimSpace(*sourcesPath) != "" {
		sources, err := readSources(*sourcesPath)
		if err != nil {
			if *bestcf {
				warn("附加来源清单不可用（%v），仅使用 bestcf 输入", err)
			} else {
				fatal("%v", err)
			}
		}
		baseDir := filepath.Dir(*sourcesPath)
		st.sourcesTotal += len(sources)
		for _, src := range sources {
			content, err := loadSource(client, src, baseDir)
			if err != nil {
				st.sourcesFail++
				warn("来源失败 %s: %v", src, err)
				continue
			}
			addSource(src, content)
		}
	}

	if !*bestcf && strings.TrimSpace(*sourcesPath) == "" {
		fatal("需要指定 -sources，或使用 -bestcf 从 bestcf.pages.dev 获取默认输入")
	}
	if len(ordered) == 0 {
		fatal("没有提取到任何地址，请检查来源与网络连通性")
	}

	if err := writeList(*outPath, ordered); err != nil {
		fatal("写出 %s 失败: %v", *outPath, err)
	}

	fmt.Fprintf(os.Stderr, "[cfdata-extract] 来源 %d 个：成功 %d，失败 %d\n", st.sourcesTotal, st.sourcesOK, st.sourcesFail)
	fmt.Fprintf(os.Stderr, "[cfdata-extract] 直连地址 %d 条，网段 %d 个（展开 %d 条），去重 %d 条，过滤非公网 %d 条\n",
		st.direct, st.cidrCount, st.cidrIPs, st.dupes, st.filtered)
	for _, s := range st.skipped {
		warn("跳过 %s", s)
	}
	fmt.Fprintf(os.Stderr, "[cfdata-extract] 输出 %d 条 -> %s\n", len(ordered), *outPath)
}

// ---------- bestcf 页面 / 缓存 ----------

func bestcfListURLs(client *http.Client, pageURL string) ([]string, error) {
	page, err := fetchURL(client, pageURL)
	if err != nil {
		return nil, err
	}
	return parseBestCFPage(page)
}

func parseBestCFPage(page string) ([]string, error) {
	page = reHTMLComment.ReplaceAllString(page, "")
	var out []string
	seen := make(map[string]bool, 128)
	for _, id := range []string{"yxym", "yxip"} {
		block, ok := sectionByID(page, id)
		if !ok {
			continue
		}
		var found []string
		for _, m := range reCopySingle.FindAllStringSubmatch(block, -1) {
			found = append(found, m[1])
		}
		for _, m := range reCopyDouble.FindAllStringSubmatch(block, -1) {
			found = append(found, m[1])
		}
		if len(found) == 0 {
			found = reAnyURL.FindAllString(block, -1)
		}
		for _, u := range found {
			u = strings.TrimSpace(u)
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				continue
			}
			if seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("页面中未解析到任何清单链接（可能页面结构已变化）")
	}
	return out, nil
}

func sectionByID(page, id string) (string, bool) {
	marker := fmt.Sprintf(`id="%s"`, id)
	i := strings.Index(page, marker)
	if i < 0 {
		return "", false
	}
	rest := page[i:]
	j := strings.Index(rest, "</section>")
	if j < 0 {
		return rest, true
	}
	return rest[:j], true
}

func fetchURL(client *http.Client, rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; cfdata-extract/1.0)")
	req.Header.Set("Accept", "text/plain,text/*,*/*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// fetchMany 并发抓取，返回与 urls 一一对应的内容（失败为空串）。
func fetchMany(client *http.Client, urls []string, workers int) ([]string, int, int) {
	out := make([]string, len(urls))
	jobs := make(chan int)
	var mu sync.Mutex
	var wg sync.WaitGroup
	okN, failN := 0, 0

	for i := 0; i < workers && i < len(urls); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				text, err := fetchURL(client, urls[idx])
				mu.Lock()
				if err != nil {
					failN++
					warn("清单失败 %s: %v", urls[idx], err)
				} else {
					out[idx] = text
					okN++
				}
				mu.Unlock()
			}
		}()
	}
	for i := range urls {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out, okN, failN
}

func loadBestCFCache(dir string, ttl time.Duration) (string, time.Time, bool, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "addresses.txt"))
	if err != nil || len(data) == 0 {
		return "", time.Time{}, false, false
	}
	metaBytes, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return string(data), time.Time{}, false, true
	}
	var meta cacheMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil || meta.FetchedAt <= 0 {
		return string(data), time.Time{}, false, true
	}
	at := time.Unix(meta.FetchedAt, 0)
	fresh := ttl > 0 && time.Since(at) < ttl
	return string(data), at, fresh, true
}

func writeBestCFCache(dir, pageURL, text string, urlCount int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "addresses.txt.tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "addresses.txt")); err != nil {
		return err
	}
	meta := cacheMeta{URL: pageURL, FetchedAt: time.Now().Unix(), URLCount: urlCount, Bytes: len(text)}
	buf, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), buf, 0o644)
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%.1f 小时", d.Hours())
	default:
		return fmt.Sprintf("%.1f 天", d.Hours()/24)
	}
}

// ---------- 来源清单 ----------

func readSources(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取来源清单失败: %w", err)
	}
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// 支持「URL  # 备注」形式的行尾注释
		if i := strings.Index(line, " #"); i > 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out, nil
}

func loadSource(client *http.Client, src, baseDir string) (string, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return fetchURL(client, src)
	}
	// 本地文件（相对路径相对于来源清单所在目录）
	path := src
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		// 容错：清单里直接写了一行地址（而不是 URL / 文件路径）时，按内联内容解析
		if looksLikeAddress(src) {
			fmt.Fprintf(os.Stderr, "[cfdata-extract] 来源不可读，按内联地址解析: %s\n", src)
			return src, nil
		}
		return "", err
	}
	if len(body) > maxBodyBytes {
		body = body[:maxBodyBytes]
	}
	return string(body), nil
}

func normalizeContent(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	if decoded, ok := maybeBase64(s); ok {
		fmt.Fprintln(os.Stderr, "[cfdata-extract] 检测到 base64 订阅内容，已解码")
		return decoded
	}
	return s
}

// maybeBase64 处理常见的 base64 订阅：整段只有 base64 字符且解码后是可读文本。
func maybeBase64(s string) (string, bool) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, s)
	if len(compact) < 16 {
		return "", false
	}
	// 明文地址一定含 ":" 或 "."；base64 字母表不会有这两个字符
	if strings.ContainsAny(compact, ":.#%~") {
		return "", false
	}
	for _, r := range compact {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=' || r == '-' || r == '_') {
			return "", false
		}
	}
	encs := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encs {
		decoded, err := enc.DecodeString(compact)
		if err != nil || len(decoded) < 12 {
			continue
		}
		if !utf8.Valid(decoded) || !mostlyPrintable(decoded) {
			continue
		}
		return string(decoded), true
	}
	return "", false
}

func mostlyPrintable(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	good := 0
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) || c >= 0x80 {
			good++
		}
	}
	return float64(good)/float64(len(b)) > 0.9
}

func parseText(text, src string, defPort int, add func(string, int, bool), expand func(string, string)) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(scanner.Text(), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 行尾 "#备注" 先去掉:否则 "IP#备注" 这类裸地址会被判非法而整行丢弃
		// (如 addressesapi 的 198.41.209.168#CM-Default)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
			if line == "" {
				continue
			}
		}
		found := false

		for _, m := range reV6CIDR.FindAllStringSubmatch(line, -1) {
			expand(m[1]+"/"+m[2], src)
			found = true
		}
		for _, m := range reV4CIDR.FindAllStringSubmatch(line, -1) {
			port := defPort
			if m[3] != "" {
				if p, ok := parsePort(m[3]); ok {
					port = p
				}
			}
			expand(m[1]+"/"+m[2]+":"+strconv.Itoa(port), src)
			found = true
		}
		for _, m := range reV6Port.FindAllStringSubmatch(line, -1) {
			if p, ok := parsePort(m[2]); ok {
				add(m[1], p, false)
				found = true
			}
		}
		for _, m := range reV4Port.FindAllStringSubmatch(line, -1) {
			if p, ok := parsePort(m[2]); ok {
				add(m[1], p, false)
				found = true
			}
		}
		for _, m := range reHostPort.FindAllStringSubmatch(line, -1) {
			if p, ok := parsePort(m[2]); ok {
				add(m[1], p, false)
				found = true
			}
		}
		if found {
			continue
		}
		// 兜底：整行是 "ip 端口" / "ip,端口" / 单个 ip 或域名
		fields := strings.FieldsFunc(line, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ',' || r == ';' || r == '|'
		})
		if len(fields) >= 2 {
			if p, ok := parsePort(fields[1]); ok && isValidHost(fields[0]) {
				add(fields[0], p, false)
				continue
			}
		}
		if len(fields) == 1 && isValidHost(fields[0]) {
			add(fields[0], defPort, false)
		}
	}
}

var errTooLarge = fmt.Errorf("网段过大")

func expandCIDR(raw string, defPort, max int, add func(string, int, bool)) (int, error) {
	spec := raw
	port := defPort
	// 形如 1.2.3.0/24:443 时拆出端口
	if i := strings.LastIndex(spec, ":"); i > strings.Index(spec, "/") {
		if p, ok := parsePort(spec[i+1:]); ok {
			port = p
			spec = spec[:i]
		}
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(spec))
	if err != nil {
		return 0, err
	}
	prefix = prefix.Masked()
	hostBits := prefix.Addr().BitLen() - prefix.Bits()
	if hostBits < 0 {
		return 0, fmt.Errorf("前缀长度不合法")
	}
	if hostBits >= 63 {
		return 0, errTooLarge
	}
	total := uint64(1) << uint(hostBits)
	if max > 0 && total > uint64(max) {
		return 0, errTooLarge
	}
	n := 0
	for a := prefix.Addr(); a.IsValid() && prefix.Contains(a); a = a.Next() {
		add(a.String(), port, true)
		n++
		if max > 0 && n >= max {
			break
		}
	}
	return n, nil
}

func parsePort(s string) (int, bool) {
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p < 1 || p > 65535 {
		return 0, false
	}
	return p, true
}

func isValidHost(h string) bool {
	h = strings.Trim(strings.TrimSpace(h), "[]")
	if h == "" || len(h) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return true
	}
	if !strings.Contains(h, ".") {
		return false
	}
	if strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") || strings.Contains(h, "..") {
		return false
	}
	if strings.ContainsAny(h, " /\\@:?#") {
		return false
	}
	labels := strings.Split(h, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 || len(tld) > 63 {
		return false
	}
	for _, r := range tld {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func isPublicIP(a netip.Addr) bool {
	if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() ||
		a.IsInterfaceLocalMulticast() {
		return false
	}
	if a.Is4() {
		for _, p := range reservedV4 {
			if p.Contains(a) {
				return false
			}
		}
	}
	return true
}

func writeList(path string, list []entry) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 64*1024)
	for _, e := range list {
		if _, err := fmt.Fprintf(w, "%s %d\n", e.host, e.port); err != nil {
			return err
		}
	}
	return w.Flush()
}

func looksLikeAddress(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t") {
		return false
	}
	if reV4Port.MatchString(s) || reV6Port.MatchString(s) || reV4CIDR.MatchString(s) || reHostPort.MatchString(s) {
		return true
	}
	return isValidHost(s)
}

func warn(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "[cfdata-extract] 警告: "+format+"\n", a...)
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "[cfdata-extract] 错误: "+format+"\n", a...)
	os.Exit(3)
}
