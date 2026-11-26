# agentops-runtime 变更记录

> 目录：`.harness/changes/agentops-runtime/`
> 需求：`docs/prd/REQ-agentops-runtime-2026-11-26.md`
> 决策：`docs/adr/ADR-029-agentops-runtime.md`

## 一、变更概览

| 项 | 内容 |
|---|---|
| 功能名 | agentops-runtime（DSH 式 agent loop，只读运维） |
| 类型 | feat + test |
| 涉及范围 | `internal/opsagent`（runtime_*.go）、`cmd/server`（opsagent_model/runtime/tools + main 装配）、`internal/panel`（status/会话表） |
| 默认状态 | **关闭**：`opsagent.agent.enabled=false` 或 `model` 为空时不建目录、不启动 goroutine、不调模型 |

## 二、需求与方案

把 DeepSeek Harness 的 agent-loop 内核按 1:1 语义裁剪进现有 `internal/opsagent`，不搬 338 个 npm 包、不新增 Go 依赖：

- append-only JSONL session（seq 连续、半行尾丢弃、已开未闭 turn 补 `turn/end{interrupted}`）
- turn/step 固定顺序：`turn/start → user/message → [step/start → compact? → request/header → assistant/message → tool/call → tool/result|tool/denied]* → turn/end`
- 工具注册 + 只读策略：写工具默认拒绝且工具函数零调用；panic/超时/8KB 截断
- 确定性压缩：保留 system + 最近消息，旧段压成角色摘要，切点回退到配对 assistant
- 取消/超时/`max_steps` 都落 `turn/end{canceled|timeout|max_steps}`
- 模型适配只走本机 `/v1/chat/completions`，5 个只读工具 `gateway_status` / `request_stats` / `recent_errors` / `deploy_reports` / `unknowns`

### 刻意不做

- 不自动改配置 / 换号 / 重启 / 部署
- 不做子 agent、goal、jobs、MCP、skills 市场、流式 token 回调
- 不改上游协议字符串（R8）
- 不把 runtime 接入生产配置，除非显式开启并提供模型名

## 三、验证

- `go test ./... -count=1`：32 包（28 ok + 4 no test）
- `go vet ./...` exit 0；`gofmt -l ./cmd ./internal` 无输出
- `scripts/check-boundaries.ps1` 通过；`scripts/check-depth.ps1` 未回退
- `scripts/check-harness.ps1` 11/11（REQ 必要性由 pre-commit 检查）
- 覆盖率：`internal/opsagent` 95.0%（基线 95.6，容差 1.0 内）；`cmd/server` 79.8%（基线 79.3）
- 全部模型测试为 fake，零付费 API 调用

## 四、回滚

- 代码：回退本变更对应的 commit；runtime 默认关闭，即使漏配也不会启动
- 生产：未部署、未改 `D:\yg-gateway\config.json`；二进制仍为 `82c71ef`
- 若启用后异常：把 `opsagent.agent.enabled` 置 false 并重启；session 目录 `opsagent-sessions` 可整目录保留审计
</content>
