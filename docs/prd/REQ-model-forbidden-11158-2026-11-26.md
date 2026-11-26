# REQ: 11158 账号类型无模型权限的分类修正（2026-11-26）

> 状态：**实施中**。对象是 `internal/upstream/errors.go` 分类、
> `internal/server/handler_errpolicy.go` 惩罚策略与 `finalErrorOf` 末端映射。

## 必要性

- 2026-11-26 生产回归探针实测：免费账号请求 `cn:hy3 ×0`，上游返回
  `400 {"code":11158,"msg":"model [hy3] is not allowed for user type
  [personal_free_user]"}`（日志 `yg-gateway-20261126-195857.err.log` 21:55:12）。
  旧分类落到通用 `ErrClient`：`NoteFailures` 累计账号连败，轮转耗尽后返回
  503 `no_healthy_account`。账号本身健康、模型名也没错，这是**账号类型
  entitlement**，不是池故障。
- 客户端据此会误以为网关服务过载并自动重试，健康账号还会被逐步推出池；
  属于“错误分类制造新故障”。

## 契约

1. `400` + 业务码 `11158`（或 `not allowed for user type` 文案）分类为
   `ErrModelForbidden`，证据等级：精确码 High / 文案 Medium。
2. 该错误**不冷却、不 NoteError/NoteFailures**；允许轮转，换到可能有更高账号
   类型的号上重试。
3. 轮转耗尽后末端返回 **400 `model_forbidden`**，message 透传上游原文
   （含 requestId），不得出现 `no_healthy_account` 或 503。
4. 其余 `ErrClient` 行为不变；11102/11133/11115/11135 等既有分类不变。

## Given-When-Then

- When 两个免费账号都返回 11158，Then 请求最终 400 `model_forbidden`，两个账号
  `consecutive_fails`/`err_total` 均不增加。
- When 第一个账号 11158、第二个账号支持该模型，Then 轮转后成功（不因首个账号
  的 entitlement 误判全池不可用）。
- When 上游返回 11102（模型不存在），Then 仍按既有 `ErrModelBlocked` 路径处理。

## 回滚

- 还原 `ErrModelForbidden` 常量/分类/策略/末端映射及对应测试；无数据迁移。
- 已产生的 400 归档行保留为审计事实。
