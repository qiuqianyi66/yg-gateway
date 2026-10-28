# 2026-10-28 structured-output-p2

## 背景与必要性

V3-12（`docs/prd/REQ-structured-output-2026-10-24.md`）P2：真机一发验证上游 CodeBuddy
是否认 `response_format`。P1（透传锚定，payload_passthrough_test.go）已于 2026-10-24 落地；
不做 P2 则「上游认不认」停留在 unknown——客户端要 json_object 却可能拿到自由文本，是静默
语义破坏，且无法裁决是否需要网关兜底。

## 改了什么

- **真机一发（2026-10-28 15:44，生产实例 7863 + 可用账号）**：
  请求 = POST `/v1/chat/completions`，`model: cn:deepseek-v4-flash`，
  `response_format: {"type":"json_object"}`，`max_tokens: 128`，`stream: false`。
  响应 = **HTTP 200**，`content` 精确返回 `{"ok": true}`，`finish_reason: stop`。
  **observation：上游认 json_object，且按请求 schema 返回合法 JSON。**
  usage：total_tokens 268（prompt 244 / completion 24，含 thinking 18），credit 0.03。
  原始响应见同目录 `evidence-response.json`。
- **决策：文档化**（REQ P2 分支①）。透传即正确行为——OpenAI 客户端请求经网关 AST
  重组原样透传，上游直接兑现，无需 Anthropic 额外适配。
- **落档**：REQ 状态改 P1/P2 全落地；REQ-INDEX 行更新；PLAN-frontend-functional
  F11 行 ✅（顺手校准 F12 行 ✅，其产物 `docs/agent-eval/V3-14-harness-product-eval-2026-10-27.md`
  已于 2d39e9c 提交但未回填）；BACKLOG A4 行 ✅；README「核心能力·模型与网关」表新增
  结构化输出行（observation / unknown 四态口径）。

## 怎么验证的

- 真机一发原始响应落档为证据文件（上文），非转述。
- 四态口径自查：仅对 `deepseek-v4-flash` + `json_object` 形态写 **observation**；
  json_schema 形态与其余模型未逐一实测 = **unknown**（文档明确「未测」，不写「支持」）。
- 纯文档改动，门禁不受影响；仍跑 `pre-commit.ps1` 全量确认绿（见提交记录）。

## 怎么回滚

单提交 `git revert`（纯文档改动，无二进制 / 配置 / 凭证 / 数据影响）。
若未来换版改变透传行为，需重测（成本 ≈ 0.03 credit）并更新本文档结论。
