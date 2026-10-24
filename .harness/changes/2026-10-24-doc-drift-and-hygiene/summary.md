# 2026-10-24 doc-drift-and-hygiene

## 背景与必要性

全仓现状体检（2026-10-24）发现一批文档漂移与仓库卫生欠账：门禁实测为 23 包 21 ok +
2 no test files（cmd/credit 已于 10-23 补测试），但 AGENTS/README 三处数字停在旧值；
`工程结构.md` 的 internal 树只列 14/18 包、panel 清单缺 10-22 拆分产物、引用不存在的
`docs/plans/`、scripts 描述停留在「Python 诊断」；`persona.md` 被写为「可提交」而
.gitignore 已按私有资产排除；ROADMAP 头号事实源 10-24 起 untrack 未注明；
`PLAN-open-items` 自称未完成事项唯一入口但已全部收口，status 仍 active；
`.harness/changes/` 命名空间混入游离计划文件。清单滞后直接削弱该规范
「新建文件前先查归属」的功能。

## 改了什么

- `AGENTS.md`（门禁实测数 21 ok + 2 no test files，**字节等长**，上下文预算不变）
- `README.md` ×2（同口径修正）
- `.harness/rules/工程结构.md`：internal 树补 alerting/evidence/metrics/unknown；
  panel 清单补 autotask_mp/autotask_scripts/acl_notice.js/savings_note.js；
  docs 分层改为实际布局（删幻影 `docs/plans/`）；scripts 描述更新；persona.md 改为不入库；
  同文件内做等价压缩（常驻层预算纪律：净变化压到预算内）
- `.harness/rules/CODE-STANDARDS-CHECKLIST.md`：幻影 `docs/plans/` 引用修正
- `.harness/rules/开发流程规范.md`：等价压缩（由来叙事压为 2 行 + 触发条件去重 +
  0.2/0.3 表格收紧），信息不丢（裁定全文在所引用的 PLAN 文档里）
- `docs/roadmap/ROADMAP-prd-v2-2026-10-22.md`：事实源 research html 标注
  「2026-10-24 起移出版本控制，远端 clone 不可得」
- `docs/PLAN-open-items-2026-10-17.md`：status → closed + 收口横幅（指向 ROADMAP §9
  与 2026-10-24-repo-hygiene summary）；`docs/PLAN-next-2026-10-18.md` 同步加横幅
- `.harness/changes/2026-10-13-plan.md` → `docs/PLAN-2026-10-13.md`（游离文件归位，
  全仓 grep 零引用）
- 删除 8 个已合并本地分支（debt-split-×3 / feature/multi-api-key / key-ip-whitelist /
  panel-features / protocol-drift / usage-incremental；`git branch --merged` 实测）

**有意不做**：superpowers HANDOFF 三份**不归档**——十几个变更记录把它们当出处引用
（且 `2026-10-16-merge-publish-lines/summary.md:105` 明确写了旧文件名不改），移动弊大于利。
`.gitignore` 的 `/.mimosa/` 条目系 Mimosa 钩子外部落入工作树的改动（L-21：不混入本任务提交）。

## 怎么验证的

`check-context-budget.ps1`（15818/15822，压降后达标）、`check-harness.ps1`、全量
`pre-commit.ps1` 通过；包数以 `go list ./...`（23）+ `go test ./...`（21 ok + 2 no test）
实测为准。

## 怎么回滚

单提交 revert；分支删除可由 reflog 恢复（`git branch <name> <sha>`，sha 已在删除输出中）。
