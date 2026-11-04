# 变更记录 — harness 治理层渗透（magpie 吸收补全）

日期：2026-11-04
类型：治理（harness 工程自身）——非功能代码
关联提交：`298b368`（本会话治理渗透）+ 本轮 ADR-022/023 + 检项 [9]（一起提交）
基线：`298b368` 之前为 `313edb4`

## 背景

用户指正：magpie 吸收「只做了皮面工程，没有渗透到各个部分」。第一轮补渗透
（`298b368`）落地 wiki 条目 + ADR-020/021 + L-26 + 索引压缩/基线收紧。
本轮复核（用户追问「确定做好了？」）自查出 3 处残留缺口，补齐：

## 改了什么

### 1. 机制决策 ADR 覆盖不全 → 补 ADR-022 / ADR-023

上轮只给 RPM 硬闸（ADR-020）和 /status 三态（ADR-021）写了 ADR。补审发现
P0-1/P1-3 同为「难逆转 / 未来读者会困惑 / 有真实权衡」的机制决策，缺 ADR：

- `docs/adr/ADR-022-proxy-fail-closed.md`：fail-open 为何是缺陷（静默共享出口 IP、
  成功响应不报错）、哨兵 errProxyConfig + failDial 形态、直连边界（空=直连唯一来源）。
- `docs/adr/ADR-023-panel-authz-mechanism.md`：Bearer 审计推翻 cookie 组合拳前提
  （CSRF 天然免疫）、p.api() 强制鉴权（机制防漏包非约定）、静态资源边界。

### 2. `298b368` 自身缺变更记录 → 本文件

AGENTS.md「每次改动建 .harness/changes/{feat}/」是硬约束，治理自身改动同样适用
（历史先例：2026-10-19-harness-engineering）。补此记录覆盖 298b368 + 本轮。

### 3. LESSONS 索引完整性未机器化 → check-harness.ps1 检项 [9]

L-24/L-25 漂移（正文在、索引无）实锤 1 次，当场不报错。检项 [7] 只查 Seen≥2
升级，不查「索引 == 正文」一致性。新增检项 [9]：双向一致（索引有正文无 /
正文有索引无均 error），排除非 L-NN 命名（flake.md）。这是把已实证的漂移
变成退出码，防复发。

## 怎么验证的

- `powershell -NoProfile -File scripts/check-harness.ps1`：9 检项全绿（含新 [9]），
  ADR 23 篇无重号。
- `powershell -NoProfile -File scripts/pre-commit.ps1 -Fast`：11/11 全绿
  （检查 9 现在带 [9] 输出；分母仍 11，检项 [8] 不受影响）。
- `go test ./...`：无 .go 改动（纯治理），不重跑全量（-Fast 语义）。

## 回滚

- ADR-022/023：删除两文件即可（ADR 断号合法，检项 [4] 仅提示不判错）。
- 检项 [9]：删除 check-harness.ps1 对应段（索引漂移防复发能力随之消失，需人工把关）。
- 本记录：删除本目录。
- `298b368` 自身的回滚见其提交（git revert）。
