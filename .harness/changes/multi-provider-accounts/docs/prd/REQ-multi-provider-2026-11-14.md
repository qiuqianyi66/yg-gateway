# REQ：多 Provider 账号网关（P0 抽象脊柱）

| 项 | 值 |
|---|---|
| 分级 | L3（架构 / 领域模型 / 承重墙） |
| 复杂度 | C 耦合（auth + pool + upstream + server + panel） |
| 状态 | P0 已实现（骨架），P1/P2 待评审 |
| ADR | `docs/adr/ADR-026-provider-abstraction.md` |
| 对标 | `docs/reference/similar-projects-learnings-2026-10-25.md`（magpie 专项） |

## 一、问题

`pool` 的每一处语义都绑死 CodeBuddy：

- `auth.Auth` 只承载 OAuth 三件套（accessToken / refreshToken / expiresAt）+ device_token；
- `pool.entry` 承载 credits、到期批次、tier 成本；
- `pick.weightOf` 是积分专用权重组合；
- `scheduler` 的 17 个养号任务是 CodeBuddy 活动 API。

结果是第二家账号（火山方舟 Ark / 豆包 / 任意订阅）只能走 `fallback` 兜底——**不能进池、不参与选号、不共享并发闸门**。若把豆包「点位倍率」硬塞进 `credits`，语义污染不可逆（credit 是"到期会烧掉的积分"，不是"按倍率扣的点数"）。

## 二、目标

四层抽象，逐层落地（**不是**把 pool 改成 provider-agnostic 大重构）：

| 层 | 回答的问题 | 落点 |
|---|---|---|
| Provider | 这是什么上游（协议族 / 端点 / 模型目录 / 额度语义） | `internal/provider`（P0） |
| Credential | 凭什么调（OAuth / APIKey / Cookie）+ 怎么续 | `auth`（P1） |
| Quota | "剩余额度"是什么意思（积分批次 / 余额 / 滚动窗口） | `internal/quota`（P1） |
| Router | 下一个用谁（积分权重 / order / rotate / usage / pace） | `internal/router`（P2） |

P0 只落 Provider 层 + 注册表，**不接线、不改任何现有文件**，契约是零回归。

## 三、验收（Given-When-Then）

**G1 零回归**：Given 现有 30 包测试；When 加入 `internal/provider` 后跑 `go test ./...`；Then 25 ok + 5 no test files 不变，`/status` 字段名与顺序不变。

**G2 叶子层**：Given 新包；When 跑 `scripts/check-boundaries.ps1`；Then 通过，且 allow 表新增 `provider = @()`（零 internal 依赖），不引入任何新方向。

**G3 注册表语义**：Given 空注册表；When 注册重复 ID / 非法 Kind / 空 ID；Then 返回 error 且不写入；When 查未知 ID；Then 返回 `(Descriptor{}, false)`，不 panic。List 按 ID 排序，稳定输出。

**G4 四态纪律**：Given `Descriptor.Models` 为空；When 被面板/状态消费；Then 按 **unknown** 处理（字段省略），禁止渲染成「不支持」——同 AGENTS.md「`/v1/models` supported_efforts 为空 = 未知」口径。

**G5 并发安全**：Given 一个 Registry；When N 个 goroutine 并发 Register + List；Then 无数据竞争、无 panic（本机无 gcc，`-race` 由 CI 独占）。

## 四、不做（本期）

- **不接豆包 C 端 / 豆包工作**：需 Chromium 注入 a_bogus/msToken 或任务式接口适配，破坏「单二进制零依赖」；先消解 unknown（见 §六）。
- **不改 `auth.Auth`**：P1 才加 `Provider` / `APIKey` 字段。
- **不做多租户计费 / 兑换码 / OIDC**：定位红线，不因本需求松动。
- **不做路由组五模式完整实现**：P2。

## 五、P1 / P2 预告（不在本期验收）

- **P1**：`auth.Auth` 加 `Provider`（零值 = codebuddy）+ `APIKey`；`pool` 允许非 credits 账号入池（选号走 order，跳过 floor / tier / 到期逻辑）；`upstream` 加 `ark.go`（OpenAI 兼容 + Bearer，复用 `OpenAICompatEndpoint` 模板）。
- **P2**：`/status` 与面板加 provider 维度；路由组 `order/rotate/usage/pace`（`smart` 与现有"快过期优先"同构，不重复实现）。

## 六、Unknown（显式登记，不写成否定）

| Unknown | requires |
|---|---|
| 豆包工作 / 豆包 C 端是否有可查「剩余点位」的接口 | 一次带登录态的请求抓包，或官方文档 |
| 豆包工作的接口是否可映射为 chat completions 流 | 抓一次真实任务请求，看 SSE 事件形状 |
| Ark 免费额度（50 万 tokens/模型）的查询端点与口径 | 实测 `/api/v3` 的 usage/billing 端点 |

> 上述三项未消解前，「豆包进池」一律按 unknown 处理，不得写成「不支持」。

## 七、风险

- **R8**：provider 化时，WorkBuddy 的三段式 UA、`wb2api-<ms>` conversationId、`chatcmpl-wb2api` 哨兵必须留在 codebuddy adapter 内部，**不得**做成可配置模板或"统一改名"。
- **依赖边界**：P1 新增 `quota` / `router` 包必须更新 allow 表并写明理由，否则 `check-boundaries` 默认拒绝。
- **数据兼容**：`state.json` 缺 `provider` 字段一律按 codebuddy 解释；ADR-009 版本闸门不变（不升版本）。
- **两处漂移**：Descriptor（静态形状）与 pool（运行态）并存期间，禁止在 Descriptor 里放运行期字段（剩余额度、健康态）。

## 八、回滚

- P0：删除 `internal/provider` 包 + allow 表一行，无数据影响。
- P1：`config.providers` 置空 = 单 provider；`auth` 新字段零值 = codebuddy。
- 全量：不涉及 `state.json` 迁移，无不可逆变更。
