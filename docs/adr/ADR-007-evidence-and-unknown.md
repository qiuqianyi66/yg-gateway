# ADR-007：事实四态标记与 Unknown 消解实体

- 状态：已采纳
- 日期：2026-10-15
- 相关：ADR-005（EWMA 与 pick 读锁）、`docs/prd/REQ-yg-gateway-architecture-foundation-evidence-2026-10-15.md`

## 背景

网关对外暴露的很多值**看起来是事实，实际是推断**。典型三例：

1. **成本 tier**：`accounts[].model_costs[].cost_per_1k` 是从少量实测 `usage.credit`
   样本外推的。线上实测（2026-10-15）同一次快照里，账号 `00c67396` 的 tier 只有
   **1 个样本**，账号 `71b4291d` 有 **145 个样本**——两者的可信度天差地别，但
   旧结构里长得完全一样。
2. **EWMA 成功率**：是平滑派生值，不是真实成功率；冷启动时 2 条样本算出 1.0 会被
   读成"这个号 100% 成功"。
3. **能力字段缺失**：`/v1/models` 的 `supported_efforts` 为空、`supportsImages`
   缺失，其实是上游**精简目录没返回**（CLI 三段式 UA 拿到的是精简目录），属于
   **未知**——但旧实现把它当成"不支持"处理。

消费方（养号决策、面板、agent）无法区分这三者与真观察，后果是**基于噪声做硬性
排除**：凭 1-2 条样本把一个健康号踢出选号，而且**不报错**——比功能缺失更难发现。

同一问题的另一面：存在大量"暂时不知道、但可消解"的事实（如"账号 X 对模型 Y 的
成本"），旧实现把它压成一个枚举值（tier 1）就没人跟了——没人知道它何时能被消解、
还差什么。而 tier 未知时 `credit_floor` 会放行收费模型，可能一笔打穿余额
（kimi-k3-1 实案形态）。

## 决策

### 1. 引入四态标记（`internal/evidence/`）

对外暴露的每个事实标注其认识论类别：

| Kind | 含义 |
|---|---|
| `observation` | 直接观测到（上游状态码、账号被禁用） |
| `derivation` | 无损或误差已知地算出来（EWMA、均值、分桶插值分位数） |
| `inference` | 基于样本推测，有置信度问题（成本 tier、USD 折算价） |
| `unknown` | 不知道，且说明还需要什么 |

采纳 rea 的原则：**Missing coverage is unknown, not absence**（没覆盖 = 未知，
而非"没有"）。

三条实现约束：
- **零值即 unknown**：忘了标注时落到最保守一档，而不是默认当成观察。
- **样本数随值一起出去**：`samples` 字段是可信度/消解距离的唯一结构化表达。
  >0 就输出，与 Kind 无关（unknown 但已有 2/5 样本时，那也是重要信息）。
- **零依赖 domain 层**：不 import 任何 internal 包，可被任意层引用。

### 2. Unknown 升级为一等公民实体（`internal/unknown/`）

- 登记时**必须**写明 `requires`（需要什么才能消解）；
- 带 `revision`，用过期的 revision 调 Resolve **必须报错**，调用方须重读后重试，
  禁止盲重试；
- 消解**只能由合格观察证据**完成（必须给 evidence ID）；
- 同 `(question, requires)` 幂等去重（ID 为内容哈希，跨重启稳定）；
- `List` 只返回未消解（不占注意力），`All` 返回全部含已消解（供审计）。

## 关键取舍

### 取舍一：标注不确定性 vs 隐藏不确定的值

**本轮真实踩坑**：最初实现是"EWMA 样本不足就**不输出** `ewma_success` 数字"。
`TestEwmaStatusExposure`（ADR-005 既有契约测试）立刻抓到——它断言"开启 + 1 次
成功后 ewma_success 应 = 1.0"。

这个回归暴露了 ADR-005 的**既有透出契约**：样本未达 `minSamples` 也透出原始值，
因为"是否参与加权"与"是否透出"是两个独立问题，运维需要能看到 EWMA 是否在工作。

**修正为**：保留数值 + 标注 `unknown`。理由来自 rea 的 *"Preserve partial facts"*
——不确定性应当被**标注**出来，而不是把值藏掉；藏掉会让运维失去观察手段。
标注比隐藏更诚实，也更少副作用。

### 取舍二：Unknown 登记点放在请求路径，但不放进 pick 临界区

`TrackCostUnknown` 在 handler 选号成功后调用，而不是在 `pick` 内部：
pick 处于 pool 的锁临界区（ADR-005 特意改成读锁以避免串行化），不该再碰别的锁。

幂等去重用 `sync.Map.LoadOrStore`（一次原子读）而非 `p.mu`，避免让"同一组合只
登记一次"的判重与选号争锁。ID 由内容预先派生，因此可以"占位 + 拿到 ID"一步
原子完成，不存在"占位了但 ID 还没写"的窗口。

### 取舍三：UNKNOWN 状态不持久化

`unknown.Registry` 是进程级单例，重启归零。理由：unknown 的语义是"当前还不知道"，
重启后重新发现是自然的，且重新发现成本低（一次请求即可）。持久化会增加落盘格式
与迁移成本，收益不足。ID 是内容派生的（跨重启稳定），将来若需持久化可直接落盘
——这一点已为演进留好路。

## 后果

**正面**
- 消费者能区分"1 个样本的推断"与"145 个样本的推断"，不再基于噪声排除健康号。
- 能力字段缺失被表达为未知，而不是被当成"不支持"或"放行"。
- tier 未知的 (账号,模型) 组合被显式跟踪：`/metrics` 的 `yg_gateway_unknowns_open`
  **持续不为零本身就是信号**（要么池子在扩，要么某些未知永远消解不掉——后者说明
  `requires` 设计有问题）。
- 消解链路自动：实测到 credit 即消解，无需人工介入。

**负面/成本**
- `/status` 输出变大（每个账号多 provenance + unknowns 段）。
- 热路径上多一次 `sync.Map.LoadOrStore`（命中路径一次原子读，纳秒级；已在
  handler 选号成功后调用，不在 pick 临界区）。
- 为下游工具（如 `internal/tools/doccheck`）提供了新的 lint 目标：新暴露的事实
  若缺 provenance 应被提示。

## 真机验证（2026-10-15，PID 24572）

- `accounts[].model_costs[0].provenance` = `kind=inference samples=1` 与
  `samples=145` 并存——正是本 ADR 要解决的问题的可观测证明。
- 冷门模型 6 发全部返回 credit → `unknowns_open 0`（首次使用登记、完成即消解，
  无残留 = **消解链路在工作**）。
- 请求不存在的模型（上游 400、无 credit）→ `unknowns_open 3`，三个账号各留一条
  未消解未知，Q/R/rev 均正确 = **登记链路在工作**。

## 参考

- morluto/rea：`AGENTS.md`（Distinguish observations, derivations, inferences, and
  unknowns）、`docs/tool-design.md`、`.agents/skills/reverse-engineer-anything/references/evidence-workflows.md`
