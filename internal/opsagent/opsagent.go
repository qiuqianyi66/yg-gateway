// Package opsagent 网关内置运维 agent（MVP：观察-建议，不自动行动）。
//
// 定位（REQ-opsagent-2026-10-24 / ADR-017）：把「异常信号 → 假设 → 建议 → 审计」
// 串成闭环的编排层。骨架全部复用既有语义：
//   - 观察：装配层注入的观察面（Observers，闭包投影自 pool/metrics），不引入新抓取；
//   - 假设：登记进 unknown.Registry（requires 强制非空、幂等去重、带 revision）；
//   - 建议：经 Sink 输出（面板 notice / alerting webhook 由装配层实现），不直接执行；
//   - 审计：每轮一条 Round 报告（Facts 用 evidence 四态标注，推断不得裸奔）。
//
// 动作白名单（护栏）：MVP 唯一放行 ActionSuggest（它本身只是输出）。
// 其余动作（含预留的 ActionCostExplore）由默认执行器拒绝并落入 Round.Rejected——
// 拒绝是可观测事件，不是静默丢弃。
//
// 依赖方向（ADR-017）：本包只 import domain 层（evidence/logfmt/unknown），
// 不依赖 pool/metrics——观察数据由 cmd/server 装配期注入，保证边界最小。
package opsagent

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"yg-gateway/internal/evidence"
	"yg-gateway/internal/logfmt"
	"yg-gateway/internal/unknown"
)

// Action 建议附带的可执行动作分类。MVP 白名单只有 suggest。
type Action string

const (
	// ActionSuggest 仅输出建议（经 Sink）。MVP 唯一白名单成员。
	ActionSuggest Action = "suggest"
	// ActionCostExplore 预留：触发一次成本探索（pick 既有机制）。MVP 执行器拒绝。
	ActionCostExplore Action = "cost-explore"
)

// AccountView 池内单账号的最小观察面（装配层从 pool.List 投影）。
// CoolKind 取粗粒度口径（"hard"/"soft"/"breaker"/"degrade"，空 = 无冷却）——
// 装配层负责把 pool 的细粒度词表（hard_credit/soft_rate）归一（ADR-017 投影即契约）。
type AccountView struct {
	UID       string
	Nickname  string
	Credits   int64 // 本地插值口径余额（只会偏低不会偏高，见 pool.NoteModelCost 注释）
	CoolKind  string
	CoolUntil time.Time
}

// PoolCounts 池级计数快照（装配层从 pool.CountsDetailed 投影）。
type PoolCounts struct {
	Total, Healthy, Cooling, Disabled, InFlightFull int
}

// Suggestion 一条建议（含依据的四态标注）。
type Suggestion struct {
	Rule     string // 规则名（审计分类，如 "capacity" / "credit-402-contradiction"）
	Title    string
	Detail   string
	Facts    []evidence.Fact
	Action   Action // MVP 恒 ActionSuggest；其余值由执行器拒绝
	UID      string // 相关账号（池级建议为空）
	Nickname string
}

// Sink 建议输出口。生产装配实现为日志 + alerting webhook / 面板 notice。
type Sink interface {
	Suggest(Suggestion)
}

// LogSink 零依赖兜底输出：WARN 日志一行。
type LogSink struct{}

// Suggest 实现 Sink。忽略写日志的返回值：log.Printf 无错误返回（判据②类清理路径）。
func (LogSink) Suggest(s Suggestion) {
	log.Printf("WARN: [opsagent] rule=%s uid=%q: %s", s.Rule, logfmt.UID8(s.UID), s.Title)
}

// Executor 动作白名单执行器。默认实现 DefaultExecutor 只放行 ActionSuggest。
type Executor interface {
	Execute(a Action, s Suggestion) error
}

type whitelistExecutor struct {
	allowed map[Action]bool
}

// DefaultExecutor MVP 白名单：仅 suggest（它经 Sink 输出，本身无副作用）。
func DefaultExecutor() Executor {
	return whitelistExecutor{allowed: map[Action]bool{ActionSuggest: true}}
}

func (e whitelistExecutor) Execute(a Action, _ Suggestion) error {
	if e.allowed[a] {
		return nil
	}
	return fmt.Errorf("opsagent: action %q 不在白名单（MVP 仅 suggest）", a)
}

// Observers 注入的观察面。三个函数都必须非 nil（New 会 fail fast）。
type Observers struct {
	Counts   func() PoolCounts
	Accounts func() []AccountView
	Now      func() time.Time
}

// Config 运行参数。零值可用（默认见 normalized）。
type Config struct {
	Interval    time.Duration // 轮询间隔；<=0 取 defaultInterval
	MaxRounds   int           // 历史环保留条数；<=0 取 defaultMaxRounds
	HealthyFrac float64       // 容量告警阈值（healthy/total 严格小于该值触发）；<=0 取 defaultHealthyFrac
}

const (
	defaultInterval    = 10 * time.Minute
	defaultMaxRounds   = 50
	defaultHealthyFrac = 0.5
	// minPoolSizeForCapacity 容量规则的池规模下限：池太小（<3）时 healthy 波动
	// 是噪声不是信号——1/2 冷却可能只是一次签到间隙。
	minPoolSizeForCapacity = 3
)

func (c Config) normalized() Config {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.MaxRounds <= 0 {
		c.MaxRounds = defaultMaxRounds
	}
	if c.HealthyFrac <= 0 {
		c.HealthyFrac = defaultHealthyFrac
	}
	return c
}

// Round 一轮观察-假设-建议-审计的报告（审计本体）。
type Round struct {
	At          time.Time
	Facts       []evidence.Fact
	Unknowns    []unknown.Item
	Suggestions []Suggestion
	Rejected    []string // 被白名单拒绝的动作（护栏审计：拒绝必须可见）
}

// Agent 运维 agent 本体。并发安全（Round 可被 Run 与手动触发并发调用）。
type Agent struct {
	cfg  Config
	obs  Observers
	reg  *unknown.Registry
	sink Sink
	exec Executor

	mu   sync.Mutex
	hist []Round
}

// New 构造。reg 允许 nil（假设登记跳过——装配层总应传真实示例，nil 仅测试用）。
func New(cfg Config, obs Observers, reg *unknown.Registry, sink Sink) *Agent {
	cfg = cfg.normalized()
	if obs.Now == nil {
		obs.Now = time.Now
	}
	if obs.Counts == nil {
		obs.Counts = func() PoolCounts { return PoolCounts{} }
	}
	if obs.Accounts == nil {
		obs.Accounts = func() []AccountView { return nil }
	}
	if sink == nil {
		sink = LogSink{}
	}
	return &Agent{cfg: cfg, obs: obs, reg: reg, sink: sink, exec: DefaultExecutor()}
}

// discardSink canary 用：建议只进审计不外发（不污染 webhook/日志告警通道）。
type discardSink struct{}

func (discardSink) Suggest(Suggestion) {}

// CanaryRound 规则心跳（ROADMAP v3 V3-7 / NEW-FRONTIERS #2）：注入**合成观察**
// （1/4 健康 = capacity 必触发；hard 冷却+正余额 = 402-contradiction 必触发），
// 断言规则真的会响。词表口径类 bug（如 hard vs hard_credit 匹配永假）在单测里
// 以编译期词表为锚，但 pool 词表演进后的**运行态漂移**只有运行中自检能抓到。
//
// 隔离口径：合成观察跑在独立 probe Agent 上（sink=discard、reg=nil）——不污染
// 真实 registry、不外发 webhook、不进历史环。返回失败清单（空 = 全部规则存活）。
func (a *Agent) CanaryRound(now time.Time) []string {
	synth := Observers{
		Counts: func() PoolCounts { return PoolCounts{Total: 4, Healthy: 1, Cooling: 3} },
		Accounts: func() []AccountView {
			return []AccountView{{UID: "canary-synth", Nickname: "合成观察", Credits: 42, CoolKind: "hard"}}
		},
		Now: func() time.Time { return now },
	}
	probe := &Agent{cfg: a.cfg.normalized(), obs: synth, sink: discardSink{}, exec: DefaultExecutor()}
	rd := probe.Round()
	hasCap, hasContradiction := false, false
	for _, s := range rd.Suggestions {
		switch s.Rule {
		case "capacity":
			hasCap = true
		case "credit-402-contradiction":
			hasContradiction = true
		}
	}
	var failures []string
	if !hasCap {
		failures = append(failures, "capacity 规则在合成观察上未触发（规则可能因字段/词表漂移而永假）")
	}
	if !hasContradiction {
		failures = append(failures, "credit-402-contradiction 规则在合成观察上未触发（同上）")
	}
	if len(failures) > 0 {
		log.Printf("WARN: [opsagent] canary FAIL：%v（规则自检失败=规则静默死亡，需排查）", failures)
	} else {
		log.Printf("[opsagent] canary ok：全部规则在合成观察上存活")
	}
	return failures
}

// Run 周期执行 Round 直到 ctx 取消（R9：ctx.Done 即返回，无泄漏 goroutine）。
// 启动时先跑一次 canary 自检（失败只告警不阻断——规则死了比不知道规则死了好）。
func (a *Agent) Run(ctx context.Context) {
	a.CanaryRound(a.obs.Now())
	t := time.NewTicker(a.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Round()
		}
	}
}

// ConfigSnapshot 返回运行参数快照（面板「运维 Agent」视图展示用）。
// 参数装配期固定（与 pool.Set* 同风格），运行期不提供热改。
func (a *Agent) ConfigSnapshot() (interval time.Duration, healthyFrac float64) {
	return a.cfg.Interval, a.cfg.HealthyFrac
}

// History 返回保留的轮报告（旧→新）。返回副本，调用方改不动内部状态。
func (a *Agent) History() []Round {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Round, len(a.hist))
	copy(out, a.hist)
	return out
}

// Round 执行一轮：观察 → 规则 → 假设登记 → 建议（过白名单）→ 审计留档。
func (a *Agent) Round() Round {
	now := a.obs.Now()
	rd := Round{At: now}

	// ── 观察 1：池级计数（直接观测）。
	pc := a.obs.Counts()
	rd.Facts = append(rd.Facts, evidence.Observe(pc, "pool.CountsDetailed"))

	// ── 规则 capacity：健康占比低于阈值且池有规模。
	// 判据本身是 derivation（分数由观测计数相除得出）；触发条件写进 Detail。
	if pc.Total >= minPoolSizeForCapacity {
		frac := float64(pc.Healthy) / float64(pc.Total)
		if frac < a.cfg.HealthyFrac {
			a.emit(&rd, Suggestion{
				Rule: "capacity",
				Title: fmt.Sprintf("池容量告急：healthy %d/%d（%.0f%%，阈值 %.0f%%），cooling=%d disabled=%d",
					pc.Healthy, pc.Total, frac*100, a.cfg.HealthyFrac*100, pc.Cooling, pc.Disabled),
				Detail: "流量将集中到剩余健康号（minPickGap 与在途上限只能延缓集中）。建议核对冷却号占比的构成：402 硬冷却（等签到）与熔断（看错误分类）处置不同。",
				Facts: []evidence.Fact{
					evidence.Derive(frac, "healthy/total（由池级计数派生）", pc.Total),
					evidence.Observe(pc.Cooling, "pool 冷却计数"),
				},
				Action: ActionSuggest,
			})
		}
	}

	// ── 观察 2：逐账号状态（直接观测）。
	avs := a.obs.Accounts()
	rd.Facts = append(rd.Facts, evidence.Observe(len(avs), "账号观察面条数"))

	// ── 规则 credit-402-contradiction：402 硬冷却（余额门禁）与本地正余额矛盾。
	// 本地余额是插值口径（只会偏低不会偏高），却仍为正时上游却判 402——
	// 两口径矛盾值得跟踪：要么上游 quota 结构变化（插值将持续偏低），
	// 要么余额门禁规则变了。登记 unknown（requires = 权威对账），幂等去重。
	for _, av := range avs {
		if av.CoolKind != "hard" || av.Credits <= 0 {
			continue
		}
		label := logfmt.Label(av.UID, av.Nickname)
		question := fmt.Sprintf("账号 %s 收到 402 硬冷却但本地插值余额为正（上游余额门禁与本地口径矛盾）", label)
		requires := "下次签到后的权威余额对账（若 quota 结构变化，插值口径将持续偏离）"
		if a.reg != nil {
			if it, err := a.reg.Record(question, requires); err == nil {
				rd.Unknowns = append(rd.Unknowns, it)
			} else {
				// 登记失败不吞（R10）：进审计而非静默。
				rd.Rejected = append(rd.Rejected, fmt.Sprintf("unknown-record/%s: %v", label, err))
			}
		}
		a.emit(&rd, Suggestion{
			Rule:   "credit-402-contradiction",
			Title:  fmt.Sprintf("账号 %s 402 硬冷却中但本地余额 %d 为正（口径矛盾）", label, av.Credits),
			Detail: "本地插值余额只会偏低不会偏高，为正仍被 402 判停说明上游门禁依据了别的量。已登记 unknown 等权威对账；不建议人工解冻。",
			Facts: []evidence.Fact{
				evidence.Observe(av.Credits, "本地插值余额（偏低方向安全）"),
				evidence.Observe("hard", "pool 冷却分类"),
			},
			Action:   ActionSuggest,
			UID:      av.UID,
			Nickname: av.Nickname,
		})
	}

	// ── 审计收尾：本轮建议数是 derivation（由本轮事件计数得出）。
	rd.Facts = append(rd.Facts, evidence.Derive(len(rd.Suggestions), "本轮建议数（由规则触发派生）", len(avs)))

	a.mu.Lock()
	a.hist = append(a.hist, rd)
	if excess := len(a.hist) - a.cfg.MaxRounds; excess > 0 {
		a.hist = a.hist[excess:]
	}
	a.mu.Unlock()
	return rd
}

// emit 过白名单后经 Sink 输出建议；被拒的动作落入审计（不静默）。
func (a *Agent) emit(rd *Round, s Suggestion) {
	if err := a.exec.Execute(s.Action, s); err != nil {
		rd.Rejected = append(rd.Rejected, fmt.Sprintf("%s/%s: %v", s.Rule, s.Action, err))
		return
	}
	a.sink.Suggest(s)
	rd.Suggestions = append(rd.Suggestions, s)
}
