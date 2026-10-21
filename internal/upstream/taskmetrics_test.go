package upstream

import (
	"io"
	"strings"
	"testing"
)

// TestTaskMetricsRecordSnapshot 验证：首调用上下文只取首轮 prompt_tokens、
// 后续轮累加 total_tokens、turns 计轮次、未观测到 usage 的会话整体 unknown。
func TestTaskMetricsRecordSnapshot(t *testing.T) {
	tm := newTaskMetrics()
	defer tm.Stop()

	if _, ok := tm.Snapshot(); ok {
		t.Fatal("空采集器应 HasData=false（unknown）")
	}

	// 单会话两轮：首轮带 prompt_tokens，次轮只带 total_tokens。
	tm.Record("c1", map[string]any{"prompt_tokens": 100, "total_tokens": 150})
	tm.Record("c1", map[string]any{"total_tokens": 200})

	v, ok := tm.Snapshot()
	if !ok {
		t.Fatal("两轮后应 HasData=true")
	}
	if v.Tasks != 1 {
		t.Errorf("Tasks=%d want 1", v.Tasks)
	}
	if !v.FirstCallKnown || v.FirstCallPromptTokens != 100 {
		t.Errorf("首调用上下文: known=%v val=%v want known=true val=100", v.FirstCallKnown, v.FirstCallPromptTokens)
	}
	if v.TaskTotalTokens != 350 { // 150 + 200
		t.Errorf("TaskTotalTokens=%v want 350", v.TaskTotalTokens)
	}
	if v.TaskTurns != 2 {
		t.Errorf("TaskTurns=%v want 2", v.TaskTurns)
	}
}

// TestTaskMetricsUnknownFirstCall 验证：某会话从未观测到 prompt_tokens 时，
// FirstCallKnown=false（字段应省略，不是 0）。
func TestTaskMetricsUnknownFirstCall(t *testing.T) {
	tm := newTaskMetrics()
	defer tm.Stop()
	tm.Record("cx", map[string]any{"total_tokens": 300})
	v, ok := tm.Snapshot()
	if !ok || v.Tasks != 1 {
		t.Fatalf("快照异常: %+v", v)
	}
	if v.FirstCallKnown {
		t.Error("从未观测 prompt_tokens → FirstCallKnown 应为 false（unknown）")
	}
	if v.TaskTotalTokens != 300 || v.TaskTurns != 1 {
		t.Errorf("汇总异常: %+v", v)
	}
}

// TestObserveUsageRecordsOnClose 验证包装器：透传字节不被改写，且在 Close 时把
// 最终 usage 记入采集器。
func TestObserveUsageRecordsOnClose(t *testing.T) {
	TaskMetrics.Reset()
	const conv = "obs-conv"
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n" +
		"data: {\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":30,\"total_tokens\":150}}\n" +
		"data: [DONE]\n"

	rc := observeUsage(io.NopCloser(strings.NewReader(sse)), conv)
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if string(got) != sse {
		t.Fatalf("包装器改写了流字节: %q", string(got))
	}
	v, ok := TaskMetrics.Snapshot()
	if !ok {
		t.Fatal("应记录到 usage")
	}
	if v.FirstCallPromptTokens != 120 || v.TaskTotalTokens != 150 || v.TaskTurns != 1 {
		t.Errorf("快照: %+v", v)
	}
}

// TestObserveUsageNonSSE 验证非 SSE 单条 JSON 响应的兜底采集。
func TestObserveUsageNonSSE(t *testing.T) {
	TaskMetrics.Reset()
	body := `{"id":"x","usage":{"prompt_tokens":50,"total_tokens":99}}`
	rc := observeUsage(io.NopCloser(strings.NewReader(body)), "obs-non-sse")
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	v, ok := TaskMetrics.Snapshot()
	if !ok {
		t.Fatal("应记录到 usage")
	}
	if v.FirstCallPromptTokens != 50 || v.TaskTotalTokens != 99 {
		t.Errorf("快照: %+v", v)
	}
}

// TestObserveUsageEmptyConv 验证空 conversationID 时直接透传、不记录（记 unknown）。
func TestObserveUsageEmptyConv(t *testing.T) {
	TaskMetrics.Reset()
	sse := "data: {\"usage\":{\"prompt_tokens\":1,\"total_tokens\":2}}\n"
	rc := observeUsage(io.NopCloser(strings.NewReader(sse)), "")
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := TaskMetrics.Snapshot(); ok {
		t.Error("空 convID 不应记录")
	}
}
