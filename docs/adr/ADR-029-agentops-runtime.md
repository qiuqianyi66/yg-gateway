# ADR-029：运维 agent runtime——DSH 内核切片的落点与边界

> 状态：**已接受（实施中）**，2026-11-26。
> 关联：`docs/prd/REQ-agentops-runtime-2026-11-26.md`、ADR-017（opsagent 位置）。
> 参照源码：`D:\DeepSeek Harness\resources\app\dsh\node_modules\@deepseek-ai\dsh-agent-loop`（0.2.2-rc.5）。

## 背景

现有 `internal/opsagent.Agent` 是规则引擎：固定 Observers → 两条规则 → Sink，ADR-017 明确只依赖 domain 层。它没有 session、没有 turn/step、没有模型、没有工具调用。2026-11-26 的 413 事故暴露了第二层诊断缺口：规则能报“容量/402 矛盾”，但不能按现场证据选择 `request_stats`/`deploy_reports`/`recent_errors` 去定位一个新形状。

DSH 的 `dsh-agent-loop` 提供了成熟契约：append-only session 事件、turn/step、model call、guarded tool pipeline、compaction、cancellation、persistence。目标是把这套**内核语义**1:1 落到 Go 单二进制里，而不是复制它的插件生态。

## 决策

**D1：runtime 放 `internal/opsagent` 包内，新增 `Runtime`，不新建包。**

- 候选 A：新建 `internal/agentops`。干净，但会新增 `panel→agentops`、`cmd→agentops` 依赖方向，`check-boundaries.ps1` 冻结表要改；且 runtime 与规则共享 unknown/evidence 语义时还要跨包转换。
- 候选 B（采用）：同包新增 `runtime_*.go`，只 import 标准库；模型、工具、策略全部接口注入。包边界不变，`opsagent.Agent` 规则不动。
- 代价：包体量上升；用文件切分（session/tools/loop/compact/types）控制在 1000 行/文件、120 行/函数棘轮内。

**D2：事件 taxonomy 采用 DSH 的 `domain/verb` 形状。**

事件类型：`session/created`、`turn/start`、`step/start`、`request/header`、`assistant/message`、`tool/call`、`tool/result`、`tool/denied`、`compaction`、`turn/end`、`session/disposed`。字段 `seq/time/type/turn/step/data`。不复刻 DSH 全部 `agent/*` 事件，只保留可审计闭环所需最小集。

**D3：模型与工具不直连上游/文件。**

`Model` 接口由 `cmd/server` 注入；生产适配器只调本机 `/v1/chat/completions`，默认模型空=不启动。工具由 `cmd/server` 注入闭包，runtime 不 import pool/server/upstream（R6/边界）。MVP 工具全只读；`ReadOnly=false` 默认拒绝，无审批回调。

**D4：持久化用 JSONL + fsync，不用 SQLite/Redis。**

运维 agent 事件量小（每 10 分钟一轮），JSONL 可直接 grep/回放，零新依赖。重开忽略不完整尾行，补 `turn/end{interrupted}`；这与 DSH “physically valid log + interrupted turn closers”同语义。

**D5：上下文压缩用“保留 system + 最近消息 + 摘要旧段”，不引入模型摘要。**

MVP 压缩是确定性机械摘要：旧段截断为文本清单，保证不拆散 tool 配对。理由：压缩本身不能成为新的故障源；模型摘要留给后续阶段。

## 后果

- 正面：单二进制内获得可回放 agent 循环；规则引擎与 runtime 可并行；默认关闭零回归；测试全 fake model，零付费调用。
- 负面/延期：无流式 token 回调、无 steer/inject 完整语义、无子 agent/goal/jobs/skills/MCP；工具结果上限 8KB，长日志需先聚合。
- 观测：`/panel/api/opsagent/status` 增加 `runtime` 块；session JSONL 是审计真相。
- 回滚：配置关闭即停；代码删除 runtime 文件即回规则引擎；session 目录独立可删。

## 强性质与反例

- **append 原子性**：单事件一行 + fsync；崩溃只会留下不完整尾行，重开丢弃。反例“写成功但调用方不知道”→ 重开时以文件内容为准，`seq` 连续。
- **工具副作用**：策略在 Execute 前判定；拒绝路径不调用工具函数。反例“拒绝后仍执行”→ `runtime_tools_test` 用计数器断言零调用。
- **取消**：ctx cancel 后不再发新模型请求；已写 assistant 事件保留。反例“取消后仍跑完”→ fake model 阻塞，断言 `turn/end{canceled}` 且 steps 停止。
- **默认关闭**：`enabled=false` 不建目录/不启动。反例“配置缺失仍启动”→ 装配测试断言 nil。
