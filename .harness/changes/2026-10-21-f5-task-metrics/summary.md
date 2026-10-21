# 变更摘要：F5 度量计数器落地（2026-10-21）

> 关联：`docs/prd/REQ-f5-metric-counter-2026-10-20.md`（状态 DRAFT→IMPLEMENTED）
> 用户指令：「先把没有做的一次性做完」——即实现 F5 计数器（此前仅写了 REQ，未写代码）。

## 改了什么

把 F5 度量基线从「unknown」变成「observation」：在 `internal/upstream` 响应透传处采集
`usage`，按会话 ID 聚合首调用上下文与总 token，挂到现有 `/status` 透出。

| 文件 | 改动类型 | 说明 |
|---|---|---|
| `internal/upstream/taskmetrics.go` | 新增 | 会话级采集器 `TaskMetrics` + `observeUsage` 流包装器 |
| `internal/upstream/client.go` | 修改 | `ChatStreamContext` 成功返回处用 `observeUsage` 包装流 |
| `internal/server/handler.go` | 修改 | `/status` 条件输出 `task_metrics` 段 |
| `internal/upstream/taskmetrics_test.go` | 新增 | 采集器 + 包装器测试 |
| `internal/server/handler_taskmetrics_test.go` | 新增 | `/status` 省略/透出验收 |
| `docs/prd/REQ-f5-metric-counter-2026-10-20.md` | 修改 | 状态 DRAFT→IMPLEMENTED + §七 实现记录 |

## 怎么验证的

- `go test ./internal/upstream/ ./internal/server/`：新增测试 + 既有回归全绿。
- `go vet ./...`、`gofmt -l ./cmd ./internal`（应无输出）。
- `powershell -NoProfile -File scripts/check-boundaries.ps1`：依赖方向无新增违规。
- `powershell -NoProfile -File scripts/check-depth.ps1`：体量棘轮未放宽。
- `scripts/pre-commit.ps1` 全量五门禁通过。

## 关键设计决策（REQ §六 待决议拍板）

- TTL = 1h（采纳建议值）。
- provider 不返回 usage → 直接 unknown（不补本地估算兜底，本期不做）。
- 不持久化（纯内存，不写 `state.json`）。

## 边界纪律

- 采集点只在 `internal/upstream`（R6：出站逻辑集中）。
- 后台清理 goroutine 带 `Stop()` 回收路径（R9）。
- 缺失 usage 是观测缺口，不记 0（R10）。
- `server→upstream` 调用 `TaskMetrics.Snapshot()` 已在 allow 表，无新增违禁方向。

## 怎么回滚

- 反向 `git revert` 本变更提交即可；功能为纯只读观测，回滚零风险。
- 回滚不影响选号、转发、凭证等既有逻辑（采集器只旁观流，不改写字节）。

## 未完成 / 明确不做（维持 REQ §五）

阈值告警、可视化大盘、持久化、按账号/模型成本分摊、本地 tokenizer 精确计数、本地估算兜底：
均未做。
