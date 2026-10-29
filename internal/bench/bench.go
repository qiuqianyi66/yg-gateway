// Package bench 网关级 benchmark（REQ-benchmark-2026-10-29）：三项可验证指标
// 的纯函数计算——选号算法效果（M1）、账号池健康度（M2）、模型路由准确率（M3）。
//
// 纪律：
//   - 纯函数：输入 []reqlog.Event + 账号现态快照，输出 Report；无 IO/时钟/
//     随机/全局态（调用方负责读取归档与快照文件），同一输入重跑逐字节一致。
//   - 四态：每个指标带 Kind（observation/derivation/inference/unknown）+
//     Samples；样本不足标 unknown 而非 0（AGENTS.md 四态纪律 + L-22）。
//   - 零出站：不发起任何上游调用（用户约束：不发起付费验证）。
//   - 不对标 LLM 榜单（不同量纲，硬对比违反四态纪律）。
package bench

import (
	"sort"
	"time"

	"yg-gateway/internal/reqlog"
)

// Kind 指标的认识论状态（四态，见 AGENTS.md）。
type Kind string

const (
	KindObservation Kind = "observation"
	KindDerivation  Kind = "derivation"
	KindInference   Kind = "inference"
	KindUnknown     Kind = "unknown"
)

// Metric 单指标输出。
type Metric struct {
	Value   float64            `json:"value"`             // 指标值（样本不足时为 0）
	Kind    Kind               `json:"kind"`              // 四态标注
	Samples int                `json:"samples"`           // 有效样本数
	Note    string             `json:"note,omitempty"`    // 口径/限制说明
	Buckets map[string]float64 `json:"buckets,omitempty"` // 分桶（如按天成功率）
}

// AccountState 账号现态快照的最小投影（REQ-snapshot 落盘文件同字段）。
type AccountState struct {
	UID               string    `json:"uid"`
	CreditsExpiring   int64     `json:"credits_expiring,omitempty"`
	EarliestExpiry    time.Time `json:"credits_earliest_expiry,omitempty"`
	EarliestRemaining int64     `json:"credits_earliest_remaining,omitempty"`
	Disabled          bool      `json:"disabled"`
	Paused            bool      `json:"paused"`
	Cooling           bool      `json:"cooling"`
	SuccessCount      int64     `json:"success_count,omitempty"`
	LastSuccessKnown  bool      `json:"last_success_known"`
}

// Report benchmark 输出。
type Report struct {
	GeneratedAt time.Time         `json:"generated_at"` // 调用方填（本包不取时钟）
	WindowDays  int               `json:"window_days"`
	Events      int               `json:"events"`
	PickBias    Metric            `json:"pick_expiring_bias"` // M1
	Health      HealthMetric      `json:"health"`             // M2
	Routing     map[string]Metric `json:"route_success"`      // M3，按模型分桶
}

// HealthMetric M2：池健康度。
type HealthMetric struct {
	SuccessRate7d Metric            `json:"success_rate_7d"`
	HealthyRatio  Metric            `json:"healthy_ratio"`
	PerDay        map[string]Metric `json:"per_day,omitempty"` // 每日成功率
}

// expiring window 常量：M1 到期压力窗口与账号现态 expiring 同口径
// （Pool 配置窗口内积分；快照里已含，无需重算）。
const minRouteSamples = 5 // M3 每模型最小样本数，低于 = unknown

// Eval 计算三项指标。events 为全量归档（ReadAll 输出，时间升序）；
// accounts 为当前（或最近）账号现态快照投影。窗口按 events 时间跨度推算。
func Eval(events []reqlog.Event, accounts []AccountState, generatedAt time.Time) Report {
	rep := Report{
		GeneratedAt: generatedAt,
		Events:      len(events),
		Routing:     make(map[string]Metric),
	}
	if len(events) == 0 {
		rep.PickBias = Metric{Kind: KindUnknown, Samples: 0, Note: "无请求归档，无法评估选号"}
		rep.Health.SuccessRate7d = Metric{Kind: KindUnknown, Samples: 0, Note: "无请求归档"}
		rep.Health.HealthyRatio = healthRatio(accounts)
		return rep
	}
	// 窗口天数：首个与末个事件跨度，至少 1。
	d0, d1 := events[0].Time, events[len(events)-1].Time
	days := int(d1.Sub(d0).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	rep.WindowDays = days

	rep.PickBias = pickExpiringBias(events, accounts)
	rep.Health = health(events, accounts)
	rep.Routing = routeSuccess(events)
	return rep
}

// pickExpiringBias M1：被选账号的到期压力 vs 池内基线。
//
// 兑现判据：每次成功/失败请求命中账号的 CreditsExpiring（窗口内将到期积分）——
// 统计所有事件命中账号的 expiring 总和占比。由于快照是"当前现态"，历史的
// expiring 只能近似：用事件里出现过的账号在最新快照中的 expiring 聚合。
// 输出 expiring_bias = 被选账号平均 expiring / 池内账号平均 expiring。
// 样本 <2 个账号标 unknown。
//
// 账号匹配口径：reqlog 的 Event.Account 是「昵称(uid8)」展示标签（R2 脱敏，
// 不存完整 UID），快照 AccountState.UID 是完整 uuid——取事件标签括号内 uid8
// （无括号则整体）与快照 UID 前 8 位比对。匹配不上 = 该事件账号在快照中无
// 现态（可能是归档窗口内已被移除的账号），不计入样本（unknown 而非 0）。
func pickExpiringBias(events []reqlog.Event, accounts []AccountState) Metric {
	// 事件出现过的账号集合（标签 → uid8 键）。
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev.Account != "" {
			seen[accountKey(ev.Account)] = true
		}
	}
	// 快照账号：UID 前 8 位为键。
	expByKey := make(map[string]int64)
	for _, a := range accounts {
		key := uidKey(a.UID)
		expByKey[key] = a.CreditsExpiring
	}
	var pickedSum, pickedN int64
	for key := range seen {
		if e, ok := expByKey[key]; ok {
			pickedSum += e
			pickedN++
		}
	}
	if pickedN == 0 || len(accounts) == 0 {
		return Metric{Kind: KindUnknown, Samples: 0, Note: "无事件账号命中快照，无法计算选号到期偏置"}
	}
	pickedAvg := float64(pickedSum) / float64(pickedN)
	var poolSum int64
	for _, a := range accounts {
		poolSum += a.CreditsExpiring
	}
	poolAvg := float64(poolSum) / float64(len(accounts))
	bias := 0.0
	if poolAvg > 0 {
		bias = pickedAvg / poolAvg
	}
	// 观察/派生双环：expiring 是快照字段（observation），bias 是派生（derivation）。
	return Metric{
		Value:   round2(bias),
		Kind:    KindDerivation,
		Samples: int(pickedN),
		Note:    ">1 = 快过期优先兑现；expiring 为最新快照现态（observation），偏置为派生",
	}
}

// accountKey 事件账号标签 → 匹配键：取「昵称(uid8)」中括号内 uid8；
// 无括号则整体（测试直接传 "A" 亦匹配快照 UID 前 8 位 = "A"）。
func accountKey(label string) string {
	i, j := 0, len(label)
	if s := indexByte(label, '('); s >= 0 {
		i = s + 1
		if e := indexByte(label, ')'); e > i {
			j = e
		}
	}
	return label[i:j]
}

// uidKey 快照完整 UID → 匹配键（前 8 位，与 R2 日志脱敏口径一致）。
func uidKey(uid string) string {
	if len(uid) > 8 {
		return uid[:8]
	}
	return uid
}

// indexByte 定位字节 c 在 s 中的下标（-1 = 不存在）。
// 用本函数而非 strings.IndexByte 仅为保持 bench 零依赖（纯标准库）。
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// health M2：7 天成功率 + 健康账号占比 + 每日分桶。
func health(events []reqlog.Event, accounts []AccountState) HealthMetric {
	h := HealthMetric{}
	// 近 7 天窗口（按事件时间）。
	now := events[len(events)-1].Time
	cutoff := now.AddDate(0, 0, -7)
	var ok, total int
	for _, ev := range events {
		if ev.Time.Before(cutoff) {
			continue
		}
		total++
		if ev.OK {
			ok++
		}
	}
	if total == 0 {
		h.SuccessRate7d = Metric{Kind: KindUnknown, Samples: 0, Note: "7 天窗口无请求"}
	} else {
		h.SuccessRate7d = Metric{
			Value:   round2(float64(ok) / float64(total)),
			Kind:    KindDerivation,
			Samples: total,
			Note:    "成功/总（reqlog 观测），7 天窗口",
		}
	}
	h.HealthyRatio = healthRatio(accounts)
	// 每日成功率分桶。
	perDay := make(map[string]Metric)
	byDay := map[string][2]int{} // date -> [ok,total]
	for _, ev := range events {
		d := ev.Time.Format("2006-01-02")
		p := byDay[d]
		p[1]++
		if ev.OK {
			p[0]++
		}
		byDay[d] = p
	}
	for d, p := range byDay {
		perDay[d] = Metric{
			Value:   round2(float64(p[0]) / float64(p[1])),
			Kind:    KindDerivation,
			Samples: p[1],
		}
	}
	h.PerDay = perDay
	return h
}

// healthRatio 健康账号占比（现态快照，observation）。
// 健康 = 非 disabled 非 paused 非 cooling。无账号 = unknown。
func healthRatio(accounts []AccountState) Metric {
	if len(accounts) == 0 {
		return Metric{Kind: KindUnknown, Samples: 0, Note: "无账号快照"}
	}
	var healthy int
	for _, a := range accounts {
		if !a.Disabled && !a.Paused && !a.Cooling {
			healthy++
		}
	}
	return Metric{
		Value:   round2(float64(healthy) / float64(len(accounts))),
		Kind:    KindObservation,
		Samples: len(accounts),
		Note:    "健康=非禁用/暂停/冷却（现态快照）",
	}
}

// routeSuccess M3：按请求模型分桶的成功率。
// 样本 <minRouteSamples 的模型标 unknown（推断不作数）。
func routeSuccess(events []reqlog.Event) map[string]Metric {
	out := make(map[string]Metric)
	type cnt struct{ ok, total int }
	byModel := make(map[string]cnt)
	for _, ev := range events {
		if ev.Model == "" {
			continue // 无模型字段：无法归因（缺失 = 未知，不并入）
		}
		c := byModel[ev.Model]
		c.total++
		if ev.OK {
			c.ok++
		}
		byModel[ev.Model] = c
	}
	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
	}
	sort.Strings(models)
	for _, m := range models {
		c := byModel[m]
		if c.total < minRouteSamples {
			out[m] = Metric{Kind: KindUnknown, Samples: c.total,
				Note: "样本不足，推断不作数"}
			continue
		}
		out[m] = Metric{
			Value:   round2(float64(c.ok) / float64(c.total)),
			Kind:    KindDerivation,
			Samples: c.total,
			Note:    "该模型请求成功率（reqlog 观测）",
		}
	}
	return out
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
