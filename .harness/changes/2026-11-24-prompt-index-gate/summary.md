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

- 不在 `pre-commit.ps1` 里重复实现 [11]：`pre-commit` 检查 9 直接调用 `check-harness.ps1`，[11] 已经随之执行；本次只修掉 `pre-commit` 里残留的「10 检项」旧标签（2026-11-25 核实）。
- 不校验模板正文里的命令是否与 `pre-commit` 同源：实测 5 个模板 10 处命令已全部存在且被引用（2026-11-24），当前无真问题，不立项。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 正向 | `powershell -NoProfile -File scripts/check-harness.ps1` | `[11] OK 5 个模板与路由表一致`；`ERRORS: []` |
| 负向探针 | 新建未登记的 `prompts/zz-probe.md` 后复跑 | `[11] FAIL 登记 5 / 磁盘 6`；`ERRORS: [prompts/README 与磁盘不一致…]` |
| 探针清理 | 删除 `zz-probe.md` 后复跑 | `[11] OK`；`ERRORS: []` |
| Linux runner 兼容 | 2026-11-25 在 Linux（Debian 6.6，pwsh 7.4.6）对 HEAD `7d9a958` 快照实跑 `check-harness.ps1` | 11/11 OK（[11] 5 模板一致）；路径分隔与 UTF-8 均正常，与 Windows 输出一致 |

## 三、未完成 / 待补（诚实清单）

- [x] `requires: 实测` —— **已消解（2026-11-25）**：Linux Debian 6.6 + pwsh 7.4.6 对 HEAD `7d9a958` 快照实跑 11/11 OK；CI runner 为 `ubuntu-latest` + `pwsh`（`gate.yml:110`），路径分隔（脚本按 L-7 分段 `Join-Path`）与 UTF-8 均正常。真实 GitHub Actions 仍待本地 5 个提交推送后触发（未推送 ≠ 兼容性未验证）。
- [x] `requires: 用户拍板` —— 已核实 `pre-commit.ps1` 检查 9 调用 `check-harness.ps1`，[11] 随该门禁执行，无需另接入；旧标签「10 检项」已改「11 检项」。

## 四、怎么回滚

删除 [11] 代码块，`git checkout -- scripts/check-harness.ps1`；无数据副作用。

## 五、Learning

根因：索引类资产（技能、提示词）各自有登记表，但机器检查只覆盖了其中一族，另一族的漂移只能靠人看。
