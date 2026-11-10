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
152.175.7.63:8443#HKG-HK
```

读法是 `IP:端口#机房-出口地`。上面这行就是：香港机房、香港出口。

想知道延迟和速度也写进结果里，加一个设置就行（见下面「结果里带上延迟和速度」）：

```
152.175.7.63:8443#HKG-HK-43ms-10.69MB/s
```

默认标准是延迟不超过 100 毫秒、速度不低于 5MB/s，两个条件都满足才算合格。完整跑一轮一般 1~3 分钟（香港实测：2128 条地址里筛出 8 条合格，全程约 2 分半）。

也可以用 `docker compose up --build`，效果一样。

## 结果文件

跑完后，`data` 目录里有这几个文件：

| 文件 | 里面是什么 |
| --- | --- |
| `cfdata-results.txt` | **只看这个**：合格的地址，一行一条，能直接用 |
| `cfdata-results.csv` | 所有候选地址的完整记录，包括不合格的，别拿它当合格清单 |
| `a.txt` | 这次测过的所有地址，留着方便复查 |

用了「排除出口地区」（`CF_EXCLUDE_LOC`）时，还会多出一个 `cfdata-scan-all.csv`。那是中途的一份扫描记录，想知道某个地址为什么被排除时可以翻它，平时不用管，删掉也不影响下次运行。

## 常用用法

### 只要某个地区的地址

```bash
docker run --rm -v "$PWD/data:/data" -e CF_REGION=HK cfdata-web:1.8.5 bestcf
```

`CF_REGION` 也可以写 `JP`（日本）、`SG`（新加坡）、`US`（美国），或者组合起来：`HK,SG`。还能直接写机房名，比如 `HKG`、`NRT`、`LAX`。

不知道该写什么？先查一下某个地区对应哪些机房：

```bash
docker run --rm cfdata-web:1.8.5 extractor -region JP
```

### 不要某个出口地区的地址

结果里有时会看到 `NRT-CN` 这种：走的是东京机房（`NRT`），但你这边的出口 IP 归属地是中国（`CN`）。如果你需要出口「看起来」不在国内，这类地址虽然延迟低、速度也不差，但对你没用。

这种情况比想象中普遍。实际跑日本机房那一轮，20 条达标结果里有 17 条是 `NRT-CN`，只有 3 条出口真的在日本。所以想要干净出口，建议一开始就排除：

```bash
docker run --rm -v "$PWD/data:/data" -e CF_EXCLUDE_LOC=CN cfdata-web:1.8.5 bestcf
```

同样的条件加上排除后，结果从 20 条变成 5 条，5 条全是 `NRT-JP`，一条 CN 都没有。

写法参考：

| 需求 | 怎么写 |
| --- | --- |
| 排除一个国家 | `-e CF_EXCLUDE_LOC=CN` |
| 排除多个 | `-e CF_EXCLUDE_LOC=CN,RU` |
| 大小写 | 都行，`cn` 和 `CN` 一样 |
| 分隔符 | 英文逗号、中文逗号、分号、空格都能认 |
| 排除三个以上 | 照这样接着加：`CN,RU,IR` |

**只能填 2 位国家码**，比如 `CN`、`RU`、`IR`。填 `NRT` 这种机房名会直接报错并提示你改用 `CF_REGION` —— 机房和出口归属是两回事：机房是地址接入的 Cloudflare 机房，出口是你自己这边的网络出口在哪注册。

**要多花一点时间**。普通一趟就是扫一遍、测一遍；加了排除之后，会先摸清每个地址的机房和出口归属，再只对符合条件的测速，所以比平时多花两成左右时间（实测 2 分 32 秒 → 2 分 55 秒）。日志里会看到「阶段 1/2」「阶段 2/2」，第一次看到不用紧张，第一轮不是白跑的。

**怎么确认结果干净**。跑完会打印一句话：

```
终检通过：结果中没有 CN 的出口地址
```

看到这句，就说明 `cfdata-results.txt` 里一条 CN 都没有。

极少数情况下会打印「终检剔除了 N 条」：个别地址在测速那一刻出口变了，已经自动从结果里删掉，不用管。如果打印的是「终检跳过」，说明复核没跑成，结果可能还留着个别漏网的，重跑一次即可。

**和地区筛选一起用**，两个条件同时生效：

```bash
# 只要日本机房，但不要出口在 CN 的
docker run --rm -v "$PWD/data:/data" -e CF_REGION=JP -e CF_EXCLUDE_LOC=CN cfdata-web:1.8.5 bestcf
```

排除后如果结果变得很少，多半是这批地址的出口大多落在你排除的地区 —— 可以放宽延迟（`-e CF_DELAY=150`）让更多地址进测速，或者换个机房试试。

### 结果里带上延迟和速度

默认结果只写机房和出口地。想直接看到延迟与速度：

```bash
docker run --rm -v "$PWD/data:/data" -e CF_OUT_FIELDS=dc,loc,latency,speed cfdata-web:1.8.5 bestcf
```

结果就变成这样，一眼能看出快慢：

```
43.161.253.137:443#HKG-HK-11ms-32.23MB/s
198.20.133.248:8443#HKG-HK-82ms-20.81MB/s
```

字段按你写的顺序拼，想多要就接着加，比如 `dc,loc,latency,speed,asnOrg`。能用的字段名见文末「输出字段」。

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
| `CF_EXCLUDE_LOC` | （空） | 不要出口在哪个地区的地址（`CN`、`CN,RU`），空表示不排除；用了会多花些时间 |
| `CF_PORT` | 443 | 地址没写端口时默认用这个 |
| `CF_OUT_FIELDS` | compact | 结果每行显示哪些信息。默认 `IP:端口#机房-出口地`；想带延迟和速度就填 `dc,loc,latency,speed` |

第一次跑会多下载约 9MB 的数据文件，之后存在 `data` 里重复使用。优选清单本身默认缓存 3 小时，缓存期内重复跑几乎是秒出结果；想每次都重新抓，加上 `-e CF_BESTCF_TTL=0`。

## 出问题怎么办

| 现象 | 一般是为什么 | 怎么办 |
| --- | --- | --- |
| 跑完了，`cfdata-results.txt` 是空的 | 这一轮没有地址同时满足延迟和速度 | 先放宽标准试试：`-e CF_SPEED_MIN=1`，确认链路没问题再调回去 |
| 速度全是 0 或测速失败 | 测速地址被挡了 | 保持默认的 50MB 测速地址，别调大，太大反而会被拒绝 |
| 构建时下载很慢 | GitHub 直连不稳 | 默认已经走了加速镜像；还不行可以换一个：`--build-arg CFDATA_MIRROR=https://gh-proxy.com/` |
| 想看的地区一个都没筛出来 | 这批地址在当前网络下没落到那个机房 | 日志里会印出这次实际出现的机房分布，照着改地区码再跑一轮 |
| 结果里延迟都不到 100ms | 正常 | 默认延迟上限就是 100ms，超的在扫描时已经被筛掉了 |
| 设了 `CF_EXCLUDE_LOC` 后跑得比以前慢 | 正常 | 用了排除会先摸清每个地址的出口归属，再挑出来测速；日志里「阶段 1/2」就是在做这件事。想快一点可以收紧 `CF_DELAY` |
| 排除后结果只剩几条，甚至一条不剩 | 这批地址的出口大多在你排除的那个地区 | 放宽 `CF_DELAY` 让更多地址进测速，或换个机房（`CF_REGION`）再试 |
| 结果里还是有被排除的地区 | 极少数情况下测速那一刻出口变了，而复核没跑成 | 看日志有没有「终检跳过」「终检失败」，有就重跑一次 |
| `CF_EXCLUDE_LOC` 报「格式不对」 | 填了机房名而不是国家码 | 只能填 2 位国家码（`CN`、`RU`）；要按机房筛选用 `CF_REGION` |

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
| `CF_EXCLUDE_LOC` | 空 | 出口地区排除：国家码，逗号分隔可组合（`CN`、`CN,RU`）；命中的地址不会被测速 |
| `CF_SCAN_ALL` | `cfdata-scan-all` | 用了出口地区排除时，中途那份扫描记录的文件名（落在 `/data`） |
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

`ip:port` 永远在最前，其余字段用 `#` 连接、字段间用 `-` 分隔。可用字段名：`ip`、`port`、`tls`、`lossRate`、`latency`、`scanMode`、`speed`、`outboundIP`、`dc`（机房，如 `HKG`）、`loc`（出口归属国家码，如 `HK`）、`region`、`city`、`asnNumber`、`asnOrg`、`originalInput`、`ipType`。写错的字段名会被忽略，全部无效时只输出 `ip:port`。

怎么看自己在不在乎 `dc` 和 `loc` 的差别：`dc` 是地址接入的 Cloudflare 机房（`CF_REGION` 按它挑），`loc` 是你这一侧出口的归属地（`CF_EXCLUDE_LOC` 按它排除），两者可以不一致 —— 就会出现 `NRT-CN` 这种「机房在日本、出口在国内」的组合。

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
