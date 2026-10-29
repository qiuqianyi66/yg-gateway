// trends.go — 面板时间序列证据端点（独立文件：panel.go 已近千行棘轮）。
//
// 回答「账号池健康度 / 选号 / 路由的周级趋势」：
//   - days：reqlog 归档全量按天聚合（requests/succeeded/failed/success_rate/
//     active_accounts/tokens），时间升序——**无条数截断**（ReadAll），周/月级
//     成功率按最近 1000 条截断会系统性丢早期数据；
//   - accounts：账号现态（credits/到期批次/健康），叠加 Pool.List() 快照，
//     供「存活率/到期曲线」的当前点（observation）；历史存活曲线需要定期
//     落盘快照，当前版本只给现态 + 标注 unknown（requires 后续轮次快照积累）。
//
// 四态纪律（与 AGENTS.md / REQ-trends-2026-10-29 对齐）：success_rate 是
// derivation（成功/总，reqlog 观测）；requests==0 的天其 success_rate=0 语义是
// unknown（无观测），前端按「0 请求 = 无观测」展示；credits_expiring /
// earliest_expiry 是当前快照（observation，非历史曲线）。
package panel

import (
	"net/http"
	"time"

	"yg-gateway/internal/reqlog"
)

// trends handler：GET /panel/api/trends（鉴权路由在 panel.go routes() 注册）。
func (p *Panel) trends(w http.ResponseWriter, r *http.Request) {
	if p.cfg.RequestLog == nil {
		writeErr(w, http.StatusNotImplemented, "request logger not available")
		return
	}
	events, err := p.cfg.RequestLog.ReadAll(reqlog.Filter{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read archive: "+err.Error())
		return
	}
	// 账号现态（不含任何凭证：只取展示/健康/到期字段，R1）。
	type acctStat struct {
		UID              string    `json:"uid"`
		Nickname         string    `json:"nickname"`
		Credits          int64     `json:"credits"`
		CreditsTotal     int64     `json:"credits_total"`
		CreditsExpiring  int64     `json:"credits_expiring"`
		EarliestExpiry   time.Time `json:"earliest_expiry"`
		EarliestRemain   int64     `json:"earliest_remaining"`
		Disabled         bool      `json:"disabled"`
		Paused           bool      `json:"paused"`
		Cooling          bool      `json:"cooling"`
		SuccessCount     int64     `json:"success_count"`
		LastSuccess      time.Time `json:"last_success"`
		LastSuccessKnown bool      `json:"last_success_known"` // zero time = 从未成功（unknown 语义）
	}
	accounts := make([]acctStat, 0)
	for _, s := range p.cfg.Pool.List() {
		ls := s.LastSuccessTime
		accounts = append(accounts, acctStat{
			UID:              s.UID,
			Nickname:         s.Nickname,
			Credits:          s.Credits,
			CreditsTotal:     s.CreditsTotal,
			CreditsExpiring:  s.CreditsExpiring,
			EarliestExpiry:   s.CreditsEarliestExpiry,
			EarliestRemain:   s.CreditsEarliestRemaining,
			Disabled:         s.Disabled,
			Paused:           s.Paused,
			Cooling:          s.Cooling,
			SuccessCount:     s.SuccessCount,
			LastSuccess:      ls,
			LastSuccessKnown: !ls.IsZero(),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days":     reqlog.AggregateDaily(events),
		"accounts": accounts,
		"window":   len(events),
	})
}
