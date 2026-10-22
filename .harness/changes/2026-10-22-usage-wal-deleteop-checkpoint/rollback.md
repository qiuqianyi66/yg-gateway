# usage-wal-deleteop-checkpoint 回滚手册

## 回滚方式

`git revert` 本提交。

## 数据兼容

- `usage.wal.jsonl` 里新出现的 `d` 字段（delete 记录）：旧二进制 unmarshal 时忽略未知字段，
  不报错；旧版不会再写新的 delete 记录。
- `config.json` 里 `usage.wal_checkpoint_after`：旧二进制忽略未知键，回落默认 20000。
- 无凭证改动（`auths/` 不随本回滚动）。

## 回滚后验证

`go test ./...` 全绿 + 启动后 `/healthz` 正常。
