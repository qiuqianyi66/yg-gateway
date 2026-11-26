// opsagent_runtime_view_test.go 面板 runtime 只读块。
package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yg-gateway/internal/opsagent"
)

type fakePanelRuntimeModel struct{}

func (fakePanelRuntimeModel) Complete(context.Context, opsagent.RuntimeModelRequest) (opsagent.RuntimeModelResponse, error) {
	return opsagent.RuntimeModelResponse{Content: "runtime ok"}, nil
}

func TestOpsAgentStatusIncludesRuntime(t *testing.T) {
	p := newOpsAgentTestPanel(t)
	rt, err := opsagent.NewRuntime(opsagent.RuntimeConfig{
		SessionDir: t.TempDir(), Model: "fake-free", Interval: time.Hour,
	}, fakePanelRuntimeModel{}, nil, nil, "sys")
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, err := rt.Turn(context.Background(), "巡检"); err != nil {
		t.Fatal(err)
	}
	p.cfg.OpsRuntime = rt

	rec := httptest.NewRecorder()
	p.opsAgentStatus(rec, httptest.NewRequest(http.MethodGet, "/panel/api/opsagent/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct {
		Enabled bool `json:"enabled"`
		Runtime *struct {
			Model     string `json:"model"`
			SessionID string `json:"session_id"`
			LastTurn  int    `json:"last_turn"`
			LastOut   string `json:"last_outcome"`
		} `json:"runtime"`
		Sessions []opsagent.RuntimeSessionSummary `json:"runtime_sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Enabled || out.Runtime == nil {
		t.Fatalf("enabled=%v runtime=%#v", out.Enabled, out.Runtime)
	}
	if out.Runtime.Model != "fake-free" || out.Runtime.SessionID == "" || out.Runtime.LastTurn != 1 || out.Runtime.LastOut != "success" {
		t.Fatalf("runtime=%#v", out.Runtime)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].Turns != 1 {
		t.Fatalf("sessions=%#v", out.Sessions)
	}
}
