# REQ: 客户端取消与上游 5xx 的错误分类修正（2026-11-26）

> 状态：**实施中**。对象是 `/v1/chat/completions` 传输错误路径与轮转末端错误构造。

## 必要性

- **真实问题**：2026-11-26 归档 966 条请求中 5 条 503；其中 4 条 stderr 明确是
  `context canceled`（DSH 客户端主动取消/断连），但 `handleTransportError` 把它归为
  “上游网络抖动”，设置 503、`NoteFailures`、`run.fail`，最后退化成
  `no_healthy_account`。结果是客户端取消污染归档、健康号被累积连败。
- **第二个真实问题**：`rotateAndServe` 在循环结束后用循环前的 `lastErr/modelBlock/
  groupFilteredOut` 快照回写，抹掉 `handleErrorPath` 写入的最终上游错误；上游 5xx
  原文（含 requestId）丢失，统一伪装成本地无可用号。
- 不做会怎样：每次客户端取消都可能把健康号推出可用池，后续产生真正的
  `no_healthy_account` 503；上游 5xx 无法排障。

## 契约

1. **客户端取消先行判定**：`r.Context().Err() != nil`（含 `context.Canceled` /
   `DeadlineExceeded`）时，记录一次 attempt，状态记 499（内部审计值，不写 wire），
   outcome 记 `interrupted`，释放租约，**不调用** `NoteFailures` / `NoteError`，
   不继续轮转，不写响应。
2. **只有上游错误才罚号**：保留原有 `isUpstreamTimeout`、网络抖动、`NoteFailures`
   路径给真实上游失败；客户端取消不得进入这些分支。
3. **末端错误不覆盖**：`rotateAndServe` 结束后不回写循环前快照；
   `run.lastErr/modelBlock/groupFilteredOut` 保留最后一次 `handleErrorPath`/
   `selectAccount` 写入的值。
4. **上游 5xx 语义**：`finalErrorOf` 保留 503 可重试状态，但业务码改为
   `upstream_error`；`message` 把上游 body 压成一句可读文本（`code: msg`，
   有 requestId 则附上），不把 JSON 原样嵌进 `error.message`。
5. **零回归**：成功、上游 4xx、限流、模型阻塞、空流等既有路径不变；仅新增
   客户端取消分类与末端原文保留。

## Given-When-Then

- When 客户端在请求途中取消，Then 归档 status=499、outcome=interrupted；账号
  `consecutive_fails` / `err_total` 不增加；不写 503 响应。
- When 上游返回 503 且最终轮转耗尽，Then 响应 message 含上游 `msg`/`requestId`，
  业务码为 `upstream_error`，不再出现 `no_healthy_account`。
- When 上游返回 400 `bad_params`，Then 仍 fail-fast 400 且原文可读，不轮转。
- When 上游超时，Then 行为不变（`upstream_timeout`，不换号不罚号）。

## 回滚

- 代码回滚：还原 `internal/server/chatrun.go` 的 `statusClientClosedRequest` 分支与
  `rotateAndServe` 收尾；还原 `internal/server/chatflow.go` 的
  `upstreamMessageText` 与 `upstream_error` 映射；删除对应测试。
- 数据回滚：已写入的 499/interrupted 归档行保留（审计事实），不影响 pool/usage。
