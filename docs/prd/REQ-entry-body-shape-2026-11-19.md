# REQ-entry-body-shape-2026-11-19

**变更类型**：行为/契约变更（客户端可见）。
**一句话**：四个接 JSON body 的入口在读取后立刻判定「顶层是否为 JSON 对象」，
不是就地 `400 invalid_request`，不再透传到上游。

## 必要性

**不做会怎样（三条都有实测，不是假设）**：

1. `/v1/messages/count_tokens` 收到 `null` 时，`json.Unmarshal` 把结构体解成零值且
   **不报错**，静默返回 0 token——客户端拿到 200，以为计费口径成立。**没有任何信号**，
   比报错危险。
2. `[]` / `"x"` / `7` 每次都要占一个账号名额、跑一次上游往返，才拿回 11101；
   结果是「网关自己该拒的东西」被记成上游的错，排障时方向就错了。
3. `null` 在生产上真实出现过（2026-11-19 两次 panic 就是真实客户端发的）。
   「客户端不会发非对象 body」这个前提已被实测推翻，不能当默认。

**为什么现在做**：根因（nil map panic）已在 `upstream` 修好，但那是**兜底**——
请求还是要走到上游才被拒。补齐入口这一层，兜底才不必被触发。

**判定为「可以不做」的条件**（写下来供复核）：若上游对所有非对象 body 都能给出
稳定、可归因的客户端错误，且不消耗账号与往返，则该改无收益。实测三条都不成立。

**边际成本**：一个 20 行 helper + 4 行调用，无新依赖、无新配置、无新端点。

## 背景（三条旧结局，都是实测）

| body | 旧行为 | 问题 |
|---|---|---|
| `null` | 透传到 `upstream.prepareBody` → nil map 赋值 panic（已单独修复） | 请求被丢弃、无错误提示 |
| `[]` / `"x"` / `7` | 透传到上游 → 11101 → 包装成 `400 bad_params` | 白占一个账号名额 + 一次往返，错误被记成上游的 |
| `null` 打 `/v1/messages/count_tokens` | `json.Unmarshal` 到结构体零值且不报错 → **静默返回 0 token** | 看起来像成功 |

## 变更内容

新增 `internal/server/body_shape.go`：
- `bodyIsJSONObject`：只看**首个非空白字节**是否为 `{`（O(1)，不做完整解析）。
- `rejectIfNotJSONObject`：不是对象 → `400 invalid_request` + `request body must be a JSON object`。

四个调用点（读取 body 并处理完读错误之后立即判）：
`chatflow.go`（/v1/chat/completions）、`anthropic.go`（/v1/messages）、
`responses.go`（/v1/responses）、`count_tokens.go`（/v1/messages/count_tokens）。

## 刻意改变的既有语义（必须写明）

`chatflow_test.go` 的快照 case 3 此前钉的是「畸形 JSON 宽容放行到上游」，
注释称其为有意设计（重构期不引入新失败模式）。本次**有意改变**：非对象 body
一律就地拒绝。快照已按新语义重写并保留变更理由。

**代价（真实）**：上游 11101 的具体诊断文本（如 `unexpected EOF`）不再透出，
客户端只拿到「必须是 JSON 对象」。对一个本来就不合法的请求体，这是可接受的损失。

## 验收（GWT）

- **G1** 四个入口收到 `null` / `[]` / `"x"` / `7` / 空 body / 纯空白 → `400 invalid_request`。
- **G2** 对象形状 body 不被这条守卫拦（不误伤）。
- **G3** 被拒请求**不打上游**（用 `called` 标志断言）。
- **G4** 牙齿：注释掉任一处调用 → `TestNonJSONObjectBodyRejectedAtEntry` FAIL（实测 chat 路径 FAIL）。

## 回滚

删掉四个调用点即可恢复旧语义；`body_shape.go` 与其测试可一并删除，无其它依赖。

## 不做

不在入口做完整 JSON 解析（大 body 二次解码是纯 CPU 支出）；不改上游返回的诊断透传逻辑。
