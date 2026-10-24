# REQ: 结构化输出（response_format）的支持与锚定（2026-10-24）

> 状态：**立项待执行**。本文记录实测事实与最小方案骨架，不含代码改动。
> 路线依据：`docs/roadmap/HARNESS-GAP-AND-AGENT-EVOLUTION-2026-10-24.md` §三 L1 首个落地候选。

## 必要性

- **真实问题**（2026-10-24 实测链路）：出站 body 是 AST 级重组
  （`payload.go` unmarshal → `prepareBodyAST` 改写 → marshal 回去）。显式删除的键
  只有 `max_completion_tokens` / `tools` / `functions` / `tool_choice` /
  `reasoning_effort`（payload.go:148,378-417 / thinking.go:170）——`response_format`
  **不被删除，会被无意识地透传给上游**。
- **不做会怎样**（两个静默风险）：
  1. 透传无任何测试/金样锚定——`prepareBodyAST` 将来若改成字段白名单式重组，
     `response_format` 会**静默消失**，依赖结构化输出的 agent 客户端解析破裂且无报错；
  2. 上游（CodeBuddy）是否认这个字段**未知**（全仓 docs/reference 与抓包记录 0 命中）：
     若不认，客户端要 json_object 却拿到自由文本，同样是静默语义破坏。
- **现状是否变了**：从未支持过（生产代码 0 显式处理）。
- **更便宜的等效手段**：无——透传已经"碰巧"发生，但"碰巧对"不等于"被保证对"。

## 方案骨架（两步走）

- **P1 锚定透传（✅ 2026-10-24 已落地）**：`internal/upstream/payload_passthrough_test.go`
  ——json_object / json_schema 两形态 × sanitize 开/关 × cache 注入全组合，断言
  response_format 原样保留与不被改写。测试绿 = 当前透传行为的 observation 证据。
- **P2 上游行为实测 + 决策**：真机发一发带 `response_format: json_object` 的请求，
  观察上游是否返回合法 JSON（观测口径，unknown → observation）。
  - 上游认 → 文档化 + Anthropic 适配对齐；
  - 上游不认 → 决策点：网关侧兜底（非流式 response 后处理校验/重试）或明确登记
    unknown 并在 README/文档声明不支持（诚实优先，不做假透传）。

## Given-When-Then 验收（执行时）

- When 客户端 body 含 `response_format`，Then 出站 body 保留该键（金样断言，P1）。
- When P2 实测完成，Then 结论以 observation 落档（上游认/不认，证据=响应样本），
  且 README「核心能力」表与实际行为一致。
- When 上游不认且决策为不支持，Then 面板/文档明确「未知/不支持」而非沉默。

## 解锁动作

① ~~P1 测试~~（已落地）→ ② 真机一发（需生产实例与一个可用账号，人工触发）→
③ 按结果走 P2 分支。
