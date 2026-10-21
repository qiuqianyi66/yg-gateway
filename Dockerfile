# syntax=docker/dockerfile:1
# 基础镜像必须 ≥ go.mod 要求的版本：低版本带 stdlib CVE（HTTP/2 + TLS），且官方 golang
# 镜像设了 GOTOOLCHAIN=local——基础镜像落后于 go.mod 时 `go mod download` 直接报错
# 拒绝继续（2026-10-21 实证：go.mod 升 1.27.3 修 GO-2026-6809/x509，浮动标签
# `golang:1.27-alpine` 的更新滞后一个补丁号，就足以把镜像构建打红）。
# GOTOOLCHAIN=auto：允许「浮动标签的基础镜像略旧于 go.mod」，缺的补丁由 go 命令在
# 构建容器内自动获取（CI 有网络），产物仍由 go.mod 要求的工具链构建——安全补丁的
# 生效不依赖 Docker Hub 的更新 timing。
# 构建阶段跑在**构建机架构**上（$BUILDPLATFORM），再用 GOOS/GOARCH 交叉编译到目标架构。
# 为什么（2026-10-17，REQ-image-buildplatform / PLAN B5）：原来是 `FROM golang:1.27-alpine`，
# 构建 linux/arm64 时把 `go mod download` 与**整段 go build 都放进 QEMU 模拟**执行——实测
# build-image 239s，是 test/gate/race（100–107s）的 2.4 倍，且它单独构成 PR 反馈的关键路径。
# CGO_ENABLED=0 使交叉编译成立（本地实测两架构合计 34.6s；ELF e_machine 分别 0x3E / 0xB7）。
# 注意：这里**不**砍掉 arm64 验证——不能用「PR 只造 amd64」省时间（那会把 arm64 的构建问题
# 推到 merge 之后才暴露），只是去掉了模拟编译的开销。
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ENV GOTOOLCHAIN=auto
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# 一次编译全部二进制（工具进镜像，容器内可直接跑脚本）。全部 -trimpath -s -w。
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/yg-gateway ./cmd/server \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/signin_bin ./cmd/signin \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/login ./cmd/login \
 && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/credit ./cmd/credit
# 架构断言（独立复审 🟡，2026-10-17）：交叉编译把「产物架构正确」从**结构性保证**变成了
# 「依赖 $TARGETOS/$TARGETARCH 两个变量」——变量为空时 `go build` **静默产出宿主架构**，
# 构建依然成功，于是 arm64 镜像里装 amd64 二进制，生产跑起来才 `exec format error`。
# 改动前这种错误不可能发生（stage 平台 = 目标平台），所以这条断言是必要成本。
# 用 `go version -m`：构建阶段自带 go，不需要 od/readelf/hexdump（实测输出含 `GOARCH=arm64`）。
RUN set -eu; \
    for f in /out/*; do \
      info=$(go version -m "$f"); \
      echo "$info" | grep -q "GOOS=$TARGETOS"   || { echo "架构断言失败(OS)：$f 不是 $TARGETOS"; echo "$info"; exit 1; }; \
      echo "$info" | grep -q "GOARCH=$TARGETARCH" || { echo "架构断言失败(ARCH)：$f 不是 $TARGETARCH"; echo "$info"; exit 1; }; \
    done

FROM alpine:3.20
# python3：login.sh 的 JSON 解析 / 签到 / 落盘；bash：shell 脚本体。
RUN apk add --no-cache wget ca-certificates tzdata python3 bash \
 && adduser -D -u 10001 app \
 && mkdir -p /app/auths /app/data \
 && chown -R app:app /app
WORKDIR /app
# 脚本置入 + 去 CRLF（Windows 检出可能性）在切到 app 之前以 root 完成——
# app 对 root 所有文件无写权限，sed -i 需要写权限。
COPY --from=build /out/yg-gateway /app/yg-gateway
COPY --from=build /out/signin_bin /app/signin_bin
COPY --from=build /out/login /app/login
COPY --from=build /out/credit /app/credit
COPY login.sh signin.sh credit.sh /app/
COPY scripts/probe_active.py /app/scripts/probe_active.py
RUN sed -i 's/\r$//' /app/login.sh /app/signin.sh /app/credit.sh && chmod 755 /app/login.sh /app/signin.sh /app/credit.sh
# 镜像不带真实配置：落 example 作为默认（生产由挂载卷 /app/config.json 覆盖）
COPY config.example.json /app/config.json
USER app
EXPOSE 7863
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:7863/healthz || exit 1
ENTRYPOINT ["/app/yg-gateway", "-config", "/app/config.json"]