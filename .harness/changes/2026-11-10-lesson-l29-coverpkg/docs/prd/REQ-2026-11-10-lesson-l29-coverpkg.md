# REQ — 记 L-29（`-coverpkg` 目录式静默 0%）并压降常驻层

## 必要性

`internal/growth` 覆盖率度量时，`-coverpkg=./internal/growth/` 报 0.0%，换完整导入路径
`yg-gateway/internal/growth` 同批用例报 89.9%，两次退出码都是 0、用例全绿——没有任何提示。
这类"零值假象"会直接把"该包零覆盖"写进方案与立项依据，属于会伪造结论的坑，
按 LESSONS 索引纪律（只记会伪造结论的坑）必须登记 L-29。

登记后常驻层从 14835 涨到 14864（超预算 29），常驻层预算只许下调、不许上调，
所以本轮必须**先净压降**再收棘轮。

## 需求

1. `.harness/lessons/L-29.md` 正文 + `.harness/LESSONS.md` 索引一行（Seen 1）。
2. 常驻层净压降：把 `AGENTS.md`「部署纪律」里与 `deploy-prod.ps1` 重复的三条要点
   （备份 R15 / 同一条命令 / 部署后冒烟）合并为一句指向脚本，不删任何约束语义。
3. 压降后 `check-context-budget.ps1 -Update` 收紧基线（14835 → 14805）。

## 验收（GWT）

- Given 索引多一行 → When 跑 `check-harness.ps1` → Then 检项 [7]/[9] 报「索引 29 条与正文 29 个一致」。
- Given 合并后的 AGENTS.md → When 跑 `check-context-budget.ps1` → Then 总量 14805 < 预算 14835。
- Given 收紧动作 → When 跑 `-Update` → Then 只允许下调（`14835 → 14805`），拒绝上调（退出码 2）。
- Given 全量门禁 → Then `pre-commit.ps1` 退出码 0。

## 不做

- 不迁走 `ERROR-HANDLING.md` §六 的历史审计明细（虽属常驻层历史叙述，但被多处注释按
  「判据」引用，迁移会牵动断链；本轮只需要 30 token，改它属扩大范围）。
- 不动任何生产代码；不新增门禁。
