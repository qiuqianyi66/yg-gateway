# 代码块：HTTP 端点回 JSON

## 标准写法（两处逐字节相同，**故意不合并**）

```go
func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
```

出处：`internal/panel/panel.go`、`internal/server/handler.go`（2026-11-02 实测**逐字节相同**）。

## 为什么不抽成公共包

- 它是 6 行、无状态、无业务语义的样板；抽出去只多一层 import 跳转，不消除任何认知负担。
- 真正会漂移的是**错误体形状**，而两处形状本来就不同：
  - `panel`：`{"ok":false,"error":"..."}`（面板内部约定）
  - `server`：`{"error":{"message","type","code"}}`（OpenAI 兼容 wire 契约，客户端在解析）
  合并这两个才是错的——会破坏 OpenAI 兼容形状。
- 判据来自 `.harness/snippets/README.md`：**先有第二份、且第二份会一起变，才建抽象**。
  这里两份不会一起变（一个内部约定、一个对外契约）。

## 重评条件

出现**第三处**同类写出，或两处开始需要一起改（例如统一加 `Cache-Control: no-store`）时，
再抽 `internal/pkg/httpx.WriteJSON`。

## 注意（R10）

`json.Marshal` 的错误在这里被显式忽略，属于**已论证**的忽略：`v` 都是本包构造的
可序列化结构，Marshal 失败只可能是循环引用/不支持类型（编程错误，测试会先炸）。
新增此类忽略必须按 `.harness/rules/ERROR-HANDLING.md` 写理由，否则 `pre-commit` 检查 2 会红。
