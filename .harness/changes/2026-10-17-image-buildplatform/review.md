# 评审记录 — 镜像构建挪到 BUILDPLATFORM（B5，L2）

- 变更：`Dockerfile`（构建阶段 `FROM --platform=$BUILDPLATFORM` + `GOOS/GOARCH` 交叉编译）
- 需求：`docs/prd/REQ-image-buildplatform-2026-10-17.md`　记录：`summary.md`（同目录）
- 评审方式：第一轮自评（六维）；第二轮并入本轮「B1a + B5」的整体独立复审（见 §三）

---

## 一、结论

**通过**（🔴 无、🟡 无）——但**验证是分开的**：交叉编译部分本地实测；镜像构建只能由 CI 验证，
本文件与 summary 都明确写了这一点，不把「本地没验」写成「已验证」。

---

## 二、第一轮：六维自评

### 1. 正确性

| 判据 | 证据 / 推理 |
|---|---|
| 交叉编译成立 | 本地实测：`GOOS=linux GOARCH=amd64` 与 `arm64` 均 exit 0；ELF `e_machine` = `0x3E` / `0xB7`（硬证据，不是改名字） |
| 产物等价 | 编译参数一字未改（`-trimpath -s -w`、`CGO_ENABLED=0`）；`-trimpath` 保证路径无关。交叉编译与「模拟目标架构后原生编译」产出同架构同参数二进制 |
| 最终阶段仍是目标架构 | `FROM alpine:3.20` 未加 `--platform` → buildx 按 `platforms` 逐架构构建，运行时层是目标架构 |
| 不会静默产出错架构镜像 | buildx 逐平台构建；若 `COPY --from` 取错架构，镜像起不来（运行时报 `exec format error`），而 CI 的 build 失败会让它红 |

> **⚠ 上表最后一行在第二轮复审里被推翻，已修**：交叉编译把「产物架构正确」从**结构性保证**
> （stage 平台 = 目标平台）变成「依赖 `$TARGETOS/$TARGETARCH` 两个变量」。复审实测：
> `GOOS="" GOARCH=""` 调 `go build` **exit 0 并静默产出宿主架构**——所以若 `ARG` 被删/改名，
> arm64 镜像里会装 amd64 二进制、构建依然成功、CI 不红，生产跑起来才 `exec format error`。
> 已在构建阶段末尾加**架构断言**（`go version -m` 读二进制里的 GOOS/GOARCH，构建阶段自带 go，
> 不需要 od/readelf），并本地验过正反两向：错架构会被咬住并 exit 1，正架构通过。

### 2. 性能

`go mod download` 与 4 次 `go build` 从「QEMU 模拟的目标架构」变成「构建机原生 + 交叉编译」。
本地两次交叉编译 34.6s（真实 CI 里还有模块下载与层缓存，量级可比）。预期 `build-image` 从 239s
降到 ~100s 量级；`apk add` 仍需 QEMU（几秒），不消除。

### 3. 安全

- 不新增凭证、不新增网络访问、不改 `permissions`。
- 基础镜像版本未变（仍是 `golang:1.27-alpine` / `alpine:3.20`），不引入新 CVE 面。
- 仍 `CGO_ENABLED=0` → 静态二进制，交叉编译不引入 glibc 依赖差异。

### 4. 一致性

- 与 `REQ-image-buildplatform` §三、`summary.md` §一 三处描述一致。
- 与 `release.yml` 的 `platforms` 声明一致（两架构仍被构建）。
- 没有引入「PR 只造 amd64」这条与门禁原则冲突的捷径。

### 5. 可维护性

- 改动点集中在 `Dockerfile` 前 14 行，且**为什么**写在注释里（含 239s vs 100s 的实测与
  「为什么不用更便宜的方案」），下一个读者不需要重新推导。
- 交叉编译的 `GOOS/GOARCH` 显式写出（而不是依赖 `ARG` 的隐式继承），4 个二进制逐一可见。

### 6. 架构合规

- 未触碰 R1/R2/R5/R6/R8/R9/R10/R14/R15；不改任何 Go 源码。
- 单二进制零依赖的定位不受影响（镜像里仍是静态二进制 + alpine 运行时）。

---

## 三、第二轮：整体独立复审

见本轮整体复审结论（覆盖 B1a 与 B5）。评审对象为提交 `7bf974f`（B1a）与 B5 提交。
