// opsagent_view.go 运维 agent 面板视图 API（REQ-opsagent 阶段 2 / ADR-017）。
//
// 单一端点 GET /panel/api/opsagent/status（只读）：未知台账（registry 全量，
// 含成本口径与 agent 登记两类来源）+ 最近审计轮（建议/被拒动作）。
// OpsAgent 为 nil（未启用）时返回 {"enabled":false}，前端据此展示引导文案。
package panel

import (
	"net/http"
	"time"

	"yg-gateway/internal/opsagent"
	"yg-gateway/internal/unknown"
)

// maxStatusRounds 面板单次返回的审计轮数上限：可回溯但不刷屏（内部保留 50）。
const maxStatusRounds = 10

type opsAgentStatusResp struct {
	Enabled      bool             `json:"enabled"`
	IntervalSec  int              `json:"interval_sec,omitempty"`
	HealthyFrac  float64          `json:"healthy_frac,omitempty"`
	UnknownsOpen []unknown.Item   `json:"unknowns_open,omitempty"`
	Rounds       []opsagent.Round `json:"rounds,omitempty"`
}

// opsAgentStatus 只读端点：agent 未启用 ≠ 错误（200 + enabled=false，前端有引导态）。
func (p *Panel) opsAgentStatus(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	oa := p.cfg.OpsAgent
	if oa == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	resp := opsAgentStatusResp{Enabled: true}
	interval, frac := oa.ConfigSnapshot()
	resp.IntervalSec = int(interval / time.Second)
	resp.HealthyFrac = frac
	if p.cfg.Pool != nil {
		resp.UnknownsOpen = p.cfg.Pool.AllOpenUnknowns()
	}
	hist := oa.History()
	if len(hist) > maxStatusRounds {
		hist = hist[len(hist)-maxStatusRounds:]
	}
	resp.Rounds = hist
	writeJSON(w, http.StatusOK, resp)
}
