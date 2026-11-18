# WAF IP 级拦截状态机零验证

日期：2026-11-18
提交：`<待填>`
上一提交：`9652bb9`

## 一、问题

`internal/server/wafip.go` 是 WAF IP 级拦截的 fail-fast 状态机：

- `noteWaf(uid) bool` —— 记一次某账号的 WAF 403，返回是否判定为 IP 级拦截
- `active() bool` —— 报告是否处于激活期（末端错误文案区分措辞用）

**两个函数覆盖率都是 0.0%，仓库里没有任何 wafip 测试**。

机制本身是接通的（`chatrun.go:631` 调用 `noteWaf`，`chatflow.go:232` 调用 `active()`），
所以这不是死代码 —— 是一个**在工作、但从未被验证过**的安全机制。

## 二、它保护什么

`wafip.go` 头部注释写清了动机：WAF 403 拦的是网关**出口 IP**，不是某个账号
（BulidH 实测 3 个账号 1 秒内全 403）。既有账号级软冷却在 IP 级拦截下不够：
轮转会把一次客户端请求放大 `MaxRotate` 倍，同一出口 IP 继续打上游只会加重风控。

判据是「窗内**不同** UID 数」而非命中次数。这个口径如果不成立，后果是双向的：

| 失效方向 | 后果 |
|---|---|
| 判得太松（同号也累计 / 阈值判 1） | 一个坏号把整条出口路径封掉，所有健康号跟着挨饿 |
| 判得太紧（窗不清 / 不剪枝） | 风控持续期不明，轮转放大风控，账号池被连带打死 |

两个方向都不会报错，只会让养号效果变差 —— 与 AGENTS.md 里「基于噪声把健康号排除
比功能缺失更危险」是同一类风险。

## 三、补的 6 个用例（全部对应注释里明写的契约）

| 用例 | 守的契约 | 失效后果 |
|---|---|---|
| `TestWafIPGateSingleAccountNeverActivates` | 「单号反复 403 永不触发」——连续 10 次同 uid 不激活，且 `len(hits)==1` | 一个坏号封掉整池 |
| `TestWafIPGateTwoDistinctAccountsActivate` | 窗内两个不同 UID → 激活 | IP 级拦截永远建不起来 |
| `TestWafIPGateExpiredHitsArePruned` | 窗外命中被剪枝（注入 50ms 窗，sleep 80ms） | 孤立的旧事件和今天的新事件拼成「多号」，误判 |
| `TestWafIPGateActiveDoesNotRenew` | 激活期内新命中不续期 | 持续流量下永不解封 |
| `TestWafIPGateClearsWindowOnActivate` | 激活时清空判定窗 | 一次误判自我延续 |
| `TestWafIPGateDeactivatesAfterWindow` | 窗口过去后自然解除，且新命中要从零计数 | 解除瞬间立刻再次激活 |

时间控制：`wafIPWindow` 是 `var`（生产恒 60s），测试用 `withWafWindow` 注入短窗，
`t.Cleanup` 恢复原值 —— 不恢复会污染同包其它用例（这是包级全局变量）。

## 四、变异验证（确认测试真的有牙齿）

把 `len(g.hits) >= wafIPThreshold` 改成 `>= 1`（即「单号第一次就激活」）：

```
--- FAIL: TestWafIPGateSingleAccountNeverActivates
--- FAIL: TestWafIPGateTwoDistinctAccountsActivate
```

恢复后 `ok`。两个用例分别从两个方向拦住这个变异 —— 不是只有正向路径有断言。

## 五、覆盖率

| 函数 | 改前 | 改后 |
|---|---|---|
| `wafIPGate.noteWaf` | 0.0% | **100.0%** |
| `wafIPGate.active` | 0.0% | **100.0%** |
| `internal/server` 包 | 81.2% | **82.1%** |

单批 +0.9pp 是本轮最大一次提升 —— 因为两个函数是从 0 直接到 100。

棘轮 `-Update` 到 82.1。

## 六、能力边界

1. **只测单元状态机，没测端到端接线**：`noteWaf` 在 `chatrun.go:631` 由
   `kind == upstream.ErrWafBlock` 触发，`active()` 在 `chatflow.go:232` 改错误文案。
   这两条链路的**集成行为**（真撞 WAF → 真的终止轮转 → 客户端真的看到 IP 级措辞）
   仍只有既有 chatrun 测试间接覆盖，本批未新增端到端用例。
2. **未验证真实 WAF 场景**：所有用例都是构造 UID 序列，没有真实上游 403。
   阈值 2 的依据是 BulidH 实测（3 号 1s 全拦），那是一次性外部证据，不可复现。
3. **进程内状态**：重启清零是设计意图（窗 60s，重建成本低），本批未测重启行为
   —— 也不需要，因为根本没有持久化路径。

## 七、回滚

```powershell
git revert --no-edit <本批 sha>
```

回滚后包覆盖率回 81.2%，基线需同步调回。
