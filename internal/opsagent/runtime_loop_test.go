// runtime_loop_test.go turn/step 的端到端契约（fake model，零付费）。
package opsagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRuntimeModel struct {
	mu        sync.Mutex
	responses []RuntimeModelResponse
	repeat    *RuntimeModelResponse
	err       error
	block     bool
	once      sync.Once
	started   chan struct{}
	calls     int
	onCall    func()
	panicVal  any
}

func (f *fakeRuntimeModel) Complete(ctx context.Context, _ RuntimeModelRequest) (RuntimeModelResponse, error) {
	f.mu.Lock()
	f.calls++
	onCall := f.onCall
	panicVal := f.panicVal
	f.mu.Unlock()
	if panicVal != nil {
		panic(panicVal)
	}
	if onCall != nil {
		onCall()
	}
	if f.block {
		if f.started != nil {
			f.once.Do(func() { close(f.started) })
		}
		<-ctx.Done()
		return RuntimeModelResponse{}, ctx.Err()
	}
	if f.err != nil {
		return RuntimeModelResponse{}, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.responses) > 0 {
		resp := f.responses[0]
		f.responses = f.responses[1:]
		return resp, nil
	}
	if f.repeat != nil {
		return *f.repeat, nil
	}
	return RuntimeModelResponse{}, errors.New("fake model: no response")
}

func newRuntimeForTest(t *testing.T, model RuntimeModel, tools *RuntimeToolRegistry, cfg RuntimeConfig) *Runtime {
	t.Helper()
	if cfg.SessionDir == "" {
		cfg.SessionDir = t.TempDir()
	}
	if cfg.Model == "" {
		cfg.Model = "fake-free"
	}
	if cfg.Interval == 0 {
		cfg.Interval = time.Hour
	}
	rt, err := NewRuntime(cfg, model, tools, RuntimeReadOnlyPolicy{}, "test system prompt")
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func TestRuntimeTurnToolThenFinal(t *testing.T) {
	reg, err := NewRuntimeToolRegistry(testTool("gateway_status", true, func(context.Context, json.RawMessage) (string, error) {
		return `{"healthy":2,"total":4}`, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeRuntimeModel{responses: []RuntimeModelResponse{
		{ToolCalls: []RuntimeToolCall{{ID: "c1", Name: "gateway_status", Arguments: json.RawMessage(`{}`)}}},
		{Content: "结论：global 池不可用"},
	}}
	rt := newRuntimeForTest(t, model, reg, RuntimeConfig{MaxSteps: 4, ContextMaxTokens: 1000})

	res, err := rt.Turn(context.Background(), "巡检")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if res.Outcome != RuntimeOutcomeSuccess || res.Steps != 2 || res.ToolCalls != 1 {
		t.Fatalf("res=%#v", res)
	}
	if !strings.Contains(res.Content, "global") {
		t.Fatalf("content=%q", res.Content)
	}
	msgs := rt.session.Messages()
	if len(msgs) != 4 {
		t.Fatalf("messages=%d: %#v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[2].Role != "tool" {
		t.Fatalf("tool 消息投影错: %#v", msgs)
	}
	if got := rt.Status().LastOutcome; got != RuntimeOutcomeSuccess {
		t.Fatalf("status outcome=%q", got)
	}
	if len(rt.History()) != 1 {
		t.Fatalf("history=%d", len(rt.History()))
	}
}

func TestRuntimeDeniedWriteZeroCall(t *testing.T) {
	var calls atomic.Int32
	writeTool := testTool("set_config", false, func(context.Context, json.RawMessage) (string, error) {
		calls.Add(1)
		return "changed", nil
	})
	reg, err := NewRuntimeToolRegistry(writeTool)
	if err != nil {
		t.Fatal(err)
	}
	model := &fakeRuntimeModel{responses: []RuntimeModelResponse{
		{ToolCalls: []RuntimeToolCall{{ID: "w1", Name: "set_config", Arguments: json.RawMessage(`{"x":1}`)}}},
		{Content: "已拒绝写操作"},
	}}
	rt := newRuntimeForTest(t, model, reg, RuntimeConfig{MaxSteps: 3})
	res, err := rt.Turn(context.Background(), "尝试改配置")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != RuntimeOutcomeSuccess {
		t.Fatalf("res=%#v", res)
	}
	if calls.Load() != 0 {
		t.Fatalf("写工具被调用 %d 次", calls.Load())
	}
	var denied int
	for _, ev := range rt.session.Events() {
		if ev.Type == RuntimeEventToolDenied {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("tool/denied=%d, want 1", denied)
	}
}

func TestRuntimeMaxSteps(t *testing.T) {
	reg, err := NewRuntimeToolRegistry(testTool("gateway_status", true, func(context.Context, json.RawMessage) (string, error) {
		return "ok", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	repeat := RuntimeModelResponse{ToolCalls: []RuntimeToolCall{{ID: "c", Name: "gateway_status", Arguments: json.RawMessage(`{}`)}}}
	rt := newRuntimeForTest(t, &fakeRuntimeModel{repeat: &repeat}, reg, RuntimeConfig{MaxSteps: 2})
	res, err := rt.Turn(context.Background(), "循环")
	if err == nil || res.Outcome != RuntimeOutcomeMaxSteps {
		t.Fatalf("res=%#v err=%v", res, err)
	}
}

func TestRuntimeModelError(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("gateway_status", true, func(context.Context, json.RawMessage) (string, error) { return "ok", nil }))
	rt := newRuntimeForTest(t, &fakeRuntimeModel{err: errors.New("upstream 503")}, reg, RuntimeConfig{})
	res, err := rt.Turn(context.Background(), "巡检")
	if err == nil || res.Outcome != RuntimeOutcomeError {
		t.Fatalf("res=%#v err=%v", res, err)
	}
	if !strings.Contains(res.Error, "503") {
		t.Fatalf("error=%q", res.Error)
	}
	last := rt.session.Events()[len(rt.session.Events())-1]
	if last.Type != RuntimeEventTurnEnd {
		t.Fatalf("last=%s, want turn/end", last.Type)
	}
}

func TestRuntimeCancel(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("gateway_status", true, func(context.Context, json.RawMessage) (string, error) { return "ok", nil }))
	model := &fakeRuntimeModel{block: true, started: make(chan struct{})}
	rt := newRuntimeForTest(t, model, reg, RuntimeConfig{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = rt.Turn(context.Background(), "巡检")
	}()
	<-model.started
	rt.Cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel 后 Turn 未退出")
	}
	if got := rt.Status().LastOutcome; got != RuntimeOutcomeCanceled {
		t.Fatalf("outcome=%q, want canceled", got)
	}
}

func TestRuntimeTimeoutOutcome(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("gateway_status", true, func(context.Context, json.RawMessage) (string, error) { return "ok", nil }))
	model := &fakeRuntimeModel{block: true, started: make(chan struct{})}
	rt := newRuntimeForTest(t, model, reg, RuntimeConfig{TurnTimeout: 50 * time.Millisecond})
	res, err := rt.Turn(context.Background(), "巡检")
	if err == nil || res.Outcome != RuntimeOutcomeTimeout {
		t.Fatalf("res=%#v err=%v", res, err)
	}
}

func TestNewRuntimeRequiresModel(t *testing.T) {
	if _, err := NewRuntime(RuntimeConfig{SessionDir: t.TempDir()}, nil, nil, nil, ""); err == nil {
		t.Fatal("model 为空应报错")
	}
}
