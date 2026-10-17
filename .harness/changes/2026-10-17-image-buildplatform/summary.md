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

提交 `bf5a4aa` 触发的 `release` 流水线（run **`39014207601`**）→ **`completed / success`**：

| job | 改动前（run `39000655780`） | 改动后（run `39014207601`） | 变化 |
|---|---|---|---|
| **build-image** | **239s** | **108s** | **−131s（−55%）** |
| race | 107s | 123s | +16s（噪声区间，与本改动无关） |
| gate / gate | 101s | 98s | −3s |
| test | 100s | 93s | −7s |
| 5×build | 28–37s | 28–37s | 不变 |
| publish | skipped | skipped | 一致 |

**结论**：

1. **两架构都构建成功**（`platforms: linux/amd64,linux/arm64` 未改）→ 验证强度没有降低，
   这是本方案相对「PR 只造 amd64」的核心优势。
2. `build-image` 不再是关键路径：改动前它是唯一显著超过其它 job 的（239s vs ~100s），
   改动后与 `race`（123s）同量级，流水线关键路径回到测试本身。
3. 预期「降到 ~100s 量级」成立（实测 108s）。
4. 风险面（arm64 因 `--platform` 语义出错而构建失败）**没有发生**；若发生，表现会是
   `build-image` 红且不推送，属可发现的失败。

## 七、第二轮复审驱动的修正（1 🟡 + 2 🟢）

| 级别 | 问题 | 修法 |
|---|---|---|
| **🟡** | **「改错会红 / 不会静默产出错架构镜像」是假保证**。复审实测：`GOOS="" GOARCH=""` 调 `go build` **exit 0 并静默产出宿主架构**。而本改动恰好把架构正确性从「结构性保证（stage 平台=目标平台）」变成「依赖两个环境变量」——`ARG` 被删/改名就会静默错架，构建成功、CI 不红，生产跑起来才 `exec format error`。**这是本改动新引入的唯一失败面，而它原本没有任何断言** | 构建阶段末尾加**架构断言**：`go version -m "$f"` 读二进制内嵌的 `GOOS`/`GOARCH` 并与 `$TARGETOS/$TARGETARCH` 比对，不符即 `exit 1`。选 `go version -m` 而不是 `od`/`readelf`/`hexdump`：构建阶段自带 go，无需额外工具（复审对 busybox `od` 是否可用标了「待确认」）。**本地验过正反两向**：`TARGETARCH=arm64` 对 amd64 产物 → 「架构断言失败」+ exit 1；各自匹配 → 通过 |
| 🟢 | `summary`/`review` 把「本地没验镜像构建」与「CI 会红」混着写，容易被读成「已保证」 | 改为显式区分：交叉编译本地实测；**镜像构建只能由 CI 验证**；错架构现在由断言咬住（不再靠「会红」这种口头保证） |

**保留的未知（复审点明）**：arm64 镜像里的二进制到底是不是 arm64，仍未 pull 镜像查 ELF（本机无 daemon）。
现在多了一层保障——构建阶段自己断言了产物架构，所以「镜像里装错架构」这条路已从「静默」变成「构建即失败」。

## 八、加上架构断言后的 CI 复核（run `39014873155`，提交 `435dae8`）

**`completed / success`**，`build-image` **96s**（比加断言前的 108s 还略快，属噪声；相对基线 239s −60%）。

**断言确实在容器里、且对两个架构都执行了**（日志原文，注意 `linux/amd64->arm64` 这个标记正是
「构建阶段跑在 amd64、产出 arm64」的直接证据）：

```
#24 [linux/amd64->arm64 build 7/7] RUN set -eu; for f in /out/*; do info=$(go version -m "$f"); …
      echo "$info" | grep -q "GOARCH=arm64" || { echo "架构断言失败(ARCH)：$f 不是 arm64"; … }
#35 [linux/amd64         build 7/7] RUN … grep -q "GOARCH=amd64" …
```

两层都 success ⇒ 断言对 `yg-gateway` / `signin_bin` / `login` / `credit` 四个产物都通过了。

> 读日志的一个坑（记下来防误读）：`grep '架构断言失败'` 会命中**命令文本本身**（RUN 行里就写着这句话），
> 但那只说明断言存在，不说明它触发了。判断是否触发要看该层是否 `success`——两层都 success。

