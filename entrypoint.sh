#!/bin/sh
# CFData-WEB 容器入口：来源清单 -> 地址提取 -> 非标扫描 -> Cloudflare 测速 -> 达标结果导出
set -eu

CF_SOURCES="${CF_SOURCES:-/config/sources.txt}"
CF_WORKDIR="${CF_WORKDIR:-/data}"
CF_OUT="${CF_OUT:-cfdata-results}"
CF_MAX_CIDR="${CF_MAX_CIDR:-4096}"
CF_PORT="${CF_PORT:-443}"
CF_THREADS="${CF_THREADS:-100}"
CF_SCAN_RESULT_LIMIT="${CF_SCAN_RESULT_LIMIT:-1000}"
CF_DELAY="${CF_DELAY:-100}"
CF_SPEED_THREADS="${CF_SPEED_THREADS:-2}"
CF_SPEED_MIN="${CF_SPEED_MIN:-5}"
CF_SPEED_LIMIT="${CF_SPEED_LIMIT:-20}"
CF_SPEED_URL="${CF_SPEED_URL:-https://speed.cloudflare.com/__down?bytes=50000000}"
CF_RELINK="${CF_RELINK:-true}"
CF_OUT_FIELDS="${CF_OUT_FIELDS:-compact}"
CF_DNS="${CF_DNS:-}"
CF_ALLOW_EMPTY="${CF_ALLOW_EMPTY:-0}"
CF_BESTCF_URL="${CF_BESTCF_URL:-https://bestcf.pages.dev/}"
CF_BESTCF_TTL="${CF_BESTCF_TTL:-3h}"
CF_BESTCF_CACHE="${CF_BESTCF_CACHE:-/data/.cache/bestcf}"
# 地区筛选：留空不筛选；支持 2 位国家码（HK/JP/US）或 3 位机房码（HKG/NRT/LAX），可逗号分隔组合
CF_REGION="${CF_REGION:-}"
CF_SCAN_ALL="${CF_SCAN_ALL:-cfdata-scan-all}"
export CF_SOURCES

log()  { printf '[cfdata] %s\n' "$*" >&2; }
warn() { printf '[cfdata] 警告: %s\n' "$*" >&2; }
die()  { printf '[cfdata] 错误: %s\n' "$*" >&2; exit 1; }

MODE="${1:-run}"
shift || true

BESTCF_ENABLED=false
case "$MODE" in
  run|extract|scan) ;;
  bestcf|bestcf-extract) BESTCF_ENABLED=true ;;
  shell|sh) exec /bin/sh "$@" ;;
  cfdata) exec /usr/local/bin/cfdata "$@" ;;
  extractor) exec /usr/local/bin/cfdata-extract "$@" ;;
  -*) exec /usr/local/bin/cfdata "$MODE" "$@" ;;
  *) printf '[cfdata] 未知模式: %s（可用: run / bestcf / extract / bestcf-extract / scan / shell / cfdata / extractor）\n' "$MODE" >&2; exit 64 ;;
esac

# 输出文件名去掉扩展名，最终统一产出 <name>.txt / <name>.csv
CF_OUT="${CF_OUT%.txt}"
CF_OUT="${CF_OUT%.csv}"

is_uint() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
  esac
  return 0
}

is_uint "$CF_PORT" || die "CF_PORT 必须是整数: $CF_PORT"
[ "$CF_PORT" -ge 1 ] && [ "$CF_PORT" -le 65535 ] || die "CF_PORT 必须在 1-65535: $CF_PORT"
is_uint "$CF_MAX_CIDR" || die "CF_MAX_CIDR 必须是整数: $CF_MAX_CIDR"
is_uint "$CF_THREADS" && [ "$CF_THREADS" -gt 0 ] || die "CF_THREADS 必须是正整数: $CF_THREADS"
is_uint "$CF_SCAN_RESULT_LIMIT" && [ "$CF_SCAN_RESULT_LIMIT" -gt 0 ] || die "CF_SCAN_RESULT_LIMIT 必须是正整数: $CF_SCAN_RESULT_LIMIT"
is_uint "$CF_SPEED_LIMIT" && [ "$CF_SPEED_LIMIT" -gt 0 ] || die "CF_SPEED_LIMIT 必须是正整数（0 会关闭测速）: $CF_SPEED_LIMIT"
is_uint "$CF_SPEED_THREADS" || die "CF_SPEED_THREADS 必须是整数: $CF_SPEED_THREADS"
[ "$CF_SPEED_THREADS" -ge 1 ] || die "CF_SPEED_THREADS 必须 ≥1: $CF_SPEED_THREADS"
is_uint "$CF_DELAY" || die "CF_DELAY 必须是整数: $CF_DELAY"
case "$CF_SPEED_MIN" in
  ''|*[!0-9.]*) die "CF_SPEED_MIN 必须是数字（MB/s）: $CF_SPEED_MIN" ;;
esac

mkdir -p "$CF_WORKDIR"
cd "$CF_WORKDIR"

# 首次启动时生成 CLI 配置（cfdata 找不到配置时只写模板然后退出，所以先生成好）
CFG="$CF_WORKDIR/cfdata-config.json"
if [ ! -f "$CFG" ]; then
  log "初始化 CLI 配置: $CFG"
  /usr/local/bin/cfdata -cli -nocolor -config "$CFG" -mode nsb >/dev/null 2>&1 || true
  [ -f "$CFG" ] || die "生成 CLI 配置失败: $CFG"
fi

INPUT=/tmp/a.txt
if [ "$MODE" = "scan" ]; then
  INPUT="$CF_WORKDIR/a.txt"
  [ -s "$INPUT" ] || die "scan 模式需要已存在的 $INPUT（先执行 extract 或 run）"
  log "跳过提取，直接使用 $INPUT"
else
  SRC_ARG=""
  if [ "$BESTCF_ENABLED" = "true" ]; then
    if [ -f "$CF_SOURCES" ]; then
      SRC_ARG="$CF_SOURCES"
      log "输入: bestcf 优选清单 + 附加来源 $CF_SOURCES"
    else
      log "输入: bestcf 优选清单（页面 ${CF_BESTCF_URL}，缓存 ${CF_BESTCF_TTL}）"
    fi
  else
    [ -f "$CF_SOURCES" ] || die "找不到来源清单 $CF_SOURCES，请挂载：-v ./sources.txt:/config/sources.txt:ro（或使用 bestcf 模式）"
    SRC_ARG="$CF_SOURCES"
    log "读取来源清单: $CF_SOURCES（单网段上限 ${CF_MAX_CIDR}，缺省端口 ${CF_PORT}）"
  fi
  /usr/local/bin/cfdata-extract \
    -bestcf="$BESTCF_ENABLED" \
    -bestcf-url "$CF_BESTCF_URL" \
    -bestcf-ttl "$CF_BESTCF_TTL" \
    -cache-dir "$CF_BESTCF_CACHE" \
    -sources "$SRC_ARG" \
    -out "$INPUT" \
    -max-cidr "$CF_MAX_CIDR" \
    -port "$CF_PORT" || die "地址提取失败（退出码 $?）"
  TOTAL=$(wc -l < "$INPUT" | tr -d ' ')
  [ "$TOTAL" -gt 0 ] || die "未提取到任何地址"
  cp -f "$INPUT" "$CF_WORKDIR/a.txt"
  log "提取完成: $TOTAL 条地址 -> $INPUT（副本 $CF_WORKDIR/a.txt）"

  if [ "$MODE" = "extract" ] || [ "$MODE" = "bestcf-extract" ]; then
    log "仅提取模式，输出地址清单:"
    cat "$INPUT"
    exit 0
  fi
  if [ "$TOTAL" -gt 3000 ]; then
    warn "地址数量较多（$TOTAL 条），扫描与测速耗时可能较长"
  fi
fi

if [ "$CF_DELAY" = "0" ]; then
  DELAY_DESC="不限"
else
  DELAY_DESC="≤${CF_DELAY}ms"
fi
log "开始非标扫描：TLS=on 并发=${CF_THREADS} 延迟${DELAY_DESC} 结果上限=${CF_SCAN_RESULT_LIMIT}"
log "测速地址: ${CF_SPEED_URL}"
log "合格条件: 延迟${DELAY_DESC} 且 ≥${CF_SPEED_MIN}MB/s，达标上限=${CF_SPEED_LIMIT} 条，测速线程=${CF_SPEED_THREADS}"

DNS_ARG=""
if [ -n "$CF_DNS" ]; then
  DNS_ARG="-dns $CF_DNS"
fi

# ---------- 地区筛选 ----------
# 地区码先展开成机房码集合：
#   单机房（HK->HKG、SG->SIN、KR->ICN…）直接用 cfdata 的 -nsbdc 在扫描期过滤，单轮完成；
#   多机房（JP->4 个、US->47 个）无法用 -nsbdc 表达，走两阶段：全量扫描拿 colo -> 过滤 -> 定向测速。
NSB_DC_ARG=""
if [ -n "$CF_REGION" ]; then
  REGION_INPUT="/tmp/region-input.txt"
  REGION_LOG="/tmp/region-colo.log"
  REGION_COLOS=$(/usr/local/bin/cfdata-extract \
    -region "$CF_REGION" \
    -locations "$CF_WORKDIR/locations.json" \
    -locations-url "${CF_LOCATIONS_URL:-https://www.baipiao.eu.org/cloudflare/locations}" \
    -cache-dir "$CF_BESTCF_CACHE" 2>"$REGION_LOG") || {
      cat "$REGION_LOG" >&2
      die "地区码解析失败，请检查 CF_REGION（见上方提示）"
    }
  cat "$REGION_LOG" >&2
  COLO_COUNT=$(printf '%s\n' $REGION_COLOS | sed '/^$/d' | wc -l | tr -d ' ')
  [ "$COLO_COUNT" -ge 1 ] || die "地区码 ${CF_REGION} 没有对应任何机房"

  if [ "$COLO_COUNT" -eq 1 ]; then
    NSB_DC_ARG="-nsbdc $REGION_COLOS"
    log "地区 ${CF_REGION} -> 机房 ${REGION_COLOS}（单机房），扫描时直接按机房筛选"
  else
    rm -f "$CF_WORKDIR/$CF_SCAN_ALL" "$CF_WORKDIR/$CF_SCAN_ALL.csv" "$CF_WORKDIR/$CF_SCAN_ALL.txt" "$REGION_INPUT"
    log "地区 ${CF_REGION} -> ${COLO_COUNT} 个机房（$REGION_COLOS），阶段 1/2 先全量扫描获取机房归属（地址多时需要几分钟）"
    STAGE1_LOG="/tmp/stage1-scan.log"
    set +e
    /usr/local/bin/cfdata -cli \
      -mode nsb \
      -nsbfile "$INPUT" \
      -nsbtls=true \
      -nsbfallbackport "$CF_PORT" \
      -nsbthreads "$CF_THREADS" \
      -nsbresultlimit 100000 \
      -nsbdelay "$CF_DELAY" \
      -nsbspeedtest 0 \
      -relink=false \
      $DNS_ARG \
      -skipgeo \
      -nocolor \
      -out "$CF_SCAN_ALL" \
      -outformat csv \
      -outfields compact \
      -outqualified all \
      -outstartrow 1 \
      -outendrow 0 \
      -progress=false \
      -config "$CFG" > "$STAGE1_LOG" 2>&1
    RC=$?
    set -e
    if [ "$RC" -ne 0 ]; then
      tail -20 "$STAGE1_LOG" >&2 || true
      die "阶段 1 扫描失败（退出码 $RC，完整日志 $STAGE1_LOG）"
    fi
    # 逐条 scan-result 太啰嗦：只回显汇总行，完整日志留在 /tmp/stage1-scan.log
    grep -v "scan-result" "$STAGE1_LOG" 2>/dev/null | tail -5 >&2 || true

    # -outformat csv 会额外写一份同名无扩展名文件，只保留 .csv 供查看/过滤
    SCAN_ALL_CSV="$CF_WORKDIR/$CF_SCAN_ALL.csv"
    if [ -f "$SCAN_ALL_CSV" ]; then
      rm -f "$CF_WORKDIR/$CF_SCAN_ALL"
    else
      SCAN_ALL_CSV="$CF_WORKDIR/$CF_SCAN_ALL"
    fi
    [ -f "$SCAN_ALL_CSV" ] || die "阶段 1 没有产出扫描结果（地址可能全部不可用）"
    rm -f "$CF_WORKDIR/$CF_SCAN_ALL.txt"

    /usr/local/bin/cfdata-extract \
      -filter-csv "$SCAN_ALL_CSV" \
      -region "$CF_REGION" \
      -locations "$CF_WORKDIR/locations.json" \
      -locations-url "${CF_LOCATIONS_URL:-https://www.baipiao.eu.org/cloudflare/locations}" \
      -cache-dir "$CF_BESTCF_CACHE" \
      -out "$REGION_INPUT" || die "地区码解析或过滤失败，请检查 CF_REGION（见上方提示）"

    HIT=$(wc -l < "$REGION_INPUT" | tr -d ' ')
    if [ "$HIT" -eq 0 ]; then
      warn "地区 ${CF_REGION} 下没有可用地址（全量扫描结果保留在 $SCAN_ALL_CSV，可查看实际机房分布）"
      if [ "$CF_ALLOW_EMPTY" = "1" ]; then
        exit 0
      fi
      exit 2
    fi
    log "阶段 1/2 完成：命中地区地址 $HIT 条，阶段 2/2 只对这些地址扫描测速"
    INPUT="$REGION_INPUT"
  fi
fi

# 清掉上一轮结果，避免本轮 0 条时误读旧文件
rm -f "$CF_WORKDIR/$CF_OUT" "$CF_WORKDIR/$CF_OUT.txt" "$CF_WORKDIR/$CF_OUT.csv"

set +e
/usr/local/bin/cfdata -cli \
  -mode nsb \
  -nsbfile "$INPUT" \
  -nsbtls=true \
  -nsbfallbackport "$CF_PORT" \
  -nsbthreads "$CF_THREADS" \
  -nsbresultlimit "$CF_SCAN_RESULT_LIMIT" \
  -nsbdelay "$CF_DELAY" \
  -nsbspeedtest "$CF_SPEED_THREADS" \
  -nsbspeedmin "$CF_SPEED_MIN" \
  -nsbspeedlimit "$CF_SPEED_LIMIT" \
  -nsburl "$CF_SPEED_URL" \
  -relink="$CF_RELINK" \
  $NSB_DC_ARG \
  $DNS_ARG \
  -skipgeo \
  -nocolor \
  -out "$CF_OUT" \
  -outformat txt \
  -outfields "$CF_OUT_FIELDS" \
  -outqualified qualified \
  -outstartrow 1 \
  -outendrow "$CF_SPEED_LIMIT" \
  -config "$CFG"
RC=$?
set -e
[ "$RC" -eq 0 ] || die "cfdata 运行失败（退出码 $RC）"

# cfdata 会同时写 <name>（全量 CSV）与 <name>.txt（CLI 导出）；归并成清晰的两个文件
if [ -f "$CF_OUT" ]; then
  mv -f "$CF_OUT" "$CF_OUT.csv"
fi

TXT="$CF_WORKDIR/$CF_OUT.txt"
if [ ! -f "$TXT" ]; then
  if [ -f "$CF_OUT.csv" ]; then
    warn "扫描到候选地址，但没有达到 ${CF_SPEED_MIN}MB/s 的地址（全量扫描结果见 $CF_OUT.csv）"
  else
    warn "扫描未产生任何结果（地址可能全部不可用）"
    if [ -n "$CF_REGION" ] && [ -n "${NSB_DC_ARG:-}" ]; then
      warn "当前地区筛选为 ${CF_REGION}（机房 ${REGION_COLOS:-}），也可能是这批地址都没有落到该机房"
    fi
  fi
  if [ "$CF_ALLOW_EMPTY" = "1" ]; then
    exit 0
  fi
  exit 2
fi

ROWS=$(tr -d '\357\273\277' < "$TXT" | awk 'NF{c++} END{print c+0}')
if [ "$ROWS" -eq 0 ]; then
  warn "没有达到 ${CF_SPEED_MIN}MB/s 的地址（全量扫描结果见 $CF_OUT.csv）"
  if [ "$CF_ALLOW_EMPTY" = "1" ]; then
    exit 0
  fi
  exit 2
fi

log "达标地址（延迟${DELAY_DESC} 且 ≥${CF_SPEED_MIN}MB/s）共 $ROWS 条："
tr -d '\357\273\277' < "$TXT" | sed '/^[[:space:]]*$/d'
log "结果文件: $TXT / $CF_OUT.csv"
