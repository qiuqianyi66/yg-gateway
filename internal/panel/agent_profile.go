// agent_profile.go — V3-10 agent 流量画像的面板端点（R5）。
//
// 数据契约与 /status 的 agent_profile 块同源（internal/reqlog/profile.go）：
// ReadArchive 最近 1000 条 chat 事件 → AggregateProfile。四态纪律一致：
//   - 未接线 RequestLog → 501（与 request_metrics / trends 端点同口径）；
//   - 有归档但零条 / 全无 session_fp+agent_kind 信号 → present:false（「整块省略」
//     的显式 knowable 表达，面板据此隐藏区块而非显示空态）；
//   - unknown 桶（UA 未采集）与 other 桶（UA 未识别）分开输出，禁止合并；
//   - cache_miss_unknown 不累加 miss（miss 未知 ≠ miss=0）。
//
// 路由注册：待外部 trends 轮合并后加一行到 Panel.routes()（panel.go 当前被外部
// 未提交改动占用，本文件不碰它——见 .harness/changes/2026-10-29-agent-profile-ui）。
package panel

import (
	"net/http"

	"yg-gateway/internal/reqlog"
)

// agentProfileArchiveLimit 与 server /status 同口径：最近 1000 条 chat 事件。
const agentProfileArchiveLimit = 1000

// agentProfile GET /panel/api/agent_profile。
func (p *Panel) agentProfile(w http.ResponseWriter, r *http.Request) {
	if p.cfg.RequestLog == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": "request logger not available"})
		return
	}
	events, err := p.cfg.RequestLog.ReadArchive(agentProfileArchiveLimit, reqlog.Filter{Kind: reqlog.KindChat})
	if err != nil || len(events) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"present": false})
		return
	}
	// 全无信号（V3-10 之前的归档行 / 开关全关窗口）：聚合出来是空 unknown 桶空榜，
	// 与 /status 的整块省略同源——显式 present:false 让面板隐藏区块。
	hasSignal := false
	for i := range events {
		if events[i].SessionFP != "" || events[i].AgentKind != "" {
			hasSignal = true
			break
		}
	}
	if !hasSignal {
		writeJSON(w, http.StatusOK, map[string]any{"present": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"present": true, "profile": reqlog.AggregateProfile(events)})
}
