# 评审记录 — R-102 协议漂移观测层（protocol-drift）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：commit `009f724`（与 R-107 同提交）。
- 分级：L3（上游协议观测，ADR-014）。**本轮为第 1 轮独立复审。**

## 结论

**PASS**（0 🔴 / 0 🟡 / 2 🟢 / 2 🔵）

## 逐条 GWT 证据（REQ-protocol-drift / ADR-014）

| GWT | 代码级证据 |
|---|---|
| 播种/比对/自愈观测层 | `internal/upstream/drift.go`（219 行）：DriftMonitor 播种快照、比对目录特征、失败翻牌、自愈、Save/Load。main 上 `go test ./internal/upstream` 绿（含 drift_test）。 |
| 四态：快照缺失/未播种 = unknown（不是「无漂移」） | drift.go:73 `state:"unknown"`；Load 文件缺失 return nil（:85 注释「快照缺失=unknown，不是错误」）；版本不符回 unknown 重新播种（:95）。Status() 未播种输出 unknown + 空 features。 |
| /status 四态（nil=省略=外层 unknown） | summary：/status 增 `protocol_drift` 键，nil=省略。handler.go 装配 Drift 字段。 |
| goroutine 回收（R9） | `runDriftProbe`（main.go:598）：ctx 取消立即 return（:610 `case <-ctx.Done(): return`），defer ticker.Stop()；首轮立即执行后轮询。装配处 `go runDriftProbe(ctx,...)`（:486），ctx 由 signal.NotifyContext 派生、停机时取消。 |
| R8 回归 | 周期探测复用既有 FetchModels（报文/UA 不新造）；`go test ./internal/upstream` 含 r8_golden_test 随绿。 |

## 发现的问题

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | SSE 帧级形状观测未做（chatrun.go/sse.go 在文件域禁区） | summary 未完成节如实登记；本只观测 catalog 级条数。设计边界清晰，接受。 |
| 🟢 | 漂移自动处置（切备用）未做，只观测 | ADR 明确「观测层，不动选号/不罚号」。另立 REQ 范畴。接受。 |
| 🔵 | drift.go 219 行，含 Save/Load/Observe/Status 多职责 | 未越线；提示观测逻辑再膨胀时拆 status 子文件。 |
| 🔵 | runDriftProbe 的 driftProbeOnce 内部选号失败只影响观测、不报错 | 注释明示「纯观测层：探测失败只影响观测状态，不动选号」。语义正确，记录备查。 |

## 对后续建议

无需返工。若后续做漂移自动处置（切备用），需另立 REQ + ADR，并把 runDriftProbe 从「只观测」升级为「观测+处置」，注意处置动作要可回滚。
