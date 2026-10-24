# 2026-10-24 r10-leftovers-taskcenter-400

## 背景与必要性

`ERROR-HANDLING.md` 的 2026-10-16 全量审计之后新进/未覆盖的少量静默忽略点没有判据注释
（R10 要求「保留 `_` 但必须紧邻注释」）；另发现 `panel/taskcenter.go` 的畸形请求体处理
与兄弟端点约定不一致。

**核实后排除的误报**（独立调研 agent 提出、复核签名后否定，避免噪音 diff）：
`_, _ = a.BackfillRealm()`（panel/login.go / panel/import.go）——`BackfillRealm` 返回
`(bool, string)` 不含 error，且 `ERROR-HANDLING.md` §六 已明确豁免。

## 改了什么

**补判据注释（行为零变化）**：
- `upstream/streak.go` `rand.Read`：crypto/rand 自 Go 1.24 契约保证不返回错误（判据④）
- `upstream/sse.go` 空流兜底帧 writeRaw：写响应路径 + 紧邻 [DONE] 错误已检查（判据①）
- `upstream/blackcat.go` 排干 body：回收连接前的清理路径（判据②）
- `panel/login.go` / `cmd/login/main.go` 账号信息 Unmarshal：零值由紧邻的 UID 空校验
  兜底（502）/ login.sh 空 uid 终止（判据③）
- `reqlog/reqlog.go` Stat 失败路径 Close：真实错误经返回值上报（判据②）

**行为修正**：`panel/taskcenter.go tasksRunQueue` 畸形 JSON 由「静默走默认值并启动队列」
改为 400——与 autotask/tasks 端点约定对齐；旧行为等于一次损坏请求可误触发全账号任务执行。
`coverage_autotask_test.go` 同步改断言（400 + 合法空体默认并发 1 启动两腿），测试与生产
改动同提交（L-11）。

## 怎么验证的

`go build ./...`、`go test ./internal/panel/ -run TestPanelAutoTaskTasksRunQueue`（count=1）
通过；全量 `pre-commit.ps1` 通过（R10 检查器对新增行放行：注释均带「忽略/判据」格式）。

## 怎么回滚

单提交 revert；行为修正回滚后测试需一并回滚（同提交内）。
