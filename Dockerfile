# syntax=docker/dockerfile:1
# 基础镜像必须 ≥ go.mod 要求的 1.27.2：低版本带 9 个 stdlib CVE（HTTP/2 + TLS），
# 且 GOTOOLCHAIN=local 时 go mod download 直接报错拒绝继续（见 go.mod 的 go 指令）。
# 构建阶段跑在**构建机架构**上（$BUILDPLATFORM），再用 GOOS/GOARCH 交叉编译到目标架构。
# 为什么（2026-10-17，REQ-image-buildplatform / PLAN B5）：原来是 `FROM golang:1.27-alpine`，
# 构建 linux/arm64 时把 `go mod download` 与**整段 go build 都放进 QEMU 模拟**执行——实测
# build-image 239s，是 test/gate/race（100–107s）的 2.4 倍，且它单独构成 PR 反馈的关键路径。
# CGO_ENABLED=0 使交叉编译成立（本地实测两架构合计 34.6s；ELF e_machine 分别 0x3E / 0xB7）。
# 注意：这里**不**砍掉 arm64 验证——不能用「PR 只造 amd64」省时间（那会把 arm64 的构建问题
# 推到 merge 之后才暴露），只是去掉了模拟编译的开销。
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
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