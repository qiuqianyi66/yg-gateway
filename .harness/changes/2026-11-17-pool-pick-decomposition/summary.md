# 变更：把选号决策切成三段（pick.go 解除第三个超长函数）

- 日期：2026-11-17
- 分诊：L1（纯内部重构，无对外契约变化；6 个 Pick 入口签名与选号语义逐位不变）
- 前置：`7a46be6`

## 为什么

`internal/pool/pick.go` 的 `(p *Pool) pick` 是全仓第 3 大函数（**180 代码行**，阈值 120），
它把四件不同的事压在一个函数里：

1. **候选收集**：tried/provider/healthy/breaker/floor/inFlight 六道过滤（读锁内，热路径）；
2. **分层决策**：costTier 预计算、条件探索（exploreMu + 窗口判定 + 水位切层）；
3. **排序抽签**：`maxCredits` 全集口径、权重预计算、等权重洗牌、top5 截断、LRU 兜底、加权抽签；
4. **写回**：lastUsed/usedSeq/pickReason/half-open 占位 + 探索日志。

删除测试：把 3+4 切出去后，`pick` 只剩「路由 + 锁 + 过滤 + 兜底」这条**能被单独读懂的短链**，
复杂度集中到两个各有单一职责的函数，而不是搬家——这是值得做的信号。

## 改动

`internal/pool/pick.go` 一个文件，无新增文件：

| 段 | 承载 | 行数（代码） |
|---|---|---|
| `(p *Pool) pick` | provider 路由 + 读锁 + guards + 候选收集 + 全冷却兜底 | 约 50 |
| `(p *Pool) selectFromCandidates` | 成本分层 + 条件探索 + 权重预计算 + 洗牌 + 排序 + top5 截断 + LRU/加权抽签 | 119 |
| `(p *Pool) commitPick` | 写回（lastUsed/usedSeq/pickReason/half-open 占位）+ 探索事件日志 | 约 22 |
| `maxCreditsOf`（包级纯函数） | 候选集最高 credits（全集口径基准） | 约 10 |
| `pickSelection`（struct） | `e/mode/tier/explored` 四字段决策溯源，供 commitPick 写 pickReason | — |

`pick` 与 `commitPick` 之间用 `pickSelection` 传值，不共享可变状态；`selectFromCandidates`
的入参与原函数一致（`cands/reqModel/realm/now/class`），锁语义仍是**只持读锁**（ADR-005 D-500）。

## 数字

| 指标 | 前 | 后 |
|---|---|---|
| 全仓 >120 行函数 | 3 | **2** |
| `pick` 代码行 | 180 | 约 50（三段已拆出） |
| `selectFromCandidates` 代码行 | — | 119（<120） |
| `pick.go` 非空行 | 694 | 720（新增函数注释与签名） |
| `oversizeFuncs`（棘轮） | 3 | **2** |
| `maxFileLines`（棘轮） | 942 | 942（未变，pick.go 仍是非最大文件） |

## 验证

1. `go build ./...` = 0；`go vet ./internal/pool` = 0；`gofmt -l` 无输出
2. `go test ./internal/pool/ -count=1` 全绿（0.370s）
3. `scripts/pre-commit.ps1` 全量 11 项检查全过（exit 0）
4. **牙齿验证（关键）**：把 `maxCreditsOf` 的 `if e.credits > m` 改成 `if false && e.credits > m`
   （等价于恒返回 0），`go test ./internal/pool/` **立即 FAIL**（exit 1）；改回后立即绿——
   证明既有测试真的锁住了这次搬运的权重基准，不是「搬完恰好全绿」的空跑。
5. 棘轮：`check-depth -Update` 输出 `+ oversizeFuncs 3 → 2`；`.harness/depth-baseline.json`
   `oversizeFuncs: 2`、`updatedAt: 2026-11-17`。

## 回滚

`git revert <this-commit>`；纯包内重构，无数据/配置/接口影响。选号语义（权重公式、分层顺序、
探索窗口、LRU 兜底、写回原子性）逐位未动。

## 主动未做

- 未动 `main()`（228）与 `Aggregate`（211）：一次只动一个目标，避免两个口径同时搬动后无法归因。
- 未拆 `internal/panel/app.js`（1,640）：check-depth 脚本注释已写明「本轮只纳入度量 + 冻结基线，
  不拆文件：没有浏览器级 E2E 兜底，风险大于收益」。
- 未改 `pickWeighted` / `weightOf` / `routingWeightOf` 的算法本身：本轮只做结构拆分，不动权重公式
  （算法优化的独立评估需要 benchmark 对照，不混在重构里）。
