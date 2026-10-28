# 2026-10-28 responses-endpoint (F8)

## 背景与必要性

F8（PLAN-frontend-functional-2026-10-27）：OpenAI 客户端正在迁移 Responses API（Codex
CLI 已切默认协议）——客户端发 `POST /v1/responses`（请求体 `input`/`instructions`、
响应 `response` 对象、流式事件名完全不同）。本网关此前该路径未注册（走 notFound 错误
信封），Codex 直连即失败。REQ：`docs/prd/REQ-responses-endpoint-2026-10-28.md`。

## 改了什么

- **`internal/server/responses.go`（新）**：`POST /v1/responses` handler + 转换 A
  （responses 请求 → 等价 chat body：`input`(string|items)→`messages`、
  `instructions`→system、`max_output_tokens`→`max_tokens`、tools/tool_choice/
  temperature/top_p/seed/stop/response_format 原样直通）。换 body 后复用既有
  chatCompletions 全管线（鉴权/选号/提示词/轮转/用量/reqlog 零改动）。
- **`internal/server/responses_out.go`（新）**：转换 B——非流式 chat resp →
  `response` 对象（`output[].message.content[].output_text` + function_call items +
  usage 重命名 input_tokens/output_tokens）；流式用 `responsesStreamWriter` 帧级转译
  chat SSE → Responses 事件（response.created → output_text.delta →
  output_item.done → response.completed；上游 error 帧 → response.failed；`[DONE]`
  吞掉不发）。**upstream/sse.go 零改动**（热路径零回归）。
- **`internal/server/chatrun.go`**：`chatRun` 加 `outProto` 字段；serveNonStream 出口
  按标记分支 `writeResponsesObject`（非流式转换 B）。
- **`internal/server/handler.go`**：NewHandler 注册 `POST /v1/responses` →
  `withAuth(h.responses)`；chatCompletions 读 context 标记设 outProto。
- **`internal/server/responses_test.go`（新）**：8 个测试——转换 A（string/items/
  错误）、转换 B（对象/流式事件/工具分片聚合）、端到端（非流式 response 对象、
  流式事件流、缺 input 400 不触上游）。全复用既有 newFakeUpstream/testPoolWith。
- **文档回填**：REQ 状态改「落地」；REQ-INDEX + 行；PLAN F8 行 ✅；README 核心能力表
  +「Responses API」行（四态口径：协议转换已测、真机 Codex 验证 unknown）。
- v1 明确不支持（忽略不报错，README 已声明）：store / previous_response_id /
  metadata / reasoning。

## 怎么验证的

- `go test ./internal/server -run TestResponses` 8/8 绿；全量 `go test ./...` 全绿
  （无回归）；gofmt 零漂移；go vet 干净。
- 提交前总闸 `pre-commit.ps1` 全量通过（11 项）。
- 测试性质：协议级转换 + 端到端（fake upstream 模拟 chat SSE）；真实上游语义在
  既有 chat 管线测试已覆盖，responses 层不重复。

## 怎么回滚

单提交 `git revert` 回滚代码；保留 REQ/README/变更目录。真机 Codex 验证（解锁动作②，
需客户端环境）不在本提交验收内——README 标 unknown，验证后再按结果更新 README 状态行。
