# Rollback — bugfix-alerting-redisstore（2026-10-22）

## 回滚方式

`git revert <本提交 hash>` 一条命令即可。本批次不涉及数据迁移、凭证文件、wire 协议字段
或部署产物，纯代码/脚本回滚无副作用。

## 各改动点的逆操作（若手工回滚）

| 编号 | 文件 | 逆操作 |
|---|---|---|
| C-1 | `internal/alerting/notifier.go` | 删除失败分支的 `delete(n.last, key)`（err 与 non-2xx 两处），恢复「登记保留」原行为 |
| C-2 | `internal/alerting/notifier.go` | `bodyEv` 副本与 `logfmt.UID8` 调用改回直接 `json.Marshal(ev)`；删除 `yg-gateway/internal/logfmt` import |
| F-01 | `internal/redisstore/redisstore.go` | 删除 `iter.Err()` 检查与 WARN 日志块 |
| 白名单 | `scripts/check-boundaries.ps1` | `'alerting' = @('logfmt')` 行改回 `'alerting' = @()`（叶子） |

## 回滚后验证

1. `powershell -NoProfile -File scripts/check-boundaries.ps1` → 应报 alerting→logfmt 违规
   （若 C-2 未一并回滚）或通过（两者都回滚）。
2. `go test ./internal/alerting ./internal/redisstore -count=1` → C-1/C-2/F-01 新增 5 个测试
   应失败（回滚后行为回归旧语义，测试即红——这是预期，证明测试钉住了修复）。
