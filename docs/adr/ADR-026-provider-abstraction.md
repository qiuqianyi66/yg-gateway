# ADR-026：Provider 抽象分层（单厂商养号池 → 多厂商账号网关，增量式）

- 状态：提案（P0 已落地骨架）
- 日期：2026-11-14
- 关联：REQ `REQ-multi-provider-2026-11-14`、ADR-009（持久化版本闸门）、ADR-015（备用上游级联）

## 背景

magpie 的账号侧是三件事：provider 预设 + 任意兼容 URL、每订阅多账号额度耗尽切换、五种路由组。YG 的账号侧是另一件事：CodeBuddy 账号的**生命周期运营**（签到 / 成长 / 连签 / 旅行 / 积分到期 / 成本实测 / 熔断 / 连败降权）。

两者不在同一层，但共用同一个问题：「下一个请求该发给谁」。现在 pool 的答案只有一种语义——credits。要接第二家（Ark / 豆包 / 订阅），必须先把「provider 是什么形状」抽出来，否则 credits 会被硬塞进没有积分概念的账号。

## 决策

引入 `internal/provider` 作为第一层抽象，**只描述静态形状**（ID / 协议族 / 额度语义 / 端点 / 模型目录），**不含任何运行期状态**。它是叶子包（零 internal 依赖），P0 阶段不接线，现有代码一行不改。

分层顺序：Provider（P0）→ Credential + Quota（P1）→ Router + 面板（P2）。

## 备选与权衡

| 方案 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| **A 双轨并行**：新建 `internal/accounts` 通用池，与 pool 并行 | 零风险，今天就能接 Ark | 长期两套池、面板两个真相、并发闸门与熔断各算一套；两套都要养 | 否：短期省事，长期双维护 |
| **B 一步重构**：`auth.Auth` 直接拆成通用 `Credential` | 一步到位，无中间态 | 30 包全受影响（auth/pool/scheduler/panel/server/upstream），无零回归路径；失败无回滚点 | 否：不可逆面太大 |
| **C 分层增量**（采纳） | 每阶段可独立回滚；P0 零回归；新能力按需接入 | 并存期存在两个概念（Descriptor 在 provider，credits 在 pool），需纪律防漂移 | 是 |

C 的防漂移边界：**Descriptor 只答「什么形状」，pool 只答「现在什么状态」**。任何剩余额度、健康分、冷却时间都不得进入 Descriptor。

## 后果

正面：
- 接第二家 provider 不再需要改 pool 内部状态机；
- 选号策略可按 `QuotaKind` 分派，而不是在一个权重函数里塞 if；
- `fallback` 从"唯一兜底"升级为"注册表里的一个成员"，语义统一。

负面（明写）：
- 并存期两处概念，需要评审时检查漂移（见上）；
- P1 会动 `auth.Auth`（加字段），属于凭证层变更，需单独 ADR 记录；
- 不解决豆包逆向的签名问题——那需要运行时，与本 ADR 无关。

## 回滚

- P0：删除 `internal/provider` + `scripts/check-boundaries.ps1` 的 `provider` 条目，无数据影响。
- P1：`config.providers` 空 = 单 provider；新字段零值 = codebuddy。
- 不涉及 `state.json` 版本升级（ADR-009 闸门不动）。
