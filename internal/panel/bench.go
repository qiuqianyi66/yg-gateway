// bench.go — 面板网关级 benchmark 摘要端点（REQ-benchmark G4，独立文件同 trends.go）。
//
// GET /panel/api/benchmark：复用 internal/bench.Eval 纯函数（三项可验证指标
// M1 选号到期偏置 / M2 池健康度 / M3 路由成功率），数据源 = reqlog 归档全量 +
// Pool.List() 账号现态。零出站零付费（不发起任何上游调用，用户硬约束）。
//
// 四态纪律：每个指标带 kind + samples，样本不足 = unknown 而非 0；前端按
// 「unknown = 无观测」展示，不做 0 美化。
package panel

import (
	"net/http"
	"time"

	"yg-gateway/internal/bench"
	"yg-gateway/internal/reqlog"
)

// benchmark handler：GET /panel/api/benchmark（鉴权路由在 panel.go routes() 注册）。
func (p *Panel) benchmark(w http.ResponseWriter, r *http.Request) {
	if p.cfg.RequestLog == nil {
		writeErr(w, http.StatusNotImplemented, "request logger not available")
		return
	}
	events, err := p.cfg.RequestLog.ReadAll(reqlog.Filter{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read archive: "+err.Error())
		return
	}
	// 账号现态投影（R1 不含凭证；与 trends.go 同字段口径）。
	accts := make([]bench.AccountState, 0, len(p.cfg.Pool.List()))
	for _, s := range p.cfg.Pool.List() {
		accts = append(accts, bench.AccountState{
			UID:               s.UID,
			CreditsExpiring:   s.CreditsExpiring,
			EarliestExpiry:    s.CreditsEarliestExpiry,
			EarliestRemaining: s.CreditsEarliestRemaining,
			Disabled:          s.Disabled,
			Paused:            s.Paused,
			Cooling:           s.Cooling,
			SuccessCount:      s.SuccessCount,
			LastSuccessKnown:  !s.LastSuccessTime.IsZero(),
		})
	}
	writeJSON(w, http.StatusOK, bench.Eval(events, accts, time.Now()))
}
