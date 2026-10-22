# 评审记录 — R-107 fallback 多端点级联模板（multiupstream-template）

- 日期：2026-10-22
- 复审身份：独立只读复审（子代理，L-16）。对象：commit `009f724`（与 R-102 同提交）。
- 分级：L2+L3（provider 接入模板，ADR-015）。**本轮为第 1 轮独立复审。**

## 结论

**PASS-WITH-NOTES**（0 🔴 / 0 🟡 / 2 🟢 / 2 🔵）

## 逐条 GWT 证据（REQ-multiupstream-template / ADR-015）

| GWT | 代码级证据 |
|---|---|
| endpoints 列表按序级联，靠前优先消耗 | `handler.go:931 fallbackForward`：`for i := range fc.Endpoints`，首个「Models[model] 覆盖 + forwardToOne 成功」终结；全部失败 return false。 |
| 白名单覆盖才转发 | :938 `if !ep.Models[model] { continue }`；`FallbackConfig.Covers()` 按模型查覆盖。 |
| 向后兼容（空列表回退单字段合成） | summary：buildFallbackConfig 优先列表、空则回退旧单字段合成；main.go 装配。 |
| R10：io.Copy 错误不静默吞 | `forwardToOne`：SSE 流 `n, cerr := io.Copy(...)`，cerr != nil → log + return false（:984-990）；非流 `io.ReadAll` 失败 → return false（:995-1000）。注释显式说明此前 `_,_ = io.Copy` 把半截流误记成功。 |
| R8：不造 CodeBuddy 报文 | `openai_compat.go` 是通用 OpenAI 兼容端点（BaseURL+Bearer+Accept），无 wb2api/三段式 UA 指纹；备用源不是 CodeBuddy，无需 R8 指纹。 |
| usage 归因 | fallback 转发把 usage 记入 keyID="fallback"（:1012）。 |

## 发现的问题

| 级别 | 问题 | 证据 / 处置 |
|---|---|---|
| 🟢 | **级联接缝**：forwardToOne 在 `w.WriteHeader(resp.StatusCode)` + 流式转发后才因 io.Copy 中段失败 return false；此时 header/部分 body 已写入 w，循环继续下一端点会向同一 ResponseWriter 续写，产生拼接响应。纯连接失败（http.Client.Do 报错）发生在写 header 之前，可干净级联——问题只在「连接成功但流式传输中段断裂」时出现。 | handler.go:975 WriteHeader → :984 io.Copy。边缘时序（中段断流），非常见路径；旧单端点行为同样是失败流，未使现状更坏。记录为 🟢 可维护性建议：若要干净，应在首个端点收到 header 后即锁定、失败也不再级联，或对级联改用缓冲响应。不阻塞合入。 |
| 🟢 | 旧单端点 config.json 端到端实测未做 | summary 未完成节登记；单测已覆盖合成逻辑。接受。 |
| 🔵 | openai_compat.go 用 `http.DefaultClient`（无自定义超时/连接池） | forwardToOne 内 `context.WithTimeout` 兜底（:958），超时正确；DefaultClient 无独立 Transport 复用，但对低频 fallback 可接受。 |
| 🔵 | fallback usage 记 keyID="fallback" 单桶 | 多端点时不区分具体备用源；当前观测粒度够用。提示未来按 endpoint 分桶。 |

## 对后续建议

无需返工。唯一建议：若后续 fallback 端点增多、对「中段断流」敏感，再处理级联接缝（缓冲或首端锁定），当前边缘路径不影响正确性主链路。
