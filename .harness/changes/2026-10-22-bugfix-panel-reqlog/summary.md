# 2026-10-22 全仓体检修复 C-3 / C-5 / C-6 变更摘要

> 分级：L2（业务逻辑）。需求：`docs/prd/REQ-2026-10-22-panel-reqlog-audit-fixes.md`。
> 无 ADR（无难逆转、无未来读者会困惑的权衡）。

## 一、改了什么

| 文件 | 作用 / 改动 |
|---|---|
| `internal/reqlog/reqlog.go` | C-3：`read()` 新增文件名日期段整文件粗筛（< fromDay / > toDay 跳过）；新增 `archiveDay(name)` helper |
| `internal/reqlog/reqlog_test.go` | 新增 `TestReadArchiveDayRangeCoarseFilter`（跨三天三向等价断言） |
| `internal/panel/autotask.go` | C-5：`truncateStr` 改为委托 `logfmt.Truncate`（rune 边界回退，n<=0 空串） |
| `internal/panel/truncate_test.go` | 新增 `TestTruncateStrUTF8Boundary`（中文截断合法性牙齿测试） |
| `internal/panel/login.go` | C-6：token 校验通过后在 loginMu 内做 check-and-delete 原子占位；删尾部重复 delete |
| `internal/panel/login_concurrent_test.go` | 新增 `TestLoginPollConcurrentPlaceholder`（并发双 poll 只放行一个） |

**没做的（明写，免得下轮重提）**：
- C-1（alerting 失败回退注册去重）、C-2（webhook 全量 uid）、C-4（archiveWriter recover）
  本轮不修——不在本次授权范围；维持审查报告现状记录。
- 未动 app.js / index.html / panel.go / index.go / server / usage / upstream / httpauth /
  session / config.go / main.go / scripts（授权文件域之外）。

## 二、怎么验证的

| 验证 | 命令 | 结果 |
|---|---|---|
| 构建 | `go build ./...` | exit 0 |
| 静态检查 | `go vet ./internal/reqlog ./internal/panel` | 无输出 |
| 定向新+旧测试 | `go test ... -run TestReadArchive\|TestTruncateStr\|TestLoginPollConcurrent\|TestArchive*` | 全 PASS（含 3 个新测试） |
| 全量回归 | `go test ./...` | （见提交前记录） |
| 格式 | `gofmt -l internal/reqlog internal/panel` | 无输出 |

**负向探针**：C-6 测试中 accountHits 计数——修复前两个并发 poll 会各打一次 account 端点
（计数=2），现在断言=1。探针随测试入库，不删除（它本身就是验收牙齿）。
C-3 等价断言：三向（闭区间 / 仅 From / 仅 To）结果集与粗筛前逐文件全读一致。

## 三、未完成 / 待补（诚实清单）

- [ ] C-6 只测了 cn realm 的并发路径；global realm 的 GlobalCompleteRegistration 分支
      未构造桩（同一 loginMu 占位逻辑，realm 分支在占位之后，不受影响）。
- [ ] 本机 Go 工具链（go1.27.3 module toolchain）在开发中途被 Windows Defender 隔离过一次，
      已重下载恢复；这是环境事件，与改动无关。

## 四、怎么回滚

纯代码改动，无二进制部署、无数据迁移：

```powershell
git revert <commit>
```

或 `git checkout HEAD~1 -- internal/reqlog internal/panel`（仅回退代码，测试随代码一起走）。
凭证目录 `auths/` 不受本次改动影响。

## 五、Learning

根因：三个问题同属「读盘/锁边界粗糙」——读盘不看时间窗、截断不看 rune 边界、
锁不包住 check-then-act。修法统一是把边界条件显式化，而不是堆新逻辑。
