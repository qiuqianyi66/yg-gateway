# bugfix-alerting-redisstore 变更摘要（C-1 / C-2 / F-01 + 边界白名单）

日期：2026-10-22
分级：P2 bugfix（全仓体检批次 6，OrganizeAgent 直办——批次 6 派发时 sub-agent 并发上限 5/5）

---

## 一、改了什么

| 编号 | 文件 | 问题 | 修复 |
|---|---|---|---|
| C-1 | `internal/alerting/notifier.go` | `send()` 在 POST 发出**前**登记去重时间戳；发送失败（网络错误 / non-2xx）时登记保留 → 整个 DedupWindow 内同事件静默不再重试 | 失败路径（err 分支与 status>=300 分支）删除去重登记 `n.last[key]`；成功才保留。下一次同事件触发即可重试，原始「防风暴刷屏」去重语义不受破坏（成功仍去重）。 |
| C-2 | `internal/alerting/notifier.go` | 外发 webhook body 携带**完整 AccountUID**，与 R2「UID 只留前 8 位」脱敏口径不一致（webhook 下游可能被转存/转发） | body 序列化前用副本 `bodyEv := ev; bodyEv.AccountUID = logfmt.UID8(ev.AccountUID)`；去重键保持**完整 uid**（内部键，防 8 位前缀碰撞吞事件）。新增依赖 `internal/logfmt`。 |
| F-01 | `internal/redisstore/redisstore.go` | `LoadBinds` 的 SCAN 迭代错误未检查（`iter.Next(ctx)` 后无 `iter.Err()` 判定）——SCAN 中途出错时返回部分映射且静默 | 循环后判 `iter.Err()`，出错打 WARN 日志（`[redisstore] LoadBinds scan error`），不阻断启动恢复（仍返回部分映射）。 |
| 白名单 | `scripts/check-boundaries.ps1` | alerting 由叶子包（allow=[]）变为依赖 logfmt，原表未覆盖 → 边界检查违规 | `-Fix` 实测打印 `alerting -> logfmt`（证据留存），手工更新 allow 表：`'alerting' = @('logfmt')` + 理由注释（R2 口径脱敏、logfmt 纯叶子 helper、无反向依赖风险）。 |

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 新增测试（alerting，4 个） | `go test ./internal/alerting -count=1` | ok（0.404s） |
| 新增测试（redisstore，1 个） | `go test ./internal/redisstore -count=1` | ok（0.468s） |
| go vet | `go vet ./internal/alerting ./internal/redisstore` | 无输出 |
| gofmt | `gofmt -l internal/alerting internal/redisstore` | 无输出（干净） |
| 边界检查 | `powershell -NoProfile -File scripts/check-boundaries.ps1` | ✅ 通过（CHECKRC=0） |

测试清单（本批次新增，均实跑 PASS）：
- `TestNotifierFailureClearsDedup`：连接拒绝失败 → 去重登记被清除（允许下次重试）。
- `TestNotifierHTTPErrorRetriesNextTrigger`：non-2xx 失败 → 登记清除、再次触发重发（hits 递增）。
- `TestNotifierSuccessKeepsDedup`：成功后登记保留 → 窗口内同事件不去重（原始语义不破坏）。
- `TestNotifierBodyUIDTruncated`：body 只含前 8 位（`01234567`），完整 uid（16 位）不得出现。
- `TestFRSLoadBindsScanErrorIsWarned`：SCAN 返回错误 → 不 panic、返回非 nil 空映射、日志含 `LoadBinds scan error`。

> **工具链说明**：Windows Defender 间歇性把 go1.27.3 toolchain 的 `go.exe` 判定为 PUA 并隔离。
> 本批次用 `GOTOOLCHAIN=go1.27.2` 显式指定已缓存、未被拦截的 1.27.2 工具链完成编译级验证
> （go.mod 保持 `go 1.27.3` 未动——工作树中并行批次误降的 `go 1.27.2` 已恢复）。R14
> （≥1.27.2）语义上满足；CI 与用户侧恢复 1.27.3 后需复跑全量门禁（见遗留）。

## 三、未完成 / 残留

- [ ] 全量 `go test ./...` 与 pre-commit 10/10：本批次仅定向验证两个包；全量验证待全部批次
      合流后由 OrganizeAgent 统一跑（含其它批次的 panel 截断等测试）。
- [ ] 工具链：Defender 对 go1.27.3 `go.exe` 的 PUA 误报需用户侧加排除（管理员权限）或恢复后
      复跑门禁。

## 四、怎么回滚

`git revert` 本提交。C-1 是 send() 失败分支删除登记的局部改动；C-2 是 body 副本 + UID8 截断；
F-01 是 iter.Err() WARN；白名单改动在 `scripts/check-boundaries.ps1`（alerting 行删掉即恢复
叶子）。均无 wire 字段变更、无凭证变更，回滚无副作用。

## 五、Learning

- 「先登记后发送」的去重结构必须把失败视为**未发送**：登记只在成功路径保留，否则一次失败
  就吞掉整个窗口的重试机会（静默失效与 R10 同族）。
- 外发字段与内部键可以分层脱敏：去重键用完整 uid（防碰撞），body 用 uid8（R2 口径）——
  一个结构里两个口径都有正当位置，不要一刀切。
- check-boundaries.ps1 的 `-Fix` 只**打印**实测依赖、不写表——白名单更新是人工动作，必须
  以 `-Fix` 实测输出为唯一证据并注明理由（AGENTS.md「新增方向必须说明理由并更新脚本」）。
