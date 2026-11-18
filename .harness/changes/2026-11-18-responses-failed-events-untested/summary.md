# /v1/responses 的 failedEvents 零验证

日期：2026-11-18
提交：`28f9577`
上一提交：`4a7b872`

## 一、问题

`responses_out.go:386 failedEvents` 覆盖率 **0.0%** —— 一个**专门处理错误**的函数
从未被任何测试执行过。它是 `/v1/responses` 流式路径把上游 error 帧转成
`response.failed` 事件的唯一出口。

对比同期发现的 anthropic 缺陷（`d03b52e`）：那条路径的错误帧被静默丢弃。
`/v1/responses` 这边**实现是对的**（`translateFrame` 第 245 行把 `chunk["error"]`
分给 `failedEvents`），只是没人测过它。两个 API 的对比本身是个有价值的互证：
它证明 WAF/上游确实会发 error 帧，不是我在猜需求。

## 二、三条契约（都写在代码里，都未被验证）

| # | 契约 | 代码位置 | 失效后果 |
|---|---|---|---|
| 1 | error 帧 → `response.failed`，`status: "failed"` | 396-401 | 客户端收到的东西里没有任何失败迹象 |
| 2 | 上游 error 对象原文透传 | 392、399 | 排障时只剩「失败了」，不知道为什么 |
| 3 | 流未 started 时 model 取自 error 帧 | 393-395 | response 对象 model 为空，客户端无法判断是哪个模型失败 |

## 三、补的 3 个用例

| 用例 | 守什么 |
|---|---|
| `TestResponsesStreamFailedEvents` | 契约 1 + 2 + 3，一次全验（四断言） |
| `TestResponsesStreamFailedSuppressesCompletion` | failed 之后再收 `[DONE]` 不许发 `response.completed` |
| `TestResponsesStreamFailedOnlyOnce` | 连续 3 个 error 帧只转译 1 次 |

**为什么第 2 条重要**：上游实现不一致时会出现「先发 error 帧、再补一个 `[DONE]`」。
若不加互斥，客户端会先看到 `failed` 再看到 `completed` —— 后者可能覆盖前者，
把一个已失败的请求读成成功。这正是 anthropic 侧的镜像风险。

## 四、变异验证（两个变异，分别被不同用例抓住）

| 变异 | 结果 |
|---|---|
| 删 `failedEvents` 的 `if st.done { return nil }` | `TestResponsesStreamFailedOnlyOnce` FAIL（实际发 3 次） |
| （既有）`completionEvents` 侧的 `done` 检查 | 由 `SuppressesCompletion` 守 |

关键观察：**删除 `failedEvents` 的 `done` 守卫，`SuppressesCompletion` 仍然 PASS** ——
因为 `st.done = true` 那一行还在，`completionEvents` 照样被挡住。这说明两个用例
**各守一侧的互斥**，不是重复断言：

- `OnlyOnce` 守 `failedEvents` 自己的幂等
- `SuppressesCompletion` 守 `completionEvents` 对 failed 的让位

## 五、覆盖率

| 函数 | 改前 | 改后 |
|---|---|---|
| `responsesStreamWriter.failedEvents` | 0.0% | **100.0%** |
| `internal/server` 包 | 82.1% | **82.6%** |

## 六、能力边界（一条真实的注释与实现不符，本批**不改**）

`failedEvents` 的注释写「已开始则带已累积输出」，但第 399 行是硬编码
`"output": []any{}` —— 即使已经流式输出了文本，`response.failed` 里 output 也是空的。

- 这是**行为与注释不符**，不是 bug 与否可以单方面判定的：要不要在 failed 事件里
  带部分 output 属于 OpenAI Responses 语义的产品决策。
- 本批只补测试、**钉住现状**（用例里没断言 output 内容），不擅自改行为。
- 记为待决策项：若产品决定「failed 也带部分内容」，改第 399 行为
  `st.partialResponse("failed")["output"]`，并把 `TestResponsesStreamFailedEvents`
  补一条 output 断言。

## 七、回滚

```powershell
git revert --no-edit <本批 sha>
```
