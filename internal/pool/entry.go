// Package pool 账号池：单一状态机（健康/冷却/熔断/连败降权）+ 在途租约 + 最早到期优先/加权挑选 + state.json 持久化。
package pool

import (
	"sync/atomic"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/evidence"
	"yg-gateway/internal/unknown"
)

// degradeReason 连败降权（issue #114）写 reason 的固定文案：与冷却域的
// reason（"429 rate limit" / "waf 403 block" / "余额不足"）共用一个字段，
// 运维在 /status 一处即可看到「为什么被降权/冷却」，不新增台账字段。
const degradeReason = "consecutive failures"

type CoolKind int

const (
	CoolHard CoolKind = iota // 余额不足 → 冷却到次日 04:00（等签到恢复）
	CoolSoft                 // 429 → 短冷却
)

func (k CoolKind) String() string {
	switch k {
	case CoolHard:
		return "hard_credit"
	case CoolSoft:
		return "soft_rate"
	}
	return "unknown"
}

// TokenUsage 账号聊天请求的累计 token 用量摘要（不包含任何原始凭证）。
type TokenUsage struct {
	RequestCount        int64     `json:"request_count,omitempty"`
	UsageCount          int64     `json:"usage_count,omitempty"`
	PromptTokens        int64     `json:"prompt_tokens,omitempty"`
	CompletionTokens    int64     `json:"completion_tokens,omitempty"`
	TotalTokens         int64     `json:"total_tokens,omitempty"`
	LastLatencyMs       int64     `json:"last_latency_ms,omitempty"`
	LastTokensPerSecond *float64  `json:"last_tokens_per_second,omitempty"`
	LastUsedAt          time.Time `json:"last_used_at,omitempty"`
	LastModel           string    `json:"last_model,omitempty"`
}

// TokenUsageDelta 是一次聊天账号尝试的 usage 增量。
// 各 Has* 字段用于区分上游缺少字段与字段值确实为 0。
type TokenUsageDelta struct {
	Model               string
	HasPromptTokens     bool
	PromptTokens        int64
	HasCompletionTokens bool
	CompletionTokens    int64
	HasTotalTokens      bool
	TotalTokens         int64
	HasLatencyMs        bool
	LatencyMs           int64
	HasTokensPerSecond  bool
	TokensPerSecond     float64
}

// Status 单个账号对外暴露的状态（脱敏）。
type Status struct {
	UID          string `json:"uid"`
	Nickname     string `json:"nickname,omitempty"`
	Credits      int64  `json:"credits"`
	CreditsTotal int64  `json:"credits_total,omitempty"` // 积分总额度（各套餐聚合）；0 = 未知（旧 state/查询失败）
	// CreditsExpiring / CreditsEarliestExpiry / CreditsEarliestRemaining 描述当前积分中
	// 的到期压力。CreditsExpiring 是配置窗口内的剩余积分；后两者是全部未来到期批次中
	// 最早的一批及其剩余量，供 WorkDaddy 同口径的“最早到期优先”路由使用。
	CreditsExpiring          int64     `json:"credits_expiring,omitempty"`
	CreditsEarliestExpiry    time.Time `json:"credits_earliest_expiry,omitempty"`
	CreditsEarliestRemaining int64     `json:"credits_earliest_remaining,omitempty"`
	Cooling                  bool      `json:"cooling"`
	CoolKind                 string    `json:"cool_kind,omitempty"`
	CoolRemaining            int64     `json:"cool_remaining_sec,omitempty"`
	Until                    time.Time `json:"until,omitempty"`
	Reason                   string    `json:"reason,omitempty"`
	SoftStreak               int       `json:"soft_streak,omitempty"` // 连续软冷却次数（指数退避指数，见 entry.softStreak）
	// RateLimitedModels 当前仍在限额的模型列表（issue #36 限额台账）。
	// 仅「带解析时间 6004」触发的模型级独立冷却（modelCooldowns 未到期条目）时非空，
	// 每模型一行；运维据此看到"账号 A 的模型 X 还在限额中，预计 Z 时间恢复"。到期即消失（零回归）。
	RateLimitedModels []RateLimitedModel `json:"rate_limited_models,omitempty"`
	// Realm 账号域（cn/global，auth.Realm() 计算值；含 global.enabled 开关闸）。
	// 供面板/状态接口按域分组展示。
	Realm string `json:"realm,omitempty"`
	// Group 账号池分组标签（P1-B，auth.Auth.Group 透传）。空 = 未分组。
	Group string `json:"group,omitempty"`
	// Enterprise 企业版账号（auth.EnterpriseID 非空，计算值不落盘）。
	// 企业版无个人成长体系（签到/成长任务/连登/旅行/夜猫子 上游均拒），
	// 面板据此隐藏对应按钮；选号、保活、额度展示不受影响。
	Enterprise     bool   `json:"enterprise,omitempty"`
	Disabled       bool   `json:"disabled"`
	DisabledReason string `json:"disabled_reason,omitempty"` // 仅 disabled 账号：禁用原因（运维可见）
	// Paused 暂停选号：退出选号候选（与 disabled 一样不参与选号），但**照常参与**
	// 签到 / 活跃上报 / 保活 / 余额刷新四类保号任务。与 disabled 正交——disabled 是
	// 「授权/session 终态，需人工 revive」，paused 是「运维临时让位」（多号轮换场景），
	// 账号本身健康，只是暂不接流量。
	Paused          bool      `json:"paused,omitempty"`
	SuccessCount    int64     `json:"success_count,omitempty"`
	ErrTotal        int64     `json:"err_total,omitempty"`
	LastSuccessTime time.Time `json:"last_success,omitempty"`
	LastErrTime     time.Time `json:"last_err,omitempty"`
	// CheckinDone 本地今日已签到（签到成功或上游"今天已签到"幂等拒绝均算）。
	// global 域账号无签到体系，恒为 false。面板签到按钮据此显示 签到/已签。
	CheckinDone bool       `json:"checkin_done,omitempty"`
	TokenUsage  TokenUsage `json:"token_usage,omitempty"`
	// ModelCosts 每模型实测成本台账（P1-anti-monopoly 可观测性）：运维据此自查
	//「为什么总选它」——tier 0（免费）垄断 / tier 2 单价排序一眼可见。
	// 仅 modelCostTTL 内的有效观测，每模型一行（cost_per_1k + last_seen +
	// samples）；无观测/全部过期 → nil（tier 1 未知层）。
	ModelCosts []ModelCostStatus `json:"model_costs,omitempty"`
	// ConsecutiveFails 连续失败计数（连败降权用，见 entry.consecutiveFails）。
	// 零值也透出（运维口径：与 err_total/session_dead_fails 一致，零值缺失会让人
	// 误以为"没记录"，实际是零值被 omitempty 省略）。
	ConsecutiveFails int       `json:"consecutive_fails"`
	DegradeUntil     time.Time `json:"degrade_until,omitempty"` // 连败降权截止（非零且未过 = 降权中）
	// 运行态（不持久化）：在途请求数 + 熔断器状态。
	InFlight     int       `json:"in_flight"`
	BreakerFails int       `json:"breaker_fails"`
	BreakerUntil time.Time `json:"breaker_until,omitempty"`
	// EWMA 表现信号（ADR-005 D-502）：仅 ewma 开启且样本 > 0 时透出（指针 = 有值）。
	// EwmaSamples 为成功率事件样本数（成功/失败各 +1；延迟样本数独立不在此透出）。
	EwmaSuccess   *float64 `json:"ewma_success,omitempty"`    // 成功率 EWMA（0~1）
	EwmaLatencyMs *float64 `json:"ewma_latency_ms,omitempty"` // 延迟 EWMA（毫秒，保留 1 位小数）
	EwmaSamples   int64    `json:"ewma_samples,omitempty"`    // 成功率事件样本数
	// EwmaProvenance EWMA 表现信号的认识论标注（AGENTS.md「四态语义纪律」）。
	//
	// 语义：
	//   - 样本数 ≥ ewmaMinSamples → derivation（EWMA 是平滑**派生**值，不是真实成功率）
	//   - 样本数 <  ewmaMinSamples → unknown（冷启动阶段，数值不可作事实使用）
	// 注意：数值本身在两种情况下都透出（ADR-005 既有透出契约），provenance 用来
	// 表达"这个数字有多可信"，而不是决定要不要给。
	EwmaProvenance *evidence.Fact `json:"ewma_provenance,omitempty"`
	// Unknowns 该账号未消解的成本未知（AGENTS.md「Unknown 消解纪律」）。
	//
	// 只在注入 unknown registry 且确有未消解条目时非空。面板/agent 据此知道
	//「这个号的哪些成本还没摸清、还差什么」，而不是把它当成已知的免费号。
	Unknowns []unknown.Item `json:"unknowns,omitempty"`
}

// ModelCostStatus 单个 (账号, 模型) 的成本台账行（P1-anti-monopoly 可观测性）。
// tier 不单独落字段：cost_per_1k ≤ 0 即 tier 0（免费），> 0 即 tier 2（收费），
// 无观测即 tier 1——由调用方/面板按值推出，避免双表示漂移。
type ModelCostStatus struct {
	Model string `json:"model"`
	// CostPer1k 实测每千 token 单价（EMA 平滑值）。≤0 = 实测免费（tier 0）。
	CostPer1k float64 `json:"cost_per_1k"`
	// LastSeen 最近一次观测时刻（过期即从台账消失，同 modelCostTTL 口径）。
	LastSeen time.Time `json:"last_seen"`
	// Samples 累计观测次数（EMA 收敛度参考）。
	Samples int `json:"samples,omitempty"`
	// Provenance 该行 tier 判定的认识论标注（AGENTS.md「四态语义纪律」）。
	//
	// 为什么需要：tier 0/2 是**推断**——凭有限次 credit 实测外推出来的。1 条样本
	// 判"收费"和 20 条样本判"收费"，可信度完全不同，但旧结构里两者长得一样，
	// 消费者无从区分，于是可能凭噪声把一个健康号踢出选号且不报错。
	// 无观测（tier 1）时本字段为 nil，调用方按 unknown 处理。
	Provenance *evidence.Fact `json:"provenance,omitempty"`
}

// RateLimitedModel 单个被限流模型的台账行（issue #36）。
type RateLimitedModel struct {
	Model string `json:"model"`
	// Kind 区分限流与模型不可用：6004 是 rate_limit，11102 是 model_unavailable。
	Kind string `json:"kind"`
	// Until 冷却到期时刻 = 该模型的独立冷却截止（modelCooldowns[m].Until，截断后），
	// 多模型限流时不再等于 Status.Until（账号级）。
	Until time.Time `json:"until,omitempty"`
	// ResetAt 上游「将在 … 重置」的原始墙钟（未经 soft_rate_max 截断）；
	// 截断后 Until==ResetAt 时省略 ResetAt 让台账自然减少一列。
	ResetAt time.Time `json:"reset_at,omitempty"`
	// Reason 触发原因（运维可读文案）。
	Reason string `json:"reason,omitempty"`
}

// modelCooldown 单个 (账号, 模型) 的模型级独立冷却记录。
// 承载两种「该模型在此账号上不可用」语义：
//   - 6004 模型级限流：Until 对齐上游重置墙钟；ResetAt 记录权威恢复时刻。
//   - 11102 该后端无此模型：Until 为指数退避 TTL（6h 起、封顶 24h）；Hits 记录
//     累计命中次数驱动退避（6004 无 hits 概念，Hits 恒 0）。
type modelCooldown struct {
	// Until 该模型的冷却截止（6004：now+min(resetAt-now, soft_rate_max)；11102：now+退避 TTL）。
	Until time.Time
	// ResetAt 上游「将在 … 重置」的原始墙钟（未经 soft_rate_max 截断）。
	// 与 Until 的区别同：Until 可能截断，ResetAt 是上游权威恢复时刻。
	// 11102 无重置文案，ResetAt 恒零值。
	ResetAt time.Time
	// Reason 触发原因（透出运维可读文案，同 Status.Reason）。
	Reason string
	// Hits 11102 负缓存的累计命中次数（驱动指数退避）。6004 条目 Hits 恒 0。
	Hits int
	// AuditOnly 为 true 时仅用于状态展示（例如无重置时间的 6004），
	// healthyForModel 与 modelExempt 必须忽略它，避免改变选号行为。
	AuditOnly bool
}

// modelCostTTL 成本观测的有效期。取 6 小时：既覆盖"夜间免费"这类时段性优惠的
// 单次会话，又不至于让昨天的价格决定今天的选择——过期的免费观测若永久有效，
// 白天会把已开始收费的号继续当成免费。
const modelCostTTL = 6 * time.Hour

// modelCostEntry 运行时成本账本（持久化镜像 stateModelCost 与其字段一一对应）。
type modelCostEntry struct {
	CostPer1k float64
	LastSeen  time.Time
	Samples   int
}
type entry struct {
	a            *auth.Auth
	credits      int64
	creditsTotal int64 // 积分总额度（UserResource 聚合；0 = 未知）
	// creditsExpiring 配置窗口内即将过期的可用积分子集，是 credits 的一部分。
	// creditsEarliestExpiry / creditsEarliestRemaining 是全部未来到期批次中的最早一批；
	// 两者均由签到/余额刷新更新，供 earliest-expiry 路由和状态观测使用。
	creditsExpiring          int64
	creditsEarliestExpiry    time.Time
	creditsEarliestRemaining int64
	successCount             int64      // 累计成功
	errTotal                 int64      // 累计错误（供成功率权重 successRate = successCount/(successCount+errTotal)，不清零）
	lastErr                  time.Time  // 最近一次错误时间
	lastSuccess              time.Time  // 最近一次成功时间
	tokenUsage               TokenUsage // 聊天请求 token 用量摘要（持久化）
	// lastCheckinDay 最近一次签到成功的本地日期（"2006-01-02"）。签到成功与上游
	// 幂等拒绝（"今天已签到"）都算；statusOf 据此输出 CheckinDone 供面板按钮显示
	// 签到/已签。持久化：跨重启不丢当日状态。
	lastCheckinDay string
	coolKind       CoolKind
	until          time.Time // 冷却截止（即时冷却：CoolSoft 429 / CoolHard 余额耗尽）
	disabled       bool
	// paused 暂停选号：与 disabled 正交。置位后退出选号候选（healthy 判否），
	// 但保号任务遍历只按 Disabled 过滤，故 paused 号天然继续参与签到 / 活跃上报 /
	// 保活 / 余额刷新。持久化（state.json），跨重启不丢。
	paused bool
	reason string
	// lastUsed 最近被选中时刻（防并发撞号）。atomic（unixnano，0=从未使用）：
	// pick 改读锁后（ADR-005 D-500）选中写回不再持写锁，普通字段在读锁下写入
	// 会成数据竞争；原子化后读锁路径可直接 Load/Store。运行态，不持久化。
	lastUsed atomic.Int64
	// usedSeq 单调递增的选中序号：每次被 pick 选中时取 p.pickSeq 自增值。
	// Windows 等平台 time.Now() 精度有限（~0.5ms），高并发/快速连续选号时多个
	// 账号 lastUsed 完全相等，基于 wall-clock 的 LRU/防惊群判定失效。
	// usedSeq 提供严格全序，与时间精度无关。运行态，不持久化。
	// atomic（同 lastUsed 口径，ADR-005 D-500）。
	usedSeq atomic.Uint64
	// breakerUntil / fails / retryCount 为熔断器运行态（不持久化）。
	// fails 是唯一的"连续失败"计数器：任何错误喂入，达到 breakerThreshold 触发熔断（指数退避），
	// 跨入口累计，成功/熔断/统一复活时清零（保留 retryCount 驱动退避指数）。
	breakerUntil time.Time // 熔断截止（指数退避）
	fails        int       // 连续失败计数（熔断用，唯一权威）
	retryCount   int       // 已熔断次数（指数退避的指数）
	// breakerProbeAt 最近一次 half-open 试探时刻（ADR-003 D-400）。
	// 熔断到期后不再一次性全量放回，而是每 breakerProbeGap 至多 1 次试探；
	// 本字段记录上次试探，供限速判定。运行态，不持久化（重启后首次到期即试探，
	// 语义安全：最坏多探一次）。atomic（unixnano，0=无试探），同 lastUsed 口径。
	breakerProbeAt atomic.Int64
	// lastPrune 上次清理模型级台账（modelCost / modelCooldowns）的时刻
	// （ADR-003 D-402）。pick 的写锁内每轮对全池每个 entry 扫这两个 map，
	// 实测占 pick 耗时 ~85%；改为按 pruneInterval 时间节流后，绝大多数 pick
	// 直接跳过扫描。运行态，不持久化。
	lastPrune time.Time
	// softStreak 连续软冷却次数（CoolSoft），独立于熔断器 fails 的**冷却域**计数器：
	// fails 会被熔断触发清零、且被 hard 冷却与 NoteError 污染，无法表达"连续软限流"。
	// 重置点只有两处（都是账号被证明恢复的时刻）：NoteSuccess、reviveCoolingLocked。
	// 持久化（stateAccount.SoftStreak）：重启后软限流仍在退避，不因重启回到基数。
	softStreak int
	// modelCooldowns 6004 模型级 limit 的**独立**冷却表：model → 该模型的冷却截止/重置。
	// 与 until（全账号级）正交：6004 只写本表、不写 until，因此多个模型同时 6004 时
	// 各自独立计时，互不覆盖（A 触发后 B 再触发，A 的冷却截止不被 B 覆盖——这是
	// 单 until 字段做不到的）。仅 6004 触发时记录；空 map = 无模型级限流（不豁免）。
	// 持久化（stateAccount.ModelCooldowns）：未过期条目跨重启恢复，否则 healthyForModel
	// 重启失忆、重新踩同一个 6004 雷区（与下方 stateModelCooldown 同一口径）。
	modelCooldowns map[string]modelCooldown
	// sessionDeadFails 连续 12153（ErrSessionDead）计数。12153 在真实环境会被临时性触发
	// （网络抖动/上游闪断/refresh 竞态），一次失败就永久禁用太粗暴——连续达到阈值才判死。
	// 持久化（stateAccount.SessionDeadFails）：上游持续 session dead 时重启归零会导致
	// 重学（再吃 2 次失败才禁用，期间每次都白打一轮上游）；清零点（refresh/chat 成功、
	// 手工复活）同样落盘，重启后不残留旧计数。
	sessionDeadFails int
	// consecutiveFails 连续失败计数（连败降权，issue #114）——「不知道原因的兜底」：
	// 覆盖 ErrClient（未知 4xx）与传输层失败（连不上上游）这类 applyErrorPolicy
	// default 分支不罚号的形态。与 sessionDeadFails 同构但独立计数：12153 的终态
	// 是 Disable，这里的终态是临时出池（degradeUntil）。清零点：NoteSuccess。
	// 持久化（stateAccount.ConsecutiveFails + DegradeUntil）：restart 归零会让
	// 「上游持续故障 + 频繁重启」的组合重新学满 5 次；degradeUntil 持久化让降权期
	// 重启不失忆（与 breakerUntil 同口径）。
	consecutiveFails int
	// degradeUntil 连败降权截止：非零且未到期时该账号不参与 normal 选号（临时
	// 出池）。与冷却/熔断**取更长者不叠加**（healthy 判定是并列或门，任一截止
	// 未到期即不可选，生效的一定是最远者），到期自动回池，无需显式复位。
	degradeUntil time.Time
	// modelCost 实测扣费账本：model → 观测。由每次成功请求的 usage.credit
	// 折算而来（上游没有"按模型的用量"接口，只能实测）。选号时据此把「该模型上
	// 免费/便宜的号」排在前面（pick 的 costTier 硬分层）。
	// 持久化（stateAccount.ModelCosts，P1-anti-monopoly）：重启后成本知识保留；
	// 落盘/恢复按 modelCostTTL 惰性过滤，陈旧观测不复活。
	modelCost map[string]modelCostEntry
	// inFlight 单账号在途请求数（运行态，不持久化）。用 atomic 避免 Pick 热路径拿写锁。
	inFlight atomic.Int64

	// EWMA 成功率/延迟信号（ADR-005 D-502）：把「最近表现」回灌选号权重，
	// 替代被删的终身累计成功率因子（errTotal 只增不减，早期出错会永久压权）。
	// 运行态，不持久化（重启回中性）；仅在 p.ewmaEnabled 且样本达
	// p.ewmaMinSamples 后参与加权（冷启动中性，不饿死新号）。
	// 全部在 p.mu 写锁路径（NoteSuccess/NoteError/NoteFailures/RecordTokenUsage）
	// 更新，pick 读锁路径只读，普通字段即可。
	ewmaSuccess        float64 // 成功率 EWMA：1=全成、0=全败（懒初始化 1.0：首个事件前视同从未失败；样本不足不参与）
	ewmaSuccessSamples int64   // 成功率事件样本数（成功/失败各 +1）
	ewmaLatencyMs      float64 // 每请求延迟 EWMA（毫秒；首个观测直接赋值）
	ewmaLatencySamples int64   // 延迟样本数
}

// EWMA 表现回灌的默认参数与因子边界（ADR-005 D-502）。
// 默认关闭（零回归）：需 config pool.ewma.enabled=true 才参与加权。
// cmd/server/config.go 里的 defaultEwmaAlpha/defaultEwmaMinSamples 必须与本组一致。
const (
	defaultEwmaAlpha      = 0.1 // 平滑系数：约 30~40 次观测收敛到稳态
	defaultEwmaMinSamples = 5   // 每账号样本下限：少于该值因子恒 1（冷启动中性）
	// 成功率因子边界：sr 高于基线加分、低于基线减分，线性映射并钳制在
	// [1-successPenaltyMax, 1+successBoostMax]——不可能把账号权重清零（饿死防护），
	// 也只温和拉开档位，不颠覆 credits/闲置/快过期的主导地位。
	ewmaSuccessBaseline   = 0.95
	ewmaSuccessBoostMax   = 0.15
	ewmaSuccessPenaltyMax = 0.15
	// 延迟因子边界：仅惩罚显著慢于池中位数的账号（> 中位数 × slowFactor），
	// 超阈值部分线性升到封顶；低于阈值不罚（对噪声稳健，快账号不额外加分，
	// 避免振荡与「快号越用越多」的正反馈）。
	ewmaLatencySlowFactor = 1.5
	ewmaLatencyPenaltyMax = 0.3
)

// lastUsedTime 最近选中时刻（0=从未使用 → 零值 time.Time）。
func (e *entry) lastUsedTime() time.Time {
	n := e.lastUsed.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// setLastUsed 记录最近选中时刻（原子写，pick 读锁路径与粘性路径共用）。
func (e *entry) setLastUsed(t time.Time) { e.lastUsed.Store(t.UnixNano()) }

// clearLastUsed 清空最近选中时刻（测试/复位用）。
func (e *entry) clearLastUsed() { e.lastUsed.Store(0) }

// probeAtTime 最近 half-open 试探时刻（0=无试探 → 零值 time.Time）。
func (e *entry) probeAtTime() time.Time {
	n := e.breakerProbeAt.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// setProbeAt 记录 half-open 试探时刻（原子写）。
func (e *entry) setProbeAt(t time.Time) { e.breakerProbeAt.Store(t.UnixNano()) }

// clearProbeAt 清空试探记录（NoteSuccess/Revive 随恢复清零）。
func (e *entry) clearProbeAt() { e.breakerProbeAt.Store(0) }

// noteEwmaSuccess 喂一次成功事件：sr 向 1 收敛（alpha 平滑）。
// 首个事件前懒初始化 sr=1.0（乐观初值）：健康新号从样本 1 起就保持满分，
// 不会被「从 0 慢速爬升」误罚（否则 minSamples 门槛内外的头几十个样本
// 都会低于基线 0.95 而被持续减权）。
// 调用方必须已持有 p.mu 写锁。
func (e *entry) noteEwmaSuccess(alpha float64) {
	if e.ewmaSuccessSamples == 0 {
		e.ewmaSuccess = 1.0
	}
	e.ewmaSuccess += alpha * (1 - e.ewmaSuccess)
	e.ewmaSuccessSamples++
}

// noteEwmaFailure 喂一次失败事件（NoteError/NoteFailures）：sr 向 0 收敛。
// 首个事件前同样懒初始化 sr=1.0（从未观测过 = 从未失败，乐观但可被后续失败
// 迅速拉低：1 次失败即 0.9、5 次 ≈0.59）。调用方必须已持有 p.mu 写锁。
func (e *entry) noteEwmaFailure(alpha float64) {
	if e.ewmaSuccessSamples == 0 {
		e.ewmaSuccess = 1.0
	}
	e.ewmaSuccess -= alpha * e.ewmaSuccess
	e.ewmaSuccessSamples++
}

// noteEwmaLatency 喂一次请求延迟观测（毫秒）。首个观测直接赋值（避免从 0 慢速
// 爬升）。调用方必须已持有 p.mu 写锁。
func (e *entry) noteEwmaLatency(alpha float64, ms int64) {
	if e.ewmaLatencySamples == 0 {
		e.ewmaLatencyMs = float64(ms)
	} else {
		e.ewmaLatencyMs += alpha * (float64(ms) - e.ewmaLatencyMs)
	}
	e.ewmaLatencySamples++
}

// modelCostOf 返回该账号在指定 model 上的有效成本观测；无观测或观测过期返回 ok=false。
func (e *entry) modelCostOf(model string, now time.Time) (modelCostEntry, bool) {
	if model == "" || len(e.modelCost) == 0 {
		return modelCostEntry{}, false
	}
	mc, ok := e.modelCost[model]
	if !ok {
		return modelCostEntry{}, false
	}
	if mc.LastSeen.IsZero() || now.Sub(mc.LastSeen) > modelCostTTL {
		return modelCostEntry{}, false // 过期：时段性优惠（夜间免费）不得跨时段生效
	}
	return mc, true
}

// healthy 报告账号当前是否可选（未禁用、未处于任一冷却/熔断/连败降权期）。
// 连败降权与冷却/熔断同入本判定（取更长者不叠加：三个截止是并列的或门，
// 只要任一未到期即不可选，天然「并存取更远者」——不需要显式比较长短）。
func (e *entry) healthy(now time.Time) bool {
	if e.disabled || e.paused {
		return false
	}
	if !e.until.IsZero() && now.Before(e.until) {
		return false
	}
	if !e.breakerUntil.IsZero() && now.Before(e.breakerUntil) {
		return false
	}
	if !e.degradeUntil.IsZero() && now.Before(e.degradeUntil) {
		return false
	}
	return true
}

// modelExempt 报告账号是否处于「6004 模型级软冷却」形态：存在任一有效的 6004
// 模型级冷却（modelCooldowns 非空），且尚未禁用、未熔断。
// 此形态下账号仅对限流中的模型不可用，对其他模型仍可选（issue #31）。
// healthyForModel 与 ServableNow 共用本谓词，保证 chat 选号与探活口径一致。
// 调用方负责 now 与冷却有效性的判断（本方法只看形态，不看冷却是否已过期）。
func (e *entry) modelExempt() bool {
	if e.disabled || e.paused || !e.until.IsZero() || !e.degradeUntil.IsZero() || !e.breakerUntil.IsZero() {
		return false
	}
	for _, mc := range e.modelCooldowns {
		if !mc.AuditOnly && !mc.Until.IsZero() {
			return true
		}
	}
	return false
}

// modelCooled 报告账号对指定 model 是否正处 6004 模型级冷却（该模型的独立冷却未过期）。
// 空 reqModel / 未记录 → false（不因模型级维度限制账号）。
func (e *entry) modelCooled(now time.Time, reqModel string) bool {
	if reqModel == "" {
		return false
	}
	mc, ok := e.modelCooldowns[reqModel]
	if !ok || mc.AuditOnly {
		return false
	}
	return !mc.Until.IsZero() && now.Before(mc.Until)
}

// healthyForModel 报告账号对指定 model 是否可选（含 6004 模型级独立冷却判定）：
//   - disabled → 永不可选（最高优先级）；
//   - 该模型正处 6004 独立冷却（modelCooldowns[reqModel] 未过期）→ 不可选
//     （多模型限流时各自独立，互不影响）；
//   - 否则 → 回落到账号级 healthy（until/breakerUntil 维度）。
//
// 对比旧实现（softRateModel 单字段豁免"仅锁一个模型、其他豁免"），新语义天然支持
// 任意多个模型同时限流：被 B 限流的账号对 A 请求仍可选（A 不在 modelCooldowns 拦截
// 且账号级 healthy 成立）。空 reqModel / 未记录模型 → 等价 healthy。
func (e *entry) healthyForModel(now time.Time, reqModel string) bool {
	if e.disabled || e.paused {
		return false
	}
	if e.modelCooled(now, reqModel) {
		// 该模型在 6004 独立冷却中 → 不可选。
		return false
	}
	// 账号级冷却/熔断先判；若未冷却则由账号级健康决定。
	return e.healthy(now)
}

// pruneInterval 模型级台账的时间节流间隔（ADR-003 D-402）。
//
// 实测（pick_prune_bench_test.go，50 账号各 10 条台账）：每轮 pick 扫这两个 map
// 占 pick 耗时的 **~85%**（16.6µs vs 2.4µs），且全部发生在 pick 的写锁内——
// 而选号是每请求必经、写锁是全池唯一串行点。
//
// 过期条目在**读路径天然被跳过**（modelCostOf / status 都判有效期），晚删一轮对
// 正确性零影响，只是内存多占一会儿。故按时间节流：每 entry 每 interval 至多真扫一次。
// 选 10s：被清理对象的时间尺度是小时级（modelCostTTL）/ 分钟级（冷却 Until），
// 10s 的删除延迟相对可忽略。
const pruneInterval = 10 * time.Second

// pruneExpiredModelCosts 删除 modelCost 中已过期的条目（惰性清理，时间节流）。
//
// 为什么必须有它：modelCost 只在**读**（modelCostOf）、**落盘**（persist）、
// **恢复**（persist）、**status**（state.go）四处做"过期过滤"，内存里的条目本身
// 从不回收——即「map 只增不减」。本函数补上真正的删除（与
// pruneExpiredModelCooldowns 同调用点：pick 写锁路径）。
//
// 快路径（任一即返回）：空 map｜距上次清理不足 pruneInterval。
// 调用方必须已持有 p.mu 写锁。
//
// 返回**本次真正删掉的模型名**：数据没了但知识也没了——调用方据此把该
// (账号,模型) 的成本未知重新打开（见 pool.reopenCostUnknown）。
func (e *entry) pruneExpiredModelCosts(now time.Time) []string {
	if len(e.modelCost) == 0 {
		return nil // 空 map：绝大多数账号的常态，零成本
	}
	if !e.lastPrune.IsZero() && now.Sub(e.lastPrune) < pruneInterval {
		return nil // 时间节流：同一 entry 每 interval 至多真扫一次
	}
	e.lastPrune = now
	var expired []string
	for m, mc := range e.modelCost {
		if mc.LastSeen.IsZero() || now.Sub(mc.LastSeen) > modelCostTTL {
			delete(e.modelCost, m)
			expired = append(expired, m)
		}
	}
	return expired
}

// pruneExpiredModelCooldowns 删除 modelCooldowns 中已过期的条目（惰性清理，时间节流）。
// pick 写锁路径与 revive 调用，防止 map 无限膨胀；status 只读遍历天然跳过过期项。
// 与 pruneExpiredModelCosts 共用 lastPrune：后者先被调用并做节流判定，
// 本函数只判空 map（避免同一轮 pick 判两次）。
// 调用方必须已持有 p.mu 写锁。
func (e *entry) pruneExpiredModelCooldowns(now time.Time) {
	if len(e.modelCooldowns) == 0 {
		return // 空 map：多数账号从无 6004，零成本
	}
	for m, mc := range e.modelCooldowns {
		if mc.Until.IsZero() || !now.Before(mc.Until) {
			delete(e.modelCooldowns, m)
		}
	}
}

// expiry 返回账号当前仍在生效的最近冷却/熔断/降权截止时间（三个截止取最早者）；不在冷却期返回零值。
// 供全冷却兜底选取"最早到期"账号用。连败降权计入兜底口径：降权号参与兜底（其失败
// 形态是「不知道原因」，到期放行半开试探正是兜底语义——CoolHard 才被排除）。
func (e *entry) expiry(now time.Time) time.Time {
	var t time.Time
	if !e.until.IsZero() && now.Before(e.until) {
		t = e.until
	}
	if !e.breakerUntil.IsZero() && now.Before(e.breakerUntil) {
		if t.IsZero() || e.breakerUntil.Before(t) {
			t = e.breakerUntil
		}
	}
	if !e.degradeUntil.IsZero() && now.Before(e.degradeUntil) {
		if t.IsZero() || e.degradeUntil.Before(t) {
			t = e.degradeUntil
		}
	}
	return t
}

// fallbackKind 报告兜底账号属于哪一类冷却（soft：即时软冷却/连败降权；breaker：熔断期）。
// 只对参与兜底的账号调用（CoolHard 已被 pickEarliestExpiryLocked 排除）。判定口径：
// 若熔断截止是当前生效的最近截止（含"仅有熔断无软冷却"），记为 breaker；否则记为 soft
// （连败降权与软冷却同归 soft：都按各自截止到期放行，兜底处置无差异）。
func (e *entry) fallbackKind(now time.Time) string {
	if !e.breakerUntil.IsZero() && now.Before(e.breakerUntil) {
		if e.until.IsZero() || !now.Before(e.until) || e.breakerUntil.Before(e.until) {
			return "breaker"
		}
	}
	return "soft"
}

// stateAccount 单个账号的持久化状态（JSON tag 全小写下划线，向后兼容：缺字段零值）。
type stateAccount struct {
	Credits      int64     `json:"credits"`
	CreditsTotal int64     `json:"credits_total,omitempty"`
	Disabled     bool      `json:"disabled"`
	Paused       bool      `json:"paused,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	Until        time.Time `json:"until,omitempty"`
	CoolKind     CoolKind  `json:"cool_kind"`
	SuccessCount int64     `json:"success_count,omitempty"`
	// err_total 累计错误计数。旧版 err_count（连续错误）仍可读：加载时映射到 err_total，
	// 仅作一次性迁移，不再回写 err_count。
	ErrTotal    int64     `json:"err_total,omitempty"`
	ErrCount    int       `json:"err_count,omitempty"` // 兼容旧文件的迁移源，仅读取
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastErr     time.Time `json:"last_err,omitempty"`
	// LastCheckinDay 最近一次签到成功的本地日期（entry.lastCheckinDay 同源）。
	// 持久化以保留「当日已签」状态：签到后重启，面板按钮不回退成「签到」。
	LastCheckinDay string     `json:"last_checkin_day,omitempty"`
	TokenUsage     TokenUsage `json:"token_usage,omitempty"`
	// 运行态计数（soft_streak/session_dead_fails/credits_expiring）不用 omitempty：
	// 零值缺失会让人误以为"没记录"，实际是零值被省略。
	SoftStreak int `json:"soft_streak"`
	// SessionDeadFails 连续 12153 计数（判定 session 死亡的进度）。持久化以保留
	// 「重启后连续计数继续累计」。
	SessionDeadFails int `json:"session_dead_fails"`
	// ConsecutiveFails 连续失败计数（连败降权进度，见 entry.consecutiveFails）。
	ConsecutiveFails int `json:"consecutive_fails"`
	// DegradeUntil 连败降权截止（issue #114）。仅未过期才持久化（落盘/恢复均惰性
	// 过滤），避免降权期重启失忆；过期/零值不写。指针语义同 BreakerUntil。
	DegradeUntil *time.Time `json:"degrade_until,omitempty"`
	// BreakerUntil 熔断截止（指数退避）。仅未过期才持久化（落盘/恢复均惰性过滤），
	// 避免熔断期重启失忆：breakerUntil 在未来时重启后仍阻断选号。过期/零值不写。
	// 用 *time.Time（而非 time.Time）：Go 的 omitempty 对非指针 time.Time 的零值
	// 不生效（会序列化成 0001-01-01T00:00:00Z）；指针 nil 才能真正被 omitempty 省略，
	// 与落盘"过期不写"的口径一致。
	BreakerUntil *time.Time `json:"breaker_until,omitempty"`
	// RetryCount 已熔断次数（指数退避的指数）。持久化以保留"越熔越长"的退避累积——
	// 重启归零会让反复熔断只从最小退避开始。恢复时若 BreakerUntil 已过期则归零。
	RetryCount int `json:"retry_count,omitempty"`
	// CreditsExpiring 快过期积分子集（credits 的子集）。持久化以保留到期路由
	// （最早到期路由）的偏好——重启后到下次签到之间不应失忆。
	CreditsExpiring int64 `json:"credits_expiring"`
	// CreditsEarliestExpiry / CreditsEarliestRemaining 最早未来到期批次及剩余量。
	// 与 CreditsExpiring 一并持久化，重启后首次请求仍可沿用最近一次余额快照。
	CreditsEarliestExpiry    time.Time `json:"credits_earliest_expiry,omitempty"`
	CreditsEarliestRemaining int64     `json:"credits_earliest_remaining,omitempty"`
	// ModelCooldowns 模型级独立冷却表（model → 冷却记录：6004 重置墙钟 / 11102
	// 负缓存退避）。持久化：6004 对齐上游重置墙钟后单模型冷却可长达数小时，
	// 跨重启是常态；不持久化会导致 healthyForModel 重启失忆、重新踩雷区。
	// 恢复时惰性过滤已过期条目。
	ModelCooldowns map[string]stateModelCooldown `json:"model_cooldowns,omitempty"`
	// ModelCosts 实测扣费账本（model → 单价观测，见 entry.modelCost）。持久化
	// （P1-anti-monopoly）：重启后成本知识保留，限免/夜间免费的跨重启窗口不再
	// 重新付学费探测。落盘/恢复均按 modelCostTTL 惰性过滤（6h 外不写不恢复——
	// 陈旧价格不复活）；恢复侧剔除非法值（负 per1k/零 LastSeen 的结构破损条目）。
	ModelCosts map[string]stateModelCost `json:"model_costs,omitempty"`
}

// stateModelCooldown 单个 (账号, 模型) 的模型级独立冷却持久化记录，与运行态
// modelCooldown 同构（Until/ResetAt/Reason 字段名与语义对齐），落盘/恢复往返无损。
// Hits 不落盘（重启后 11102 退避从 6h 基数重新学习，同 modelCost 口径）。
type stateModelCooldown struct {
	Until     time.Time `json:"until"`
	ResetAt   time.Time `json:"reset_at,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	AuditOnly bool      `json:"audit_only,omitempty"`
}

// stateModelCost 单个 (账号, 模型) 的成本观测持久化记录，与运行态 modelCostEntry
// 同构（单一表示，内存与落盘不搞两套）。
type stateModelCost struct {
	CostPer1k float64   `json:"cost_per_1k"`
	LastSeen  time.Time `json:"last_seen"`
	Samples   int       `json:"samples,omitempty"`
}

// stateFileVersion 是 state.json 的当前格式版本（ADR-009 同族闸门）。
// 缺 version 字段的老文件按 0 读入 → 视为旧版、正常加载（零迁移成本）。
// 版本 > 本值 → 拒绝启动（旧程序读新文件会按零值丢账号状态，静默且不可逆）。
const stateFileVersion = 1

// stateFile 持久化格式。
type stateFile struct {
	Version  int                     `json:"version"`
	Accounts map[string]stateAccount `json:"accounts"`
}

// flushInterval 后台落盘周期。
const (
	defaultBreakerThreshold   = 3
	defaultBreakerCooldown    = 30 * time.Minute
	defaultBreakerCooldownMax = 6 * time.Hour
)

// defaultSoftRateMax 软冷却指数退避的默认封顶：softRateMax 未注入（<=0）时按此值算，
// 避免测试/裸用池时退避无上限。
const defaultSoftRateMax = 2 * time.Hour

// defaultDegrade* 连败降权默认参数（issue #114）：连败 5 次临时出池 10 分钟。
const (
	defaultDegradeThreshold   = 5
	defaultDegradeCooldown    = 10 * time.Minute
	defaultDegradeCooldownMax = 2 * time.Hour
)

// defaultCostExploreInterval costTier 条件探索的默认窗口（issue #136 方案 a′）。
// 取 30m：≤ 48 次/天/模型 的探索上限算术（24h/30m=48），与池规模和 QPS 无关。
// 探索=搭车改道（把一个既有真实用户请求改道给 tier 1 号），零新增上游请求；
// 增量成本只是「该请求本可打免费号、实际打了可能收费的号」的期望计费差，
// 且 tier 1 枯竭（活跃模型全号已学）后税基收敛到 0。config 显式 "0" 关停。
const defaultCostExploreInterval = 30 * time.Minute

// sessionDeadThreshold 连续 ErrSessionDead（12153）达到该次数才永久禁用。
// 12153 会被临时性触发（网络抖动/上游闪断/refresh 竞态），一次失败即禁用的旧行为
// 会误杀健康账号（P0-1：13 个 disabled 号全是误判）。3 次连续才判死：容忍偶发抖动，
// 又不会让真正的死 session 留在池里反复被选中。
const sessionDeadThreshold = 3

// sessionDeadReason 12153 判定为 session 死亡时的持久化 reason。
const sessionDeadReason = "12153 session dead"

// SessionDeadThreshold 暴露连续 12153 的禁用阈值（供 scheduler 日志/运维文档引用）。
func SessionDeadThreshold() int { return sessionDeadThreshold }

// softStreakShiftMax 软冷却退避的最大左移位数（防 1<<streak 溢出成负数/零）。
// 无论 streak 累积多少，封顶逻辑总会先生效，此值只是溢出兜底。
const softStreakShiftMax = 16

// StoreSnapshotter 池状态快照镜像的最小接口（redisstore.Store 满足；Noop 空实现安全）。
// 与本地 state.json 并存，作启动恢复备份：快照比本地新才采用，否则本地优先。
const (
	defaultIdleWeightPerHour = 0.5
	defaultIdleWeightMax     = 5.0
)

// New 构建池；stateFp 非空时尝试加载旧状态，并启动后台周期性落盘 goroutine。
