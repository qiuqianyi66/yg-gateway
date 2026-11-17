# alerting 去重测试竞态修复（2026-11-17）

## 触发

v2.2.1（`97eb61f`）的 release run `44071886063` 里 `gate` job 红：

```
--- FAIL: TestNotifierDedup (0.00s)
FAIL  yg-gateway/internal/alerting  0.344s
```

`check-depth.ps1` 的失败信息是「go test -cover 失败（测试红 → 棘轮数字不可信）」。
同一个 run 的 `test` / `race` job 反而是绿的——它们只是运气更好。

## 根因（不是产品 bug）

`internal/alerting/notifier.go:123-160` 的 `send` 失败路径有 `delete(n.last, key)`，
这是**有意设计**：登记了却发不出去，要允许下一次重试。

测试却用 `WebhookURL: "http://127.0.0.1:1/none"` 制造必然失败，于是：

| 平台 | 连接 127.0.0.1:1 的行为 | 结果 |
|---|---|---|
| Windows | 失败慢（SYN 重试） | 异步 goroutine 还没跑到 `delete`，断言读到时间戳 → **侥幸绿** |
| Linux | `ECONNREFUSED` 立即返回 | goroutine 迅速 `delete`，断言读到零值 → **`t.Fatal`** |

`-count=1` 的 Windows 本地跑永远看不到这个问题。

## 修法

`internal/alerting/notifier_test.go` 新增 `okWebhook(t)`——`httptest.NewServer` 返回 200，
`t.Cleanup(srv.Close)`。`TestNotifierDedup` 与 `TestNotifyCreditLowThreshold` 改用它。
`TestNotifierConcurrent` 保持用连不上的地址：它不断言 `n.last`，只验并发安全，
失败路径反而更快结束。

成功路径下 goroutine 不删登记，断言与异步之间不再有竞争。

**产品行为未改一行**：`notifier.go` 未触碰。

## 验证

| 项 | 证据 |
|---|---|
| 本地 `go test ./internal/alerting/ -cover -count=5` | ok，`coverage: 78.7% of statements`，exit 0 |
| CI `gate` job | run `44073726962`：`gate / gate=success` |
| CI `test` / `race` | 同 run 均 success |
| CI 5 平台 build | 同 run 全 success |
| e2e | run `44073727050` success |

## 本轮仍未解决

`build-image` job 仍红：`denied: permission_denied: write_package`。
这是**用户级 GHCR 包**的 Actions 写权限缺口，无 REST 端点可调（只有 org 有），
需要网页操作：`Package settings → Manage Actions access` 给仓库加 Write，然后 rerun。
自 v2.1.0 起一直失败，不是本轮引入。

## 回滚

`git revert dd03bfe`。回滚后 Linux 上的 `gate` 会重新红——修复本身没有副作用。
