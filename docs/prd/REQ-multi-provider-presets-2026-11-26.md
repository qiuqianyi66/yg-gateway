# REQ: 多厂商 provider 预设与零 auths 接入（2026-11-26）

> 状态：**实施中**。对象是 `config.providers[]`、`cmd/server` 多 provider 装配与
> `internal/server` 选号路径。

## 必要性

- **真实问题**：现有 `providers[]` 已支持 Ark / OpenAI 兼容端点，但启用一个
  provider 仍必须手写 `auths/*.json` 账号；只配 `providers[].api_key` 时池里没有
  该 provider 账号，请求会得到 `no_healthy_account`。这不是配置错误提示问题，而是
  “配置完整却不可用”。
- **第二个真实问题**：非 codebuddy provider 账号没有 OAuth token（`ExpiresAt=0`），
  `selectAccount` 仍走 `NeedsRefresh`，每请求 refresh 失败 → provider 即使有 API Key
  也无法出站。
- 用户需求：把网关做成多个厂商的中转站，并先接入有免费额度的 Cerebras；不做
  多租户计费、兑换码、OIDC。

## 契约

1. **preset**：`providers[].preset` 可取 `cerebras`；只填未显式给出的
   `id/kind/quota/base_url/models`，显式字段优先，凭证永不从 preset 读取。
   未知 preset 即使 `enabled=false` 也 fail fast。
2. **零 auths 接入**：启用 provider 且有 `providers[].api_key`、且池内没有该
   provider 账号时，启动时创建内存账号 `provider:<id>`（不写 auths、不落盘）；
   已有 auths 账号时不创建，保持多 key 形态。
3. **非 codebuddy 不刷新 OAuth**：只有 `ProviderID()=="codebuddy"` 的账号才走
   `NeedsRefresh`；API Key provider 直接出站。
4. **路由不串池**：provider 模型仍按 `models[]` 白名单路由；未命中走 CodeBuddy；
   provider 失败只换该 provider 的账号，不泄漏到 credits 池。
5. **provider 失败语义**：非 2xx 保留最后一次上游状态码/body 到 `run.lastErr`，
   新增 `ErrProvider`：5xx 映射 503、4xx 映射 502，业务码 `upstream_error` 并透传
   可读原文；客户端取消（`r.Context().Err()!=nil`）记 499/interrupted，不
   `NoteError`/不罚 provider 账号、不再轮转。
6. **零回归**：`providers` 为空、旧 auths provider 账号、Ark 路径行为不变。

## Given-When-Then

- When 只配 `{"preset":"cerebras","api_key":"..."}`，Then `/v1/models` 出现预设模型，
  请求命中后出站到 `https://api.cerebras.ai/v1/chat/completions`，Bearer 使用该 key。
- When provider 已有 auths 账号，Then 不再创建虚拟账号，账号级 key 优先。
- When provider 账号无 OAuth token，Then 不尝试 refresh，不产生每请求 503。
- When provider 上游返回 5xx，Then 末端不再只有 no_healthy_account，原文/requestId
  可读；客户端已断开时则记 499/interrupted 且不罚号。
- When 模型未命中任何 provider 白名单，Then 仍走 CodeBuddy credits 池。

## 非目标

- 不做几十个 provider 的全家桶管理、计费、兑换码、OIDC、多租户。
- 不把厂商模型目录写成永久承诺；preset 模型需按官方文档/`/v1/models` 核对。
- 不自动从网络抓取 provider 模型列表（后续可加，需单独 PRD）。

## 回滚

- 配置回滚：删 `preset`/provider 条目或置 `enabled=false`。
- 代码回滚：还原 `ProviderConfig.Preset`、`provider.Presets`、
  `ensureProviderAccounts` 与 `selectAccount` 的 provider 刷新条件；删除新增测试。
