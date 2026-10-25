# 新开辟方向（NEW-FRONTIERS，2026-10-25）

> 来源：对标学习（magpie/CRS/one-api/LiteLLM，见 similar-projects-learnings）×
> 本 agent 在仓内干活的**亲历痛点**。判据：①同类没有（差异化）②已有资产够得着
> （不是从零造）③符合 AGENTS 定位红线。每块给亲历依据与第一刀。

## #1 决策溯源（pick provenance）——**本次已落地第一刀**

- **亲历依据**：词表口径 bug（规则匹配 hard_credit 永假）靠投影测试才抓到；
  response_format 真相靠链路实测才澄清——本仓核心资产是「可审计」，但只覆盖数据
  不覆盖决策。「为什么选这个号」无人能答。
- **差异化**：四个对标项目均无此层。
- **第一刀（已完成）**：pickReason 原子结构 + 四选号路径落理由 + Status.PickReason
  透出（REQ-pick-provenance-2026-10-25）。
- **后续**：reqlog 请求级归档带理由（M）；opsagent 消费（「兜底反复捞回 hard 号」
  可成假设）；面板展示（待 app.js 解锁）。

## #2 规则心跳（canary rounds）——opsagent 规则的自检

- **亲历依据**：词表 bug 的本质是「规则在生产永假但无人知道」。单测覆盖编译期词表，
  但 pool 词表演进（如新增 hard_quota 细分）时，运行中的规则可能悄悄失配。
- **方案**：opsagent 每 N 轮注入一次**合成观察**（fabricated AccountView：hard 冷却
  +正余额、低 healthy 占比），断言对应建议确实产出；失配则以 observation 落证据并
  告警——「会自检的规则」比「写了测试的规则」多覆盖运行态漂移。
- **量级**：S（Agent 加一个 canaryRound 方法 + 测试）。
- **风险**：合成建议会走 Sink——需标记 canary 前缀并在 webhook 侧抑制（或 canary
  Sink 单独注入）。

## #3 Agent 负载画像——「懂 agent 的网关」定位

- **亲历依据**：我是这个网关的目标用户（编码 agent），我的请求模式（长对话+高频
  工具循环+prompt cache 复用）与人类流量完全不同，但网关的用量视图不区分。
- **差异化**：magpie 做 agent 接入不做画像；CRS/one-api 只按 key 计量。按
  「agent 会话」聚合（UA 自识别 Codex/Claude Code + 会话键 → 每任务轮数/每轮 token/
  cache 命中率）是空白面。
- **第一刀**：reqlog 已有 model/stream/client 信息，加「agent 会话维度聚合」到
  usage（S-M）；面板展示待 app.js 解锁，可先出 /status JSON。
- **定位收益**：直接支撑 README「养号友好」——agent 流量对账号的伤害模式
  （高频小请求 vs 低频大请求）第一次可见。

## #4 自省闭环（harness runner）——金样重放的独立执行器

- **亲历依据**：金样首次重放被环境并发配额阻塞（run-2026-10-24 blocked ×3）——
  缺一个不依赖会话内子 agent 的执行器。
- **方案**：`cmd/harnessrun`（或 scripts/harness-run.ps1）：起隔离进程逐任务跑
  金样（每任务独立 go test 式沙箱 + 超时），评分点外置。环境修复前它是唯一可信的
  重放路径；修复后它是 CI 化的入口（L3 路线）。
- **量级**：M（需要任务隔离与超时设计，复用 mutation 沙箱模式）。
- **顺带**：它也是对外开源的候选——「harness eval runner」本身在 awesome-harness-
  engineering 分类里缺 Go 实现。

## 不开的（写明理由）

- **隐私脱敏改写**（magpie 最亮功能）：动出站内容层=R8 风控画像风险，等价物已有
  （R2+提示词体系）。留档观察。
- **opsagent 自主行动升级**（用网关自己的 /v1 做推理）：消耗池内积分+自主性风险，
  等 MVP 真机数据后再议（REQ-opsagent 解锁动作 ④）。
