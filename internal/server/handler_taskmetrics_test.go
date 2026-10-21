package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/upstream"
)

// TestStatusOmitsTaskMetricsWhenUnknown 验证：从未观测到 usage 时，/status 不输出
// task_metrics 段（未知=省略，不是 null / 不是 0）。
func TestStatusOmitsTaskMetricsWhenUnknown(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "nick", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})

	upstream.TaskMetrics.Reset()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	if strings.Contains(body, "task_metrics") {
		t.Errorf("无数据时应省略 task_metrics，实际 body=%s", body)
	}
}

// TestStatusExposesTaskMetrics 验证：观测到多轮 usage 后，/status 透出
// task_total_tokens=各轮之和、first_call_prompt_tokens=首轮 prompt、task_turns、tasks。
func TestStatusExposesTaskMetrics(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1", Nickname: "nick", AccessToken: "at", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})

	upstream.TaskMetrics.Reset()
	upstream.TaskMetrics.Record("conv-smoke", map[string]any{"prompt_tokens": 100, "total_tokens": 150})
	upstream.TaskMetrics.Record("conv-smoke", map[string]any{"prompt_tokens": 80, "total_tokens": 200})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != 200 {
		t.Fatalf("code=%d", rec.Code)
	}
	var statusBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("status 非 JSON: %v", err)
	}
	tmRaw, ok := statusBody["task_metrics"]
	if !ok {
		t.Fatalf("task_metrics 缺失，body=%s", rec.Body.String())
	}
	tm, ok := tmRaw.(map[string]any)
	if !ok {
		t.Fatalf("task_metrics 形态异常: %T", tmRaw)
	}
	if tm["task_total_tokens"] != float64(350) {
		t.Errorf("task_total_tokens=%v want 350", tm["task_total_tokens"])
	}
	if tm["first_call_prompt_tokens"] != float64(100) {
		t.Errorf("first_call_prompt_tokens=%v want 100", tm["first_call_prompt_tokens"])
	}
	if tm["task_turns"] != float64(2) {
		t.Errorf("task_turns=%v want 2", tm["task_turns"])
	}
	if tm["tasks"] != float64(1) {
		t.Errorf("tasks=%v want 1", tm["tasks"])
	}
}
