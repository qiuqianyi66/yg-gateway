# Anthropic 流中途错误帧被静默丢弃

日期：2026-11-18
提交：`<待填>`
上一提交：`0ae962c`

## 一、怎么发现的

上一批（`addde41`）修完「整段上游失败」的静默问题后，变更记录里留了一条已知缺口：
「上游**中途**断开（已输出若干 chunk 后失败）的错误体为空，识别不出来」。本批就是把这条补上。

继续读 `streamInterceptWriter.Write` 的 SSE 解析循环时确认了机制：

```go
line = strings.TrimSpace(line)
if !strings.HasPrefix(line, "data: ") { continue }
payload := strings.TrimPrefix(line, "data: ")
if payload == "[DONE]" { sw.done = true; break }
var chunk struct{ Model; Choices; Usage }   // ← 没有 error 字段
json.Unmarshal([]byte(payload), &chunk)      // 错误帧也是合法 JSON，能成功
// chunk.Choices 为空 → 什么都不做 → 继续下一行
```

**关键**：错误帧 `data: {"error":{"message":"..."}}` 是合法 JSON，`Unmarshal` 成功，
但解析出的 `chunk` 没有 `Choices`，于是被当成「空 chunk」**静默跳过**。

## 二、缺陷本身

上游返回：一个正常 chunk（content="你好"）+ 一个 error chunk，不发 `[DONE]`。

客户端实测收到（改前）：

```
message_start
content_block_start
content_block_delta   {"text":"你好"}
content_block_stop
message_delta         {"stop_reason":"end_turn"}
message_stop
```

**比整段失败更隐蔽**：客户端已经拿到部分内容了，更容易相信结果完整。
它不知道上游在中途报错、不知道回答被截断，也不会重试。

## 三、修法

`Write` 里的 chunk 结构加 `Error *struct{ Message, Type }`，命中时：

```go
if chunk.Error != nil && chunk.Error.Message != "" {
    sw.errBody = append(sw.errBody, []byte(chunk.Error.Message)...)
    sw.done = true
    break
}
```

两点决定：
1. **停在错误帧**：上游报错后到达的内容不可信，不该继续当正常 delta 转发
2. **不再解析后续行**：`done = true` 让循环立刻退出（也挡住重复错误帧）

复用上一批已有的 `upstreamErrorMessage` 出口 —— 它先试 JSON 信封、失败退回原文，
所以这里直接存纯文本 message 是最省事的正确选择（实测 `event: error` 里的
message 就是 `upstream reset by peer`，不带转义）。

## 四、验证

**先红后绿**：新用例改实现前 FAIL，红时输出里能直接看到
`{"text":"你好"}` 之后就是 `stop_reason: end_turn`，中间没有任何 error 证据。

改实现后 PASS。三个断言：
- 必须有 `event: error`
- 错误帧的 message 必须到达客户端（唯一排障线索）
- **已产出的 content 不能因后续报错而丢失**（部分内容对客户端仍有价值，不能因为报错就回滚已发出的 delta）

**回归**：`TestAnthropicStreamReportsRealTerminalState`（正常流）、
`TestAnthropicStreamSurfacesUpstreamFailure`（整段失败）都仍 PASS，说明新分支没有误伤正常路径。

## 五、覆盖率

| 函数 | 改前 | 改后 |
|---|---|---|
| `streamInterceptWriter.Write` | 93.5% | **97.1%** |
| `internal/server` 包 | 81.1% | **81.2%** |

棘轮 `-Update` 到 81.2。

## 六、能力边界（明确未验证）

1. **无真实 Anthropic 客户端联调**：流内 error 事件能否被 Claude Code / anthropic SDK
   正确呈现，仍未验证（与上一批同一未验面）。
2. **上游静默断开（不发 error 帧、也不发 [DONE]）仍识别不出来**：此时 `done == false`
   且 `errBody` 为空、`finishReason` 为空。当前判据只覆盖「有明确错误信号」的情况。
   要不要把「`finishReason` 为空且未 done」也判成异常，**需要产品决策** —— 有些上游
   本来就不发 finish_reason，直接判异常会误伤。列为待决策，不擅自动。
3. 错误帧结构假设是 OpenAI 风格 `{"error":{"message":...}}`；如果上游用
   `{"error":"纯字符串"}`，`Message` 会为空 → 走不到新分支。这一形态**未验证存在**，
   不预先加兼容（会变成猜需求）。

## 七、回滚

```powershell
git revert --no-edit <本批 sha>
```

回滚后 `streamInterceptWriter.Write` 回到 93.5%，基线 81.2 会报 REGRESSED，
需把基线调回 81.1。
