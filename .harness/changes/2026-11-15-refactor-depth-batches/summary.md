# 2026-11-15-refactor-depth-batches 变更摘要

需求：`docs/prd/REQ-2026-11-15-refactor-depth-batches.md`
分级：L2（业务代码结构，对外契约与出站字节流零改动）
ADR：无（不满足三条件：改动可逆、无新权衡、不改变任何外部可见语义）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| 修改：`scripts/check-depth.ps1` | 度量 3（函数体量）边界改按**花括号深度**判定。新增 `Get-CodeOnly` 做最小词法剥离（`//` 行尾注释、`"..."`、`'...'`、`` `...` `` 跨行原生串、`/* */` 跨行块注释），避免 `"{"` 污染计数；无函数体的声明（`//go:linkname`）回落「下一个顶层声明」兜底。 |
| 修改：`.harness/rules/编码规范.md` | 删掉已漂移的断言「两种口径下 >1000 行的都是同样 5 个文件」（实测只剩 `upstream/sse.go`，现已 0 个），改成「行数与超限名单都是会漂的派生结论」。顺带把该段压短（常驻层 −67 token）。 |
| 修改：`cmd/server/config.go` | `normalize()` 218 → **27 代码行**，只保留顺序声明。文件 958 → 735 非空行。 |
| 新增：`cmd/server/config_normalize.go` | 9 个按配置段命名的 `normalize*` 方法（308 非空行）。**纯搬移**，逐段注释一并迁移。 |
| 修改：`internal/upstream/sse.go` | 移出流式透传全家（`Stream`/`StreamHint`/`StreamOption`/`With*`/`sseKeepAlive` 家族），文件 1041 → **630** 非空行；文件头注释改成「聚合路径与帧处理基础件」；随之删掉 `net/http`/`sync`/`sync/atomic` 三个无用 import。 |
| 新增：`internal/upstream/stream.go` | `streamRun` 结构体（字段按「装配期只读 / 心跳组 / 跨帧状态」三组归类）+ 13 个方法；`StreamHint` 退化为「装配 → 起心跳 → 读循环 → 收尾」四步（**23 代码行**）。483 非空行。 |
| 修改：`.harness/depth-baseline.json` | `-Update` 抬高棘轮（见下表）。 |

**没做的（明写，免得下轮重提）**：

- 不抽公共 `writeJSON`（`internal/panel/panel.go:988` 与 `internal/server/handler.go:907`
  逐字相同）：只有 2 处，未达「≥3 处稳定重复」判据；且抽出去要么新增包、要么让 `server`
  反向依赖 `panel`（红线）。
- 不合并两套登录协议（`cmd/login/main.go:88` 与 `internal/panel/login.go:92` 各一份
  `doJSON`/`commonHeaders`/`apiEnvelope`）：2 处 + 需新增包与边界 allow 表条目。
  **顺带记录的既有漂移**：`panel/login.go:106` 读 body 限 1 MiB，`cmd/login` 不限——两处
  已不同步，但本轮不动（超出批准范围）。
- 不再拆 `main()`（228 代码行）：上一轮已论证继续拆须把 30+ 局部变量变参数，收益为负。
- 不动 `internal/usage/usage.go:753 SnapshotWindow`(193)、`internal/upstream/sse.go:105
  Aggregate`(211)、`internal/panel/app.js`(1640 非空行，唯一 >1000 前端文件)。
- 不删 `internal/upstream/headers.go:381 ExtractClientIP`（生产零调用、仅测试引用，
  且是 `trusted_proxy.go:109` 标注的危险变体）：属独立死代码批次，未经批准不动。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 度量对拍 | 改前后各跑 `check-depth.ps1 -Detail` | `oversizeFuncs` 7 → 6，**仅** `CoolKind.String()` 消失；其余 6 项 260/228/218/211/193/180 逐位不变 |
| 归一化等价性 | 旧 `normalize()` 体 vs 新方法体，剥空行与 `//` 后逐行比对 | 旧 216 条代码行**全部**命中；差异只有函数签名/花括号/import/9 行编排调用/3 处 `var err`+`return nil` |
| 流式等价性 | `go test ./internal/upstream -count=1` | ok 3.0s（`sse_test.go` 1113 行 + `coverage_parse_test.go` 逐字节与金样断言） |
| 单包 | `go test ./cmd/server -count=1` | ok 0.6s |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出 |
| 静态检查 | `go vet ./...` | exit 0 |
| 全量测试 | `go test ./... -count=1`（30 包：25 ok + 5 no test files） | 全绿 |
| 依赖方向 | `check-boundaries.ps1` | ✅ 通过（无新增方向） |
| 深度棘轮 | `check-depth.ps1 -Update -Detail` | `maxFileLines 1041→997`、`oversizeFiles 1→0`、`oversizeFuncs 7→4`、`minCoverage 75→75.5` |
| 常驻上下文 | `check-context-budget.ps1` | 14767 / 14805（余 38，本改动净 −67） |
| 总闸 | `pre-commit.ps1 -Base fc54879` | 见文末「总闸结果」 |

**负向探针**（证明检查真的有牙齿，全部实跑）：

1. **度量修正的对象确实是那处边界**：用旧算法（`下一个 ^func` 前一行）与花括号算法对**同一份**
   代码各算一次 —— `pool.CoolKind.String()` 旧 = 155、新 = 8，其余 6 个超限函数数字逐位相同。
   即「差异只来自边界口径」，不是别的路径顺带变了。
2. **`Get-CodeOnly` 的剥离是承重的，不是保险**：把花括号计数改成「直接数原行」（不剥离字面量）
   重扫全仓生产代码，有且仅有 **1 个**函数体终点变化 ——
   `internal/upstream/taskmetrics.go:208 processLine`：剥离后正确收在 228 行（8 代码行），
   不剥离则深度永远回不到 0（该函数会吞掉文件其后全部内容）。根因是第 224 行
   `if len(s) > 0 && s[0] == '{' {` 里有 **rune 字面量 `'{'`**，被当成真实开括号。
   同一探针也说明：其余全仓函数不受字面量影响（差异集合大小为 1）。
   探针脚本跑了即弃，未提交。
3. **归一化搬移无遗漏**：比对用 `Compare-Object` 多重集（保留重复项），丢任何一条语句
   （含重复的 `}`）都会出现「仅旧」行 —— 实跑「仅旧」为**空**。

**已知局限（不隐瞒）**：

- 本机无 gcc，`-race` 不可用（CI 独占）。B2 只搬移了锁的**所有权**（写路径仍是同一把
  `writeMu`、心跳仍内联写、`[DONE]` 仍走 `writeThrough`），**没有新增并发路径**，因此
  未新增竞态回归测试；证据是静态等价 + 既有测试全绿。
- `check-depth.ps1` 的 `Get-CodeOnly` 是**最小词法剥离**，不是完整 Go 解析器。畸形转义会让
  括号计数偏保守（多算）→ 函数体可能被判长。方向是「宁可误报长、不可漏报短」，未对
  全仓做词法器正确性证明。

## 三、未完成 / 待补（诚实清单）

- [ ] `cmd/login` 与 `panel/login.go` 的登录协议双实现收敛。`requires`：新增包 + 改
      `scripts/check-boundaries.ps1` allow 表 + 决定 `cmd/login` 是否接受面板的 1 MiB body 上限
      （这是行为变更，需产品确认"cli 拉超大响应"要不要拦）。
- [ ] `internal/upstream/headers.go:381 ExtractClientIP` 死代码删除。`requires`：先跑穷尽
      grep（含非 `.go` 引用）确认零调用后再删，并按 L-15 附原始输出。
- [ ] `app.js` 1640 非空行拆分（唯一 >1000 前端文件）。`requires`：按既有
      `panelScripts` 模式外置内聚块 + `TestComponentLoadOrder` 覆盖。
- [ ] `usage.go` `SnapshotWindow`(193) / `sse.go` `Aggregate`(211) 拆分。未评估，无测量证据。

## 四、怎么回滚

三批各自独立，均为纯搬移/工具修正，`git revert` 单条即可：

| 批 | commit | 回滚 |
|---|---|---|
| B0 度量修正 | `674b185` | `git revert 674b185`（同时回退 `.harness/depth-baseline.json` 到旧值，因为棘轮是 B2 抬的，B0 只改算法） |
| B1 config 拆分 | `a3f6367` | `git revert a3f6367`（`config_normalize.go` 在 git 历史完整保留） |
| B2 sse 拆分 | `c5fd618` | `git revert c5fd618`；若只想回滚棘轮，`git checkout fc54879 -- .harness/depth-baseline.json` |

二进制换版与生产（`D:\yg-gateway`，端口 7863）无关：本轮未构建、未部署、未替换 exe。

## 五、Learning

根因：**「度量口径」与「状态所有权」两处债都是同一个模式——约束写在注释里，没有写进
结构里**。`check-depth` 的边界靠「下一个 `^func`」隐式近似（注释解释了，代码没表达）；
`StreamHint` 的锁口径靠「与 writeSSE 同锁」的注释约束（上一轮已因此漏掉 `[DONE]` 的锁）。
两处都改成结构/词法显式表达后，假阳性和漏锁都没有了立足点。
