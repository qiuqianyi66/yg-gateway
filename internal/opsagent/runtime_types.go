// runtime_types.go 运维 agent runtime 的类型与事件 taxonomy（ADR-029）。
//
// 1:1 对照 DSH `dsh-agent-loop` / `dsh-session` 的可观察契约：
// append-only session 事件、turn/step、model call、guarded tool pipeline、
// compaction、cancellation。这里只保留可审计闭环所需的最小集，不复制
// DSH 的全部 agent/* 事件与插件生态。
package opsagent

import (
	"context"
	"encoding/json"
	"time"
)

// Runtime 事件类型。命名采用 DSH 的 `domain/verb` 形状，便于日志与面板同口径。
const (
	RuntimeEventSessionCreated   = "session/created"
	RuntimeEventTurnStart        = "turn/start"
	RuntimeEventStepStart        = "step/start"
	RuntimeEventRequestHeader    = "request/header"
	RuntimeEventUserMessage      = "user/message"
	RuntimeEventAssistantMessage = "assistant/message"
	RuntimeEventToolCall         = "tool/call"
	RuntimeEventToolResult       = "tool/result"
	RuntimeEventToolDenied       = "tool/denied"
	RuntimeEventCompaction       = "compaction"
	RuntimeEventTurnEnd          = "turn/end"
	RuntimeEventSessionDisposed  = "session/disposed"
)

// Runtime turn 结果。
const (
	RuntimeOutcomeSuccess     = "success"
	RuntimeOutcomeError       = "error"
	RuntimeOutcomeCanceled    = "canceled"
	RuntimeOutcomeMaxSteps    = "max_steps"
	RuntimeOutcomeTimeout     = "timeout"
	RuntimeOutcomeInterrupted = "interrupted"
)

// RuntimeEvent 是 session JSONL 的一行。seq 从 1 连续递增；time 用 RFC3339Nano。
type RuntimeEvent struct {
	Seq  int64           `json:"seq"`
	Time string          `json:"time"`
	Type string          `json:"type"`
	Turn int             `json:"turn,omitempty"`
	Step int             `json:"step,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// RuntimeMessage 是模型可见的一条消息（OpenAI chat 形状）。
// ToolCalls 只在 assistant 消息上出现；ToolCallID 只在 tool 消息上出现。
type RuntimeMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []RuntimeToolCall `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
}

// RuntimeToolCall 模型发起的一次工具调用。Arguments 保留原始 JSON。
type RuntimeToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// RuntimeToolSpec 注册的工具。Execute 由装配层注入；runtime 不直接读文件/网络。
type RuntimeToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	ReadOnly    bool
	Execute     func(context.Context, json.RawMessage) (string, error)
}

// RuntimeToolSchema 发给模型的工具声明（不含 Execute）。
type RuntimeToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// RuntimeUsage 上游 usage 的最小副本。
type RuntimeUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
}

// RuntimeModelRequest 一次模型请求。
type RuntimeModelRequest struct {
	Model     string              `json:"model"`
	Messages  []RuntimeMessage    `json:"messages"`
	Tools     []RuntimeToolSchema `json:"tools,omitempty"`
	MaxTokens int                 `json:"max_tokens,omitempty"`
}

// RuntimeModelResponse 一次模型响应。
type RuntimeModelResponse struct {
	Content      string            `json:"content"`
	ToolCalls    []RuntimeToolCall `json:"tool_calls,omitempty"`
	Usage        RuntimeUsage      `json:"usage"`
	FinishReason string            `json:"finish_reason,omitempty"`
}

// RuntimeModel 模型适配器。生产实现只调本机 /v1/chat/completions；测试用 fake。
type RuntimeModel interface {
	Complete(context.Context, RuntimeModelRequest) (RuntimeModelResponse, error)
}

// RuntimePolicy 工具策略。返回 allowed=false 时 reason 必须非空，落 tool/denied。
type RuntimePolicy interface {
	Authorize(context.Context, RuntimeToolSpec, json.RawMessage) (bool, string)
}

// RuntimeToolResult 一次工具执行结果；Denied 表示策略拒绝（工具函数未被调用）。
type RuntimeToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Denied  bool   `json:"denied,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// RuntimeConfig runtime 参数。零值由 NewRuntime 兜底。
type RuntimeConfig struct {
	Enabled          bool
	Model            string
	SessionDir       string
	Interval         time.Duration
	MaxSteps         int
	MaxTokens        int
	ContextMaxTokens int
	TurnTimeout      time.Duration
	ToolTimeout      time.Duration
	MaxToolResult    int
}

// RuntimeStatus 面板/状态端点的只读快照。
type RuntimeStatus struct {
	Enabled       bool   `json:"enabled"`
	Model         string `json:"model,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	LastTurn      int    `json:"last_turn,omitempty"`
	LastOutcome   string `json:"last_outcome,omitempty"`
	LastAt        string `json:"last_at,omitempty"`
	LastSteps     int    `json:"last_steps,omitempty"`
	LastToolCalls int    `json:"last_tool_calls,omitempty"`
	Sessions      int    `json:"sessions,omitempty"`
}

// TurnResult 一轮的调用方结果。
type TurnResult struct {
	SessionID string       `json:"session_id"`
	Turn      int          `json:"turn"`
	Steps     int          `json:"steps"`
	ToolCalls int          `json:"tool_calls"`
	Content   string       `json:"content,omitempty"`
	Usage     RuntimeUsage `json:"usage"`
	Outcome   string       `json:"outcome"`
	Error     string       `json:"error,omitempty"`
}

// RuntimeSessionSummary 会话目录里的一个文件摘要（面板列表用）。
type RuntimeSessionSummary struct {
	ID          string `json:"id"`
	Size        int64  `json:"size"`
	Events      int    `json:"events"`
	Turns       int    `json:"turns"`
	StartedAt   string `json:"started_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	LastOutcome string `json:"last_outcome,omitempty"`
}

// 事件 data 形状。
type runtimeSessionCreated struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	StartedAt string `json:"started_at"`
}

type runtimeTurnStart struct {
	Prompt string `json:"prompt"`
}

type runtimeStepStart struct {
	Step     int `json:"step"`
	Messages int `json:"messages"`
	Tools    int `json:"tools"`
}

type runtimeRequestHeader struct {
	Model     string `json:"model"`
	Messages  int    `json:"messages"`
	Tools     int    `json:"tools"`
	EstTokens int    `json:"est_tokens"`
	Compacted bool   `json:"compacted,omitempty"`
}

type runtimeAssistantMessage struct {
	Content   string            `json:"content,omitempty"`
	ToolCalls []RuntimeToolCall `json:"tool_calls,omitempty"`
}

type runtimeToolCallData struct {
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type runtimeToolResultData struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	Denied  bool   `json:"denied,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type runtimeCompactionData struct {
	Shadowed  int              `json:"shadowed"`
	Summary   string           `json:"summary"`
	Kept      []RuntimeMessage `json:"kept"`
	EstBefore int              `json:"est_before"`
	EstAfter  int              `json:"est_after"`
}

type runtimeTurnEnd struct {
	Outcome   string       `json:"outcome"`
	Steps     int          `json:"steps"`
	ToolCalls int          `json:"tool_calls"`
	Content   string       `json:"content,omitempty"`
	Usage     RuntimeUsage `json:"usage"`
	Error     string       `json:"error,omitempty"`
}
