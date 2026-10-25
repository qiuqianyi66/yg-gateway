# 2026-10-25 routing-p1p2（软带 + 近期用量衰减落地）

## 背景与必要性

用户指令链：实跑 magpie 找真实差距 → 研究明白了动工 → 子 agent 审查。前两轮（深读
+ 二轮源码研读）已把 pace 公式/90% 带三档分层/半衰期 token 维度全部实锤并修正方案；
本轮实跑 magpie（nogui 构建 71.5MB + `magpie serve` headless + CLI 探测）补齐运行时
观察（深读文档 §七：12 端点/富模型卡/账号自动发现/usage.jsonl 的 ep+kind+source 维度/
**WorkBuddy 仅签到无养号体系**——差异化叙事实证），随后按 REQ 实现 P1+P2。

## 改了什么

**P1 软带（默认无开关即生效——floor>0 的部署才受影响）**：
- `internal/pool/softband.go`（新）：`softBandUpper`（带宽 = max(floor×1.2,
  floor+2×实测每1k扣费×20k典型请求)——不再拍倍率，按 REQ「还敢接 N=2 次收费请求」
  定义；20k token/请求为显式假设，真机分布后校准）+ `softBandFactorOf`。
- `routingWeightOf` 挂 ×0.5 因子；`pickReason.Softband` 标记（决策溯源同步）。
- ≤floor 仍硬拦（软带不是逃逸口，测试锚定）。

**P2 近期用量衰减（`pool.recent_usage_factor` 默认关，零回归）**：
- `entry.recentTokens/recentTokensAt` 衰减账（写于 RecordTokenUsage 的 p.mu 写锁，
  读于 pick 读锁——锁互斥保证安全）；`decayRecentTokensTo`（先衰减再累加）+
  `recentUsageFactorOf`（2^(-v/200k)）。
- `RecordTokenUsage` 拆出 `RecordTokenUsageWithTime`（p.now() 注入口径——顺手统一
  时钟，与 F-2 同族）；缺字段调用不入账（unknown 不当 0 记）。
- `SetRecentUsageFactor` + main 装配一行；config.go 加字段前压缩注释腾行（1089=基线）。

## 怎么验证的

pool 包全量绿（新增 5 测试：带内/带外/硬拦三态、floor 关无带、200k→0.5 与 12h→1
衰减数值、缺字段不污染账）；全量 pre-commit（见下）；**测试抓到一个真 bug**：首次
入账 recentTokensAt 未初始化（decay 只推进已有时间戳）→ 因子恒 1——修复后数值锚定。

## 怎么回滚

P1：单提交 revert（floor>0 部署恢复纯硬拦）。P2：config 键删掉即等效关闭
（默认 false），代码 revert 同一提交。

## 遗留

bandMult 的 20k token/请求假设与实测台账中位数的取法（当前逐次计算，仅近 floor 号
触发，热路径无感）——真机分布后复核（REQ 状态注明）。

## 独立审查状态（如实记录）

子 agent 独立复审按协议尝试 **2 次，均被环境并发配额阻断**（model concurrency limit
exceeded）→ 按编排协议放弃子 agent 路线，转**主线自查**（不冒充独立复审——L-16
要求另起一人，review.md 留空待将来独立补）。主线自查发现并修复（这些是真实 findings，
但复核者与实现者同为本次会话，证明力打折扣）：

- 🟡 **热路径声称不成立**：summary/注释原称「中位数只在近 floor 号上被调用，
  热路径无感」——实际 `routingWeightOf` 对**所有**健康候选调用 `softBandFactorOf`，
  中位数计算会跑满热路径。修复：带宽硬上限 `floor×0.5`（软带是坡道不是跑道）+
  一行预筛（credits > floor×1.5 恒带外），中位数只对预筛通过的少数号执行。
  新测试 TestSoftBandPrescreenCap 直查 `softBandUpper` 断言 cap（500→150）。
- 🟢 测试算术错误两处（149/cap 边界算错、modelCost nil map 直填）——测试修复过程
  中被机器抓到（panic + 数值断言），未流入生产。
- 🟢 R10 形态一处（`f, _ :=` 忽略布尔）——pre-commit 检查器抓到，改用 inBand 分支。
- 🟢 P2 真 bug 一处：首次入账 recentTokensAt 未初始化（decay 只推进已有时间戳）→
  因子恒 1——新测试的数值断言抓到，已修+锚定。

待办：环境允许时（或人工新会话）按本文清单跑一次真正的独立复审补 review.md。
