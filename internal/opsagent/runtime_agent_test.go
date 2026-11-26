// runtime_agent_test.go 调度/句柄方法与故障路径。
package opsagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeRunFollowupAndWhenIdle(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("gateway_status", true, func(context.Context, json.RawMessage) (string, error) {
		return "ok", nil
	}))
	rt := newRuntimeForTest(t, &fakeRuntimeModel{responses: []RuntimeModelResponse{{Content: "done"}}}, reg, RuntimeConfig{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.Run(ctx)
	if err := rt.Followup("手动巡检"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rt.WhenIdle():
	case <-time.After(2 * time.Second):
		t.Fatal("followup 后未回到 idle")
	}
	if len(rt.History()) != 1 || rt.History()[0].Content != "done" {
		t.Fatalf("history=%#v", rt.History())
	}
}

func TestRuntimeFollowupFull(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	for i := 0; i < cap(rt.inbox); i++ {
		if err := rt.Followup("x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := rt.Followup("overflow"); err == nil {
		t.Fatal("inbox 满应报错")
	}
}

func TestRuntimeWhenIdleBusyThenClosed(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("gateway_status", true, func(context.Context, json.RawMessage) (string, error) { return "ok", nil }))
	model := &fakeRuntimeModel{block: true, started: make(chan struct{})}
	rt := newRuntimeForTest(t, model, reg, RuntimeConfig{TurnTimeout: 2 * time.Second})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = rt.Turn(context.Background(), "x") }()
	<-model.started
	ch := rt.WhenIdle()
	select {
	case <-ch:
		t.Fatal("busy 时 WhenIdle 不应关闭")
	default:
	}
	rt.Cancel()
	<-done
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("turn 结束后 WhenIdle 未关闭")
	}
}

func TestRuntimeRunOneBusyAndModelError(t *testing.T) {
	model := &fakeRuntimeModel{block: true, started: make(chan struct{})}
	rt := newRuntimeForTest(t, model, nil, RuntimeConfig{TurnTimeout: 2 * time.Second})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = rt.Turn(context.Background(), "x") }()
	<-model.started
	rt.runOne(context.Background(), "should skip")
	rt.Cancel()
	<-done

	model2 := &fakeRuntimeModel{err: errors.New("model down")}
	rt2 := newRuntimeForTest(t, model2, nil, RuntimeConfig{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt2.Run(ctx)
	if err := rt2.Followup("x"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rt2.WhenIdle():
	case <-time.After(2 * time.Second):
		t.Fatal("model error 后未 idle")
	}
	if len(rt2.History()) != 1 || rt2.History()[0].Outcome != RuntimeOutcomeError {
		t.Fatalf("history=%#v", rt2.History())
	}
}

func TestRuntimeNewDefaults(t *testing.T) {
	rt, err := NewRuntime(RuntimeConfig{}, &fakeRuntimeModel{}, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.cfg.MaxSteps != 12 || rt.cfg.ContextMaxTokens != 120000 || rt.cfg.MaxTokens != 4096 {
		t.Fatalf("cfg=%#v", rt.cfg)
	}
	if rt.cfg.Interval != 10*time.Minute || rt.cfg.TurnTimeout != 5*time.Minute || rt.cfg.ToolTimeout != 30*time.Second {
		t.Fatalf("durations=%#v", rt.cfg)
	}
}

func TestRuntimeToolCallErrorsContinue(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(
		testTool("fail", true, func(context.Context, json.RawMessage) (string, error) { return "", errors.New("tool down") }),
		testTool("panic", true, func(context.Context, json.RawMessage) (string, error) { panic("boom") }),
	)
	model := &fakeRuntimeModel{responses: []RuntimeModelResponse{
		{ToolCalls: []RuntimeToolCall{
			{ID: "u", Name: "unknown", Arguments: json.RawMessage(`{}`)},
			{ID: "f", Name: "fail", Arguments: json.RawMessage(`{}`)},
			{ID: "p", Name: "panic", Arguments: json.RawMessage(`{}`)},
		}},
		{Content: "done"},
	}}
	rt := newRuntimeForTest(t, model, reg, RuntimeConfig{MaxSteps: 3})
	res, err := rt.Turn(context.Background(), "x")
	if err != nil || res.Outcome != RuntimeOutcomeSuccess || res.ToolCalls != 3 {
		t.Fatalf("res=%#v err=%v", res, err)
	}
	var results int
	for _, ev := range rt.session.Events() {
		if ev.Type == RuntimeEventToolResult {
			results++
		}
	}
	if results != 3 {
		t.Fatalf("tool/result=%d", results)
	}
}

func TestRuntimeSessionsAndPath(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{responses: []RuntimeModelResponse{{Content: "ok"}}}, nil, RuntimeConfig{})
	if _, err := rt.Turn(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(rt.session.Path(), ".jsonl") {
		t.Fatalf("path=%s", rt.session.Path())
	}
	list, err := rt.Sessions(5)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%#v err=%v", list, err)
	}
}

func TestRuntimeCloseWhileBusy(t *testing.T) {
	model := &fakeRuntimeModel{block: true, started: make(chan struct{})}
	rt := newRuntimeForTest(t, model, nil, RuntimeConfig{TurnTimeout: 2 * time.Second})
	done := make(chan struct{})
	go func() { defer close(done); _, _ = rt.Turn(context.Background(), "x") }()
	<-model.started
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close 未取消在跑的 turn")
	}
}

func TestRuntimeClosedTurnRejected(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Turn(context.Background(), "x"); err == nil {
		t.Fatal("关闭后 Turn 应报错")
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("重复 Close 应幂等: %v", err)
	}
}

func TestRuntimeSessionCreateMkdirError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateRuntimeSession(file, "id", time.Now()); err == nil {
		t.Fatal("dir 是文件时应报错")
	}
}

func TestRuntimeSessionOpenErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := OpenRuntimeSession(dir, "missing"); err == nil {
		t.Fatal("缺失 session 应报错")
	}
	s := mustSession(t, dir)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRuntimeSession(dir, "test-session"); err == nil {
		t.Fatal("disposed session 应拒绝重开")
	}
}

func TestRuntimeSessionCorruptAndGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	lines := []string{
		`{"seq":1,"type":"session/created","data":{}}`,
		`not json`,
		`{"seq":3,"type":"turn/end","data":{}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRuntimeSession(dir, "bad"); err == nil {
		t.Fatal("中间损坏行应报错")
	}
	lines = []string{
		`{"seq":1,"type":"session/created","data":{}}`,
		`{"seq":3,"type":"turn/end","data":{}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRuntimeSession(dir, "bad"); err == nil {
		t.Fatal("seq 跳号应报错")
	}
}

func TestRuntimeSessionAppendClosed(t *testing.T) {
	s := mustSession(t, t.TempDir())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(RuntimeEventUserMessage, 1, 0, runtimeUserMessage{Text: "x"}); err == nil {
		t.Fatal("关闭后 Append 应报错")
	}
}

func TestRuntimeFollowupEmpty(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	if err := rt.Followup(""); err == nil {
		t.Fatal("空 followup 应报错")
	}
}

func TestRuntimeRunRequeuesFollowupWhileBusy(t *testing.T) {
	model := &fakeRuntimeModel{block: true, started: make(chan struct{})}
	rt := newRuntimeForTest(t, model, nil, RuntimeConfig{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.Run(ctx)
	if err := rt.Followup("first"); err != nil {
		t.Fatal(err)
	}
	<-model.started
	if err := rt.Followup("second"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // 让 Run 在 busy 时消费第二封并回队
	model.mu.Lock()
	model.block = false
	model.responses = []RuntimeModelResponse{{Content: "second done"}}
	model.mu.Unlock()
	rt.Cancel()
	deadline := time.Now().Add(2 * time.Second)
	for len(rt.History()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(rt.History()) < 2 {
		t.Fatalf("followup 被丢弃: history=%#v", rt.History())
	}
}

func TestRuntimeRunTicker(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{repeat: &RuntimeModelResponse{Content: "tick"}}, nil, RuntimeConfig{Interval: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go rt.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for len(rt.History()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if len(rt.History()) == 0 {
		t.Fatal("ticker 未触发巡检")
	}
}

func TestRuntimeWhenIdleInitiallyClosed(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	select {
	case <-rt.WhenIdle():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("初始 idle 未关闭")
	}
}

func TestRuntimeSessionMarshalError(t *testing.T) {
	s := mustSession(t, t.TempDir())
	defer s.Close()
	if err := s.Append("x", 0, 0, make(chan int)); err == nil {
		t.Fatal("不可序列化 data 应报错")
	}
}

func TestRuntimeCompactionSingleNoopAndSummaryBranches(t *testing.T) {
	res := compactRuntimeMessages([]RuntimeMessage{{Role: "user", Content: strings.Repeat("超", 100)}}, 5)
	if res.Shadowed != 0 {
		t.Fatalf("单条消息不应制造影子: %#v", res)
	}
	summary := runtimeCompactionSummary([]RuntimeMessage{
		{Role: "assistant", ToolCalls: []RuntimeToolCall{{ID: "c", Name: "x"}}, Content: ""},
		{Role: "user", Content: ""},
		{Role: "user", Content: "a"}, {Role: "user", Content: "b"},
		{Role: "user", Content: "c"}, {Role: "user", Content: "d"},
		{Role: "user", Content: "e"}, {Role: "user", Content: "f"},
	})
	if !strings.Contains(summary, "tool call") || !strings.Contains(summary, "(empty)") || !strings.Contains(summary, "more") {
		t.Fatalf("summary=%q", summary)
	}
}

func TestRuntimeCompactionToolFallback(t *testing.T) {
	res := compactRuntimeMessages([]RuntimeMessage{
		{Role: "user", Content: strings.Repeat("old", 200)},
		{Role: "assistant", ToolCalls: []RuntimeToolCall{{ID: "c", Name: "x", Arguments: json.RawMessage(`"` + strings.Repeat("a", 400) + `"`)}}},
		{Role: "tool", ToolCallID: "c", Content: "ok"},
		{Role: "user", Content: "now"},
	}, 40)
	if res.Shadowed == 0 {
		t.Fatalf("未压缩: %#v", res)
	}
	for i, m := range res.Kept {
		if m.Role == "tool" && (i == 0 || res.Kept[i-1].Role != "assistant") {
			t.Fatalf("tool 未配对: %#v", res.Kept)
		}
	}
}

func TestRuntimeSessionDuplicateCreateAndWriteError(t *testing.T) {
	dir := t.TempDir()
	s := mustSession(t, dir)
	if _, err := CreateRuntimeSession(dir, "test-session", time.Now()); err == nil {
		t.Fatal("重复 id 应报错")
	}
	_ = s.file.Close()
	if err := s.Append(RuntimeEventUserMessage, 1, 0, runtimeUserMessage{Text: "x"}); err == nil {
		t.Fatal("文件关闭后 Append 应报错")
	}
}

func TestLegacyOpsAgentHelpers(t *testing.T) {
	LogSink{}.Suggest(Suggestion{Rule: "r", UID: "1234567890", Title: "t"})
	discardSink{}.Suggest(Suggestion{})
	a := New(Config{}, Observers{}, nil, nil)
	interval, frac := a.ConfigSnapshot()
	if interval != 10*time.Minute || frac != 0.5 {
		t.Fatalf("interval=%s frac=%v", interval, frac)
	}
}

func TestRuntimeRunOneClosedAndTurnAppendError(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	rt.runOne(context.Background(), "closed") // res=nil 分支

	rt2 := newRuntimeForTest(t, &fakeRuntimeModel{responses: []RuntimeModelResponse{{Content: "x"}}}, nil, RuntimeConfig{})
	_ = rt2.session.file.Close() // 模拟 fs 故障：turn/start 写入失败
	if _, err := rt2.Turn(context.Background(), "x"); err == nil {
		t.Fatal("session 文件关闭后 Turn 应报错")
	}
}

func TestRuntimeTurnEmptyPromptCanceledAndAppendError(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{responses: []RuntimeModelResponse{{Content: "ok"}}}, nil, RuntimeConfig{})
	if _, err := rt.Turn(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	rt2 := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res, err := rt2.Turn(ctx, "x"); err == nil || res.Outcome != RuntimeOutcomeCanceled {
		t.Fatalf("res=%#v err=%v", res, err)
	}

	rt3 := newRuntimeForTest(t, &fakeRuntimeModel{responses: []RuntimeModelResponse{{Content: "x"}}}, nil, RuntimeConfig{})
	rt3.model.(*fakeRuntimeModel).onCall = func() { _ = rt3.session.file.Close() }
	if _, err := rt3.Turn(context.Background(), "x"); err == nil {
		t.Fatal("assistant 事件写入失败应报错")
	}
}

func TestRuntimeRememberCap(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	for i := 0; i < 55; i++ {
		rt.remember(&TurnResult{Turn: i, Outcome: RuntimeOutcomeSuccess})
	}
	if len(rt.History()) != 50 || rt.History()[49].Turn != 54 {
		t.Fatalf("history len=%d last=%d", len(rt.History()), rt.History()[len(rt.History())-1].Turn)
	}
}

func TestRuntimeNewRuntimeSessionErrorAndOpenMissing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRuntime(RuntimeConfig{SessionDir: file}, &fakeRuntimeModel{}, nil, nil, ""); err == nil {
		t.Fatal("SessionDir 是文件时应报错")
	}
	if _, err := OpenRuntimeSession(t.TempDir(), "missing"); err == nil {
		t.Fatal("打开不存在会话应报错")
	}
}

func TestRuntimeSessionMessagesMalformedAndListLimit(t *testing.T) {
	dir := t.TempDir()
	raw := strings.Join([]string{
		`{"seq":1,"type":"user/message","data":"bad"}`,
		`{"seq":2,"type":"assistant/message","data":123}`,
		`{"seq":3,"type":"tool/result","data":[]}`,
		`{"seq":4,"type":"tool/denied","data":true}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "malformed.jsonl"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := OpenRuntimeSession(dir, "malformed")
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Messages()) != 0 {
		t.Fatalf("坏 data 不应投影消息: %#v", sess.Messages())
	}
	_ = sess.Close()

	for _, id := range []string{"a", "b"} {
		s, err := CreateRuntimeSession(dir, id, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
	list, err := ListRuntimeSessions(dir, 1)
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%#v err=%v", list, err)
	}
}

func TestRuntimeSessionMessagesErrorAndDenied(t *testing.T) {
	dir := t.TempDir()
	raw := strings.Join([]string{
		`{"seq":1,"type":"session/created","data":{"id":"x","started_at":"2026-11-26T00:00:00Z"}}`,
		`{"seq":2,"type":"tool/result","data":{"call_id":"c","name":"x","content":"boom","is_error":true}}`,
		`{"seq":3,"type":"tool/denied","data":{"call_id":"d","name":"y","reason":"read-only"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "effects.jsonl"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := OpenRuntimeSession(dir, "effects")
	if err != nil {
		t.Fatal(err)
	}
	msgs := sess.Messages()
	if len(msgs) != 2 || !strings.Contains(msgs[0].Content, "Error:") || !strings.Contains(msgs[1].Content, "Denied:") {
		t.Fatalf("msgs=%#v", msgs)
	}
	_ = sess.Close()
}

func TestRuntimeSessionCloseClosesOpenTurn(t *testing.T) {
	s := mustSession(t, t.TempDir())
	if err := s.Append(RuntimeEventTurnStart, 1, 0, runtimeTurnStart{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var interrupted bool
	for _, ev := range s.Events() {
		if ev.Type == RuntimeEventTurnEnd {
			var end runtimeTurnEnd
			if json.Unmarshal(ev.Data, &end) == nil && end.Outcome == RuntimeOutcomeInterrupted {
				interrupted = true
			}
		}
	}
	if !interrupted {
		t.Fatalf("未补 interrupted turn/end: %#v", s.Events())
	}
}

func TestRuntimeTurnPanicStillEnds(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{panicVal: "model panic"}, nil, RuntimeConfig{})
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("模型 panic 应向上传播")
			}
		}()
		_, _ = rt.Turn(context.Background(), "x")
	}()
	if len(rt.History()) != 1 || rt.History()[0].Outcome != RuntimeOutcomeError {
		t.Fatalf("panic 后未落 turn/end: %#v", rt.History())
	}
}

func TestRuntimeStepStartAppendError(t *testing.T) {
	rt := newRuntimeForTest(t, &fakeRuntimeModel{}, nil, RuntimeConfig{})
	turn := rt.session.NextTurn()
	if err := rt.session.Append(RuntimeEventTurnStart, turn, 0, runtimeTurnStart{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = rt.session.file.Close()
	if _, _, done := rt.runStep(context.Background(), turn, 1, &TurnResult{Turn: turn}); !done {
		t.Fatal("step/start 写入失败应终止")
	}
}

func TestRuntimeSessionCompactionBadDataAndCloseInterruptedError(t *testing.T) {
	dir := t.TempDir()
	raw := strings.Join([]string{
		`{"seq":1,"type":"compaction","data":"bad"}`,
		`{"seq":2,"type":"user/message","data":{"text":"after-bad"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "badcompact.jsonl"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := OpenRuntimeSession(dir, "badcompact")
	if err != nil {
		t.Fatal(err)
	}
	if msgs := sess.Messages(); len(msgs) != 1 || msgs[0].Content != "after-bad" {
		t.Fatalf("msgs=%#v", msgs)
	}
	_ = sess.Close()

	s := mustSession(t, t.TempDir())
	if err := s.Append(RuntimeEventTurnStart, 1, 0, runtimeTurnStart{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = s.file.Close()
	if err := s.closeInterruptedTurns(); err == nil {
		t.Fatal("文件关闭后补终态应报错")
	}
}

func TestRuntimeSessionMissingType(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.jsonl"), []byte(`{"seq":1,"type":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRuntimeSession(dir, "bad"); err == nil {
		t.Fatal("缺 type 应报错")
	}
}

func TestRuntimeSessionListMissingAndInvalid(t *testing.T) {
	if got, err := ListRuntimeSessions(filepath.Join(t.TempDir(), "nope"), 5); err != nil || got != nil {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad name.jsonl"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.jsonl"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListRuntimeSessions(dir, 5); err != nil {
		t.Fatalf("list 不应因坏文件报错: %v", err)
	}
}

func TestRuntimeCloseWritesInterruptedTurn(t *testing.T) {
	dir := t.TempDir()
	s := mustSession(t, dir)
	if err := s.Append(RuntimeEventTurnStart, 1, 0, runtimeTurnStart{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	re, err := OpenRuntimeSession(dir, "test-session")
	if err == nil {
		_ = re.Close()
		t.Fatal("disposed session 应拒绝重开")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "test-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), RuntimeOutcomeInterrupted) {
		t.Fatalf("关闭时未补 interrupted: %s", raw)
	}
}

func TestRuntimeOverBudget(t *testing.T) {
	model := &fakeRuntimeModel{responses: []RuntimeModelResponse{{Content: "should not call"}}}
	rt := newRuntimeForTest(t, model, nil, RuntimeConfig{ContextMaxTokens: 4, MaxSteps: 2})
	res, err := rt.Turn(context.Background(), strings.Repeat("超", 20))
	if err == nil || res.Outcome != RuntimeOutcomeError || !strings.Contains(res.Error, "超预算") {
		t.Fatalf("res=%#v err=%v", res, err)
	}
	if model.calls != 0 {
		t.Fatalf("超预算仍调了模型 %d 次", model.calls)
	}
}

func TestRuntimeToolPolicyPanic(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("ok", true, func(context.Context, json.RawMessage) (string, error) { return "x", nil }))
	ex := NewRuntimeToolExecutor(time.Second, 1024)
	res := ex.Execute(context.Background(), RuntimeToolCall{ID: "c", Name: "ok"}, reg, panicPolicy{})
	if !res.Denied || !strings.Contains(res.Reason, "policy panic") {
		t.Fatalf("res=%#v", res)
	}
}

type panicPolicy struct{}

func (panicPolicy) Authorize(context.Context, RuntimeToolSpec, json.RawMessage) (bool, string) {
	panic("boom")
}

func TestRuntimeToolUTF8Truncate(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("cn", true, func(context.Context, json.RawMessage) (string, error) {
		return "中文内容", nil
	}))
	res := NewRuntimeToolExecutor(time.Second, 2).Execute(context.Background(), RuntimeToolCall{ID: "c", Name: "cn"}, reg, nil)
	if !strings.HasSuffix(res.Content, "...[truncated]") || !json.Valid([]byte(`"`+strings.TrimSuffix(res.Content, "\n...[truncated]")+`"`)) {
		t.Fatalf("content=%q", res.Content)
	}
}
