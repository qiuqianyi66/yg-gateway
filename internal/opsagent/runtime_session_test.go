// runtime_session_test.go session 持久化与投影的牙齿（ADR-029 D4）。
package opsagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func jsonUnmarshalTest(raw []byte, v any) error { return json.Unmarshal(raw, v) }

func mustSession(t *testing.T, dir string) *RuntimeSession {
	t.Helper()
	s, err := CreateRuntimeSession(dir, "test-session", time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("CreateRuntimeSession: %v", err)
	}
	return s
}

func TestRuntimeSessionReopenAndProjection(t *testing.T) {
	dir := t.TempDir()
	s := mustSession(t, dir)
	if err := s.Append(RuntimeEventTurnStart, 1, 0, runtimeTurnStart{Prompt: "巡检"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(RuntimeEventUserMessage, 1, 0, runtimeUserMessage{Text: "巡检"}); err != nil {
		t.Fatal(err)
	}
	call := RuntimeToolCall{ID: "call-1", Name: "gateway_status", Arguments: []byte(`{}`)}
	if err := s.Append(RuntimeEventAssistantMessage, 1, 1, runtimeAssistantMessage{ToolCalls: []RuntimeToolCall{call}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(RuntimeEventToolResult, 1, 1, runtimeToolResultData{CallID: "call-1", Name: "gateway_status", Content: "healthy=2/4"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(RuntimeEventAssistantMessage, 1, 2, runtimeAssistantMessage{Content: "global 池不可用"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(RuntimeEventTurnEnd, 1, 0, runtimeTurnEnd{Outcome: RuntimeOutcomeSuccess, Steps: 2, ToolCalls: 1}); err != nil {
		t.Fatal(err)
	}
	_ = s.file.Close() // 模拟进程崩溃：没有 session/disposed

	re, err := OpenRuntimeSession(dir, "test-session")
	if err != nil {
		t.Fatalf("OpenRuntimeSession: %v", err)
	}
	defer re.Close()
	if len(re.Events()) != 7 { // created + 6
		t.Fatalf("events=%d, want 7", len(re.Events()))
	}
	msgs := re.Messages()
	if len(msgs) != 4 {
		t.Fatalf("messages=%d, want 4: %#v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("projection order wrong: %#v", msgs)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call-1" {
		t.Fatalf("tool projection wrong: %#v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content != "global 池不可用" {
		t.Fatalf("final assistant wrong: %#v", msgs[3])
	}
}

func TestRuntimeSessionDropsTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	s := mustSession(t, dir)
	if err := s.Append(RuntimeEventTurnStart, 1, 0, runtimeTurnStart{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = s.file.Close()
	path := filepath.Join(dir, "test-session.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq": 99, "type": "turn/start"`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	re, err := OpenRuntimeSession(dir, "test-session")
	if err != nil {
		t.Fatalf("OpenRuntimeSession: %v", err)
	}
	defer re.Close()
	for _, ev := range re.Events() {
		if ev.Seq == 99 {
			t.Fatalf("半行被当成事件: %#v", ev)
		}
	}
	if err := re.Append(RuntimeEventUserMessage, 1, 0, runtimeUserMessage{Text: "after reopen"}); err != nil {
		t.Fatalf("重开后追加失败: %v", err)
	}
	last := re.Events()[len(re.Events())-1]
	if last.Seq != 4 {
		t.Fatalf("seq=%d, want 4（created=1, turn/start=2, interrupted turn/end=3, user=4）", last.Seq)
	}
}

func TestRuntimeSessionRejectsPathTraversal(t *testing.T) {
	for _, id := range []string{"../x", "a/b", `a\b`, ".."} {
		if _, err := CreateRuntimeSession(t.TempDir(), id, time.Now()); err == nil {
			t.Fatalf("id %q 应被拒绝", id)
		}
	}
}

func TestRuntimeSessionClosesInterruptedTurn(t *testing.T) {
	dir := t.TempDir()
	s := mustSession(t, dir)
	if err := s.Append(RuntimeEventTurnStart, 1, 0, runtimeTurnStart{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	_ = s.file.Close()
	re, err := OpenRuntimeSession(dir, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	last := re.Events()[len(re.Events())-1]
	if last.Type != RuntimeEventTurnEnd {
		t.Fatalf("last type=%s, want turn/end", last.Type)
	}
	var d runtimeTurnEnd
	if err := jsonUnmarshalTest(last.Data, &d); err != nil {
		t.Fatal(err)
	}
	if d.Outcome != RuntimeOutcomeInterrupted {
		t.Fatalf("outcome=%s, want interrupted", d.Outcome)
	}
}

func TestRuntimeSessionList(t *testing.T) {
	dir := t.TempDir()
	s := mustSession(t, dir)
	if err := s.Append(RuntimeEventTurnStart, 1, 0, runtimeTurnStart{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(RuntimeEventTurnEnd, 1, 0, runtimeTurnEnd{Outcome: RuntimeOutcomeSuccess}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	list, err := ListRuntimeSessions(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "test-session" || list[0].Turns != 1 || list[0].LastOutcome != RuntimeOutcomeSuccess {
		t.Fatalf("list=%#v", list)
	}
	if !strings.Contains(list[0].StartedAt, "2026-11-26") {
		t.Fatalf("started_at=%q", list[0].StartedAt)
	}
}
