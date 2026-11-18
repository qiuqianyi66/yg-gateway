# Anthropic 流式错误静默：上游 5xx 被包装成「正常空回答」

日期：2026-11-18
提交：`addde41`
上一提交：`1d7a895`

## 一、怎么发现的

上一批收尾时看覆盖率，发现 `internal/server/anthropic.go` 的
`streamInterceptWriter.WriteHeader`（原 403 行）是 **0.0%** 覆盖，而且**方法体是空的**：

```go
func (sw *streamInterceptWriter) WriteHeader(code int) {
	// 流式 200 已隐式发出；不覆盖。
}
```

空实现 + 零覆盖 = 从来没有触发过。追问一句「如果真触发了会怎样」，就找到了本条缺陷。

## 二、缺陷本身

流式请求 + 上游失败时，客户端收到的是**一个完整的「正常流」**（实测输出）：

```
event: message_start      model=glm-5.2, usage 全 0
event: content_block_start text=""
event: content_block_stop
event: message_delta      stop_reason=end_turn, output_tokens=0
event: message_stop
```

四步链路，每一步都把失败抹掉一点：

1. `message_start` 已写出 → HTTP 200 隐式发出，状态码**再也改不了**（结构性限制）
2. `chatCompletions` 失败时调 `w.WriteHeader(503)` → 被**空实现吞掉**，什么都没记
3. 随后写入的 JSON 错误体**没有 `data: ` 前缀** → 被 `Write` 里的 SSE 行解析当垃圾丢弃
4. 收尾代码无条件补 `content_block_stop` / `message_delta(end_turn)` / `message_stop`

**危害**：一次上游故障被客户端读成「模型回答了个空」。它不报错，所以：
- 客户端不会重试（以为成功了）
- 用量统计记成一次零 token 的成功调用
- 排障时看不到任何上游痕迹

这属于**静默失败**，比直接 5xx 难发现得多。

## 三、修法

| 改动 | 内容 |
|---|---|
| `streamInterceptWriter` 加字段 | `status int`、`errBody []byte` |
| `WriteHeader(code)` | 记 `sw.status = code`（仍不穿透，但不再丢信息） |
| `Write(b)` 开头 | `status >= 400` 时把 body 存进 `errBody` 并返回 —— 不再走 SSE 解析（省得把错误 JSON 当垃圾行丢掉） |
| `anthropicStream` | chatCompletions 之后先判失败：`status >= 400 \|\| len(errBody) > 0` → 发 `event: error` 并**直接返回**（不再补 message_stop 伪装正常） |
| 新增 `upstreamErrorMessage` | 把错误体压成一句可读文本（优先 `error.message`，带 `code` 时拼成 `code: message`；不是 JSON 退回原文；空 body 退回状态码描述） |

为什么用 `event: error` 而不是改状态码：`message_start` 一写出，HTTP 200 就定了，
改状态码在协议上不可能。Anthropic 流式规范本来就有流内 `error` 事件，用它才是正解。

顺手修掉的一个小问题：初版把整个错误 JSON 塞进 `message`，客户端会收到
`"message":"{\"error\":{\"code\":...}}"` —— 转义后的嵌套 JSON，还得二次解析。
`upstreamErrorMessage` 就是为了消掉这层。

## 四、验证

**先红后绿**（TDD）：新用例在改实现前 FAIL，输出里能直接看到
`stop_reason: end_turn` 与"没有 error 事件"。

改实现后 PASS。修复后的实际输出：

```
event: message_start
event: content_block_start
event: error
data: {"error":{"message":"no_healthy_account: all accounts are temporarily unavailable...","type":"upstream_error"},"type":"error"}
```

**变异验证**：把 `WriteHeader` 改回空实现 →
`TestAnthropicStreamSurfacesUpstreamFailure` FAIL；恢复后 PASS。
即这个测试确实在守护失败信号，不是恰好通过。

**单测**：`TestUpstreamErrorMessage` 四条分支（带 code / 只 message / 非 JSON / 空 body）全 PASS。

## 五、覆盖率

| 函数 | 改前 | 改后 |
|---|---|---|
| `streamInterceptWriter.WriteHeader` | **0.0%** | **100.0%** |
| `anthropicStream` | 100% | 100% |
| `upstreamErrorMessage`（新增） | — | **100.0%** |
| `internal/server` 包 | 81.0% | **81.1%** |

（中途一度掉到 80.9%，因为新代码进了分母；补 `upstreamErrorMessage` 单测后回到 81.1%。）
棘轮 `-Update` 到 81.1。

## 六、能力边界（明确未验证）

1. **无真实 Anthropic 客户端联调**：流内 `error` 事件能否被 Claude Code / anthropic SDK
   正确呈现（弹错而非静默吞），**未验证**。这是本条最大的未验面。
2. **`message_start` 与 `content_block_start` 仍然先于失败发出**：客户端会先看到两个正常事件，
   再看到 error。要彻底避免得重构为先探测上游再开流，属更大的改动，本批未做。
3. 上游**中途**断开（已流式输出若干 chunk 后失败）走的是同一分支，但那条路径
   的错误体为空（status 可能仍是 200）—— 当前只会补正常收尾，识别不出来。
   **这是已知残留缺口**，列下一批候选。

## 七、回滚

```powershell
git revert --no-edit addde41
```

回滚后 `streamInterceptWriter.WriteHeader` 回到空实现、覆盖率回落到 81.0%，
`check-depth.ps1` 会因基线 81.1 报 REGRESSED，需一并把基线调回 81.0。
