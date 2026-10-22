# ADR-013：协议漂移观测层的位置与喂点选型

- 日期：2026-10-22
- 关联：R-102（`docs/prd/REQ-protocol-drift-2026-10-22.md`）
- 状态：已采纳

## 背景

CodeBuddy 上游响应形状静默变更（目录端点形状变、目录条数缩水）时网关不报错。
需要一层「播种基线 → 周期比对」的观测。文件域硬约束：upstream 仅新增文件
（禁改 client.go 现有函数/headers.go/payload.go/useragent*）；handler.go 仅
/status、fallbackForward、路由注册三区可动。观测数据从哪喂进来是核心权衡。

## 候选

1. **周期探测 goroutine**（采纳）：main 起一个 goroutine，用池里一个可用 CN 号
   调既有 `upstream.FetchModels(a)`，把目录条数与成败喂给观测器。
2. **主链路流量旁路观察**：在 chat 成功路径包一层 Reader 观察 SSE 帧 id/object 形状。
   —— 不可行：成功路径在 chatrun.go（不在可改文件域）；SSE 解析在 sse.go（禁改）。
3. **错误路径观察**：把错误分类流喂给观测器。—— 不可行：错误处置在 chatrun.go
   handleErrorPath（禁区），且错误路径只覆盖「已坏」，发现不了「形状变了但还能用」。

## 决策

采纳方案 1。观测器自包含在新文件 `internal/upstream/drift.go`（Mutex + 快照 JSON
落盘 data/），喂点在 `cmd/server/main.go` 的 `runDriftProbe`（R9：ctx 取消即退出）。

理由：
- **主链路零侵入**：不碰 chatrun/sse/client.go，行为零变化，r8_golden 不回归。
- **复用既有探测**：`FetchModels` 已经是现成的、带鉴权的目录拉取，不用另造探测逻辑。
- **四态诚实**：未播种 = unknown（不是「无漂移」）；快照缺失/版本不符回 unknown
  重新播种，不沿用错误基线。

## 代价与边界

- 观测粒度只有「目录条数 + 探测成败」，不是 SSE 帧级形状。这是文件域约束下的
  有意取舍——目录腰斩是上游变更最常见的可观测信号，帧级观察留待后续放开文件域。
- 探测会多一次目录拉取（默认 30min 一次，低频）。
- 只观测不处置：判漂移只记 WARN + 翻 /status 状态，不自动切备用（处置另立）。
