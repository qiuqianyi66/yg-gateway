# 2026-11-24-harness2-landing 变更摘要

> 目录：`.harness/changes/2026-11-24-harness2-landing/`。
> 环境仓库（非本仓）：落地对象是本仓治理面，不建 feature 分支。

需求：外部精读 Harness²（arXiv 2610.14224）后的适用性落地——补「改治理面自身时的对照纪律」。
分级：L0（纯文档 / 提示词，无业务代码）
ADR：不适用（不涉架构、数据模型、上游协议）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：.harness/prompts/harness-edit.md` | 治理面自改模板：基线 k≥1 → 双轨候选（invariant/domain）→ 选择纪律 → 对照复跑 → leave-one-out → 落盘回滚 → 成本口径 |
| `修改：.harness/prompts/README.md` | 索引加 `harness-edit.md` 一行；「四个」→「五个」 |

**没做的（明写，免得下轮重提）**：

- 不新建「经验库」：本仓 `.harness/LESSONS.md` + Seen 计数 + 检项 [7] 已是更强版本，另建即平行体系。
- 不改 `check-harness.ps1`：无实测证据支撑新检项（按「真问题 ≥1 且误报 ≤1」判据，本轮真问题为 0）。
- 不引入 k>1 自动编排：无付费 API 预算，模板只约束人工按 k 路复跑并记账。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 治理一致性 | `powershell -NoProfile -File scripts/check-harness.ps1` | `ERRORS: []`；[1][2][3][4][6][7][8][9][10] 全 OK，仅 [5] 未测（由 pre-commit 检查 8 承担） |
| 索引一致性 | `check-harness.ps1` 检项 [3]（README 路由表 vs 磁盘） | OK（11/11 技能与路由表一致；prompts 新条目由检项 [10] changes 引用断链兜底） |

**负向探针**：不适用——本次新增为提示词模板，无可执行判据；索引漂移由检项 [3] 兜底。

## 三、未完成 / 待补（诚实清单）

- [ ] `requires: 实测轮次` —— 模板里 k≥2 复跑尚未在本仓真跑过一轮；首次实际使用后把真实前后数值补进本文件。
- [ ] `requires: 用户拍板` —— 是否把它挂进 `AGENTS.md` 阶段 10 的引用，未定（当前只在 prompts 索引可见）。

## 四、怎么回滚

删除 `harness-edit.md`，`git checkout -- .harness/prompts/README.md`；无数据副作用。

## 五、Learning

根因：治理面改动此前只有「改完跑绿」验收，缺「改前有基线、改后有对照、停用可复现」三步，容易被单次绿灯误导。
同一根因在 ≥2 个切片复现 → 按「经验三级进化」提级 pattern 建议，禁自动跨级。
