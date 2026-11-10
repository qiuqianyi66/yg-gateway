# 2026-11-10-lesson-l29-coverpkg 变更摘要

需求：`docs/prd/REQ-2026-11-10-lesson-l29-coverpkg.md`
分级：L0（纯文档 / 治理，生产代码零改动）

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `新增：.harness/lessons/L-29.md` | 记「`-coverpkg` 目录式带斜杠静默报 0.0%」的坑：四条命令对照表、三步判定口径、与 `check-depth.ps1` 0% 豁免的关系 |
| `修改：.harness/LESSONS.md` | 索引表补 L-29 一行（Seen 1） |
| `修改：AGENTS.md` | 「部署纪律」四条并成一句 + 指向 `deploy-prod.ps1`；删掉与之重复的「第一条/第二条」解释句（约束语义不变：备份 R15、一条命令、部署后冒烟仍写在脚本与被保留的句子里） |
| `修改：.harness/context-baseline.json` | `budgetTokens` 14835 → 14805（净压降 30，`-Update` 收紧） |

**没做的（明写，免得下轮重提）**：
- 不迁 `ERROR-HANDLING.md` §六 的历史审计明细出常驻层（可省 ~1200 token，但被多处注释按
  「判据」引用，牵动断链，非本轮所需）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 治理一致性 | `powershell -NoProfile -File scripts/check-harness.ps1` | 检项 [7]/[9] OK：索引 29 条与正文 29 个一致；ERRORS 为空 |
| 常驻层预算 | `powershell -NoProfile -File scripts/check-context-budget.ps1` | `14805 / 14835`，未超（+之前超 29 的回归已消除） |
| 收紧棘轮 | 同上 `-Update` | `14835 → 14805（-30）`，只降不升 |
| 全量门禁 | `powershell -NoProfile -File scripts/pre-commit.ps1` | 11/11 通过（见日志 `%TEMP%\yg-precommit-l29.log`） |

**负向探针**：`-Update` 的上调保护是既有门禁逻辑（`total > budget` → exit 2），本轮未单独注入
（属既有脚本行为，改它不在本轮范围）。压降前的超预算红字本身即为探针：加索引后
`14864 > 14835（+29）` 确实被拦下，说明这条线是活的。

## 三、未完成 / 待补（诚实清单）

- [ ] 常驻层仍为零余量压线（14805 = 基线 14805）：下一条经验登记仍需先净压降。
      `ERROR-HANDLING.md` §六（历史审计明细，约 1.2k token）是最值得迁移的候选，
      `requires`：先全仓 grep 该节小节锚点引用，确认迁移不产生断链。

## 四、怎么回滚

纯文档：`git revert <提交>` 即可，恢复 AGENTS.md 原四条与 14835 基线。
不涉及二进制、配置、凭证。

## 五、Learning

根因：常驻层压到零余量后，「新增一条经验」与「不超预算」直接冲突——预算棘轮会迫使每次扩充
都先做一次净压降，而不是把预算上调。这正是设计意图（压降才允许收紧），但代价是登记被历史
叙述挤占的空间。
