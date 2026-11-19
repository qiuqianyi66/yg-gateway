# 用 fuzz 找解析器的崩溃：一个 `null` 打死整个网关

日期：2026-11-19
上一提交：`85a05f8`

## 起因

用户要我看大厂开源项目的代码风格与严谨程度。查了实际的代码库而不是听说法：

| 项目 | fuzz 目标落在哪 |
|---|---|
| `caddyserver/caddy` | `caddyconfig/caddyfile/formatter_fuzz.go`、`lexer_fuzz.go` —— 配置文件解析器 |
| `golang/go` | `src/archive/tar/fuzz_test.go`、`src/archive/zip/fuzz_test.go` —— 外部格式解析器 |
| `yg-gateway` | **0 个** |

模式很一致：**成熟项目把 fuzz 放在"解析外部不可信输入"这一步**，因为那里出问题不是
"这个请求失败"，是服务死掉。而网关天天解析外部 JSON 与 SSE，一个 fuzz target 都没有。

## 做了什么

给三个"外部输入入口"各加一个 fuzz target：

| 目标 | 函数 | 位置 |
|---|---|---|
| `FuzzTransformAnthropicToOpenAI` | Anthropic 请求 → OpenAI 请求 | `internal/server/anthropic_fuzz_test.go` |
| `FuzzResponsesToChat` | Responses 请求 → Chat 请求 | `internal/server/responses_fuzz_test.go` |
| `FuzzRewrite` | 出站 system 提示词注入 | `internal/prompt/prompt_fuzz_test.go` |

每个都内置了一批真实形态的种子（正常消息、工具调用、图片块、空对象、`null`、截断 JSON）。

## 抓到的真 bug

**`FuzzRewrite` 第一次跑种子就 panic**，栈顶 `prompt.go:62`：

```
--- FAIL: FuzzRewrite
panic: assignment to entry in nil map
  yg-gateway/internal/prompt.Rewrite(...)  prompt.go:62
```

机制：

```
var obj map[string]any
json.Unmarshal([]byte("null"), &obj)   // null 是合法 JSON，err == nil
                                        // 但 obj 是 nil map
obj["messages"] = ...                  // 对 nil map 赋值 → panic
```

`Rewrite` 与 `Append` **两处都有**同一行，都中招。

危害等级：网关是 7×24 常驻服务，任何客户端发一个字面 `null` 当 body，**整个进程崩溃**，
不是这一个请求报错。

### 修法

```go
if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
    return body
}
```

nil map 与"非对象输入"（数组、字符串、数字）同义，走同一条原样返回的降级路径，
行为矩阵不新增第三种状态。`Rewrite` 上方补了完整说明，写明为什么判据不能只看 `err`。

**`Append` 用 `replace_all` 一起修**：两个函数的这一行逐字相同，单独改一处必然漏另一处。

## 三个目标的 fuzz 结果

| 目标 | 执行次数 | 结果 |
|---|---|---|
| `FuzzTransformAnthropicToOpenAI` | 4,530,807 | 0 panic |
| `FuzzRewrite`（修复后） | 8,776,237 | 0 panic |
| `FuzzResponsesToChat` | 2,977,176 | 0 panic |

合计约 **1628 万次**任意字节输入，只有 `null` 一处崩溃。

## 为什么 seed 本身就是回归测试

`go test ./...` 会用种子集跑一遍 fuzz 函数（不加 `-fuzz` 时 fuzz target 等价于普通测试）。
`null` 已在种子里，所以**日常门禁就会守住这个修复**，不需要把长时 fuzz 挂进 pre-commit。

这样分工是清楚的：**fuzz 用来发现，seed 用来回归**。把 30 秒 fuzz 塞进每次提交会让本地
门禁无谓变慢，而它发现的每一个案例最终都该变成种子。

## 验证

| 检查 | 结果 |
|---|---|
| `go test ./internal/prompt ./internal/server -count=1` | 两包 ok |
| `gofmt -l ./internal/prompt` | 无输出 |
| fuzz 30s ×3 | 见上表 |
| 修复前 / 后同一 seed | panic → 通过 |

## 踩到的两个自己的坑

1. **Edit 的 `old_string` 划太大**：第一次改 `Rewrite` 时把 `if !ok { ... }` 整块包含进
   `old_string`、`new_string` 只写到注释行，函数体被截断（第 63 行 `if !ok {` 后直接接
   `kept := make(...)`）。`git checkout --` 恢复后重做，改成只替换一行。
   **教训：改代码时 `old_string` 只圈住要改的那一行，不要顺手带上上下文。**
2. **假设了不存在的函数**：`isJSONObject` 里先写了 `jsonUnmarshal`，实际包内只有
   `json.Unmarshal`。先查再写，别凭印象。

## 明确没做的

- **不把长时 fuzz 挂进 `pre-commit.ps1`**（理由见上）。
- **不 fuzz SSR/SSE 流解析**：那条路径需要构造 `io.Reader` 与并发写入，当前 fuzz target
  直接喂纯函数更干净；把这部分纳入需要单独设计，未做，标 unknown。
- **没有改 `serve` 层的 body 读取**：`null` 到得了 `Rewrite` 说明它被当成合法 body 放了进来，
  这是否该在更早一层拒绝，属于产品语义（是否接受 `null` body），不做决定。

## 回滚

```powershell
git revert --no-edit <本批 sha>
```
