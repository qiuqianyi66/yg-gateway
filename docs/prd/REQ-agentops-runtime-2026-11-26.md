# REQ: 网关运维 agent runtime（DSH 内核 1:1 切片，2026-11-26）

> 状态：**实施中**。对象是 `internal/opsagent.Runtime`，默认 `opsagent.agent.enabled=false`。
> 参照：本机 `D:\DeepSeek Harness\resources\app\dsh\node_modules\@deepseek-ai\dsh-agent-loop`（0.2.2-rc.5）与 `dsh-agent`/`dsh-session` README。

## 必要性

- **真实问题**（2026-11-26 实弹）：网关 7×24 常驻，异常判断仍靠人看 `/status`、request-logs、deploy 报告。当天 DSH 截图导致护栏假性 413，定位靠人逐条比对“护栏估算 vs 上游 usage”；现有 `opsagent` 只有两条固定规则，不能按现场证据选择工具、不能保留“看了什么→为什么→做了什么”的可回放链路。
- **不做会怎样**：静默。同类事故下次仍要人工从三个日志面重建现场；规则引擎漏掉的形状（413/503/延迟/账号抖动）没有第二层诊断；结论散在会话里，无法审计。
- **现状是否变了**：`opsagent` 规则、unknown registry、reqlog、deploy JSON 报告、DSH 本机源码都已就绪。缺的是 DSH 式“模型 + 工具 + session 事件”的循环内核，不是再写一条规则。
- **更便宜的等效手段**：把诊断步骤写成固定 runbook/脚本。能覆盖已知事故，但新形状一来就失效，且没有模型按证据选择下一步的能力。MVP 仍只读，风险低于任何自动改配置的方案。

## 1:1 的范围（内核切片，不是 338 个 npm 包）

对照 DSH `dsh-agent-loop` 的公开契约，复刻以下**可观察行为**：

| DSH 契约 | 本仓落点 | MVP |
|---|---|---|
| append-only Session 事件日志 | `runtime_session.go` JSONL，`seq/time/type/turn/step/data` | ✅ |
| turn → step → model → tool → step | `runtime_loop.go` | ✅ |
| 工具通过 guarded pipeline 执行 | `runtime_tools.go` 策略先于 Execute | ✅ |
| 取消保留已交付内容、落 `turn/end` | ctx cancel + 事件 | ✅ |
| 上下文压缩替换旧历史 | `runtime_compact.go` + `compaction` 事件 | ✅ |
| followup / steer / inject / whenIdle | `Runtime` 句柄 | 部分（followup/whenIdle；steer/inject 预留） |
| 子 agent / goal / jobs / skills / MCP / 流式 | — | **不做**（见非目标） |

## 契约

1. **Session 持久化**：会话目录默认 `data/opsagent-sessions`；文件名只允许 `[A-Za-z0-9._-]`，拒绝路径穿越。每事件一行 JSON，追加后 flush+fsync；重开时忽略最后一行不完整 JSON，已开未闭的 turn 补 `turn/end{outcome:"interrupted"}`。
2. **工具策略**：`ReadOnly=true` 默认放行；非只读工具默认拒绝，落 `tool/denied`，**不执行副作用**。MVP 不提供审批回调，写动作没有放行路径。
3. **循环边界**：`max_steps` 默认 12，单 turn 超时默认 300s；超限落 `turn/end{outcome:"max_steps"/"timeout"}`，不静默。
4. **上下文预算**：请求前估算消息 token；超 `context_max_tokens` 时保留 system + 最近消息，把更早内容压成摘要，落 `compaction` 事件；压缩不得拆散 tool_call/tool_result 配对。
5. **审计**：每 turn 必须有 `turn/start` 与唯一 `turn/end`；每次模型请求有 `request/header`；每次工具调用有 `tool/call` 与 `tool/result` 或 `tool/denied`。面板只读展示最近会话与事件数。
6. **零回归**：`enabled=false` 时不建目录、不启动 goroutine、不调模型；规则引擎 `opsagent.Agent` 行为不变。
7. **零付费验证**：单测全用 fake model；生产模型默认空，只有显式配置免费模型才启动。

## Given-When-Then

- When `opsagent.agent.enabled=true` 且 `model` 非空，Then 每 `interval_sec` 启动一轮巡检，落一个 session 文件；`/panel/api/opsagent/status` 可见 `runtime.session_id/last_turn/steps`。
- When 模型返回只读工具调用，Then 先落 `tool/call`，执行后落 `tool/result`，把结果作为 `tool` 消息进入下一步。
- When 模型请求写工具，Then 落 `tool/denied`，工具函数零调用，循环继续。
- When 工具 panic/超时，Then 该 call 落 `tool/result{is_error:true}`，进程不崩，后续步骤可继续。
- When turn 被取消，Then 落 `turn/end{outcome:"canceled"}`，已写入的 assistant 文本保留。
- When 估算超预算，Then 落 `compaction`，请求仍发出且不含被 shadow 的旧消息。
- When `enabled=false`，Then 行为与变更前完全一致。

## 非目标（明写，防下轮重提）

- 不引入 Node/Python/新 Go 依赖；不把 DSH 的 338 个包搬进单二进制。
- 不做自动改配置、换号、冷却、重启、部署；MVP 工具全只读。
- 不做子 agent、goal、jobs、MCP、skills 市场、流式 token 回调。
- 不替代 `opsagent.Agent` 规则引擎；规则继续跑，runtime 是第二层诊断。

## 回滚

- 配置回滚：删 `opsagent.agent` 或置 `enabled=false`，重启即回到规则引擎。
- 代码回滚：删除 `internal/opsagent/runtime_*.go` 与 `cmd/server/opsagent_runtime*.go`，还原 `main.go`/面板字段。
- 数据回滚：session 目录可整目录删除；不影响 pool/config/usage。
