# 2026-10-29-agent-profile-ui — R5 面板 agent 流量画像（V3-10 面板端）

> 状态：**部分实现，阶段提交**。后端端点主体 + 单测已就绪并提交；路由注册与
> 面板前端待外部 trends 轮合并后补（panel.go / app.js 被外部未提交改动占用，
> 同文件并发编辑不可安全提交——L-21）。REQ：REQ-agent-profile-ui-2026-10-29.md。

## 改了什么（本阶段）

- `docs/prd/REQ-agent-profile-ui-2026-10-29.md`：R5 立项（必要性 + 数据契约 +
  GWT 验收 + 约束）。
- `internal/panel/agent_profile.go`：`(p *Panel) agentProfile` handler——
  ReadArchive 最近 1000 条 chat 事件 → `reqlog.AggregateProfile`；
  四态语义：RequestLog nil → 501（同 request_metrics/trends）；零条/全无信号 →
  `present:false`（整块省略的显式 knowable 表达）；unknown/other 分桶；
  cache_miss_unknown 不累加。
- `internal/panel/agent_profile_endpoint_test.go`：3 测试直接调 handler（不经 mux，
  路由未挂）：有信号（分桶/排序/unknown 分离）、无信号、未接线 501。

## 怎么验证的

- `go test ./internal/panel -run TestPanelAgentProfile`：3/3 绿。
- go build / gofmt 待外部合并后与前端一起全量验证（本阶段文件已独立编译通过）。

## 待办（外部合并后，30 分钟内可收尾）

1. `Panel.routes()` 加一行：`p.mux.HandleFunc("GET /panel/api/agent_profile", p.withAuth(p.agentProfile))`。
2. app.js 加「流量画像」区块渲染（by_agent 汇总条 + top_sessions 榜；unknown 桶标
   「UA 未知」、other 标「未识别 UA」；present:false 隐藏区块）——按 REQ GWT。

## 怎么回滚

- revert 本提交（两个新文件 + REQ，无既有文件改动，可安全 revert）。
