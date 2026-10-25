// opsagent_wiring.go 运维 agent 装配（ADR-017）：观察面投影 + 建议输出口 + 生命周期。
//
// 为什么观察面在这里投影：opsagent 包只依赖 domain 层（evidence/logfmt/unknown），
// pool 与 metrics 的数据由本文件以闭包注入——「读哪些字段」是装配层的显式决定
// （观察面最小化是设计动作，见 ADR-017 理由 2）。
package main

import (
	"context"
	"log"
	"strings"
	"time"

	"yg-gateway/internal/alerting"
	"yg-gateway/internal/logfmt"
	"yg-gateway/internal/opsagent"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/unknown"
)

// newAlertNotifier 构造外发告警实例（P0-4）：webhook URL 空 = 不告警（零回归）。
// 放这里而非 main.go：main.go 处于 oversizeFiles 棘轮边缘，装配细节一律外移。
func newAlertNotifier(cfg *Config) *alerting.Notifier {
	return alerting.New(alerting.Config{
		WebhookURL:         cfg.Alerting.WebhookURL,
		MinCreditThreshold: cfg.Alerting.MinCreditThreshold,
		DedupWindow:        time.Duration(cfg.Alerting.DedupMinutes) * time.Minute,
	})
}

// coolKindOf 把 pool 的细粒度冷却词表（hard_credit/soft_rate）归一成 opsagent 的
// 粗粒度口径（hard/soft）；breaker/degrade 原样（已是粗口径）。opsagent 规则只认
// 粗口径——pool 未来加细分（如 hard_quota）不必改 opsagent（投影即契约，ADR-017）。
// 实测教训：pool 原生值是 "hard_credit" 而非 "hard"，规则直接匹配细词表会永假。
func coolKindOf(poolKind string) string {
	switch {
	case strings.HasPrefix(poolKind, "hard"):
		return "hard"
	case strings.HasPrefix(poolKind, "soft"):
		return "soft"
	default:
		return poolKind
	}
}

// opsAgentObservers 观察面构造（独立成函数：闭包体可在测试里直接执行——
// 若只在 startOpsAgent 内联，ticker 首轮之前闭包体永远不被覆盖）。
func opsAgentObservers(p *pool.Pool) opsagent.Observers {
	return opsagent.Observers{
		Counts: func() opsagent.PoolCounts {
			total, healthy, cooling, disabled, inFlight := p.CountsDetailed()
			return opsagent.PoolCounts{
				Total: total, Healthy: healthy, Cooling: cooling,
				Disabled: disabled, InFlightFull: inFlight,
			}
		},
		// 投影全量账号（含健康号）：opsagent 规则自行过滤 CoolKind，
		// 未来加规则不必同步改这里（观察面一次到位，投影本身无判断逻辑）。
		Accounts: func() []opsagent.AccountView {
			sts := p.List()
			out := make([]opsagent.AccountView, 0, len(sts))
			for _, st := range sts {
				out = append(out, opsagent.AccountView{
					UID: st.UID, Nickname: st.Nickname,
					Credits: st.Credits, CoolKind: coolKindOf(st.CoolKind),
				})
			}
			return out
		},
	}
}

// buildOpsAgent 构造运维 agent（REQ-opsagent / ADR-017）。
// enabled=false 或 pool 缺失时返回 nil：面板显示「未启用」，startOpsAgent no-op
// （双保险的零回归：config 无 opsagent 键的既有部署行为零变化）。
func buildOpsAgent(cfg OpsAgentConfig, p *pool.Pool, reg *unknown.Registry, n *alerting.Notifier) *opsagent.Agent {
	if !cfg.Enabled || p == nil {
		return nil
	}
	return opsagent.New(cfg.normalized(), opsAgentObservers(p), reg, opsSink{n: n})
}

// startOpsAgent 启动已构造的 agent（nil = 未启用，no-op）。
// ctx 用应用级 ctx（signal.NotifyContext）：进程退出信号即 agent 停止（R9）。
func startOpsAgent(a *opsagent.Agent, ctx context.Context) {
	if a == nil {
		return
	}
	interval, frac := a.ConfigSnapshot()
	log.Printf("[opsagent] 已启用（观察-建议模式，不自动行动）：interval=%s healthy_frac=%.2f",
		interval, frac)
	go a.Run(ctx)
}

// opsSink 建议输出口：WARN 日志一行（总是）+ 账号级建议走 alerting webhook
// （复用其去重窗口，避免同一矛盾每轮重复轰炸 webhook）。
type opsSink struct{ n *alerting.Notifier }

// Suggest 实现 opsagent.Sink。log.Printf 无错误返回（判据②类清理路径）。
func (s opsSink) Suggest(x opsagent.Suggestion) {
	log.Printf("WARN: [opsagent] rule=%s acct=%s: %s", x.Rule, logfmt.Label(x.UID, x.Nickname), x.Title)
	if x.UID != "" && s.n != nil {
		s.n.NotifyAccountDown("opsagent-"+x.Rule, x.UID, x.Nickname, x.Title)
	}
}
