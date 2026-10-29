# 2026-10-29-agent-profile-ui — R5 面板 agent 流量画像（V3-10 面板端）

> 状态：**已全部实现并提交**（5802b8b 后端阶段 + 4f4d2d3 前端接线阶段）。
> REQ：REQ-agent-profile-ui-2026-10-29.md。外部 trends 轮已合入（b208fa7），
> 无同文件冲突。

## 改了什么

- `docs/prd/REQ-agent-profile-ui-2026-10-29.md`：R5 立项（必要性 + 数据契约 +
  GWT 验收 + 约束）。
- `internal/panel/agent_profile.go`：`(p *Panel) agentProfile` handler——ReadArchive
  最近 1000 条 chat 事件 → `reqlog.AggregateProfile`；四态：RequestLog nil → 501
  （同 request_metrics/trends）；零条/全无信号 → `present:false`；unknown/other 分桶；
  cache_miss_unknown 不累加。
- `internal/panel/agent_profile.js`：自包含视图（同 trends.js 模式，不依赖 app.js）——
  by_agent 汇总表（请求/会话/token/缓存命中未命中）+ top_sessions 榜（turns 前 10，
  fp 前 8 位）；unknown 桶标「UA 未知」、other 标「未识别 UA」；`present:false` 时整块
  隐藏（与 /status 整块省略同源）；挂载 usage 视图（hashchange + btnAgentProfile 刷新）。
- `internal/panel/panel.go`：路由 2 行（`/panel/api/agent_profile` + `/panel/agent_profile.js`）。
- `internal/panel/index.go`：`go:embed agent_profile.js` + `agentProfileScript`。
- `internal/panel/index.html`：用量视图新增「流量画像 · Agent 构成」box + script 引用。
- `internal/panel/agent_profile_endpoint_test.go`：4 测试——有信号分桶/排序/unknown 分离、
  无信号 present:false、未接线 501、经完整路由可达（防路由丢失）。

## 怎么验证的

- `go test ./internal/panel -run TestPanelAgentProfile`：4/4 绿；panel 全量绿。
- `go build ./...`：EXIT=0；gofmt（本变更面）：零输出。
- pre-commit 全量：build/vet/test/深度棘轮/R10/引号/治理全绿；**gofmt 与边界 2 项被
  外部并发 bench 轮 WIP 阻塞**（internal/bench/ 与 cmd/bench/ untracked 半成品：
  gofmt 未清 + bench 边界未声明）——非本变更面，未触碰外部文件；外部轮合入后
  pre-commit 自然转绿。

## 怎么回滚

- revert 两个提交（5802b8b、4f4d2d3）：均为新文件 + 既有文件增行（panel.go 路由 2 行、
  index.go embed + 1 handler、index.html 1 box + 1 script），无删除/协议变化，可安全 revert。
