# REQ: /v1/responses 端点（OpenAI Responses API 兼容）（2026-10-28）

> 状态：**落地（2026-10-28）**。GWT 全部通过（8 个测试：协议级转换 + fake upstream 端到端）；
> 真机 Codex 验证（解锁动作②）未做 = unknown。

## 必要性

- **真实问题**：OpenAI 客户端正在迁移到新的 Responses API（Codex CLI 已切默认协议）——
  客户端不再发 `/v1/chat/completions` 而发 `POST /v1/responses`（请求体用 `input` 而非
  `messages`、响应是 `response` 对象而非 `choices`、流式事件名完全不同）。本项目目前
  `/v1/responses` 未注册（走 notFound 的 OpenAI 错误信封），Codex 直连本网关即失败。
- **不做会怎样**：Codex 是「新客户端支持」的最短路径——L1/对标已登记
  （`docs/reference/similar-projects-learnings-2026-10-25.md`：magpie 四协议互译之一）。
  不做则 Codex 客户端永远连不上本网关，只能继续用 chat 协议客户端。
- **现状是否变了**：从未支持过（handler.go 路由表无 /v1/responses；magpie 深读实测确认
  Codex 的真实缺口）。
- **更便宜的等效手段**：无——客户端协议是写死的；不做 = 不支持。

## 方案骨架（复用既有管线，两个转换层）

```
responses 请求 → (转换A) → chat body → 既有 chatCompletions 管线（鉴权/选号/提示词/
轮转/用量/reqlog 全复用）→ chat 响应 → (转换B) → responses 对象 / 流式事件
```

- **转换A（入站）**：`input`（string | items）→ `messages`；`instructions` → system；
  `max_output_tokens` → `max_tokens`；`tools`/`tool_choice`/`temperature`/`top_p`/`seed`/
  `stop`/`response_format` 原样透传；`stream` 原样。
- **转换B（出站）**：非流式 chat resp → `response` 对象（`output[].message.content
  [].output_text` + function_call items + usage 映射）；流式把 chat chunk 转译成
  Responses 事件序列（response.created → output_text.delta → output_item.done →
  response.completed）。
- **不碰上游热路径**：流式转译用 ResponseWriter 包装层实现（帧级转译），upstream/sse.go
  零改动；非流式在 serveNonStream 出口按标记分支。
- **明确不支持（v1）**：`store`/`previous_response_id`/`metadata`/`reasoning` 忽略不报错
  （语义丢失）；`input` 的 computer/audio/function_call 复杂 item 仅 function_call 族映射。
  均写入 README 诚实声明。

## Given-When-Then 验收（执行时）

- When 客户端 POST `/v1/responses`（非流式，input=string + instructions + tools），
  Then 上游收到等价 chat body（messages 含 system+user、tools 原样），客户端收到
  `object:"response"` 且 `output[0].content[0].text` = 上游正文（200）。
- When 客户端 POST `/v1/responses`（stream=true），Then 收到 Responses 事件流：
  response.created → … → response.output_text.delta（正文增量）→ response.completed，
  无 chat 格式 chunk 泄漏、流以 response.completed 收尾。
- When 未鉴权 / 模型不在 Key 白名单 / body 超限，Then 与 /v1/chat/completions 同错误
  信封（401/400/413）。
- When 上游 error 帧（6004/内容拦截），Then 转译成 response.failed 事件（不泄漏 chat
  error 帧格式）。
- Then 全量 go test 绿、gofmt 干净、pre-commit 11/11；README「核心能力」表加行。
- 真机 Codex 验证（需 Codex 客户端环境）**不在本 REQ 验收内**——标 unknown，README
  四态口径写明「协议转换已测、真机客户端未验证」。

## 解锁动作

① 实现转换A/B + 路由 + 测试（本 REQ）→ ② 真机 Codex 一发（需客户端环境，人工触发）
→ ③ 按结果回填 README 状态。
