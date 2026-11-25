# 2026-11-25-req-index-backfill 变更摘要

> 类型：**L0 纯文档**（REQ 索引导航）；无代码/行为/契约/数据变化；按 L0 写简版，无 PRD。

## 改了什么

- `docs/REQ-INDEX.md` 新增 **§F 2026-11-25 补登（此前索引缺 9 份）**：agent-profile-ui、cache-key-session-fallback、cost-tier-confidence、entry-body-shape、pick-provenance、pool-failure-window、prompt-null-panic、upstream-nilmap-panic、fallback-degrade-visibility。
- 顶部「最近核对」由 `2026-11-16（472fef4）` 改为 `2026-11-25（本地 main）`。
- 修正 §待办 里 V3-10 面板 UI 的过期否定句：原文「待 V3-6 解锁，未做」→ 已落地 `e330cb6` + `internal/panel/agent_profile.go`。
- 新增本 summary。

## 状态怎么定的（实证，不抄 frontmatter）

| 项 | 证据 |
|---|---|
| agent-profile-ui | `internal/panel/agent_profile.go` 在仓；`e330cb6` 为其提交；change summary 里的 `5802b8b` / `4f4d2d3` 在当前历史不可解析，按实际提交记 |
| cache-key / pool-failure | `ad06712` / `dc196ab` 均 `git merge-base --is-ancestor ... v2.3.0` = true，随 v2.3.0 上生产 |
| entry-body / prompt-null / nilmap / cost-tier | `c6b6eb7` / `b4f3823` / `3b72ddc` / `daeb559` 均 ancestor of `daeb559`（2026-11-21 生产换版，见 `2026-11-21-final-batch-staticcheck-cost-layout`） |
| pick-provenance | `d3fbef7` 主体落地；请求级归档/面板展示仍待 app.js 解锁（REQ §后续） |
| fallback-degrade-visibility | 本地 `0f99c0a`；未推送、未部署；独立复审待另一只读会话 |

## 验证

- `powershell -NoProfile -File scripts/check-harness.ps1`：11/11 OK（REQ 58 篇命名合规、changes 引用 0 断链）。
- `powershell -NoProfile -File scripts/pre-commit.ps1 -Fast`：exit 0（11 项检查）。
- `git diff --check`：无空白错误（提交前执行）。
- 纯文档，未跑全量 `go test`（`-Fast` 按设计跳过，L0 无需）。

## 未完成 / 待补

- [ ] 本次只补 9 份缺失项 + 1 处过期否定句，**未逐行重审** §A~§E 的每条状态；全量状态复核仍属后续。
- [ ] fallback 的独立复审、真机 fallback、浏览器跨域、生产部署均未做（见该变更 summary §三）。

## 回滚

`git revert <本提交>`。纯文档，无运行时影响。
