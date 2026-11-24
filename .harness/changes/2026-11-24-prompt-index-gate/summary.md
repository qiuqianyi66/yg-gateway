# 2026-11-24-prompt-index-gate 变更摘要

> 目录：`.harness/changes/2026-11-24-prompt-index-gate/`。
> 需求：本仓 `prompts/README.md` 维护纪律要求索引与模板同源，但 `check-harness.ps1` 检项 [3] 只校验技能族，`prompts/` 无任何机器检查。
> 分级：L0（只改体检脚本，不改业务代码）
> ADR：不适用

---

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `修改：scripts/check-harness.ps1` | 新增检项 [11]：README 链接集合 vs `prompts/*.md` 双向比对，登记无文件 / 有文件未登记均 error |

**没做的（明写，免得下轮重提）**：

- 不把提示词模板接入 `pre-commit.ps1` 作为阻塞门：模板是提示词资产，不是可执行判据；只在 `check-harness` 体检里报红。
- 不校验模板正文里的命令是否与 `pre-commit` 同源：实测 5 个模板 10 处命令已全部存在且被引用（2026-11-24），当前无真问题，不立项。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 正向 | `powershell -NoProfile -File scripts/check-harness.ps1` | `[11] OK 5 个模板与路由表一致`；`ERRORS: []` |
| 负向探针 | 新建未登记的 `prompts/zz-probe.md` 后复跑 | `[11] FAIL 登记 5 / 磁盘 6`；`ERRORS: [prompts/README 与磁盘不一致…]` |
| 探针清理 | 删除 `zz-probe.md` 后复跑 | `[11] OK`；`ERRORS: []` |

## 三、未完成 / 待补（诚实清单）

- [ ] `requires: 实测` —— 检项只在本地 PowerShell 跑过，未验证 CI runner（pwsh / Windows）行为；若 CI 用 Linux 需确认路径分隔与编码。
- [ ] `requires: 用户拍板` —— 是否将 [11] 加入 `pre-commit.ps1` 的必跑项，未定。

## 四、怎么回滚

删除 [11] 代码块，`git checkout -- scripts/check-harness.ps1`；无数据副作用。

## 五、Learning

根因：索引类资产（技能、提示词）各自有登记表，但机器检查只覆盖了其中一族，另一族的漂移只能靠人看。
