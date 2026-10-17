# 变更记录 — 文档债更正：ERROR-HANDLING 的「留待定夺」四项已实施 + 规划行回填（L0）

- 日期：2026-10-17
- 分级：**L0**（纯文档：无行为变更、无接口/数据模型/协议变更、不触承重墙）
- 来源：`docs/PLAN-open-items-2026-10-17.md` §3「文档债：写着『未做』，实测已经不成立」
- 并发边界：`README.md`、`.harness/changes/2026-10-16-t5-sandbox/`、
  `.harness/changes/2026-10-16-depth-tasks-4-6/` 的同类更正由**并行会话**在做（其 PRD
  `REQ-precommit-single-test-and-doc-debt-2026-10-17.md` 已声明），本变更**不碰**这三处，
  只处理它没覆盖的 `ERROR-HANDLING.md` 与规划文件。

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `.harness/rules/ERROR-HANDLING.md` | §「留待产品定夺（未改，避免单方面改口径）」→ §「曾经的『留待产品定夺』——**四项已全部实施**」：逐条给出落点（`usageCreditTotal` / `chatrun.go` 两处 / `anthropicStreamIntent` / `pollFailureMessage`）、语义变化、还缺什么；并补一句「四条不构成静默吞错的先例」 |
| `docs/PLAN-open-items-2026-10-17.md` | ①§9.2 的 B6 整行划除并注明「实测不成立，已关闭」；②§9.4 补 B6 与 A1 的结论；③§6 建议顺序：A1 标已完成、B6 划除、B7/§3 标「并行会话在做」 |

## 二、为什么算「文档债」而不是小事

`ERROR-HANDLING.md` 是 R10 的**判据文档**。它把四项写成「未改」时，下一个会话读到的结论是
「这些东西还没做、而且不敢做」——于是要么重复立项，要么把同样的疑点再讨论一遍。
而这四项其实已经在 T7 落地（P1–P4）。规则文档与代码不一致时，人学会的是忽略规则，
这比规则本身写错更贵（`CODE-STANDARDS-CHECKLIST.md` §九已记同族教训）。

## 三、怎么验证的（每条都是直接观测，不是转述）

| 断言 | 证据 |
|---|---|
| P1 已收紧 | `Select-String internal/server/handler.go -Pattern 'usageCreditTotal\|total_tokens'` → `handler.go:1021` 的函数注释写明「usage 缺失 / total_tokens 缺失或非法 → `ok=false`」，`:1034` 是 `t, okT := usage["total_tokens"].(float64)` |
| P2 已两处同改 | `chatrun.go:370` 与 `:460` 都是 `st.cacheHit, st.cacheMissUnknown = …`；`reqlog.go:84-88` 有 `CacheMissTokens` 与 `CacheMissUnknown` 两个字段及注释 |
| P3 已改 | `anthropic.go:26` `func anthropicStreamIntent(...) (stream bool, note string)`；`:37-38` 类型非法时返回 note |
| P4 已改 | `cmd/login/main.go:237` `func pollFailureMessage(...)`；`:238-252` 四档 switch |
| 四项在代码里真的生效 | `go build ./...` + `go test ./...` 全绿（门禁 6/6 与 7/7 两次运行） |

## 四、回滚

`git revert <本提交>`。回滚后 `ERROR-HANDLING.md` 重新声称四项「未改」（虽然不真），
规划文件回到「B6 待做」的口径。
