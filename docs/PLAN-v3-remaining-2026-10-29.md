# PLAN: ROADMAP v3 剩余在册项长任务规划（2026-10-29）

> 执行入口 = 本文件 §三。上游基准：`docs/roadmap/ROADMAP-prd-v3-2026-10-25.md`（§六
> 要求 V3 中期对账，本规划顺带执行）+ `docs/REQ-INDEX.md`（待办行权威，有漂移）。
> 证据纪律：所有「已落地」断言以 `git log` 实证（L-15/L-22：REQ-INDEX 没行 ≠ 没做）。

## 一、现状对账（2026-10-29，git log 实证）

| V3 项 | 状态 | 落点 |
|---|---|---|
| V3-1 count_tokens | ✅ 落地 | `a07727c`（无独立 REQ——执行期债，见 §三 M1） |
| V3-2 key 级用量告警 | ✅ 落地 | `a07727c`（同上） |
| V3-3 生产换版+真机校准 | ✅ 落地 | `b5ffee2`（48h 数据回填：bandMult 被双峰推翻、opsagent 未启用、count_tokens 无样本） |
| V3-4 收尾三件 | 🟡 部分 | 金样重放 ✅（`fcf5a42`）；**推送 / 重装 Go ≥1.27.3 待人工**（GitHub 工单 + 本机） |
| V3-5 表驱动归一化 | ✅ | `a0568ff` 系 |
| V3-6 E2E + 瘦身 | ✅ | `c36217e`（瘦身） + e2e 套件 |
| V3-7 opsagent 规则心跳 | ✅ 落地 | `4931e4b`（无独立 REQ） |
| V3-8 reqlog kind | ✅ | `41caf25` |
| V3-9 harness runner | ✅ | `fcf5a42`（金样 T1-T3 实跑） |
| V3-10 agent 流量画像 | ✅ 落地 | `f3385f4`（session_fp/agent_kind + /status 聚合；面板 UI 待 V3-6 后，未做） |
| V3-11 growth 子包拆分 | 🟡 **在册待执行** | REQ-growth-package-split（解锁动作 ①②③） |
| V3-12 structured-output | ✅ | `502ede3`（真机一发，上游认 json_object） |
| V3-13 README 三护城河 | ✅ | `cbf1399` + `bde24a4` |
| V3-14 harness 产品化评估 | ✅ | `2d39e9c` |

**治理债**：REQ-INDEX 待办区缺 V3-1/V3-2/V3-7/V3-10/V3-13 五条已落地行（漂移）。

## 二、剩余在册项（2026-10-29）

| # | 项 | 量级 | 依赖 | 解锁动作 / 卡点 |
|---|---|---|---|---|
| R1 | **routing bandMult 分口径**（REQ-routing 真机回填结论） | M | 无 | 决策点：聊天/评测分口径参数（见 §四）；零回归实现 |
| R2 | **growth-split**（V3-11） | L | R2a 先做 | ①行为级测试基线补齐 → ②ADR-017 定接口形态（候选 A/B）→ ③执行+边界白名单 |
| R3 | **opsagent 阶段3**（第二类白名单动作评估） | M | 生产 | ①生产开 `opsagent.enabled` 观察一轮（人工）→ ②评估 cost-explore 白名单开放 |
| R4 | **V3-4 残余**：推送 / Go ≥1.27.3 重装 | XS | 人工 | GitHub 工单清 unreachable objects；本机重装 Go |
| R5 | **V3-10 面板 UI**（agent_profile 展示） | S | 无 | V3-6 已解锁（app.js 4278 < 4378）；纯前端可选增强 |

**同类横向依据（2026-10-29 调研）**：one-api/new-api（Go，new-api 50k★）按业务域
拆独立包（channel 独立域）→ 支持 growth 候选 A；LiteLLM routing groups（模型族/
策略绑定，routing plugins 管道）→ 支持 bandMult 按流量类分口径（聊天/评测异参）。

## 三、执行序列（里程碑）

- **M1 · 治理对账**（XS，本会话）：REQ-INDEX 补 V3-1/V3-2/V3-7/V3-10/V3-13 已落地行
  （标「无独立 REQ，git log 实证」）；ROADMAP §六 中期对账回填（V3-10/V3-11/V3-13 状态）。
  验收：REQ-INDEX 与 git log 零漂移；pre-commit 11/11。
- **M2 · routing bandMult 分口径**（M，本会话）：读 `internal/pool/softband*` 现状 →
  分口径实现（聊天保持默认、评测走该号近期 P50 中位数路径；默认语义零回归）→
  决策点落档（§四）。验收：三态权重测试 + 既有 softband 测试零回归。
- **M3 · growth-split 阶段①**（M，本会话或下会话）：growth 域行为级测试基线补齐
  （blackcat/streak/tasks/billing 等，当前 coverage_* 间接覆盖的补直接断言）。
  验收：新增测试全绿，不做搬移。
- **M4 · growth-split 阶段②③**（L，独立会话）：ADR-017 定接口形态 → 候选 A/B 执行 →
  边界白名单 `-Fix` 更新（逐条理由）→ R8 金样零 diff → summary。建议独立会话防上下文退化。
- **M5 · 人工解锁项**（R3/R4）：生产开 opsagent.enabled 观察一轮（回填 REQ）；推送与
  Go 重装。由用户在合适时机执行，本规划不阻塞其他项。

## 四、决策点（bandMult 分口径参数，待用户拍板）

真机 48h 回填结论（derivation，REQ-routing §真机回填）：total_tokens 双峰——聊天
p50=762、评测流量单次 prompt 30-32 万 token（credit ≈4.4/次）。`softBandTypicalTokens=
20_000` 对两峰都不贴。

选项：
- **A（推荐）**：分口径——按 agent_kind/流量类选带宽：聊天类保持默认 20k；评测/大
  请求类走该号近期 P50 实测（`softBandUpper` 中位数路径已存在）。成本：新增一个小
  口径键，默认语义不变。
- **B**：单一 P50 中位数路径（评测大请求拉高 P50，聊天带宽被撑大——误判余量 2 次
  语义被稀释）。
- **C**：维持 20k 不动（现状；评测流量误判余量偏紧，软带提前触发——本会话 0 命中，
  实弹风险低但语义失真）。

## 五、纪律（全程不变）

- 每项走 REQ 必要性 → 实现+测试同提交 → pre-commit 11/11 → 变更目录三件套；
- 四态纪律：bandMult 口径参数是 derivation（48h 样本），样本外标 unknown；
- 用户约束：不发起付费真机验证（count_tokens 精度 / 评测带宽回填维持 unknown）；
- R6 出站集中 upstream；R8 报文字符串零改动；棘轮只许收紧。
