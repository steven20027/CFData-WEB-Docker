# CFData-WEB Docker

自动帮你找 Cloudflare 的优选 IP：从网上抓一批候选地址，逐个测延迟和速度，只把合格的留下来，给你一份能直接用的清单。

你不用自己到处找 IP、一个个测。跑一次，就能拿到结果。镜像只有约 22MB。

## 快速开始

电脑上装好 Docker，然后：

```bash
docker build -t cfdata-web:1.8.5 .
docker run --rm -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf
```

跑完后看结果：

```bash
cat data/cfdata-results.txt
```

会看到类似这样的内容，每行一个合格地址：

```
152.175.7.63:8443#HKG-HK-43ms-10.69MB/s
```

读法是 `IP:端口#机房-出口地-延迟-速度`。上面这行就是：香港机房、香港出口、延迟 43 毫秒、速度 10.69MB/s。

默认标准是延迟不超过 100 毫秒、速度不低于 5MB/s，两个条件都满足才算合格。完整跑一轮一般 1~3 分钟（香港实测：2128 条地址里筛出 8 条合格，全程约 2 分半）。

也可以用 `docker compose up --build`，效果一样。

## 结果文件

跑完后，`data` 目录里有这几个文件：

| 文件 | 里面是什么 |
| --- | --- |
| `cfdata-results.txt` | **只看这个**：合格的地址，一行一条，能直接用 |
| `cfdata-results.csv` | 所有候选地址的完整记录，包括不合格的，别拿它当合格清单 |
| `a.txt` | 这次测过的所有地址，留着方便复查 |

## 常用用法

### 只要香港的地址

```bash
docker run --rm -v "$PWD/data:/data" -e CF_REGION=HK cfdata-web:1.8.5 bestcf
```

`CF_REGION` 也可以写 `JP`（日本）、`SG`（新加坡）、`US`（美国），或者组合起来：`HK,SG`。还能直接写机房名，比如 `HKG`、`NRT`、`LAX`。

不知道该写什么？先查一下某个地区对应哪些机房：

```bash
docker run --rm cfdata-web:1.8.5 extractor -region JP
```

### 想要速度更快的

```bash
docker run --rm -v "$PWD/data:/data" -e CF_SPEED_MIN=10 cfdata-web:1.8.5 bestcf
```

### 想要延迟更低的

```bash
docker run --rm -v "$PWD/data:/data" -e CF_DELAY=80 cfdata-web:1.8.5 bestcf
```

### 只要地址列表，不测速

```bash
docker run --rm -v "$PWD/data:/data" cfdata-web:1.8.5 bestcf-extract > ips.txt 2>/dev/null
```

### 用自己的地址来源

不想用默认的优选清单，可以自己准备一份：

```bash
cp config/sources.txt.example config/sources.txt
# 编辑 config/sources.txt，每行写一个网址或文件路径，也可以直接写一行地址
docker run --rm -v "$PWD/config:/config:ro" -v "$PWD/data:/data" cfdata-web:1.8.5 run
```

清单里每行一个来源，支持：

- 网址（`https://...`），会去抓里面的地址
- 本地文件路径
- 直接写地址，比如 `104.16.1.1:443`，或者一整段 `104.16.0.0/24`
- 行尾可以加 `#备注`，`#` 开头的整行是注释

地址格式很宽松：`IP:端口`、`域名:端口`、`[IPv6]:端口`、空格或逗号分隔都行；没写端口默认 443；网段会自动展开成里面的每个 IP。

## 每天自动跑一次

加一条定时任务，每天凌晨 3 点自动更新：

```
0 3 * * * docker run --rm -v /opt/cfdata/data:/data -e CF_BESTCF_TTL=0 -e CF_ALLOW_EMPTY=1 cfdata-web:1.8.5 bestcf >/dev/null 2>&1
```

## 能调的东西

一般只用改下面这几个，写法都是 `-e 名字=值`，例如 `-e CF_DELAY=80 -e CF_SPEED_MIN=10`：

| 设置 | 默认 | 改了会怎样 |
| --- | --- | --- |
| `CF_DELAY` | 100 | 延迟上限（毫秒），越小越挑剔；填 0 表示不限延迟 |
| `CF_SPEED_MIN` | 5 | 速度下限（MB/s），越大越挑剔 |
| `CF_SPEED_LIMIT` | 20 | 最多留几条合格结果，够数就停 |
| `CF_REGION` | （空） | 只要哪个地区的地址，空表示不限 |
| `CF_PORT` | 443 | 地址没写端口时默认用这个 |
| `CF_OUT_FIELDS` | compact | 结果每行显示哪些信息，默认是 `IP:端口#机房-出口地` |

第一次跑会多下载约 9MB 的数据文件，之后存在 `data` 里重复使用。优选清单本身默认缓存 3 小时，缓存期内重复跑几乎是秒出结果；想每次都重新抓，加上 `-e CF_BESTCF_TTL=0`。

## 出问题怎么办

| 现象 | 一般是为什么 | 怎么办 |
| --- | --- | --- |
| 跑完了，`results.txt` 是空的 | 这一轮没有地址同时满足延迟和速度 | 先放宽标准试试：`-e CF_SPEED_MIN=1`，确认链路没问题再调回去 |
| 速度全是 0 或测速失败 | 测速地址被挡了 | 保持默认的 50MB 测速地址，别调大，太大反而会被拒绝 |
| 构建时下载很慢 | GitHub 直连不稳 | 默认已经走了加速镜像；还不行可以换一个：`--build-arg CFDATA_MIRROR=https://gh-proxy.com/` |
| 想看的地区一个都没筛出来 | 这批地址在当前网络下没落到那个机房 | 日志里会印出这次实际出现的机房分布，照着改地区码再跑一轮 |
| 结果里延迟都不到 100ms | 正常 | 默认延迟上限就是 100ms，超的在扫描时已经被筛掉了 |

想进容器里自己看：

```bash
docker run --rm cfdata-web:1.8.5 shell -c 'ls -la /data && head -3 /data/a.txt'
```

---

## 进阶：完整参考

> 下面是给想深究的人准备的，普通使用可以跳过。

### 运行模式

第一个参数决定容器做什么，缺省是 `run`。

| 命令 | 说明 |
| --- | --- |
| `run` | 从 `/config/sources.txt` 提取 → 扫描 → 测速 → 导出 |
| `bestcf` | 抓 bestcf 优选清单 → 去重 → 扫描 → 测速 → 导出，不用挂载清单 |
| `extract` | 只抓取并解析 `sources.txt`，把地址清单打到屏幕上，不扫描 |
| `bestcf-extract` | 只抓取 bestcf 清单并把去重后的地址打到屏幕上，不扫描 |
| `scan` | 跳过提取，直接用 `/data/a.txt` 扫描（复用上一轮地址） |
| `shell` | 进入容器排错，如 `shell -c 'ls /data'` |
| `cfdata …` | 透传到官方工具，如 `cfdata -h` |
| `extractor …` | 透传到提取器，如 `extractor -region JP` |
| `-…` | 等同透传，如 `docker run … cfdata-web:1.8.5 -h` |

`extract` / `bestcf-extract` 的地址清单走标准输出，日志走标准错误，可以直接重定向保存。

### 全部环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `CF_SOURCES` | `/config/sources.txt` | 来源清单路径；bestcf 模式下若存在会自动合并 |
| `CF_WORKDIR` | `/data` | 工作目录，放结果与缓存 |
| `CF_OUT` | `cfdata-results` | 输出文件名（不带扩展名） |
| `CF_MAX_CIDR` | `4096` | 单网段最大展开 IP 数，`0` 不限制 |
| `CF_PORT` | `443` | 地址缺省端口 |
| `CF_THREADS` | `100` | 扫描并发 |
| `CF_SCAN_RESULT_LIMIT` | `1000` | 延迟扫描结果上限 |
| `CF_DELAY` | `100` | 延迟阈值（毫秒），`0` 表示不筛延迟 |
| `CF_SPEED_THREADS` | `2` | 测速线程；要最准确可设 `1`，但耗时翻倍 |
| `CF_SPEED_MIN` | `5` | 合格速度（MB/s） |
| `CF_SPEED_LIMIT` | `20` | 合格结果条数上限，达到即停止测速 |
| `CF_SPEED_URL` | `https://speed.cloudflare.com/__down?bytes=50000000` | 测速下载地址 |
| `CF_RELINK` | `true` | 扫描后做真实链路复验 |
| `CF_OUT_FIELDS` | `compact` | 导出字段，见下一节 |
| `CF_DNS` | 空 | 自定义 DNS，空表示用 Docker 内置 DNS |
| `CF_ALLOW_EMPTY` | `0` | `1` 表示无合格结果也返回 0 |
| `CF_REGION` | 空 | 地区筛选：国家码或机房码，逗号分隔可组合；空表示不筛选 |
| `CF_SCAN_ALL` | `cfdata-scan-all` | 地区筛选阶段 1 全量扫描结果文件名（落在 `/data`） |
| `CF_LOCATIONS_URL` | `https://www.baipiao.eu.org/cloudflare/locations` | 机房位置数据缺失时的下载地址 |
| `CF_BESTCF_URL` | `https://bestcf.pages.dev/` | bestcf 模式抓取的页面 |
| `CF_BESTCF_TTL` | `3h` | 清单缓存有效期（`30m`、`6h` 这种写法），`0` 每次强制刷新 |
| `CF_BESTCF_CACHE` | `/data/.cache/bestcf` | 清单缓存目录 |

### 输出字段

`CF_OUT_FIELDS` 决定 `cfdata-results.txt` 每行写什么：

| 取值 | 实际字段 | 输出示例 |
| --- | --- | --- |
| `compact`（默认） | `ipport,dc,loc` | `152.175.7.63:8443#HKG-HK` |
| `all` | 所有有值字段（自动跳过空字段） | `152.175.7.63:8443#152.175.7.63-8443-true-0%-43ms-TCPing-10.69MB/s-...` |
| `ipport` | 只有 `ip:port` | `152.175.7.63:8443` |
| 逗号分隔 | 按书写顺序拼接 | `dc,loc,latency,speed` → `152.175.7.63:8443#HKG-HK-43ms-10.69MB/s` |

`ip:port` 永远在最前，其余字段用 `#` 连接、字段间用 `-` 分隔。可用字段名：`ip`、`port`、`tls`、`lossRate`、`latency`、`scanMode`、`speed`、`outboundIP`、`dc`（数据中心，如 `HKG`）、`loc`（出站 IP 归属国家码，如 `HK`）、`region`、`city`、`asnNumber`、`asnOrg`、`originalInput`、`ipType`。写错的字段名会被忽略，全部无效时只输出 `ip:port`。

`dc` 与 `loc` 的区别：`dc` 是目标地址实际接入的 Cloudflare 机房（`CF_REGION` 按它筛），`loc` 是出站 IP 的归属地，二者可能不一致。

### 退出码

`0` 有合格结果；`2` 有候选地址但没有同时满足延迟与速度的（方便定时任务判断本轮是否拿到可用地址，不算程序故障）；`1` 硬错误（来源清单缺失、地址提取失败、地区码非法、扫描失败等）；`64` 未知的运行模式。

### 构建参数

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `CFDATA_VERSION` | `v1.8.5` | 使用的上游 release 版本 |
| `CFDATA_MIRROR` | `https://ghfast.top/` | GitHub 加速前缀；设为空字符串则直连 |
| `ALPINE_VERSION` | `3.21` | 基础镜像版本 |

构建时会用 release 附带的 `.sha256` 校验二进制，校验失败直接中止，不会把坏二进制打进镜像。

### 两点提醒

- **只认加密端口**：全局开启了 TLS，所以纯 HTTP 端口（80/8080）的地址不会通过验证，这是设计如此。
- **延迟比体感乐观**：显示的延迟是 TCP 连接耗时，不含 DNS 解析与 TLS 握手，实际用起来会比数字略慢。

## 致谢

扫描、测速、导出的核心能力全部来自上游项目，本镜像只是打包与流程封装。

- 上游项目：[PoemMisty/CFData-WEB](https://github.com/PoemMisty/CFData-WEB)，GPL-3.0-or-later
- 机房位置数据：[baipiao.eu.org](https://www.baipiao.eu.org/cloudflare/locations)
- 优选清单来源：[bestcf.pages.dev](https://bestcf.pages.dev/)

> 本工具仅用于学习与研究。请遵守当地法律法规与 Cloudflare 服务条款，不要用于攻击、绕过访问控制或任何侵权用途。
