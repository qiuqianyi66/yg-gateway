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
## 七、补：部分缺失也能**阻止**了（2026-10-18 续，本条目收尾）

§五 留的边界（「只做到能查到，不是能阻止」）已收掉：

| 文件 | 改动 |
|---|---|
| `internal/auth/auth.go` | 抽出 `LoadDirReport(dir) (auths, skipped []string, err)`；`LoadDir` 变成薄包装（**调用面零改动**） |
| `internal/pool/pool.go` | 抽出 `syncLocked(auths, allowDelete)`；新增 `SyncToDirIncomplete(auths, skipped)` = 只增不删 + WARN |
| `cmd/server/main.go` | 用 `LoadDirReport`；`skipped > 0` 时走 `SyncToDirIncomplete`，否则走 `SyncToDir` |

语义：

- **完整扫描** → 照旧（含空集下限）；
- **不完整扫描**（有文件读不出/解析失败）→ 只新增/更新，**绝不剔除**，并 WARN 说明读到了几个、跳过了几个。

这样「10 个账号文件坏了 2 个」不会再删掉那 2 个账号的状态；而那 2 个账号的凭证确实不在池里
（读不到就是没有凭证），不影响选号正确性——丢状态才是不可逆的。

### 7.1 验证

- `go test ./internal/auth/ -run TestLoadDir -v` → 6/6 PASS（含新增 `TestLoadDirReportReturnsSkipped`：
  两个坏文件 → 0 账号、无 error、**skipped 恰好 2 条且含两个文件名**）。
- `go test ./internal/pool/ -run TestSyncToDir -v` → 3/3 PASS（含新增子用例「扫描不完整时只增不删」：
  u1 未被删、u3 被加入）。
- `go build ./...` 通过；auth / pool / cmd/server 三包测试全绿。
- **牙齿**：在**同步到本提交**的副本里把 `syncLocked(auths, false)` 改回 `true`（即「不完整也照删」）
  → 「扫描不完整时只增不删」子用例红（u1 被删）。

> 过程记录（教训）：第一次牙齿验证用了旧副本，副本里还没有 `SyncToDirIncomplete`，
> 于是红的是 `build failed` 而不是断言失败。**未提交的代码没法在旧克隆里验证**——
> 要么先提交再克隆，要么把改动一并拷进副本。