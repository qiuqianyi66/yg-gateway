// opsagent_view_test.go 面板运维 agent 视图 API 测试（REQ-opsagent 阶段 2）：
// 未启用引导态 / enabled 完整响应（unknown 全量 + 审计轮）/ JS 静态资源 serving。
package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yg-gateway/internal/opsagent"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/unknown"
)

// newOpsAgentTestPanel 构造带 agent 的面板：agent 手动跑两轮（capacity 规则触发），
// registry 预置一条已消解条目（断言 AllOpenUnknowns 过滤 resolved）。
func newOpsAgentTestPanel(t *testing.T) *Panel {
	t.Helper()
	p2 := pool.New("")
	reg := unknown.NewRegistry()
	it, err := reg.Record("成本未知（预置，已消解）", "一次实测", "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Resolve(it.ID, it.Revision, "ev-1"); err != nil {
		t.Fatal(err)
	}

	counts := opsagent.PoolCounts{Total: 4, Healthy: 1, Cooling: 3}
	oa := opsagent.New(opsagent.Config{Interval: 10 * time.Minute, HealthyFrac: 0.5},
		opsagent.Observers{
			Counts:   func() opsagent.PoolCounts { return counts },
			Accounts: func() []opsagent.AccountView { return nil },
			Now:      func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local) },
		}, reg, nil)
	oa.Round() // 两轮：capacity 规则都触发（healthy 1/4 < 0.5）
	oa.Round()

	return New(Config{Pool: p2, OpsAgent: oa})
}

// TestOpsAgentStatusDisabled 未启用：200 + {"enabled":false}（引导态，非错误）。
func TestOpsAgentStatusDisabled(t *testing.T) {
	p := New(Config{Pool: pool.New("")})
	rec := httptest.NewRecorder()
	p.opsAgentStatus(rec, httptest.NewRequest(http.MethodGet, "/panel/api/opsagent/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	var out struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Enabled {
		t.Fatal("无 agent 实例应 enabled=false")
	}
}

// TestOpsAgentStatusEnabled enabled：unknown 全量过滤 resolved、审计轮存在且带建议、
// 配置快照回传、响应含安全头。
func TestOpsAgentStatusEnabled(t *testing.T) {
	p := newOpsAgentTestPanel(t)
	rec := httptest.NewRecorder()
	p.opsAgentStatus(rec, httptest.NewRequest(http.MethodGet, "/panel/api/opsagent/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("API 响应缺 CSP 头（setSecurityHeaders 口径）")
	}

	var out struct {
		Enabled      bool    `json:"enabled"`
		IntervalSec  int     `json:"interval_sec"`
		HealthyFrac  float64 `json:"healthy_frac"`
		UnknownsOpen []struct {
			ID       string `json:"id"`
			Resolved bool   `json:"resolved"`
		} `json:"unknowns_open"`
		Rounds []struct {
			Suggestions []struct {
				Rule string `json:"Rule"`
			} `json:"Suggestions"`
			Rejected []string `json:"Rejected"`
		} `json:"rounds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不可解析: %v\n%s", err, rec.Body.String())
	}
	if !out.Enabled || out.IntervalSec != 600 || out.HealthyFrac != 0.5 {
		t.Fatalf("enabled/配置快照 = %+v", out)
	}
	for _, it := range out.UnknownsOpen {
		if it.Resolved {
			t.Fatalf("未消解列表混入 resolved 条目：%+v", it)
		}
	}
	if len(out.UnknownsOpen) != 0 {
		// 预置条目已消解；agent 本轮没登记（无 hard 冷却账号）→ 应为空。
		t.Fatalf("unknowns_open = %d, want 0（预置条目已消解）", len(out.UnknownsOpen))
	}
	if len(out.Rounds) != 2 {
		t.Fatalf("审计轮 = %d, want 2", len(out.Rounds))
	}
	for _, rd := range out.Rounds {
		if len(rd.Suggestions) != 1 || rd.Suggestions[0].Rule != "capacity" {
			t.Fatalf("每轮应有一条 capacity 建议：%+v", rd.Suggestions)
		}
		if len(rd.Rejected) != 0 {
			t.Fatalf("不应有被拒动作：%v", rd.Rejected)
		}
	}
}

// TestOpsAgentScriptServing 静态 JS 资源：200 + JS Content-Type + 非空 + CSP 头。
func TestOpsAgentScriptServing(t *testing.T) {
	p := New(Config{})
	rec := httptest.NewRecorder()
	p.opsAgentScript(rec, httptest.NewRequest(http.MethodGet, "/panel/opsagent.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺 CSP 头")
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("opsagent.js 嵌入为空（go:embed 未生效？）")
	}
}
