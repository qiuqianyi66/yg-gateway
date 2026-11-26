package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"yg-gateway/internal/pool"
)

// TestStatusGuardrailBlockAlwaysVisible 覆盖 2026-11-26 的可观测性修复：
// 护栏装配后 /status 必须恒有 guardrail_stats，块内 samples=0 + warn/max 阈值可读，
// 用来区分「没命中」与「护栏没装 / 代码没生效」。此前整块省略，实弹时误判过一次。
func TestStatusGuardrailBlockAlwaysVisible(t *testing.T) {
	g := NewGuardrail()
	g.SetContextWarnTokens(500000)
	g.SetContextMaxTokens(1000000)
	g.SetSessionRPM(20)
	h := NewHandler(Config{Pool: pool.New(""), Guardrail: g})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Guardrail *struct {
			Samples       int     `json:"samples"`
			WarnTokens    int64   `json:"warn_tokens"`
			MaxTokens     int64   `json:"max_tokens"`
			SessionRPM    int     `json:"session_rpm"`
			WarnHits      int64   `json:"warn_hits"`
			EstVsUpstream *struct {
				N      int     `json:"n"`
				Median float64 `json:"median"`
			} `json:"est_vs_upstream"`
		} `json:"guardrail_stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Guardrail == nil {
		t.Fatal("零样本时 guardrail_stats 不应整块省略")
	}
	if body.Guardrail.Samples != 0 || body.Guardrail.WarnHits != 0 || body.Guardrail.EstVsUpstream != nil {
		t.Errorf("空态字段错误: %+v", body.Guardrail)
	}
	if body.Guardrail.WarnTokens != 500000 || body.Guardrail.MaxTokens != 1000000 || body.Guardrail.SessionRPM != 20 {
		t.Errorf("阈值透出错误: %+v", body.Guardrail)
	}

	// 落一条对照样本后，同一块内 samples 与 est_vs_upstream 应同时出现。
	g.RecordCalibration(60000, 55000)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode2: %v", err)
	}
	if body.Guardrail.Samples != 1 || body.Guardrail.EstVsUpstream == nil || body.Guardrail.EstVsUpstream.N != 1 {
		t.Fatalf("有样本时未透出: %+v", body.Guardrail)
	}
	if got := body.Guardrail.EstVsUpstream.Median; got < 1.08 || got > 1.10 {
		t.Errorf("median = %v, want ≈1.09", got)
	}
}
