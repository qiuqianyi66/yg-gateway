# REQ — 用仓库 skills 库复检本日五个提交

## 必要性

今日五个提交（`b0cbb5c`/`8eec71f`/`e0e0e08`/`732e1b5`/`cbfa12e`）都是自己写、自己验的：
门禁绿、变异探针红，但**没有走过 `.harness/skills/` 的评审流程**。本仓明写「自评审 PASS ≠ 通过」，
且 expert-reviewer 要求 6 维评审 + 🟡 清零 + 评审记录落盘。缺这一轮，等于用同一双眼睛
审自己的代码——变异探针只证明"测试有牙齿"，不证明"改动本身没问题"。

## 需求

按 `.harness/skills/README.md` 路由，命中 `main-dev/expert-reviewer`（6 维评审）、
`main-dev/unit-test-write`（测试有牙齿）、`main-dev/unit-test-ci`（门禁）、
`main-dev/coding-skill`（分层与红线）四个技能，对五个提交的全部变更文件做一遍评审，
🟡 在本批内清零，评审记录落盘。

## 验收（GWT）

- Given 五个提交的 diff → When 按 6 维逐项过 → Then 每条 finding 有分级、FIX 与出处。
- Given 评出的 🟡 → Then 本批修掉并补测试/文档，不留给下一轮。
- Given 修复后 → Then `pre-commit.ps1` 退出码 0，session 包覆盖不低于改前 97.9%。
- Given 评审记录 → Then 落 `.harness/changes/2026-11-10-skill-rereview/review.md`。

## 不做

- 不重审 2026-10-23 之后 138 个历史提交（本轮范围是"我自己今天做的"，不是全仓复审）。
- 不改 R8 报文、不改面板、不动依赖。
