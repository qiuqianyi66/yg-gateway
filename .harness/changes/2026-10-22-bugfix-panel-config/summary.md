# 2026-10-22 bugfix：全仓体检版块 D 修复（D-01..D-06 / F-2）

## 背景
版块 D（基础设施/状态/CLI）只读体检发现 6+1 项问题，本变更按组织层已定方案逐项修复。

## 改了什么

| 编号 | 级别 | 位置 | 修复 |
|---|---|---|---|
| D-01 | P1 | cmd/server/main.go、internal/panel/index.html | `restartRequiredFields` 补 `alerting.webhook_url` / `alerting.min_credit_threshold` / `alerting.consecutive_failures` / `alerting.dedup_minutes`；面板两个告警字段 hint 加「改动需重启进程生效」。告警器是启动期装配（alerting.New），Notifier.cfg 不可变，选保守方案：装配期字段+提示，不做 livecfg 热更新。 |
| D-02 | P2 | internal/panel/app.js:2260 | `session_sticky.derive` 三态回填特判：`v !== false`（null/缺失=后端默认开 → checked=true）；其他复选框行为不变。零净增行。 |
| D-03 | P2 | cmd/server/main.go:662 | 磁盘 config 已是坏 JSON 时 saveConfig 直接报错（「当前配置文件已损坏…」），不再降级空 map 洗掉用户手写键。 |
| D-04 | P2 | cmd/credit/main.go | 抽出 `loadAuthFile`；损坏/不可读 auth 文件补一条 `{Error:"unreadable/corrupt: <文件名>"}` 行（对齐 cmd/signin 的 LOAD_ERR 口径）。 |
| D-05 | P3 | cmd/server/main.go:649 | saveConfig 头部注释改为「以 restartRequiredFields 函数体为唯一真相」+ 补全摘要清单。 |
| D-06 | P3 | internal/panel/index.html:1512-1515 | session_sticky_enabled / derive 两个开关行补「需重启」内联 hint。 |
| F-2 | P2 | internal/panel/panel.go:463、app.js:1064 | `can_disable_thinking` 与 /v1/models 同口径：仅「推理已知 且 可关」时 emit true，其余省略字段；前端筛选改 `=== false` 才排除（缺失=未知不排除）。 |

## 怎么验证的
- `go build ./...` ✓；`go vet ./...` ✓；gofmt（1.27.2 工具链）我负责的文件干净。
- 新增/更新测试：
  - `cmd/server/saveconfig_guard_test.go`：D-03 坏磁盘配置报错且原文件原样保留；D-01 `TestRestartRequiredFieldsIncludesAlerting`。
  - `cmd/credit/main_test.go`：D-04 `TestLoadAuthFileBroken`（坏 JSON/缺失/正常三分支）。
  - `internal/panel/frontend_test.go`：新增 `TestAppJSDeriveCheckboxDefaultOn`（node vm 切片执行回填行）；`TestAppJSModelFilter` effortOff 期望按三值语义更新。
  - `coverage_handlers_test.go`：stub JSON 补 `supportsReasoning:true`（F-2 后仅推理已知才 emit）。
- 同提交跑 `go test ./cmd/server ./cmd/credit ./internal/panel` 与全量 `./...`。

## 回滚
`git revert <commit>`。全部为面板提示/错误分支/测试改动，无状态格式、无 wire 形状破坏（derive 不加 omitempty；can_disable_thinking 仅在面板内部 JSON 改省略语义）。

## 备注
- 本机 Go 工具链 1.27.3 缓存目录中 go.exe 被删（疑似安全软件），实测环境回落到 1.27.2 工具链；`go.mod` 的 `go` 指令行由并行任务调整，不在本变更 add 清单内。
- 工作树存在其他并行 agent 的未提交改动（server/session/reqlog 等），本提交只显式 add 自己负责的文件。
