# Anthropic 响应保真：stop_reason / model / usage

日期：2026-11-17
类型：功能正确性（wire 行为变更，只新增字段与修正取值）
轨道：A 功能与产品行为 / B 正确性

## 一、发现的缺陷（改前证据）

用 `go test ./internal/server/ -coverprofile` + `go tool cover -func` 定位到
`internal/server/anthropic.go` 三个函数 **0.0% 覆盖**，逐行读实现后发现是真实缺陷，
不是"没测"而已：

| # | 位置 | 缺陷 |
|---|---|---|
| 1 | `anthropicResp`（62-75 行） | **没有 `stop_reason` 字段**，而 Anthropic Messages API 规范里它是必需字段。上游 OpenAI 的 `finish_reason` 被完全丢弃 → 「被 max_tokens 截断」和「正常结束」在客户端看来一模一样，客户端既不知道要不要续写，也不会告警 |
| 2 | `anthropicStream` 的 `message_start` | `model` **硬编码为空串** → 流式客户端拿不到模型名，无法对账成本 |
| 3 | `anthropicStream` 的 `message_delta` | `output_tokens` **硬编码为 0**、`stop_reason` **硬编码 `end_turn`** → 流式响应永远报告 0 token、永远报告正常结束 |
| 4 | `streamInterceptWriter` | 只转 `delta.content`，不捕获 `model` / `usage` / `finish_reason`，所以上面 3 条无从修 |

## 二、改了什么（4 处）

1. **`anthropicResp` 加 `StopReason string \`json:"stop_reason"\`**
2. **`openAICompletionResp.Choices[0]` 加 `FinishReason`**
3. **新增 `anthropicStopReason(finish string) string`**：
   `length→max_tokens`、`tool_calls`/`function_call→tool_use`、其余（含空）→ `end_turn`。
   未知值退到 `end_turn` 而不是留空 —— 规范里该字段必填，留空会让客户端解析失败。
4. **`streamInterceptWriter` 增加 4 个捕获字段**（`model` / `finishReason` /
   `promptTokens` / `completionTokens`），并从每个 chunk 里取"最后出现的值"；
   `anthropicStream` 结尾改用捕获值，不再硬编码。

**`message_start` 的 model 怎么取**：该事件必须先于上游首 chunk 发出，此时拿不到上游 model。
选择**回显请求里的 model**（与 Anthropic 官方语义一致：响应 model = 客户端请求的 model）。
在 `anthropicMessages` 里从 `openAIBody`（本包 Marshal 的产物）解析出 model 传给
`anthropicStream`，并显式处理 error（R10，不吞）。

## 三、验证

### 3.1 改前先红（TDD）

先写 `anthropic_fidelity_test.go`（4 个用例），此时实现还没加字段，测试**编译失败**：

```
internal\server\anthropic_fidelity_test.go:37:10: got.StopReason undefined
(type anthropicResp has no field or method StopReason)
internal\server\anthropic_fidelity_test.go:99:8: sw.model undefined
```

### 3.2 改后

| 判据 | 命令 | 结果 |
|---|---|---|
| 新增 5 用例 | `go test ./internal/server/ -run 'TestTransformOpenAIToAnthropic\|TestStreamInterceptWriter\|TestAnthropicStreamReportsRealTerminalState' -v` | **5 PASS** |
| 全包回归 | `go test ./internal/server/` | **ok 8.858s** |
| 格式 | `gofmt -l ./cmd ./internal` | 无输出 |
| 覆盖率 | `go test ./internal/server/ -coverprofile` | **78.5% → 80.5%** |

覆盖率变化明细（`go tool cover -func`）：

| 函数 | 改前 | 改后 |
|---|---|---|
| `transformOpenAIToAnthropic` | 0.0% | **100.0%** |
| `anthropicStopReason`（新增） | — | **100.0%** |
| `anthropicStream` | 0.0% | **100.0%** |
| `streamInterceptWriter.Write` | 0.0% | **92.9%** |
| `writeSSEEvent` | 0.0% | **100.0%** |
| `anthropicMessages` | 59.1% | 68.2% |

`anthropicStream` 从 0% 到 100% 靠的是第 5 个用例 `TestAnthropicStreamReportsRealTerminalState`
—— 用 `newFakeUpstream` + `testPoolWith` 造真实 Handler，走完整
`anthropicMessages → anthropicStream → chatCompletions → SSE 回写` 链路。
这不是"编排层没测"的补课，而是**本轮改动的对象本身**：改了 `anthropicStream` 却不测它，
等于没验证过这个函数。


### 3.3 测试都在断言什么

- `TestTransformOpenAIToAnthropicStopReason`：5 个 `finish_reason` 取值，既断言结构体字段，
  **也断言它真的出现在 wire JSON 里**（`"stop_reason":"max_tokens"`）——只断言字段会漏掉
  `json` tag 写错的情况。
- `TestTransformOpenAIToAnthropicKeepsModelAndUsage`：model / usage / content / `msg_` 前缀全保真。
- `TestStreamInterceptWriterCapturesTerminalState`：喂一段真实形态的 OpenAI SSE
  （两个 delta + 一个带 finish_reason 与 usage 的尾 chunk + `[DONE]`），断言
  `model=glm-5.2`、`finishReason=length`、`usage=(5,9)`，**且内容仍照常转成
  `content_block_delta`**（加了捕获不能少写事件）。
- `TestStreamInterceptWriterSurvivesSplitLines`：在**每一个字节位置**切开 SSE 输入，
  断言内容不丢。SSE 分块到达是常态，丢半截行就是吞字；原实现已在回填，
  但 0% 覆盖意味着从没人验证过。

## 四、能力边界（诚实声明）

- **无真实 Anthropic 客户端（Claude Code / anthropic SDK）联调证据。** 端到端测试用的是
  fake 上游 + `httptest`，验证的是"上游 OpenAI 形态 → 我们的 Anthropic SSE 输出"这段；
  Anthropic 真客户端能否接受，本轮未验。这是 `未验证`，不是 `已验证`。
- `message_start` 的 `usage.input_tokens` 仍为 0（该值只有上游尾 chunk 才有，
  而 message_start 在它之前发出）——属已知限制，未改。
- `transformAnthropicToOpenAI` 80.0%、`anthropicMessages` 68.2%：未覆盖的是错误分支
  （body 非法 JSON、上游非 200、transform 失败）。这些分支有既有用例如
  `TestAnthropicBodyLimit` 覆盖 413，其余未逐个打穿。

## 四之一、本轮发现但未修的问题（棘轮盲区）

`check-depth.ps1` 的 `minCoverage` 只记录**全局最低包**的覆盖率（当前 78.7 =
`internal/alerting`）。这意味着：

- `internal/server` 从 78.5% 涨到 80.5%，**这个提升不受棘轮保护** ——
  下次有人把它降回 79% 也不会变红。
- 棘轮保护的是"最差的那个包别更差"，不是"每个包别退步"。

修法（记录，未实施，避免与本批正交）：`depth-baseline.json` 增加
`packageCoverage` 映射，逐包记下限。属独立主问题。


## 五、回滚

```powershell
git checkout -- internal/server/anthropic.go
```

测试文件保留也无害（它引用的字段没了会编译失败，需一并 `git checkout` 或删除）。
