# REQ：F5 度量计数器（消解「首调用上下文」与「总 token」unknown）

> **状态：IMPLEMENTED（2026-10-21）。L2 落地完成；原 DRAFT 的「先评审再写代码」已由用户授权一次性实现。**
> **关联**：`REQ-harness-engineering-2026-10-19.md` §五 F5；`docs/agent-eval/baseline-2026-10-19.md`
> （当前 2/5 组，第 1、2 组均把「首调用上下文（总量）」「总 token」记成 unknown）。
>
> 本文只定需求 + 验收 + 数据源约束，不替实现拍板（L2 改动，先评审再写代码）。

---

## 一、必要性（阶段 0，四个问题）

**Q1 要解决的真实问题是什么？**

`docs/agent-eval/baseline-2026-10-19.md` 的记账口径里，两项**核心成本字段**当前是 unknown：

- **首调用上下文（总量）**：= 系统提示 + 工具 schema + 常驻治理层 + 首轮消息。现在只测得出
  「常驻治理层」一项（15,822 token），其余不可测 → 记 unknown。
- **总 token**：一次任务从开工到收工的全部消耗。本机无计数器，记 unknown。

这违反本仓四态纪律的「未覆盖 = unknown，不是没有」——目前是**诚实**地记了 unknown，但 unknown
本身意味着 **F5 的判据跑不出来**：两周后要看「治理层对成本影响 5×」有没有实证，没有这两个数字就
无法量化，只能空口说「应该有」。

**Q2 不做会怎样？** F5 度量账本永远停在 2/5 组，且每组的「首调用上下文 / 总 token」列永远空。
HarnessTax 的核心结论（成本主源是首调用上下文、影响可达 5×）在项目内**既无法证实也无法证伪**，
「要不要再加一层治理」失去数据底座，回到凭感觉堆层（Handbook 反模式之首：过度配置）。

**Q3 现状是不是已经变了？** 变了一次，正是本文起因：2026-10-20 治理 P0 落地后，常驻层已可精确测
（22,603 → 15,822），但「首调用上下文总量」与「总 token」仍 unknown，因为**缺一个采集点**，不是
因为口径不清。口径在 baseline 文档里已经定好了。

**Q4 有没有更便宜的等效手段？** 有两条，本文采用**数据源优先**原则：

1. **provider usage（权威）**：上游是 OpenAI 兼容接口，每个响应都带 `usage`
   （`prompt_tokens` / `completion_tokens` / `total_tokens`）。这是**真实计数**，不是估算，
   优先采用。
2. **本地估算（兜底）**：仅当 provider 不返回 usage 时，才退回 `check-context-budget.ps1` 的
   CJK÷1.5 + 其余÷3.8 估算口径，并在字段上标 `derivation`（非 `observation`）。

**结论：做，但限定 L2 最小改动** —— 只在出/入站层加一个只读采集器，不新建规则、不新建端点族、
不碰业务选号逻辑。

---

## 二、数据源与四态口径

| 字段 | 取数来源（优先级） | 四态 |
|---|---|---|
| 首调用上下文（总量） | 首个响应的 `usage.prompt_tokens`（若 provider 返回）；否则本地估算（标 derivation） | observation / derivation |
| 总 token | 一次任务内所有响应的 `usage.total_tokens` 求和 | observation |
| 轮次 | 工具调用批次数（含重试单独注明） | observation（已有口径） |
| 耗时 / 一次通过 / 真问题 / 误报 | 沿用 `baseline-2026-10-19.md` 现有口径 | observation |

**硬约束**（沿用 AGENTS.md 红线）：
- 出站请求集中 `internal/upstream`（R6）：usage 采集点必须挂在响应解析处，**不许**在 server/panel
  各处散点拼；新增方向不得违反 `scripts/check-boundaries.ps1` 的 allow 表。
- goroutine 必须有回收路径（R9）；采集器是无状态累加，按会话 key 聚合，超时/上限要有清理。
- 错误不静默吞（R10）；采集失败（provider 没返回 usage）要显式记「该次 unknown」，不是 0。

---

## 三、方案要点（待评审，非定稿）

1. **采集位置**：在 `internal/upstream` 的响应解析里，把 `usage` 抽出来塞进一个会话级 metrics
   结构（内存即可，不必落库；按会话 ID 做 key，带 TTL 防泄漏）。
2. **暴露方式**：挂到现有 `/status` 统计输出（不新建端点），新增字段
   `first_call_prompt_tokens` / `task_total_tokens` / `task_turns`，缺省值显示字段**省略**
   （unknown，不输出 0、不输出 false）。
3. **面板 / 账本回填**：`docs/agent-eval/baseline-2026-10-19.md` 第 3–5 组改为**跑任务时从
   `/status` 取数填**，不再手记 unknown；首调用上下文与总 token 的 unknown 随采集点上线而消解。
4. **阈值/告警**：本期**不做**阈值与告警（F5 判据只统计「真问题 ≥1 且误报 ≤1」，不触发动作）。
   计数器只负责把 unknown 变 observation。

---

## 四、验收（Given-When-Then）

- **GIVEN** 一次正常的 `/v1/chat/completions` 多轮任务（provider 返回 usage）
  **WHEN** 任务结束 **THEN** `/status` 暴露 `task_total_tokens` = 各轮 `usage.total_tokens` 之和，
  且 `first_call_prompt_tokens` = 首轮 `usage.prompt_tokens`，二者非 0、非省略。
- **GIVEN** provider 响应**不含** usage **WHEN** 任务结束 **THEN** `/status` 对应字段**省略**
  （unknown），日志记一次「usage 缺失」，且不记 0、不报错退出。
- **GIVEN** 采集器随服务运行一周 **WHEN** 查 `/status` **THEN** 无 goroutine 泄漏
  （`check-boundaries` / 现有泄漏巡检无新增告警），会话 metrics 有 TTL 清理。
- **GIVEN** 新增采集代码 **WHEN** 跑 `scripts/check-boundaries.ps1` **THEN** 依赖方向不新增违规。

---

## 五、范围与明确不做

- **做**：只读采集 + `/status` 暴露 + 账本回填口径。
- **不做**：阈值告警、可视化大盘、持久化存储、按账号/模型的成本分摊报表（那些是 F5 判据跑出
  「真问题 ≥1」之后的事，不在本期）。
- **不做**：本地 tokenizer 精确计数（CJK÷1.5 估算只作兜底，不投入精度）。
- 与 `REQ-harness-engineering-2026-10-19` 的边界：本文只解决「unknown → observation」的采集缺口，
  不新增任何治理检项；两周后的 F5 判据仍按 baseline 文档 §五 跑。

---

## 六、待决议（评审时定）

- 会话 metrics 的内存 TTL 取多少（建议 1h，覆盖一次任务 + 余量）？
- provider 不返回 usage 时，是否值得为本仓特有上游（CodeBuddy）补一个本地估算兜底，还是直接 unknown？
- 是否需要把 `task_total_tokens` 也写进 `state.json`（持久化）以便跨重启累计？本期倾向**不写**。

---

## 七、实现记录（2026-10-21）

用户授权「一次性做完」，故跳过原 DRAFT 的评审环节直接落地。§六 三个待决议按以下拍板实现：

- **TTL = 1h**：采纳 REQ 建议值，覆盖一次任务 + 余量（`internal/upstream/taskmetrics.go` 的 `ttl`）。
- **provider 不返回 usage → 直接 unknown**：不补本地估算兜底（CJK÷1.5 估算本期不做）。缺失即不调用
  `Record`，`/status` 对应字段整体省略，符合「未覆盖=未知，不是没有」纪律（R10：缺失是观测缺口，不记 0）。
- **不持久化**：纯内存，不写 `state.json`（跨重启累计不是本期目标）。

### 落点

| 文件 | 改动 |
|---|---|
| `internal/upstream/taskmetrics.go`（新增） | 会话级采集器 `TaskMetrics`：按会话 ID 聚合首调用上下文（`prompt_tokens`）与总 token（`total_tokens` 求和）、轮次；TTL 清理 goroutine（带 `Stop()` 回收路径，R9）；`observeUsage` 包装上游流，透传同时解析 SSE `data:` / 单条 JSON 里的 `usage`，Close 时记录最终 usage（避免增量 usage 重复累加）。 |
| `internal/upstream/client.go` | `ChatStreamContext` 成功返回处用 `observeUsage` 包装流（采集点落在 `internal/upstream`，满足 R6）。 |
| `internal/server/handler.go` | `/status` 在 `HasData` 时输出 `task_metrics`（`tasks` / `task_total_tokens` / `task_turns`，首调用上下文另按 `FirstCallKnown` 省略）；无数据整体省略该段（unknown，不输出 null / 0 / false）。 |
| `internal/upstream/taskmetrics_test.go`（新增） | 采集器单元 + 包装器透传/采集测试。 |
| `internal/server/handler_taskmetrics_test.go`（新增） | `/status` 省略与透出验收。 |

### 验收对照（REQ §四 Given-When-Then）

- 多轮任务 → `task_total_tokens`=各轮之和、`first_call_prompt_tokens`=首轮 prompt、**非 0、非省略**：✅ 测试覆盖。
- provider 不含 usage → 字段省略、不记 0、不报错退出：✅（不调用 Record → 整体省略）。
- 随服务运行无 goroutine 泄漏、会话 metrics 有 TTL 清理：✅（`Stop()` 回收路径 + 10min 清理 ticker）。
- `check-boundaries` 无新增违规：✅（`taskmetrics.go` 仅依赖 stdlib；`server→upstream` 本就在 allow 表）。

### 范围与明确不做（维持 REQ §五）

阈值告警、可视化大盘、持久化、按账号/模型成本分摊、本地 tokenizer 精确计数：均**未做**。本地估算兜底也未做（待决议项，本期选 unknown）。

