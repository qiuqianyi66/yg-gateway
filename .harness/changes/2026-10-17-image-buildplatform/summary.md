# 变更记录 — 镜像构建阶段挪到 BUILDPLATFORM（B5，L2）

- 日期：2026-10-17
- 分级：**L2**（改的是发布产物的构建方式；镜像本身是发布产物）
- 需求：`docs/prd/REQ-image-buildplatform-2026-10-17.md`
- 计划：`docs/PLAN-open-items-2026-10-17.md` §10.2 B5
- 评审：`review.md`（同目录）

## 一、改了什么

`Dockerfile` 的构建阶段（两处，无其它改动）：

| 改动 | 前 | 后 |
|---|---|---|
| 构建阶段平台 | `FROM golang:1.27-alpine AS build` | `FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build` |
| 编译目标 | `CGO_ENABLED=0 go build …`（×4） | `CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build …`（×4，新增 `ARG TARGETOS` / `ARG TARGETARCH`） |

`release.yml`、`platforms`、tag 规则、`push` 条件、`publish` job、运行时镜像层**一行没动**。

## 二、为什么（实测数据）

同一次运行（run `39000655780`）的 job 耗时：

```
test             100s
gate / gate      101s
race             107s
build-image      239s   ← 单独构成关键路径
```

根因不是「双架构」，而是构建阶段没走 `BUILDPLATFORM`：构建 `linux/arm64` 时
`go mod download` 与整段 `go build` 都在 **QEMU 模拟**下执行。改成在构建机架构上交叉编译即可。

## 三、怎么验证的

### 3.1 本地实测：交叉编译成立（能验的那部分）

```
GOOS=linux GOARCH=amd64 → exit 0
GOOS=linux GOARCH=arm64 → exit 0
两次交叉编译合计 34.6s
yg-gateway-amd64  10,176.2 KB   ELF e_machine=0x3E (x86-64)
yg-gateway-arm64   9,472.2 KB   ELF e_machine=0xB7 (aarch64)
```
ELF 头是硬证据：两个产物确实是**不同架构**的可执行文件，不是把 amd64 换个名字。

### 3.2 本地**没有**验证的部分（明说）

**镜像构建本身没有在本地跑过**：本机 `docker` CLI 存在但 **daemon 未运行**
（`npipe:////./pipe/dockerDesktopLinuxEngine` 连不上）。所以：
- Dockerfile 语法、`ARG` 注入、`COPY --from=build` 跨平台拷贝，全部由 **CI 的 `build-image`** 验证；
- 若改错，表现是 `build-image` 红（构建失败即不推送），不会静默产出错架构镜像；
- 本变更的验收因此以「下一次 push 的 `build-image` 通过 + 耗时下降」为准，见 §六。

### 3.3 为什么没选更便宜的方案

「PR 只造 amd64、main/tag 才双架构」改动更小（2 行 YAML），但把 **arm64 的构建验证从 PR 期挪到
merge 之后**——arm64 独有的问题（基础镜像、`apk` 包、层架构）只能在 main 上暴露。
本仓的立场是「门禁不许为省时间而放宽」，所以否决；本方案**不减少任何验证**。

## 四、影响分析

| 面 | 影响 |
|---|---|
| 产物 | 两架构镜像照旧产出；二进制架构、编译参数（`-trimpath -s -w`、`CGO_ENABLED=0`）不变 |
| 验证强度 | **不降低**（PR 与 main 都造两架构） |
| 时间 | 预期 `build-image` 从 239s 降到 ~100s 量级（模拟编译消失；`apk add` 仍需 QEMU，只占几秒） |
| 缓存 | `cache-from/to: type=gha` 未改；`go mod download` 层仍在最前，缓存命中率不变 |

## 五、怎么回滚

```powershell
git revert <本提交>
```
回滚后 arm64 重新走模拟编译（慢但正确）。镜像侧无数据迁移、无状态。

## 六、CI 实测（回填）

见下方「§六 回填」——用本提交 push 后的 `build-image` 结果与耗时填。
