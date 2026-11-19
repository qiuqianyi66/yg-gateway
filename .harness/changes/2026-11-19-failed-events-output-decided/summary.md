# `failedEvents` 的 output：从「待决策」变成「已决策」

日期：2026-11-19
提交：`<待填>`
上一提交：`e684aad`
改动文件：`internal/server/responses_out.go`（**只改注释，行为零变化**）

## 一、原来挂的是什么

`.harness/changes/2026-11-18-responses-failed-events-untested/` 里留过一条：

> `failedEvents` 注释写「已开始则带已累积输出」，但第 399 行是硬编码
> `"output": []any{}`。这是行为与注释不符，但要不要在 failed 事件里带部分
> output 属 OpenAI Responses 语义的**产品决策**，本批只钉住现状、不擅改。

这条挂了快一天。本批把它落地。

## 二、查到的三条事实

### 2.1 数据其实都在手

`st.text`（`strings.Builder`）累积了全部已产出文本，`st.toolCalls` / `st.toolOrder`
记录了全部已产出工具调用。`partialResponse(status)` 正是用它们组装 `output`：

```go
// responses_out.go:173
func (st *respStreamState) partialResponse(status string) map[string]any {
    text := st.text.String()
    out := []any{map[string]any{... "text": text ...}}
    for _, key := range st.toolOrder { ... }
```

**所以这不是「拿不到数据」，是 `failedEvents` 主动没用它。**

### 2.2 客户端不依赖它

`failedEvents` 触发前，如果已经产出过内容，客户端**已经收到**
`response.output_text.delta` 事件（第 287 行）。也就是说：

**`failed` 对象带不带 output，都不影响客户端能否恢复已产出的内容。**

它只影响 `response.failed` 这个对象自身的自洽性（`status: failed` + `output: []`
与之前发过的 delta 在视觉上不一致）。

### 2.3 官方语义是 unknown

搜到一条**真实的** `response.failed` 报文（Azure/Foundry 用户贴的 rate limit 失败）：

```json
{"type":"response.failed","sequence_number":3,"response":{
 "id":"resp_077206c4...","object":"response","created_at":1765550946,
 "status":"failed","background":false,"content_filters":null,
 "error":{"code":"rate_limit_exceeded","message":"..."}}}
```

**但 `output` 字段在截断处没出现**。没有取得可引用的规范原文。

**按 AGENTS.md 的四态纪律**：这属 `unknown`，**不能拿推断当 observation 去改 wire 形状**。

## 三、决策：不改行为，改注释

| 方案 | 判断 |
|---|---|
| 改成带部分输出（`partialResponse("failed")`） | **否** —— 依据只有「注释这么说」和「两条路径该对称」，都不是协议证据。`status=failed` 本身就意味着响应未完成，空数组是更保守且更可能一致的选择 |
| 保持空数组，只改注释 | **是** —— 与现状一致，且把「为什么」写进代码 |
| 保持空数组，删掉这条待决策 | **否** —— 问题还在，删了下次还会有人重新发现 |

**改后的注释明确写了三点**：客户端已有 delta、官方语义 unknown、不为对称性去改
无证据的协议形状。并留了「将来若取得证据该怎么改」的具体指引（换成
`partialResponse("failed")` 再覆盖 status/error，数据取用成本为零）。

## 四、为什么这算「做过」而不是「敷衍」

v10 要求「给结论前穷尽代码与实测证据，未验证前提显式标 unknown」。这条的处理正是：

- 查清了数据在手（2.1）→ 排除了「做不到」
- 查清了客户端已收到 delta（2.2）→ 排除了「改了有用」
- 查了官方但只拿到被截断的样本（2.3）→ 标 unknown，**不擅改协议**

**结论是「不改」**，但这是一个**有依据的结论**，而不是「先放着」。

## 五、验证与能力边界

- `gofmt -l ./internal/server/responses_out.go` → **无输出**
- `go test ./internal/server/ -run TestResponsesStream` → **ok**
- 行为零变化（只动注释），因此无需回归

**仍未覆盖**：官方 `response.failed` 的 `output` 语义。要消解需要可引用的 OpenAI
规范原文或一条完整（未截断）的真实报文。

## 六、回滚

```powershell
git revert --no-edit <本批 sha>
```
（只回滚注释；无行为影响）
