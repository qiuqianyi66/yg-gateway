# 2026-10-30-log-relocation-gofmt — 部署日志落点迁移 + 存量 gofmt 清零

> 状态：**已落地**（b8700f9，已部署验证）。

## 改了什么

- `scripts/deploy-prod.ps1`：`-LogDir` 默认从 `$env:TEMP` 改为 `<ProdDir>\data\logs`
  （生产 = `D:\yg-gateway\data\logs`）；同步 610-611 行注释。rehearsal 显式传
  `-LogDir` 不受影响（已跑排练：53 断言 0 失败）。
- `internal/server/models_handler.go` / `internal/upstream/client.go`：存量 gofmt
  漂移清零（纯格式：对齐空格/缩进，无逻辑变化）。此后 `gofmt -l ./cmd ./internal`
  全仓零输出，CI `-AllFiles` 也不再拦。

## 怎么验证的

- `scripts/deploy-prod.rehearsal.ps1`：6 场景 53 断言全过（A1/A2/B/C/D/E）。
- `scripts/pre-commit.ps1` 全量 11/11 通过（含 gofmt 全仓零输出）。
- 重新部署：报告与 stderr 日志确认落 `D:\yg-gateway\data\logs\`
  （deploy-prod-20261030-203430.json、yg-gateway-20261030-203430.err.log），
  健康 6/6、冒烟三条全过、exit=0。

## 怎么回滚

- 日志落点：`deploy-prod.ps1` 传 `-LogDir $env:TEMP` 即回旧行为（脚本默认值一处）。
- gofmt 清理：纯格式，git revert 或忽略均无功能影响。
