# CFData-WEB Docker

把 [PoemMisty/CFData-WEB](https://github.com/PoemMisty/CFData-WEB) 的 **release 二进制**装进一个约 **22MB** 的 Alpine 镜像，只保留 CLI 能力，并补上一条可直接落地的自动化链路：**取地址 → 去重 → 延迟扫描 → Cloudflare 测速 → 只导出合格 IP**。

镜像里没有编译器、没有 `curl`/`wget`、没有包管理器残留，只有三样东西：

- `cfdata`：上游官方 release 二进制（SHA-256 校验后落盘）
- `cfdata-extract`：约 5MB 的 Go 静态小工具，负责抓取来源、解析地址、展开网段、地区过滤
- Alpine 自带的 busybox（提供 `ash`，方便 `docker exec` 排错）

```
bestcf.pages.dev「优选域名 / 优选IP」全部清单   （默认输入，可缓存 3 小时）
   或  挂载的 sources.txt（多个网络地址 / 本地文件 / 内联地址）
        │  cfdata-extract：抓取 · 解析 · 网段展开 · 去重 · 过滤非公网
        ▼
   /tmp/a.txt   （每行 "host port"，cfdata 原生输入格式）
        │  cfdata -cli -mode nsb（全局 TLS 开启）
        ▼
   延迟扫描（≤CF_DELAY）→ 链路复验 → Cloudflare 测速（≥CF_SPEED_MIN）
        ▼
 /data/cfdata-results.txt（仅合格）   /data/cfdata-results.csv（全量候选）
```

## 特性

| 能力 | 说明 |
| --- | --- |
| 极小镜像 | `alpine:3.21` 基座，最终阶段零 `RUN`，约 22MB，amd64/arm64 双架构 |
| 两种输入 | 默认抓 bestcf 优选清单（94 个），或挂载自定义来源清单 |
| 地址解析 | `ip:port`、`域名:port`、`[IPv6]:port`、空格/逗号分隔、CIDR 展开、base64 订阅 |
| 自动去重 | 按 `host+port` 去重，并过滤回环/内网/CGNAT/保留地址 |
| 地区优选 | `CF_REGION=HK` 按实际接入机房筛选，支持国家码、机房码、逗号组合 |
| 双阈值合格 | 延迟 ≤ `CF_DELAY` **且** 速度 ≥ `CF_SPEED_MIN` 才算合格 |
| 可定制输出 | `CF_OUT_FIELDS` 自选字段，拼成 `ip:port#数据中心-地区-延迟-速度` |
| 可判定退出码 | `0` 有合格结果 / `2` 无合格结果 / `1` 硬错误，方便 cron、CI |

## 目录结构

| 文件 | 作用 |
| --- | --- |
| `Dockerfile` | 三阶段构建：下载并校验 release → 交叉编译提取器 → 组装运行镜像 |
| `entrypoint.sh` | 容器入口：模式分发、配置初始化、地址提取、扫描测速、结果判定 |
| `extractor/main.go` | 地址提取器主体（纯标准库，无第三方依赖） |
| `extractor/region.go` | 地区筛选：地区码 → 机房码映射、按机房过滤扫描结果 |
| `extractor/go.mod` | 提取器模块定义（无外部依赖） |
| `config/sources.txt.example` | 来源清单模板 |
| `docker-compose.yml` | 一键构建 + 运行示例 |
| `Makefile` | 常用命令（build / run / bestcf / region / multiarch） |
| `.dockerignore` | 构建上下文裁剪 |

## 快速开始

需要 Docker 20.10+（BuildKit）。arm64 交叉构建无需 qemu。

```bash
# 方式 A：直接用 bestcf 优选地址，不用准备任何清单
docker build -t cfdata-web:1.8.5 .
docker run --rm -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf

# 方式 B：用自己的来源清单
cp config/sources.txt.example config/sources.txt
vi config/sources.txt                                  # 每行一个网络地址
docker run --rm -v "$PWD/config:/config:ro" -v "$PWD/data:/data" cfdata-web:1.8.5 run

# 方式 C：compose
docker compose up --build
```

跑完后结果在：

```bash
cat data/cfdata-results.txt      # 仅合格，可直接喂给客户端
```

### 构建参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `CFDATA_VERSION` | `v1.8.5` | 使用的上游 release 版本 |
| `CFDATA_MIRROR` | `https://ghfast.top/` | GitHub 加速前缀；设为空字符串则直连 |
| `ALPINE_VERSION` | `3.21` | 基础镜像版本 |

```bash
# 直连 GitHub 构建
docker build --build-arg CFDATA_MIRROR= -t cfdata-web:1.8.5 .

# 多架构推送
docker buildx build --platform linux/amd64,linux/arm64 -t <registry>/cfdata-web:1.8.5 --push .
```

> 构建时会用 release 附带的 `.sha256` 校验二进制，校验失败直接中止构建，不会把坏二进制打进镜像。

## 运行模式

第一个参数决定行为，缺省为 `run`。

| 命令 | 说明 |
| --- | --- |
| `run` | 从 `/config/sources.txt` 提取 → 扫描 → 测速 → 导出 |
| `bestcf` | 抓 bestcf 优选域名 + 优选IP 全部清单 → 去重 → 扫描 → 测速 → 导出（无需挂载清单） |
| `extract` | 只抓取并解析 `sources.txt`，把地址清单打到 stdout，不扫描 |
| `bestcf-extract` | 只抓取 bestcf 清单并把去重后的地址打到 stdout，不扫描 |
| `scan` | 跳过提取，直接使用 `/data/a.txt` 扫描（复用上一轮地址） |
| `shell` | 进入容器 shell 排错，如 `shell -c 'ls /data'` |
| `cfdata …` | 透传到官方二进制，如 `cfdata -h` |
| `extractor …` | 透传到提取器，如 `extractor -region JP` |
| `-…` | 等同透传，如 `docker run … cfdata-web:1.8.5 -h` |

`extract` / `bestcf-extract` 的地址清单走 **stdout**，所有日志走 **stderr**，因此可以直接重定向：

```bash
docker run --rm -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf-extract > ips.txt 2> /dev/null
```

## 输入一：bestcf 优选清单（默认）

`bestcf` 模式不需要挂载任何清单：

1. 抓取 `https://bestcf.pages.dev/`，取 `#yxym`（优选域名 BestDomain）与 `#yxip`（优选IP BestIP）两个区块里**所有**清单链接（当前 94 个）；
2. 并发抓取这些纯文本清单（格式为 `IP:端口#备注` / `域名:端口#备注`，兼容 IPv6 方括号与裸 IP 行）；
3. 提取地址 → 展开网段 → 按 `host+port` 去重 → 过滤非公网地址；
4. 写入 `/tmp/a.txt`（并复制 `/data/a.txt`），交给 cfdata 扫描测速。

**缓存**：清单结果缓存在 `/data/.cache/bestcf`（`addresses.txt` + `meta.json`），默认有效期 3 小时；命中缓存约 0.03 秒完成，不再发起 94 个请求。过期自动刷新，页面或清单全部抓取失败时回退到过期缓存并告警。

```bash
# 强制刷新缓存
docker run --rm -e CF_BESTCF_TTL=0 -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf

# 只看看当前会得到哪些地址
docker run --rm -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf-extract

# bestcf 作为默认输入，同时合并自己的 sources.txt（文件存在时自动合并）
docker run --rm -v "$PWD/config:/config:ro" -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf
```

> 该模式一次面对约 2100 条地址（其中约 370 条是域名，需要先解析），扫描阶段通常 1~3 分钟。可用 `CF_SCAN_RESULT_LIMIT`（默认 1000）与 `CF_SPEED_LIMIT` 控制规模。

## 输入二：自定义来源清单

`/config/sources.txt` 每行一个网络地址：

- 支持 `http://` / `https://` URL，也支持容器内本地文件路径（相对路径以清单所在目录为基准）；
- 也可以直接写一行地址（`104.16.1.1:443`、`104.16.0.0/24`），按内联内容解析，便于快速验证；
- 空行、以 `#` 开头的整行会被忽略，行尾支持 ` # 备注`；
- 同一来源重复出现只处理一次。

抓取到的文本按以下规则提取：

| 形式 | 示例 | 结果 |
| --- | --- | --- |
| IPv4:端口 | `104.16.1.1:443` | `104.16.1.1 443` |
| 域名:端口 | `cf.example.com:8443` | `cf.example.com 8443` |
| IPv6 带方括号 | `[2606:4700::1111]:443` | `2606:4700::1111 443` |
| 空格 / 逗号分隔 | `104.16.1.1 443`、`104.16.1.1,443` | `104.16.1.1 443` |
| 网段（CIDR） | `104.16.0.0/24`、`104.17.0.0/24:2053` | 展开为该段全部 IP，端口取 CIDR 后缀或 `CF_PORT` |
| 单行裸地址 | `104.16.1.1` | 补 `CF_PORT` |
| base64 订阅 | 整段只有 base64 字符 | 自动解码后再解析 |

规则细节：

- **网段上限**：单个网段展开数量默认 4096（`CF_MAX_CIDR`），超过上限的网段会被跳过并告警，避免误填 `/8` 把容器跑死；设为 `0` 表示不限制。
- **去重**：按 `host + port` 去重，保持出现顺序。
- **端口**：地址自带端口优先，没有端口时用 `CF_PORT`（默认 443）。
- **输出**：`/tmp/a.txt`（临时文件，与 cfdata 非标模式原生输入格式一致），同时复制一份到 `/data/a.txt` 便于复查。

## 按地区优选（CF_REGION）

设置 `CF_REGION` 后，只保留**实际接入指定 Cloudflare 机房**的地址，其余在测速前就被剔除。

```bash
# 只要香港
docker run --rm -v "$PWD/data:/data" -e CF_REGION=HK cfdata-web:1.8.5 bestcf

# 日本（国家码自动展开成 FUK/KIX/NRT/OKA）
docker run --rm -v "$PWD/data:/data" -e CF_REGION=JP cfdata-web:1.8.5 bestcf

# 多地区组合：香港 + 新加坡 + 洛杉矶机房
docker run --rm -v "$PWD/data:/data" -e CF_REGION="HK,SG,LAX" cfdata-web:1.8.5 bestcf
```

**地区码两种写法**（大小写不敏感，`,` 分隔，可混用）：

| 写法 | 含义 | 示例 |
| --- | --- | --- |
| 2 位国家码 | ISO 国家码，自动展开成该国全部机房 | `HK`→`HKG`；`JP`→`FUK/KIX/NRT/OKA`；`US`→47 个机房 |
| 3 位机房码 | Cloudflare 边缘机房 IATA 码，精确匹配 | `HKG`、`NRT`、`LAX`、`SIN`、`TPE` |

不确定该填什么？先查：

```bash
# 查询地区码对应的机房
docker run --rm cfdata-web:1.8.5 extractor -region JP
```

**筛选机制**：cfdata 自带的 `-nsbdc` 只能精确匹配**单个**机房码，无法表达多个机房，因此按展开结果分两路：

| 展开结果 | 处理方式 | 耗时 |
| --- | --- | --- |
| 单个机房（`HK`→`HKG`、`SG`→`SIN`、`KR`→`ICN`） | 直接 `-nsbdc` 交给 cfdata 在扫描期过滤，**单轮**完成 | 与不筛地区基本一致 |
| 多个机房（`JP`→4 个、`US`→47 个） | **阶段 1** 全量扫描（`-nsbspeedtest 0`，只取 `colo`，存 `/data/cfdata-scan-all.csv`）→ 过滤 → **阶段 2** 只对命中地址扫描测速 | 多一轮全量扫描 |

多机房路径命中 0 条时，日志会直接给出该轮实际出现的机房分布，便于修正地区码：

```
[cfdata-extract] 警告: 目标地区没有任何可用地址；本次扫描实际出现的机房如下（可作为地区码参考）:
[cfdata-extract] 机房分布 Top: SIN(747) LAX(697) FRA(334) AMS(145) NRT(102) HKG(30) ICN(27) TPE(13)
```

> `colo` 是**目标地址实际接入的边缘机房**，由 anycast 路由决定，与扫描机自身位置无关，因此同一份清单在不同网络环境下结果会不同。

## 合格判定：延迟与速度

最终结果 = **延迟 ≤ `CF_DELAY` 且 速度 ≥ `CF_SPEED_MIN`** 的交集。两者由不同阶段实现，缺一不可：

| 条件 | 实现参数 | 生效阶段 |
| --- | --- | --- |
| 延迟 ≤ `CF_DELAY` | `-nsbdelay` | 扫描期直接丢弃超阈值地址，不会进入测速池 |
| 速度 ≥ `CF_SPEED_MIN` | `-nsbspeedmin` + `-outqualified qualified` | 测速后过滤，只导出合格行 |

几个容易被忽略的点：

- `-outqualified qualified` **只判定速度**，不含延迟；延迟靠扫描期剔除实现，所以调 `CF_DELAY` 才能同时约束两者。
- 显示的延迟一定 ≤ `CF_DELAY`：cfdata 对每个地址做 3 次连接，超阈值的尝试被丢弃、不计入均值。副作用是**丢包率会偏高**（超阈值计为丢包），看到 `33%` 不必误判成网络差。
- `CF_SPEED_LIMIT` 是**合格条数上限**，达到即停止测速，不是阈值。阈值只有 `CF_SPEED_MIN`。
- 阈值对域名同样生效，但只统计 TCP 连接耗时，DNS 解析时间不计入，实际体验会比数字略慢。

### 常用阈值组合

| 场景 | 配置 |
| --- | --- |
| 亚太/国内直连，追求低延迟 | `-e CF_DELAY=80 -e CF_SPEED_MIN=5` |
| 常规优选（默认） | `-e CF_DELAY=100 -e CF_SPEED_MIN=5` |
| 跨境、线路绕路多 | `-e CF_DELAY=200 -e CF_SPEED_MIN=5` |
| 只要高带宽 | `-e CF_DELAY=0 -e CF_SPEED_MIN=15` |
| 完全不管延迟 | `-e CF_DELAY=0` |

## 输出格式与字段

`CF_OUT_FIELDS` 决定 `/data/cfdata-results.txt` 的内容，支持四种写法：

| 取值 | 实际字段 | 输出示例 |
| --- | --- | --- |
| `compact`（默认） | `ipport,dc,loc` | `152.175.7.63:8443#HKG-HK` |
| `all` | 所有有值字段（自动跳过空字段） | `152.175.7.63:8443#152.175.7.63-8443-true-0%-43ms-TCPing-10.69MB/s-...-HKG-HK-Asia Pacific-Hong Kong-AS138997-...` |
| `ipport` | 只有 `ip:port` | `152.175.7.63:8443` |
| 逗号分隔 | 按书写顺序拼接，大小写不敏感 | `dc,loc,latency,speed` → `152.175.7.63:8443#HKG-HK-43ms-10.69MB/s` |

`ip:port` 永远在最前，其余字段用 `#` 连接、字段间用 `-` 分隔。

### 可用字段

非标扫描模式下**真正有值**的字段（16 个）：

| key | 含义 | key | 含义 |
| --- | --- | --- | --- |
| `ip` | IP 地址 | `dc` | 数据中心（如 `HKG`） |
| `port` | 端口 | `loc` | 出站 IP 归属国家码（如 `HK`） |
| `tls` | 是否 TLS | `region` | 大区名（如 `Asia Pacific`） |
| `lossRate` | 丢包率 | `city` | 城市 |
| `latency` | 网络延迟 | `asnNumber` | ASN 号码 |
| `scanMode` | 扫描方式 | `asnOrg` | ASN 组织 |
| `speed` | 下载速度 | `originalInput` | 原始输入 |
| `outboundIP` | 出站 IP | `ipType` | IPv4 / IPv6 |

另有 10 个字段只在完整探测模式下填充，非标模式写进 `CF_OUT_FIELDS` 也不会输出（自动丢弃）：`dcCountry`、`visitScheme`、`tlsVersion`、`sni`、`httpVersion`、`warp`、`gateway`、`rbi`、`kex`、`timestamp`。

> 写错的 key 会被静默忽略，全部无效时退化为只输出 `ip:port`。不确定字段名就用 `all` 跑一轮看实际值。

`dc` 与 `loc` 的区别：`dc` 是目标地址**实际接入的 Cloudflare 机房**（`CF_REGION` 按它筛），`loc` 是**出站 IP 的归属地**。二者可能不一致，例如 `HKG-CN` 表示接入香港机房但出口在中国大陆。

### 输出文件

| 文件 | 内容 |
| --- | --- |
| `/data/cfdata-results.txt` | **仅合格**结果，格式由 `CF_OUT_FIELDS` 决定，一行一条 |
| `/data/cfdata-results.csv` | 全量候选（含未测速、测速失败行），固定 16 列中文表头 |
| `/data/a.txt` | 本次展开后的地址清单（与 `/tmp/a.txt` 一致），便于复现 |
| `/data/cfdata-scan-all.csv` | 仅「地区码对应多个机房」时产生：全量扫描的机房/地区/城市 |
| `/data/GeoLite2-ASN.mmdb`、`locations.json` | 首次运行下载的缓存，约 9MB，后续复用 |

> CSV 是全量候选，**不要**当成合格清单用；合格清单只看 `cfdata-results.txt`。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CF_SOURCES` | `/config/sources.txt` | 来源清单路径；bestcf 模式下若存在会自动合并 |
| `CF_WORKDIR` | `/data` | 工作目录，放结果与缓存 |
| `CF_OUT` | `cfdata-results` | 输出文件名（不带扩展名） |
| `CF_MAX_CIDR` | `4096` | 单网段最大展开 IP 数，`0` 不限制 |
| `CF_PORT` | `443` | 地址缺省端口 |
| `CF_THREADS` | `100` | 非标扫描并发 |
| `CF_SCAN_RESULT_LIMIT` | `1000` | 延迟扫描结果上限 |
| `CF_DELAY` | `100` | 延迟阈值（ms），`0` 表示不筛延迟 |
| `CF_SPEED_THREADS` | `2` | 测速线程；要最准确可设 `1`，但耗时翻倍 |
| `CF_SPEED_MIN` | `5` | 合格速度（MB/s） |
| `CF_SPEED_LIMIT` | `20` | 合格结果条数上限，达到即停止测速 |
| `CF_SPEED_URL` | `https://speed.cloudflare.com/__down?bytes=50000000` | 测速下载地址 |
| `CF_RELINK` | `true` | 扫描后做真实 TCP+TLS+HTTP 链路复验 |
| `CF_OUT_FIELDS` | `compact` | 导出字段，见上一节 |
| `CF_DNS` | 空 | 自定义 DNS，空表示用 Docker 内置 DNS |
| `CF_ALLOW_EMPTY` | `0` | `1` 表示无合格结果也返回 0 |
| `CF_REGION` | 空 | 地区筛选：国家码或机房码，逗号分隔可组合；空表示不筛选 |
| `CF_SCAN_ALL` | `cfdata-scan-all` | 地区筛选阶段 1 全量扫描结果文件名（落在 `/data`） |
| `CF_LOCATIONS_URL` | `https://www.baipiao.eu.org/cloudflare/locations` | `locations.json` 缺失时的下载地址 |
| `CF_BESTCF_URL` | `https://bestcf.pages.dev/` | bestcf 模式抓取的页面 |
| `CF_BESTCF_TTL` | `3h` | 清单缓存有效期（Go duration：`30m`、`6h`），`0` 每次强制刷新 |
| `CF_BESTCF_CACHE` | `/data/.cache/bestcf` | 清单缓存目录 |

## 退出码

| 退出码 | 含义 |
| --- | --- |
| `0` | 有合格结果（或设了 `CF_ALLOW_EMPTY=1`） |
| `2` | 有候选地址，但没有地址同时满足延迟与速度阈值 |
| `1` | 硬错误：来源清单缺失、地址提取失败、地区码非法、扫描失败等 |
| `64` | 未知的运行模式 |

退出码 `2` 是为方便 `cron` / CI 判定「本轮是否拿到可用地址」，不把「没达标」当成程序故障。

## 典型配方

```bash
# 1) 香港 + 低延迟 + 自定义输出字段
docker run --rm -v "$PWD/data:/data" \
  -e CF_REGION=HKG -e CF_DELAY=80 -e CF_SPEED_MIN=10 \
  -e CF_OUT_FIELDS="dc,loc,latency,speed" \
  cfdata-web:1.8.5 bestcf

# 2) 只要 20 条高带宽结果，全量字段导出
docker run --rm -v "$PWD/data:/data" \
  -e CF_SPEED_MIN=15 -e CF_SPEED_LIMIT=20 -e CF_OUT_FIELDS=all \
  cfdata-web:1.8.5 bestcf

# 3) 复用上一轮地址，换更快阈值重测（不重新抓清单）
docker run --rm -v "$PWD/data:/data" \
  -e CF_SPEED_MIN=20 -e CF_SPEED_LIMIT=5 \
  cfdata-web:1.8.5 scan

# 4) 只看某份清单会解析出什么地址
docker run --rm -v "$PWD/config:/config:ro" cfdata-web:1.8.5 extract

# 5) 输出纯净地址列表（日志走 stderr）
docker run --rm -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf-extract > ips.txt 2>/dev/null

# 6) 定时任务：每天 3 点更新一次，无合格也返回 0
0 3 * * * docker run --rm -v /opt/cfdata/data:/data \
  -e CF_BESTCF_TTL=0 -e CF_ALLOW_EMPTY=1 cfdata-web:1.8.5 bestcf >/dev/null 2>&1
```

## 实测参考

| 项 | 结果 |
| --- | --- |
| 镜像大小 | 22.2MB（alpine 3.21 + release 二进制 + 5MB 提取器） |
| bestcf 抓取 | 94 个清单，通常 93 成功、1 个 404/超时（逐条告警，不影响其余） |
| 去重产出 | 约 2100 条地址（含约 370 个优选域名） |
| 缓存命中 | 约 0.03 秒，输出与联网结果一致 |
| `CF_REGION=HKG` 全流程 | 2 分 37 秒；2128 条输入 → 17 条 HKG 候选 → **8 条合格** |

一次真实的 HKG 结果样例（`CF_DELAY=100`、`CF_SPEED_MIN=5`、`CF_REGION=HKG`）：

```
152.175.7.63:8443#HKG-HK-43ms-10.69MB/s
43.161.228.229:443#HKG-CN-38ms-10.60MB/s
45.153.130.54:2053#HKG-HK-63ms-6.68MB/s
38.47.102.230:443#HKG-HK-53ms-5.36MB/s
151.243.229.191:443#HKG-HK-73ms-14.35MB/s
216.236.51.155:1891#HKG-HK-83ms-10.66MB/s
43.161.221.119:443#HKG-HK-41ms-9.88MB/s
216.236.59.168:1891#HKG-HK-81ms-9.69MB/s
```

同一轮 CSV 里的不合格项（延迟合格但速度不够，或测速失败）说明了双阈值的必要性：`83.229.127.206:443` 46ms 仅 0.08MB/s、`149.104.27.213:443` 50ms 但测速失败。

## 故障排查

| 现象 | 原因 | 解决 |
| --- | --- | --- |
| 构建时下载慢/失败 | GitHub 直连不稳 | 默认走 `ghfast.top`，可换 `--build-arg CFDATA_MIRROR=https://gh-proxy.com/`，或设空直连 |
| 测速全部失败 / 403 | `__down` 对 `bytes` 有上限，≥100MB 返回 403 | 保持默认 50MB，不要调大 |
| 无合格结果但 CSV 有数据 | 延迟扫描正常，速度没到阈值 | `-e CF_SPEED_MIN=1` 先验证链路，再改回 |
| 全部地址被链路复验剔除 | 复验探针 `probe.609909.xyz` 在本网络解析不到 | `-e CF_RELINK=false` 跳过复验 |
| 测速域名解析失败 | 显式 `CF_DNS` 强制走指定 DNS | 不设 `CF_DNS`，用 Docker 内置 DNS |
| 结果里延迟都 <100ms | `CF_DELAY` 默认 100，扫描期已剔除 | 属预期；`CF_DELAY=0` 关闭筛选 |
| 地区码没结果 | 这批地址在当前网络没落到该机房 | 看「机房分布 Top」；单机房码可改用多机房国家码跑一轮 |
| 首次运行下载 9MB | cfdata 拉 `GeoLite2-ASN.mmdb` | 正常，缓存在 `/data`，失败也不影响扫描 |
| bestcf 每次抓 94 个链接 | 缓存过期 | 默认 3 小时，`CF_BESTCF_TTL=0` 强制刷新 |
| 地址数比清单少 | 非公网地址被过滤 | 回环、内网、CGNAT、组播、保留段会被丢弃 |
| 端口 80 的地址不通过 | 全局 TLS 开启 | 本项目设计如此，非标地址多为 TLS 回源端口 |

需要进容器排查时：

```bash
docker run --rm -it --entrypoint /bin/sh cfdata-web:1.8.5
# 或
docker run --rm cfdata-web:1.8.5 shell -c 'ls -la /data && head -3 /data/a.txt'
docker run --rm cfdata-web:1.8.5 cfdata -h     # 上游完整参数
docker run --rm cfdata-web:1.8.5 extractor -h  # 提取器参数
```

## 已知取舍

- **全局 TLS**：`-nsbtls=true` 是全局开关，一次只能选一种。本项目按需求固定开启，因此纯 HTTP 端口（80/8080）的地址不会通过验证。
- **测速线程默认 2**：为了在「准确」与「候选多时不至于跑一小时」之间折中；要最准设 `CF_SPEED_THREADS=1`，耗时翻倍。
- **单机房 vs 多机房**：多机房地区码要多跑一轮全量扫描，这是为了绕开 `-nsbdc` 只能匹配单机房码的限制。
- **延迟口径**：展示的延迟是 TCP 连接均值，不含 DNS 解析与 TLS 握手，因此比浏览器实际首包时间乐观。

## 合规与致谢

本镜像只是对上游二进制的打包与流程封装，扫描、测速、导出的核心能力全部来自上游项目。

- 上游项目：[PoemMisty/CFData-WEB](https://github.com/PoemMisty/CFData-WEB)，采用 GPL-3.0-or-later，镜像内二进制版权归原作者所有
- 机房地理位置数据：[baipiao.eu.org](https://www.baipiao.eu.org/cloudflare/locations)
- 优选清单来源：[bestcf.pages.dev](https://bestcf.pages.dev/)

> 本工具仅用于学习与研究。请遵守当地法律法规与 Cloudflare 服务条款，不要用于攻击、绕过访问控制或任何侵权用途。
