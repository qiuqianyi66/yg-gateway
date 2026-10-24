# 2026-10-24 algorithm-deepening

## 背景与必要性

用户要求对核心算法加深审读。深读范围：选号两阶段（pick.go 590 行逐行）、EWMA 回灌
（entry.go 315-397）、冷却/熔断状态机（cooldown.go 退避段）、延迟分位直方图
（metrics/histogram.go 全文）。产出两处真实缺陷修复（各带回归测试）+ 评审文档 +
一处调研误报的澄清（reqlog 轮转/裁剪实测已有成套行为测试 TestArchiveRotation*/Prune*，
「测试薄弱」结论不成立，撤销原计划）。

## 改了什么

- `internal/pool/pick.go`：F-1——`pickWeighted` 改为接收调用方传入的全集 maxCredits
  （旧实现用 top5 子集重算，与 pick 头注「两阶段共享同一基准」契约不符，分布偏移可证）。
- `internal/pool/cooldown.go`：F-2——`recordBreakerFailureLocked` 的 breakerUntil 从
  `time.Now()` 改 `p.now()`（half-open 门槛用 p.now() 比较，时钟口径必须同源）。
- `internal/pool/pick_weighted_basis_test.go`（新）：两个回归测试，旧实现下均 FAIL。
- `docs/code-review/2026-10-24-algorithm-review.md`（新）：完整评审（修了什么/记录不改
  3 处/实证健康清单）。
- `docs/roadmap/HARNESS-GAP-AND-AGENT-EVOLUTION-2026-10-24.md`（新）：harness 自跑体检
  结果、与 2026 业界实践（OpenAI/LangChain/awesome-harness-engineering）对标缺口 5 项、
  agent 演进三层路线。

## 怎么验证的

`go test ./internal/pool/ -count=1` 全绿（含新测试：基准断言 8×10⁶，旧实现 24×10⁶；
时钟断言 breakerUntil == 冻结时刻+cooldown，旧实现为墙钟基准）；全量 pre-commit 通过。

## 怎么回滚

单提交 revert（测试与生产改动同提交，一并回滚）。
