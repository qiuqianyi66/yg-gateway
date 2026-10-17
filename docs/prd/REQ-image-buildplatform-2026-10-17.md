# REQ — 镜像构建的阶段挪到 BUILDPLATFORM（B5）

- 状态：已确认（用户 2026-10-17 批准 PLAN「本周」批次，B5 为其中一项）
- 日期：2026-10-17
- 分级：**L2**（改的是发布产物的构建方式；镜像本身就是发布产物）
- 来源：`docs/PLAN-open-items-2026-10-17.md` §10.2 B5

## 一、必要性（阶段 0）

**为什么现在做**：`build-image` 实测 **239s**，而同一次运行的 `test` 100s / `gate` 101s / `race` 107s。
它单独构成 PR 反馈的**关键路径**（其余 job 并行约 1.7 分钟就完事，然后还要等它 4 分钟）。
PR 迭代频率越高，这笔等待越贵。

**根因不是「双架构」本身**：`Dockerfile` 的构建阶段是 `FROM golang:1.27-alpine`，没有
`--platform=$BUILDPLATFORM`。于是构建 `linux/arm64` 时，**`go mod download` 与整段 `go build`
都在 QEMU 模拟下执行**。真正贵的是模拟编译，不是「多造一个架构」。

**不做会怎样**：每次 PR/push 继续多等 2–4 分钟；且这个等待被误认为是「多架构的必然代价」，
下一个人很可能会选「PR 只造 amd64」这个更糟的解法（见 §二 非目标）。

**更便宜但更该否决的方案**：PR 只造 amd64（`platforms: linux/amd64`）。
否决理由：它把 arm64 的验证从 PR 期挪到 merge 之后——arm64 独有的构建问题（基础镜像、
依赖、`apk` 包）只能在 main 上暴露，与本仓「门禁不许为了省时间而放宽」的原则冲突。
本方案**不减少任何验证**，只去掉模拟开销。

## 二、目标与非目标

**目标**：`docker buildx` 构建 `linux/arm64` 时，Go 编译在**构建机架构**上交叉完成；
两架构的验证范围与现在完全一致（PR 与 main 都造两架构）。

**非目标（明确不做）**：

- 不在 PR 上砍掉 arm64（理由见必要性段）；
- 不改 `platforms` 列表、不改 tag 规则、不改 `push` 条件、不动 `publish` job；
- 不动运行时镜像的基础层（`alpine:3.20` + `apk add`）——它仍需 QEMU，但只占几秒；
- 不改 `go build` 的编译参数（`-trimpath -s -w`、`CGO_ENABLED=0` 原样保留）。

## 三、设计

```dockerfile
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
...
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build ... ./cmd/server \
 && ...（四个二进制同样处理）
```

- `$BUILDPLATFORM` 让构建阶段跑在构建机架构（amd64）；`GOOS/GOARCH` 指向 `$TARGETOS/$TARGETARCH`
  做交叉编译。`CGO_ENABLED=0` 是交叉编译成立的前提（本仓全静态、go.mod 只有 redis 一个纯 Go 依赖）。
- 最终阶段 `FROM alpine:3.20` 仍是**目标架构**（buildx 按 `platforms` 逐架构构建），运行时镜像不变。
- 产物等价性：交叉编译与「在模拟的目标架构上原生编译」输出同架构、同编译参数的二进制；
  `-trimpath` 保证路径无关。

## 四、验收（Given-When-Then）

**F-B5-a 两架构仍被验证**
- Given 任意触发（PR / push main / tag）
- When `build-image` 跑
- Then `platforms` 仍是 `linux/amd64,linux/arm64`，两架构都构建成功

**F-B5-b 镜像内容仍是对应架构的二进制**
- Given 构建完成
- When 检查镜像内 `/app/yg-gateway`
- Then 每个架构的镜像里都是该架构的可执行文件（arm64 镜像不是 amd64 二进制）

**F-B5-c 墙钟下降**
- Given 同样的源码
- When 对比改动前后 `build-image` 的耗时
- Then 明显下降（预期从 239s 降到 ~100s 量级；以 CI 实测为准）

## 五、影响分析

| 面 | 影响 |
|---|---|
| 产物 | 两架构镜像仍产出；二进制的架构与编译参数不变 |
| 验证强度 | **不降低**（不砍 arm64） |
| 本地可验证性 | 本机 Docker daemon 未运行 → 镜像构建只能由 CI 验证；交叉编译部分已在本地实测（见 summary §三） |
| 失败模式 | 若改错，`build-image` 会红（构建失败即不推送），不会静默产出错架构镜像 |

## 六、实测证据

见 `.harness/changes/2026-10-17-image-buildplatform/summary.md`。
