# syntax=docker/dockerfile:1
#
# CFData-WEB 非标地址扫描镜像
#   - 以 GitHub Release 制品（cfdata-linux-<arch>）为核心，不编译上游源码
#   - 自研 cfdata-extract 负责抓取来源（含 bestcf 优选清单）、展开网段、去重、生成 a.txt
#   - 最终镜像仅 alpine + 两个静态二进制，无额外运行时依赖
#
# 构建：
#   docker build -t cfdata-web:1.8.5 .
#   docker build --build-arg CFDATA_VERSION=v1.8.5 --build-arg CFDATA_MIRROR= -t cfdata-web:1.8.5 .
#   docker buildx build --platform linux/amd64,linux/arm64 -t cfdata-web:1.8.5 --push .
#
# 说明：CFDATA_MIRROR 为 GitHub 加速前缀（默认 ghfast.top），设为空则直连 GitHub。

ARG CFDATA_VERSION=v1.8.5
ARG ALPINE_VERSION=3.21

# ---------- 1. 取回 release 制品并校验 ----------
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS release
ARG TARGETARCH
ARG CFDATA_VERSION
ARG CFDATA_MIRROR=https://ghfast.top/
WORKDIR /release

RUN set -eux; \
    apk add --no-cache curl ca-certificates; \
    bin="cfdata-linux-${TARGETARCH}"; \
    base="https://github.com/PoemMisty/CFData-WEB/releases/download/${CFDATA_VERSION}"; \
    ok=0; \
    for m in "${CFDATA_MIRROR}" ""; do \
        case "$m" in "") : ;; *) m="${m%/}/" ;; esac; \
        url="${m}${base}/${bin}"; \
        echo "==> download ${url}"; \
        if curl -fsSL --connect-timeout 8 --max-time 240 --retry 1 -o "/release/${bin}" "$url"; then ok=1; break; fi; \
        rm -f "/release/${bin}"; \
    done; \
    [ "$ok" -eq 1 ] || { echo "release 制品下载失败"; exit 1; }; \
    ok=0; \
    for m in "${CFDATA_MIRROR}" ""; do \
        case "$m" in "") : ;; *) m="${m%/}/" ;; esac; \
        url="${m}${base}/${bin}.sha256"; \
        if curl -fsSL --connect-timeout 8 --max-time 60 --retry 1 -o /release/sum.txt "$url"; then ok=1; break; fi; \
    done; \
    [ "$ok" -eq 1 ] || { echo "校验文件下载失败"; exit 1; }; \
    expect="$(tr -d ' \r\n\t' < /release/sum.txt)"; \
    actual="$(sha256sum "/release/${bin}" | cut -d' ' -f1)"; \
    [ "$expect" = "$actual" ] || { echo "sha256 校验失败：expect=${expect} actual=${actual}"; exit 1; }; \
    mv "/release/${bin}" /release/cfdata; \
    chmod 0755 /release/cfdata; \
    rm -f /release/sum.txt

# ---------- 2. 编译地址提取器（纯标准库，无外部依赖） ----------
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETARCH
WORKDIR /src
COPY extractor/go.mod ./
# 用通配符整目录拷贝：extractor 下新增 .go 文件时不必再改这里
COPY extractor/*.go ./
RUN set -eux; \
    export CGO_ENABLED=0 GOOS=linux GOARCH="${TARGETARCH}" GOFLAGS=-mod=mod; \
    go build -trimpath -ldflags="-s -w" -o /out/cfdata-extract .

# ---------- 3. 运行镜像 ----------
FROM alpine:${ALPINE_VERSION}

LABEL org.opencontainers.image.title="cfdata-web" \
      org.opencontainers.image.description="CFData-WEB 非标地址扫描与测速（CLI）" \
      org.opencontainers.image.source="https://github.com/PoemMisty/CFData-WEB" \
      org.opencontainers.image.licenses="GPL-3.0-or-later"

# alpine 基础镜像已自带 ca-certificates-bundle（/etc/ssl/certs/ca-certificates.crt），
# 无需再装包；最终阶段不做任何 RUN，arm64 交叉构建也不需要 qemu。
COPY --chmod=0755 --from=release /release/cfdata /usr/local/bin/cfdata
COPY --chmod=0755 --from=build /out/cfdata-extract /usr/local/bin/cfdata-extract
COPY --chmod=0755 entrypoint.sh /usr/local/bin/entrypoint.sh
COPY config/sources.txt.example /usr/local/share/cfdata/sources.txt.example

ENV CF_SOURCES=/config/sources.txt \
    CF_WORKDIR=/data \
    CF_OUT=cfdata-results \
    CF_MAX_CIDR=4096 \
    CF_PORT=443 \
    CF_THREADS=100 \
    CF_SCAN_RESULT_LIMIT=1000 \
    CF_DELAY=100 \
    CF_SPEED_THREADS=2 \
    CF_SPEED_MIN=5 \
    CF_SPEED_LIMIT=20 \
    CF_SPEED_URL="https://speed.cloudflare.com/__down?bytes=50000000" \
    CF_RELINK=true \
    CF_OUT_FIELDS=compact \
    CF_DNS="" \
    CF_ALLOW_EMPTY=0 \
    CF_BESTCF_URL=https://bestcf.pages.dev/ \
    CF_BESTCF_TTL=3h \
    CF_BESTCF_CACHE=/data/.cache/bestcf \
    CF_REGION= \
    CF_EXCLUDE_LOC=

WORKDIR /data
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["run"]
