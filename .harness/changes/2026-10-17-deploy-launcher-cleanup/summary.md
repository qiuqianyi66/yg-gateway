# 变更记录 — 回收 launcher（cmd.exe）：失败换版不留孤儿

- 需求：`docs/prd/REQ-deploy-launcher-cleanup-2026-10-17.md`（含必要性段）
- 来源：`docs/PLAN-open-items-2026-10-17.md` §9.3 **B4b**
- 日期：2026-10-17
- 分级：**L2**（改生产换版脚本；回滚路径受影响）

## 一、改了什么

| # | 位置 | 改动 |
|---|---|---|
| 1 | `Start-Instance` | 加 `-PassThru`，返回值增加 `Pid`（不给就没有"精确回收"这一步） |
| 2 | 新增 `Stop-Launcher($launcher, $what)` | 按**本次 launcher PID** 回收；回收前核 `ProcessName -eq 'cmd'`，不是就不杀并说明（PID 复用防护） |
| 3 | 新增 `Remove-OrphanLaunchers($exePath)` | 启动前清扫**历史**孤儿：`cmd.exe` ∧ 命令行含目标 exe 完整路径 ∧ **父进程已不存在**，三条同时成立才杀 |
| 4 | 5b 自动回滚分支 | 回滚**前**先 `Stop-Launcher $started`（否则它握着失败现场 stderr 的句柄） |
| 5 | `Invoke-Rollback` 失败分支 | 回滚实例也没起来时 `Stop-Launcher $rbStart` |
| 6 | 2/6 停旧实例之前 | `Remove-OrphanLaunchers $targetExe`；命中 0 个不打印（常态） |

## 二、怎么验证的

### 2.1 语法与成功路径

```
$ powershell -NoProfile -File scripts/deploy-prod.ps1 -DryRun
exit=0   报告：…deploy-prod-20261017-155532.json   换版完成，用时 0.8s。
```

### 2.2 清扫判据的两条分支（真造进程，同一段代码文本抽出来跑）

| 场景 | 造法 | 期望 | 实测 |
|---|---|---|---|
| **父进程已死（孤儿）** | 一次性 `powershell` 里再 `Start-Process cmd /c "<prod exe> … & ping"`，随后该 powershell 退出 | 被回收 | `killed=2`（含另一个同形态进程），剩余含该路径的 cmd = **0** |
| **父进程还活着** | 当前会话直接 `Start-Process cmd …`（父进程 = 本 pwsh `$PID` 27976） | **不杀** | `killed=0`，该 cmd 仍在（`True`） |
| 命令行不含目标 exe | — | 不杀 | 由判据 2 覆盖（`IndexOf` 未命中即 `continue`） |

### 2.3 门禁

`gofmt -l` 空 · `go vet` 空 · `go test ./...` 20 包 ok · `check-boundaries` · `check-depth`
（含前端棘轮）· `pre-commit.ps1` 全量 8 项 exit 0。

## 三、决策（含未被采纳的方案）

| # | 决策 | 选择与理由 |
|---|---|---|
| D-1 | 精确 PID vs 命令行启发式 | **失败现场用精确 PID**（`-PassThru`）；启发式只用于历史孤儿清扫，并且必须"父进程已死"——B4b 原建议只有启发式，会有误杀操作者 shell 的面 |
| D-2 | 清扫位置 | **2/6 之前**：孤儿可能握着上一次的日志句柄 |
| D-3 | PID 复用 | 回收前核 `ProcessName`，不符就不杀 + 打印原因（R10） |
| D-4 | 报告里加字段？ | **不加**。报告 schema 已被 rehearsal 断言，本次不改 schema；回收情况走 stdout 日志 |

## 四、回滚

`git revert <本提交>`（单提交，纯增量）。若只想关掉清扫：删 2/6 前的 `Remove-OrphanLaunchers` 调用
（函数留着不影响）；只想关掉失败回收：删两个 `Stop-Launcher` 调用。

## 五、遗留（如实记录）

1. **`deploy-prod.rehearsal.ps1` 未跑**：它的 6 场景/53 断言需要一次真实换版演练（会动本机 7864 沙箱与
   生产目录的时间戳）。本次只在真机验证了函数级判据（§2.2）与 `-DryRun`。**这是本变更最大的未覆盖面**，
   下次换版前应优先跑一次排练。
2. **`Get-CimInstance` 在极老系统/受限 WMI 上可能抛错** → 已 `try/catch` 返回 0（等价"没找到孤儿"），
   但这属 fail-open；若将来在意，可改成打印一行 WARN。
3. **孤儿判据依赖"父进程号已不存在"**：Windows 会复用 PID，极端情况下一个真实孤儿可能因为 PID
   被别的进程占用而被判成"父进程还活着" → 不杀。方向安全（宁可不杀）。
