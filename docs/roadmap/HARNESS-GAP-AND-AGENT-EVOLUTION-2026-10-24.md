# Harness 对标与 Agent 演进路线（2026-10-24）

> 三个问题：①现在的 .harness 自跑体检结果如何；②与 2026 年业界 harness 工程实践比
> 差什么；③本项目往 agent 方向发展怎么做。对标锚点：OpenAI《Harness engineering》
> （2026-02）、LangChain《Improving Deep Agents with harness engineering》、
> awesome-harness-engineering 的分类（evals / memory / MCP / permissions / observability /
> orchestration）、AGENTS.md 开放标准。

## 一、.harness 自跑体检（本轮实测）

| 检查 | 结果 |
|---|---|
| pre-commit 全量 10 项 | ✅ 全绿（gofmt/vet/23 包测试/R10/引号陷阱/边界/深度棘轮/REQ 必要性/治理一致性/预算） |
| 治理一致性 8 检项 | ✅ 0 error：AGENTS 22 引用 0 断链、skills 11/11 frontmatter 合规、ADR 无重号、86 changes 目录 summary 齐备、Seen≥2 全部升级 |
| 上下文预算 | ✅ 15818/15822（本轮 +281 增量已等价压缩消化） |
| deploy-prod 排练 | ✅ 53/53（6 场景） |

**自跑暴露的真实短板**（都被本轮修掉或立项）：
1. 本地工具链单点：系统 go 损坏让本地门禁静默瘫痪 2 天，靠提交信息才发现 → 已修
   （四脚本 Resolve-GoExe 实跑验证 + YG_GO_EXE 覆盖）。
2. L2 变更缺独立复审（depth-ratchet-restore 只有 summary）→ 本轮补独立复审（L-16）。
3. 常驻层预算只剩 4 token 余量：任何 AGENTS/规则新增都要先压降——这是特性也是摩擦。
4. 文档漂移（包数/目录清单）靠人工体检才发现 → 建议：给 check-harness 增加一条
   「README/AGENTS 中可机器验证的数字断言」检查项（如包数、测试数）。

## 二、与 2026 业界实践对标

**已达标甚至超前的**：
- 「规则机器化」（OpenAI：custom linters / structural tests）——本仓的 R10 diff 检查、
  边界白名单、深度棘轮、上下文预算棘轮、引号陷阱检查，覆盖度高于多数开源仓库。
- 「单一门禁源」（gate 只跑 pre-commit.ps1，分叉即假绿）——与 OpenAI 文章同构。
- AGENTS.md 标准——顶层硬约束 + 四态语义 + 否定断言穷尽（L-15）是业界少见的语义纪律。
- Lessons/Seen 计数制——等价于业界说的「eval-driven rule promotion」，且机器化更早。
- 上下文预算棘轮——LangChain 强调的 context optimization 里少见的**量化**做法。

**缺口（按影响排序）**：
1. **Agent 行为回归基准（evals）**：`docs/agent-eval/` 只有 1 份基线测评。业界把
   「同一批任务在规则/模型变更后重放」作为 harness 的核心循环。本仓规则每次变更靠
   人工感觉验证「agent 还会不会干活」。→ 建议：固化 5-10 个金样任务（如「修一个带
   测试的 bug」「做一次变更目录全套」「排练 deploy」），规则变更后重放并记分。
2. **Agent 运行追踪（tracing）**：并行 agent 的产出散在会话里（L-4 只约束返回值）。
   → 建议：多 agent 任务统一落 `.harness/changes/*/agents.md`（谁、读了什么、结论、
   证据指针），复审时可追溯。
3. **Memory 层**：LESSONS 是「制度记忆」，缺「项目语义记忆」（如生产实例的账号结构、
   上游行为模式）。`.workbuddy/` 已有但私有且非结构化。→ 低优先：等 MCP 化时一并。
4. **权限护栏**：Mimosa 钩子刚引入（含误报：BackfillRealm/SSRF）。→ 校准规则，
   否则「拦截」会训练人绕过钩子（已发生一次：login.go 注释被拦）。
5. **编排协议**：并行 agent 无标准并发上限/超时/重试约定（本轮亲历 concurrency limit
   连续 3 次）。→ 写进 skills：fan-out 上限 2、超时与降级路径。

## 三、往 agent 发展怎么做（三层，每层都有已就绪的地基）

### L1 —— 网关作为 agent 工作负载底座（最短路径）

现有地基：Anthropic `/v1/messages` 适配、DeepSeek 工具标记还原 `tool_calls`、SSE 透传、
粘性会话（agent 的多轮工具循环天然受益）、按模型延迟分位。
缺口与动作（REQ 粒度）：
- **工具调用流式保真**：~~给 r8_golden 增加流式工具调用金样~~ → **2026-10-24 复核撤销**：
  流式还原已有 5 个针对性单测（TestAggregateToolCalls / TestStreamToolCallNameOnce /
  TestStreamToolCallParallelFragments 等，覆盖分片/并行/名称语义），金样边际价值低。
- **结构化输出**：`response_format` 生产代码无显式处理，但**2026-10-24 链路实测**：
  出站 body 走 AST 级重组（payload.go），未删除的键原样保留——即 response_format
  目前**被无意识透传**（无测试锚定；上游是否认它未知）。已立项
  `docs/prd/REQ-structured-output-2026-10-24.md`（P1 锚定透传 + P2 上游实测）。
- **长循环韧性**：agent 一轮几十次调用，429/402 的退避语义对「工具循环」要可预期
  （Retry-After 已有；补文档化 + 流式场景的中途断流恢复行为说明）。
- **`/v1/responses`**（OpenAI 新 API）按需评估——Codex 客户端正在迁移。

### L2 —— 网关内置运维 agent（本项目独有的机会）

现有地基：**unknown registry（带 revision）+ evidence 四态 + scheduler 五类任务 +
alerting webhook + autotask 任务链**——这正是一个受约束 agent 的全部 belief/action 骨架，
只是现在靠 cron 和人脑串联。
形态：`internal/opsagent`（或先放 scheduler 旁路）观察-决策-行动-审计闭环：
- 观察：metrics/reqlog 异常（402 率、冷却账号数、quota 走向）→ 产出 hypothesis（写入
  unknown，requires 字段照旧强制）；
- 行动：白名单动作（触发一次探索、调整 explore 窗口、发告警、给面板 notice）——
  **只读+低风险动作先开放**，高危（改配置/换号）保持人工确认；
- 审计：每个决策落 evidence 条目（observation/inference 标注），面板可回放。
这是把「养号友好」定位升级成「自运维网关」的最小可靠路径，且四态语义直接复用。

### L3 —— .harness 本身 agent 化（把治理变成工具）

- 把 check-* 脚本包成 MCP tools，agent 会话内可自检（现在只能跑完看输出）。
- skills 已是标准 SKILL.md 格式 → 打包发布/跨项目复用。
- evals（§二缺口 1）+ tracing（缺口 2）补齐后，规则变更从「人审」升级为「重放评分」。

### 建议顺序

1. L1 的流式工具调用金样（小，直接提高 agent 客户端可靠性）；
2. §二缺口 4（钩子校准，防护栏失效）与缺口 1（金样任务 5 个）；
3. L2 的 opsagent 立项（REQ + ADR，动作白名单先行）；
4. L3 的 MCP 化（等 L2 骨架稳定后）。
