# 代码块：从 `map[string]any` 安全取值

唯一来源：`internal/pkg/jsonsafe`（2026-11-02 建）。测试：`jsonsafe_test.go`。

## 什么时候**必须**用

值的"缺失"与"零值"会导致**不同行为**时：

- 成本/用量台账（`credit` 缺失 ≠ 免费 —— 这是本仓出过的真实事故）
- 状态机判定（账号是否可用、模型是否存在、限流是否命中）
- 对外 wire 输出（字段缺失 vs 空串，客户端行为不同）
- 错误分类（`error.code` 缺失不能当 0）

```go
// ✅ 正确：缺失即 unknown，调用方显式决定
usage, ok := jsonsafe.Map(resp, "usage")
if !ok {
    return errMissingUsage // 或返回 unknown 态，别用 0 兜底
}
credit, ok := jsonsafe.Float(usage, "credit")
if !ok {
    // 上游没给 credit：这是 unknown，不是"免费"
    return ledgerEntry{CreditUnknown: true}
}
```

## 什么时候 `, _ :=` 是**合法**的（不必改）

值后面立刻被守卫，零值与"缺失"走同一条分支：

```go
// ✅ 合法：空串与缺失同样处理，语义无歧义
model, _ := obj["model"].(string)
if model == "" {
    return // 未知模型 → 不做任何注入
}

// ✅ 合法：只关心"有没有"
if id, _ := tc["id"].(string); id != "" { ... }
```

**判据一句话**：零值会不会在**没有守卫**的情况下流进决策/台账/输出？会 → 用 jsonsafe；
不会（下一行就判空）→ 保持现状。

## 反例（本仓踩过的）

```go
// ❌ 事故：credit 缺失 → 0 → 被当成"免费"写进成本台账 → 付费号被判免费号优先选号
c, _ := usage["credit"].(float64)
// ✅ 现写法（helpers.go usageCreditTotal）：jsonsafe.Float + creditUnknown 三态
```

## 为什么不做"全量迁移"

2026-11-02 实测：全仓 `x, _ := m["k"].(T)` 共 62 处（含 3 处注释）。逐处读过之后，
**绝大多数是上节说的合法形态**（`payload.go:104/184/217`、`sse.go` 的 16 处等都紧跟守卫）。
机械替换 62 处 = 触碰最热的 SSE 解析路径、零语义收益、巨大回归面。

正确做法：**改到哪、就近迁哪**；新增代码一律用 jsonsafe。已在 PRD 记为 expand-contract。
