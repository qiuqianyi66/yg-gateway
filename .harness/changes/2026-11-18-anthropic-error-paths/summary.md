# Anthropic 错误路径：缺 model 静默降级 + 4 条参数校验收紧

日期：2026-11-18
提交：`1d7a895`（本批）
上一提交：`1e45c8e`

## 一、为什么开这一批

`internal/server/anthropicMessages` 覆盖率 68.2%（前一批补完 fidelity 后），未覆盖的
32% 全是错误路径：坏 JSON、缺 model、池耗尽、上游不可解析。这些恰好是真实客户端
会遇到的情况，而且**错误路径出错的表现通常不是报错，而是"看起来成功了"** ——
那是比崩溃更难发现的一类缺陷。

## 二、抓到的真缺陷：缺 model 静默降级

写测试时按规范断言「缺 model 应 400」，实测**返回 200 + 一条正常回答**：

```
实际 200
body={"id":"msg_chatcmpl-1","type":"message","role":"assistant","model":"glm-5.2",
      "stop_reason":"end_turn","content":[{"type":"text","text":"你好"}],
      "usage":{"input_tokens":1,"output_tokens":1}}
```

链路根因：`transformAnthropicToOpenAI` 不校验 model → 生成 `{"model":"",...}` →
`chatCompletions` 也不校验空 model → 网关照常选号、照常打上游、照常 200。

**危害**：客户端指定了模型 A，网关实际用了模型 B（或上游默认），**没有任何信号**。
账单按 B 算、效果按 B 给，但调用方以为自己用的是 A。这属于静默降级 ——
比返回错误危险得多，因为它不报错。

**修法**（`internal/server/anthropic.go` 的 `transformAnthropicToOpenAI` 开头）：

```go
if strings.TrimSpace(areq.Model) == "" {
    return nil, errors.New("model 必填：Anthropic Messages API 要求 model 字段，" +
        "空 model 会让网关在不告知的情况下选用上游默认模型")
}
```

由 `anthropicMessages` 的 transform 失败分支转成 400。

## 三、三条断言的纠错（都是我的期望错、实现对）

写测试时我先按"直觉"写了三条断言，跑完发现全指向实现的正确行为：

| 用例 | 我最初的期望 | 实测 | 谁对 |
|---|---|---|---|
| 上游 429 | 透传 429 | **503 no_healthy_account** | 实现对：429 = 该号限流 → 换号重试；单账号池耗尽 → 503 |
| 上游 400 | 透传 400 | **503 no_healthy_account** | 实现对：任何上游 4xx 都被当成"这个号不行"→ 重试 |
| 上游 200 但 body 非 JSON | 500 | **502 upstream_parse** | 实现对：502 语义更准（责任在上游，可重试） |

结论：要隔离「`iw.status>=400` 透传分支」，不能用上游错误（会被重试吞掉），
只能用**请求本身非法**（缺 model）或**池为空**。两条都补了用例。

顺带记录一条对使用者有用的结论：**单账号池下，任何上游 4xx/429 最终都会表现为
503 no_healthy_account**，不是透传 —— 排查时不要以为网关丢了上游错误码。

## 四、四个用例（全部 PASS）

| 用例 | 覆盖的分支 | 断言要点 |
|---|---|---|
| `TestAnthropicMessagesRejectsNonJSONBody` | transform 失败 → 400 | 信封含 `transform`，且**不是** 500/200 |
| `TestAnthropicMessagesRejectsMissingModel` | 新增的 model 校验 → 400 | 信封含 `model`，且**不含** `"type":"message"` |
| `TestAnthropicMessagesPassesThroughPoolExhausted` | `iw.status>=400` 透传 → 503 | 空池 → 503，**不是** 200 |
| `TestAnthropicMessagesUnparseableUpstreamBody` | 响应解析失败 → 502 | 错误码必须是 `upstream_parse` |

每条都带一个**否定断言**（"不能是 200"/"不能含成功结构"），因为这一批的
失败模式就是"失败被伪装成成功"。

## 五、变异验证（证明不是假绿）

把 `if strings.TrimSpace(areq.Model) == ""` 临时改成 `if false`：

```
--- mutant: model check disabled ---
--- FAIL: TestAnthropicMessagesRejectsMissingModel (0.02s)
FAIL   yg-gateway/internal/server  0.079s
--- restored ---
ok     yg-gateway/internal/server  0.072s
```

即测试确实在守护这条校验，而不是恰好通过。

## 六、覆盖率

| 函数 | 改前 | 改后 |
|---|---|---|
| `transformAnthropicToOpenAI` | 80.0% | **91.7%** |
| `anthropicMessages` | 68.2% | **75.0%** |
| `internal/server` 包整体 | 80.5% | **81.0%** |

其余 anthropic 函数保持 100%（`anthropicStreamIntent` / `anthropicStopReason` /
`transformOpenAIToAnthropic` / `anthropicStream` / `writeSSEEvent`）。

棘轮已 `-Update`：`packageCoverage."yg-gateway/internal/server"` 80.5 → **81**，
即这两点提升是**受守护的**，后续退步会被判红。

## 七、能力边界（明确未验证）

1. **没有真实 Anthropic 客户端联调**：所有断言基于 fake 上游 + `httptest`，
   验证的是「上游 OpenAI 形态 → 我们的 Anthropic 输出」这一段。Claude Code / anthropic SDK
   能否直连，**未验证**。
2. `interceptWriter.WriteHeader`（`anthropic.go:403`）仍 **0.0%**：`chatCompletions`
   在 503/4xx 时写状态码的路径没有让这个方法被调用到（现状是走 `Write` 直接产出 body）。
   这不影响行为正确（测试断言的状态码是对的），但说明该方法是死路径 ——
   若确认无用应删除，属下一批的删除测试候选。
3. `message_start` 的 `usage.input_tokens` 仍为 0（该值只有上游尾 chunk 才有，
   而该事件在其之前发出）—— 已知限制，非缺陷。

## 八、回滚

```powershell
git revert --no-edit 1d7a895
```

回滚后 `internal/server` 覆盖率回落到 80.5%，棘轮基线需一并回到 80.5
（`check-depth.ps1` 会报 REGRESSED，属预期）。
