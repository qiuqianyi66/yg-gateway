# 变更记录 — auths ↔ state.json 对账边界（B2 实质③，L3）

- 日期：2026-10-18
- 分级：**L3**（改的是破坏性对齐路径 + 加载语义）→ 先立 ADR
- ADR：`docs/adr/ADR-010-cross-file-reconcile.md`

## 一、改了什么

| 文件 | 改动 |
|---|---|
| `internal/pool/pool.go` | `SyncToDir` 加**空集下限**：`len(auths)==0 && len(byUID)>0` → 拒绝剔除 + WARN |
| `internal/auth/auth.go` | `LoadDir` 两处静默 `continue` 改为**点名文件的 WARN**（读失败 / 解析失败） |
| `internal/pool/syncdir_empty_guard_test.go` | 空集拒绝 + 非空照常对齐 |
| `internal/auth/loaddir_warn_test.go` | 两个坏文件 → 0 账号、无 error、**2 条 WARN 且点名路径** |

## 二、为什么（一句话）

`SyncToDir` 会**删状态并落盘**（不可逆），而 `LoadDir` 对坏文件**静默跳过**返回短列表——
「目录读不到」与「账号全被删了」在系统眼里无法区分，一次权限问题就能清空全部健康度观测。

## 三、wiki ③ 的前提修正（勘察结论，先说结论）

wiki 说「auths 与 state.json **无对账**」。**实测不成立**：`cmd/server/main.go:132` 启动期就调
`p.SyncToDir(auths)`，新账号加入、消失账号剔除并写回 `state.json`，注释明写「避免已删账号下次启动被复活」。

所以本条目落地的是**对账的下限与留痕**，而不是「从无到有造对账」。
「写前意图 + 启动前滚」**明确不做**：本仓没有跨文件事务（state 单文件 rename、auths 一账号一文件、
删+存同锁内完成），凭空加 intent 只会多一个不一致来源。触发条件写在 ADR §三。

## 四、怎么验证的

- `go test ./internal/pool/ -run TestSyncToDirRefusesEmptyScan -v` → **2/2 PASS**
  （空扫描后 u1/u2 仍存在、total 仍为 2；非空扫描照常剔除/新增）。
  实跑日志可见：`WARN: [pool] 扫描结果为空但池内有 2 个账号 —— 拒绝按空集对齐…`
- `go test ./internal/auth/ -run TestLoadDirReportsSkippedFiles -v` → **PASS**
  （两个坏文件 → 0 账号、无 error、WARN 恰好 2 条且含两个文件名）。
- **牙齿**：去掉空集下限 → 第一条红；去掉两处 WARN → 第二条红。

## 五、边界与代价

- 真·清空全部账号不再能靠「删空 auths 目录后重启」——需面板逐个删除或停服手改 `state.json`。
  故意如此：那种操作与「目录读不到」形态相同，代价差一个数量级。
- 部分缺失（10 个文件里 2 个坏了）**仍会**剔除那 2 个账号的状态；本轮的改进是**能查到**
  （WARN 点名文件），不是**能阻止**。要阻止需要把「跳过数」提升为对齐的输入参数，属下一步。
- Redis 快照路径不受本条影响（`RestoreFromSnapshot` 有自己的择新逻辑）。

## 六、回滚

`git revert <本提交>`：回到「空集也照删 + 坏文件静默跳过」的旧行为。