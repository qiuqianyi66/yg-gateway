// runtime_loop.go turn/step 循环（ADR-029 D2）。
//
// 一轮 turn 的固定顺序：
//
//	turn/start -> user/message -> [step/start -> compact? -> request/header ->
//	assistant/message -> tool/call -> tool/result|tool/denied]* -> turn/end
//
// 每个 turn 必须有且只有一个 turn/end；异常路径也落终态。
package opsagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// Runtime 是运维 agent 的循环句柄。规则引擎 Agent 与它并行，互不替代。
type Runtime struct {
	cfg          RuntimeConfig
	session      *RuntimeSession
	model        RuntimeModel
	tools        *RuntimeToolRegistry
	policy       RuntimePolicy
	executor     *RuntimeToolExecutor
	systemPrompt string
	startedAt    time.Time

	runMu  sync.Mutex // 串行化 turn；scheduler/inbox 不会并发跑两轮
	mu     sync.Mutex
	cancel context.CancelFunc
	busy   bool
	closed bool
	last   *TurnResult
	hist   []TurnResult
	inbox  chan string
	idle   []chan struct{}
}

// NewRuntime 构造 runtime；model 为 nil 直接报错（不静默降级成假 agent）。
func NewRuntime(cfg RuntimeConfig, model RuntimeModel, tools *RuntimeToolRegistry, policy RuntimePolicy, systemPrompt string) (*Runtime, error) {
	if model == nil {
		return nil, errors.New("opsagent runtime: model 为空")
	}
	if cfg.SessionDir == "" {
		cfg.SessionDir = "data/opsagent-sessions"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Minute
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 12
	}
	if cfg.ContextMaxTokens <= 0 {
		cfg.ContextMaxTokens = 120000
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 4096
	}
	if cfg.TurnTimeout <= 0 {
		cfg.TurnTimeout = 5 * time.Minute
	}
	if cfg.ToolTimeout <= 0 {
		cfg.ToolTimeout = 30 * time.Second
	}
	if cfg.MaxToolResult <= 0 {
		cfg.MaxToolResult = 8 * 1024
	}
	if policy == nil {
		policy = RuntimeReadOnlyPolicy{}
	}
	sess, err := CreateRuntimeSession(cfg.SessionDir, "", time.Now())
	if err != nil {
		return nil, err
	}
	return &Runtime{
		cfg: cfg, session: sess, model: model, tools: tools, policy: policy,
		executor:     NewRuntimeToolExecutor(cfg.ToolTimeout, cfg.MaxToolResult),
		systemPrompt: systemPrompt, startedAt: time.Now(),
		inbox: make(chan string, 8),
	}, nil
}

// Turn 跑一轮。调用方可用 ctx 取消；runMu 保证同一 runtime 同一时刻只有一轮。
func (r *Runtime) Turn(ctx context.Context, prompt string) (res *TurnResult, err error) {
	if prompt == "" {
		prompt = defaultRuntimePrompt
	}
	r.runMu.Lock()
	defer r.runMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("opsagent runtime: 已关闭")
	}
	r.busy = true
	r.mu.Unlock()
	defer r.setBusy(false)

	turn := r.session.NextTurn()
	res = &TurnResult{SessionID: r.session.ID(), Turn: turn}
	ended := false
	finish := func(outcome string, e error) (*TurnResult, error) {
		ended = true
		res.Outcome = outcome
		if e != nil {
			res.Error = e.Error()
		}
		end := runtimeTurnEnd{
			Outcome: outcome, Steps: res.Steps, ToolCalls: res.ToolCalls,
			Content: res.Content, Usage: res.Usage, Error: res.Error,
		}
		if aerr := r.session.Append(RuntimeEventTurnEnd, turn, 0, end); aerr != nil {
			return res, fmt.Errorf("opsagent runtime: append turn/end: %w", aerr)
		}
		r.remember(res)
		return res, e
	}
	defer func() {
		if !ended {
			// 忽略：panic 路径下 defer 的收尾错误无法再上报，主 panic 优先；仍尽量落 turn/end。
			_, _ = finish(RuntimeOutcomeError, err)
		}
	}()

	if aerr := r.session.Append(RuntimeEventTurnStart, turn, 0, runtimeTurnStart{Prompt: prompt}); aerr != nil {
		return finish(RuntimeOutcomeError, aerr)
	}
	if aerr := r.session.Append(RuntimeEventUserMessage, turn, 0, runtimeUserMessage{Text: prompt}); aerr != nil {
		return finish(RuntimeOutcomeError, aerr)
	}
	runCtx, cancel := context.WithTimeout(ctx, r.cfg.TurnTimeout)
	defer cancel()
	r.mu.Lock()
	r.cancel = cancel
	r.mu.Unlock()

	for step := 1; step <= r.cfg.MaxSteps; step++ {
		res.Steps = step
		outcome, stepErr, done := r.runStep(runCtx, turn, step, res)
		if done {
			return finish(outcome, stepErr)
		}
	}
	return finish(RuntimeOutcomeMaxSteps, fmt.Errorf("超过 max_steps=%d", r.cfg.MaxSteps))
}

// runStep 执行一个 step。done=false 表示有工具调用、需要下一 step。
func (r *Runtime) runStep(ctx context.Context, turn, step int, res *TurnResult) (outcome string, err error, done bool) {
	if cerr := ctx.Err(); cerr != nil {
		return runtimeCancelOutcome(cerr), cerr, true
	}
	if aerr := r.session.Append(RuntimeEventStepStart, turn, step, runtimeStepStart{
		Step: step, Messages: len(r.session.Messages()), Tools: r.tools.Count(),
	}); aerr != nil {
		return RuntimeOutcomeError, aerr, true
	}
	msgs := r.session.Messages()
	comp := compactRuntimeMessages(msgs, r.cfg.ContextMaxTokens)
	if comp.Shadowed > 0 {
		if aerr := r.session.Append(RuntimeEventCompaction, turn, step, runtimeCompactionData{
			Shadowed: comp.Shadowed, Summary: comp.Summary, Kept: comp.Kept,
			EstBefore: comp.EstBefore, EstAfter: comp.EstAfter,
		}); aerr != nil {
			return RuntimeOutcomeError, aerr, true
		}
		msgs = comp.Kept
	}
	if estimateRuntimeTokens(msgs) > r.cfg.ContextMaxTokens {
		return RuntimeOutcomeError, fmt.Errorf("context 超预算 %d > %d，且无可压缩历史",
			estimateRuntimeTokens(msgs), r.cfg.ContextMaxTokens), true
	}
	modelMsgs := r.withSystem(msgs)
	if aerr := r.session.Append(RuntimeEventRequestHeader, turn, step, runtimeRequestHeader{
		Model: r.cfg.Model, Messages: len(modelMsgs), Tools: r.tools.Count(),
		EstTokens: estimateRuntimeTokens(modelMsgs), Compacted: comp.Shadowed > 0,
	}); aerr != nil {
		return RuntimeOutcomeError, aerr, true
	}
	resp, merr := r.model.Complete(ctx, RuntimeModelRequest{
		Model: r.cfg.Model, Messages: modelMsgs, Tools: r.tools.Schemas(), MaxTokens: r.cfg.MaxTokens,
	})
	if merr != nil {
		if cerr := ctx.Err(); cerr != nil {
			return runtimeCancelOutcome(cerr), cerr, true
		}
		return RuntimeOutcomeError, merr, true
	}
	res.Usage.PromptTokens += resp.Usage.PromptTokens
	res.Usage.CompletionTokens += resp.Usage.CompletionTokens
	if aerr := r.session.Append(RuntimeEventAssistantMessage, turn, step, runtimeAssistantMessage{
		Content: resp.Content, ToolCalls: resp.ToolCalls,
	}); aerr != nil {
		return RuntimeOutcomeError, aerr, true
	}
	if resp.Content != "" {
		res.Content = resp.Content
	}
	if len(resp.ToolCalls) == 0 {
		return RuntimeOutcomeSuccess, nil, true
	}
	for _, call := range resp.ToolCalls {
		res.ToolCalls++
		if aerr := r.session.Append(RuntimeEventToolCall, turn, step, runtimeToolCallData{
			CallID: call.ID, Name: call.Name, Arguments: call.Arguments,
		}); aerr != nil {
			return RuntimeOutcomeError, aerr, true
		}
		tres := r.executor.Execute(ctx, call, r.tools, r.policy)
		typ := RuntimeEventToolResult
		if tres.Denied {
			typ = RuntimeEventToolDenied
		}
		data := runtimeToolResultData{
			CallID: tres.CallID, Name: tres.Name, Content: tres.Content,
			IsError: tres.IsError, Denied: tres.Denied, Reason: tres.Reason,
		}
		if aerr := r.session.Append(typ, turn, step, data); aerr != nil {
			return RuntimeOutcomeError, aerr, true
		}
	}
	if cerr := ctx.Err(); cerr != nil {
		return runtimeCancelOutcome(cerr), cerr, true
	}
	return "", nil, false
}

func (r *Runtime) withSystem(msgs []RuntimeMessage) []RuntimeMessage {
	out := make([]RuntimeMessage, 0, len(msgs)+1)
	if r.systemPrompt != "" {
		out = append(out, RuntimeMessage{Role: "system", Content: r.systemPrompt})
	}
	out = append(out, msgs...)
	return out
}

func runtimeCancelOutcome(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return RuntimeOutcomeTimeout
	}
	return RuntimeOutcomeCanceled
}

func (r *Runtime) remember(res *TurnResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *res
	r.last = &cp
	r.hist = append(r.hist, cp)
	if len(r.hist) > 50 {
		r.hist = r.hist[len(r.hist)-50:]
	}
}

func (r *Runtime) setBusy(v bool) {
	r.mu.Lock()
	r.busy = v
	if !v && len(r.idle) > 0 {
		for _, ch := range r.idle {
			close(ch)
		}
		r.idle = nil
	}
	r.mu.Unlock()
}

// Cancel 取消当前 turn；没有在跑的 turn 时 no-op。
func (r *Runtime) Cancel() {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Followup 把下一轮提示放入 inbox；队列满返回错误，不阻塞调用方。
func (r *Runtime) Followup(prompt string) error {
	if prompt == "" {
		return errors.New("opsagent runtime: 空 followup")
	}
	select {
	case r.inbox <- prompt:
		return nil
	default:
		return errors.New("opsagent runtime: inbox 已满")
	}
}

// WhenIdle 返回一个在“当前空闲且 inbox 为空”时关闭的 channel。
func (r *Runtime) WhenIdle() <-chan struct{} {
	ch := make(chan struct{})
	r.mu.Lock()
	if !r.busy && len(r.inbox) == 0 {
		close(ch)
	} else {
		r.idle = append(r.idle, ch)
	}
	r.mu.Unlock()
	return ch
}

// Status 返回只读快照；Sessions 统计失败时保留 0（不把面板打红）。
func (r *Runtime) Status() RuntimeStatus {
	sts := RuntimeStatus{Enabled: true, Model: r.cfg.Model, SessionID: r.session.ID(),
		StartedAt: r.startedAt.UTC().Format(time.RFC3339Nano)}
	r.mu.Lock()
	if r.last != nil {
		sts.LastTurn = r.last.Turn
		sts.LastOutcome = r.last.Outcome
		sts.LastSteps = r.last.Steps
		sts.LastToolCalls = r.last.ToolCalls
		sts.LastAt = r.lastAt()
	}
	r.mu.Unlock()
	if sess, err := ListRuntimeSessions(r.cfg.SessionDir, 100); err == nil {
		sts.Sessions = len(sess)
	}
	return sts
}

func (r *Runtime) lastAt() string {
	// 事件里已有精确时间；这里用内存中的最后事件时间，避免额外状态。
	evs := r.session.Events()
	return evs[len(evs)-1].Time
}

// History 返回最近 turn 副本。
func (r *Runtime) History() []TurnResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TurnResult, len(r.hist))
	copy(out, r.hist)
	return out
}

// Sessions 返回会话摘要。
func (r *Runtime) Sessions(limit int) ([]RuntimeSessionSummary, error) {
	return ListRuntimeSessions(r.cfg.SessionDir, limit)
}

// Close 落 session/disposed 并关闭文件。先取消在跑的 turn，再等 runMu，
// 避免关闭文件时 Turn 还在 append（R9/持久化一致性）。
func (r *Runtime) Close() error {
	r.Cancel()
	r.runMu.Lock()
	defer r.runMu.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	return r.session.Close()
}

const defaultRuntimePrompt = `你是 yg-gateway 的运维 agent。只读诊断，不自动改配置。
先调用 gateway_status 获取池/健康面；再按异常选择 request_stats、recent_errors、deploy_reports。
输出四段：结论、证据（工具返回的关键数字）、风险、建议的下一步（只读验证优先）。
禁止建议或执行改配置、换号、重启、部署；这些只能作为人工建议。`

// logRuntime 统一前缀；避免各文件散落格式。
func logRuntime(format string, args ...any) {
	log.Printf("[opsagent-runtime] "+format, args...)
}
