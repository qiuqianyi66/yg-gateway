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

// TestOpsAgentStatusRoundCapAndOrder 审计轮截断与顺序契约（独立复审 🟡-2 修复）：
// History 保留 12 轮时 API 只返回最近 10 轮（maxStatusRounds），且轮内旧→新
// （前端据此 reverse 渲染新→旧——顺序契约反转会让时间线倒挂且无报错）。
func TestOpsAgentStatusRoundCapAndOrder(t *testing.T) {
	p2 := pool.New("")
	counts := opsagent.PoolCounts{Total: 4, Healthy: 1, Cooling: 3}
	base := time.Date(2026, 10, 25, 10, 0, 0, 0, time.Local)
	tick := 0
	oa := opsagent.New(opsagent.Config{Interval: time.Minute, HealthyFrac: 0.5},
		opsagent.Observers{
			Counts:   func() opsagent.PoolCounts { return counts },
			Accounts: func() []opsagent.AccountView { return nil },
			Now: func() time.Time {
				tick++
				return base.Add(time.Duration(tick) * time.Minute)
			},
		}, unknown.NewRegistry(), nil)
	for i := 0; i < 12; i++ {
		oa.Round()
	}

	p := New(Config{Pool: p2, OpsAgent: oa})
	rec := httptest.NewRecorder()
	p.opsAgentStatus(rec, httptest.NewRequest(http.MethodGet, "/panel/api/opsagent/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct {
		Rounds []struct {
			At time.Time `json:"At"`
		} `json:"rounds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rounds) != maxStatusRounds {
		t.Fatalf("返回轮数 = %d, want %d（截断）", len(out.Rounds), maxStatusRounds)
	}
	for i := 1; i < len(out.Rounds); i++ {
		if !out.Rounds[i-1].At.Before(out.Rounds[i].At) {
			t.Fatalf("轮序必须旧→新：%v 应早于 %v", out.Rounds[i-1].At, out.Rounds[i].At)
		}
	}
	// 截断保的是「最近」：末轮时刻 = 第 12 轮（tick 从 1 起）。
	wantLast := base.Add(12 * time.Minute)
	if !out.Rounds[len(out.Rounds)-1].At.Equal(wantLast) {
		t.Fatalf("末轮 = %v, want %v（应是最新一轮）", out.Rounds[len(out.Rounds)-1].At, wantLast)
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
