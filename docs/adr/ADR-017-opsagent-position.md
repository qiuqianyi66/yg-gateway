# ADR-017: opsagent 的编排层位置与观察面注入方式

- 状态：已接受（2026-10-24）
- 关联：`docs/prd/REQ-opsagent-2026-10-24.md`（MVP=观察-建议）、`docs/roadmap/HARNESS-GAP-AND-AGENT-EVOLUTION-2026-10-24.md` §三 L2
- 背景决策：ADR-007 系（unknown/evidence 语义）已把「受约束 agent 的 belief 骨架」铺好

## 背景

运维 agent 需要读池状态（pool）、错误分类（metrics）、并登记假设（unknown）。
有两种落法：A) 直接 import 三个包；B) 观察面以函数注入（依赖倒置），opsagent 只依赖
domain 层（evidence/logfmt/unknown）。

## 决策

**选 B：`internal/opsagent` 只 import evidence / logfmt / unknown（与 pool 同层的领域包），
pool 与 metrics 的数据由 `cmd/server` 装配期以闭包投影注入（Observers 结构体）。**

节拍：**自持 ticker**（默认 10m，`opsagent.interval_s` 可调），跑在应用级 ctx
（`signal.NotifyContext`）上，与 scheduler 的各任务时刻表解耦——scheduler 的节拍语义是
「养号任务何时打上游」，opsagent 的节拍是「本地状态何时复查」，混用会让加一个观察规则
变成改 scheduler 排程的事。

## 理由

1. **边界最小**：A 要在 allow 表加 3 条方向（opsagent→pool/metrics/…），此后 opsagent
   的每个测试都要构造真实 pool/metrics；B 只加 1 条（evidence/logfmt/unknown），观察面
   是纯函数，测试给 fake 即可。AGENTS 依赖方向「向内流动」在 B 下是结构保证而不是约定。
2. **投影即契约**：装配层写 `func() []AccountView` 时被迫显式选择暴露哪些字段——观察面
   的最小化是设计动作而非侥幸。A 路径下「顺手多读一个字段」没有阻力。
3. **panel 复用同一 registry**：unknownReg 在 main 装配期创建、同时注入 pool 与 opsagent
   （`pool.SetUnknownRegistry` 既有语义），面板看到的 unknown 不分来源。

## 后果

- 代价：观察字段变化要改两处（pool.Status 加字段 → 装配层投影 → AccountView）。
  接受：观察面变化频率远低于规则迭代频率，而规则迭代只碰 opsagent 包。
- 动作白名单在包内实现（`Executor` 接口 + 默认拒绝执行器）：MVP 只放行 `suggest`，
  `cost-explore` 预留枚举但默认拒绝——护栏的 teeth 是测试锚定的（非白名单动作必须
  落入 Rejected 审计），不是注释承诺。
- 审计：每轮 Round 报告（Facts 四态标注 + Unknowns + Suggestions + Rejected）进内存环
  （默认保留 50 轮）+ WARN 日志一行；面板集成是后续阶段，不阻塞 MVP。
- 放弃的替代：把规则写进 scheduler 各任务（每次签到顺带检查）——规则与任务时刻耦合，
  且 scheduler 无法承载「登记 unknown」的跨任务状态。
