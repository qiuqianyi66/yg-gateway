# usage-incremental 回滚手册

## 回滚方式

`git revert <本提交>`，回到「全量深拷贝 + 全量重写」旧行为。

## 数据文件

- `usage.json`：格式未变（fileVersion=6），旧二进制正常读。
- `usage.wal.jsonl`：新增侧车。旧二进制**不读不写**它；revert 后它残留无害——
  旧二进制下次 force flush 会重写基线，WAL 仍被忽略。再升级回来时，回放按逐字段取大合并，
  旧记录不会把新基线拉低。
- 无凭证改动（`auths/` 不随本回滚动）。

## 回滚后验证

`go test ./...` 全绿 + `go build ./cmd/server` + 启动后 `/healthz` 与 `/v1/models` 正常。
