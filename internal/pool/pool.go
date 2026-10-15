// Pool 账号池核心：结构定义、构造（New/Set* 注入）、在途租约（Acquire/Release）
// 与账号增删（Add/SyncToDir/upsertLocked）。选号/冷却/状态/持久化见同包其他文件。
package pool

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/unknown"
)

type Pool struct {
	mu      sync.RWMutex
	byUID   map[string]*entry
	stateFp string
	dirty   atomic.Bool // 内存有变更待落盘
	// store 池状态快照镜像（redisstore.Store）；nil = 无需镜像（未配置 Redis / Noop 之外也可能 nil）。
	// SaveState/LoadState 经它接线，与本地 state.json 并存作启动恢复备份。
	store StoreSnapshotter
	// 熔断器调优（SetBreaker 注入；默认值见 defaultBreaker*）。
	breakerThreshold   int
	breakerCooldown    time.Duration
	breakerCooldownMax time.Duration
	// softRateMax 软冷却指数退避的封顶（SetSoftRateMax 注入；默认 defaultSoftRateMax）。
	softRateMax time.Duration
	// costExploreInterval costTier 条件探索窗口（issue #136 方案 a′，SetCostExploreInterval
	// 注入；默认 defaultCostExploreInterval 30m）。tier 0 垄断 + tier 1 存在且距上次
	// 探索 ≥ 窗口时，本次 pick 生效层切 tier 1-only（探索=搭车改道，零新增上游请求）。
	// 0 = 关停（完全回到现状行为）。
	costExploreInterval time.Duration
	// exploreLast 各 (realm, 模型) 的上次探索时刻，键 = realm + "\x1f" + model。
	// 运行态（不持久化，同 lastUsed/usedSeq 口径）：重启归零 → 每个仍冻结的
	// (域, 模型) 多至 1 次即时重探；已毕业号经 ModelCosts 恢复 tier，学费不重付。
	// 只在探索事件时写入（tier 1 枯竭期间停走，陈旧无害）；不做对称清理。
	// exploreMu（ADR-005 D-500）：pick 改读锁后 map 不能在读锁下写；且「窗口
	// 判定 + 置位」必须原子（否则并发 pick 重复探索），独立小锁避免把写锁拉回
	// 热路径。读侧（CostExploreStatus）同样走 exploreMu。
	exploreMu   sync.Mutex
	exploreLast map[string]time.Time
	// costExploreEvents 累计探索事件数（/status 透出；exploreMu 保护）。
	costExploreEvents int64
	// degradeThreshold / degradeCooldown / degradeCooldownMax 连败降权参数
	// （SetDegrade 注入；默认值见 defaultDegrade*，issue #114）。
	degradeThreshold   int
	degradeCooldown    time.Duration
	degradeCooldownMax time.Duration
	// creditFloor 积分保底（SetCreditFloor 注入；0 = 关闭，缺省即现状）。
	// 账号 credits < floor 时不再参与选号——防止收费请求把余额打穿、连免费模型都
	// 402 冷却到次日签到。签到回血（SetCreditsDetailed）越过 floor 即自动恢复。
	// 全池触底且无免费模型可接时选号返回 nil（硬语义：宁 503 不打穿）。
	// 收费与否的判据见 floorBlockedForModel：本地实测台账优先，缺失时用上游目录
	// 倍率（modelRateOf 注入）兜底，避免「无观测的高价新模型」绕过保底。
	creditFloor int64
	// modelRateOf 按 (realm, 模型) 查上游目录积分倍率（"0.79" / "" = 未知）。
	// 由 main 用 upstream.Client.ModelRate 注入——pool 不依赖 upstream 包（避免
	// 循环依赖与分层破坏），nil 时倍率兜底不生效（退化为仅本地台账判定）。
	// 仅在持 p.mu 时由 floorBlockedForModel 调用；回调不得反向调用 Pool 方法。
	modelRateOf func(realm, model string) string
	// 加权路由的闲置补偿调优（SetWeights 注入；默认值见 defaultIdle*）。
	idleWeightPerHour float64
	idleWeightMax     float64
	// preferExpiring 最早到期优先路由开关（默认 true）。开启且快过期窗口内存在有效
	// 批次时，选号在成本层内先按最早到期排序；关闭后只使用普通加权路由。
	preferExpiring bool
	// ewmaEnabled / ewmaAlpha / ewmaMinSamples EWMA 表现回灌参数（ADR-005 D-502，
	// SetEwma 注入；默认关闭 = 零回归）。ewmaEnabled 为 false 时权重因子恒 1，
	// 与现状逐位一致；alpha/minSamples 见 entry.go defaultEwma*。
	ewmaEnabled    bool
	ewmaAlpha      float64
	ewmaMinSamples int64
	// maxInFlight 单账号最大在途请求数；0 = 不限（租约关闭）。
	maxInFlight int
	// maxInFlightGlobal global 域单账号在途上限分档（WAF 403 修复 P1-1：global 域
	// WAF 风控更紧，压低并发）；0 = 未设置，回落 maxInFlight（不分档，零回归）。
	maxInFlightGlobal int
	// randInt64N 仅供测试注入确定性随机源；nil 时用 math/rand/v2 全局源。
	// 生产代码不应设置此字段。
	randInt64N func(n int64) int64
	// breakerProbeGap 熔断到期后的 half-open 试探间隔（ADR-003 D-400）：
	// 熔断号到期后不再一次性全量放回，而是每 gap 至多放行 1 次试探；成功即全量恢复。
	// 0 = 关闭 half-open（退化为既有的「到期即全量」行为，零回归）。
	breakerProbeGap time.Duration
	// globalMaxInFlight 全局在途上限（ADR-003 D-401）：所有账号在途请求总数上限，
	// 超限立即 429 不排队。0 = 不限（默认，零回归）。经 globalInFlight 原子计数判定。
	globalMaxInFlight int
	// globalInFlight 全局在途原子计数（ADR-003 D-401）：Acquire 成功 +1、Release -1，
	// 严格配对。与每账号 inFlight 并存（后者是 per-account 上限，本字段是全局上限）。
	globalInFlight atomic.Int64
	// persistFails 本地 state.json 连续落盘失败计数（仅 saveLocked 在持锁下读写，无需 atomic）。
	// 用于落盘失败的日志节流：首败/每 N 次提醒/恢复各打一条，避免磁盘满时刷屏。
	persistFails int
	// pickSeq 单调递增的选号序号：每次 pick 选中账号时自增并记到 entry.usedSeq，
	// 为 LRU 兜底/防惊群提供与 time.Now() 精度无关的严格全序（Windows ~0.5ms 精度下
	// lastUsed 墙钟会全等）。atomic（ADR-005 D-500）：pick 改读锁后由读锁路径
	// Add(1) 分配序号，普通字段跨读锁写会成数据竞争。
	pickSeq atomic.Uint64
	// stickyPaidHits 粘性命中"已实测收费号且池里有免费/未知号"的累计次数。
	// 纯观测：不自动换号（保上游 prompt 缓存），只让面板看到"哪些会话钉在收费号上"。
	stickyPaidHits atomic.Int64
	// stopCh 关闭信号：Close 关闭它使 startFlusher 的后台 goroutine 退出。
	// nil = 未启动 flusher（stateFp 为空时 New 不起 flusher）。
	stopCh chan struct{}
	// closeOnce 保证 Close 幂等（多次调用不重复 close channel）。
	closeOnce sync.Once

	// unknownReg 未知条目注册表（可选；nil = 不跟踪未知，零回归）。
	// 见 SetUnknownRegistry / TrackCostUnknown / resolveCostUnknown。
	unknownReg *unknown.Registry
	// unknownSeen "uid|model" → 该组合在 registry 里的条目 ID。
	//
	// 为什么用 sync.Map 而不是复用 p.mu：TrackCostUnknown 在**请求路径**上被调用，
	// 目的是「同一组合只登记一次」。用 p.mu 会让这个幂等检查与选号争锁（ADR-005
	// 特意把 pick 改成读锁以避免串行化）；sync.Map 的 LoadOrStore 是原子且不碰
	// p.mu，命中路径只有一次原子读。
	unknownSeen sync.Map
}

// defaultBreaker* 熔断器默认参数（FreeBuff2API 参考口径）。
func New(stateFp string) *Pool {
	p := &Pool{
		byUID:              map[string]*entry{},
		stateFp:            stateFp,
		breakerThreshold:   defaultBreakerThreshold,
		breakerCooldown:    defaultBreakerCooldown,
		breakerCooldownMax: defaultBreakerCooldownMax,
		idleWeightPerHour:  defaultIdleWeightPerHour,
		idleWeightMax:      defaultIdleWeightMax,
		preferExpiring:     true,
		// EWMA 表现回灌（ADR-005 D-502）：默认关闭（零回归），SetEwma 从 config
		// 注入后生效；alpha/minSamples 的默认值供未注入（测试/嵌入式）场景兜底。
		ewmaAlpha:          defaultEwmaAlpha,
		ewmaMinSamples:     defaultEwmaMinSamples,
		degradeThreshold:   defaultDegradeThreshold,
		degradeCooldown:    defaultDegradeCooldown,
		degradeCooldownMax: defaultDegradeCooldownMax,
		// 探索缺省 30m：tier 0 垄断下的 tier 1 探索窗口（issue #136）。用户经
		// config 显式 "0" 关停（SetCostExploreInterval(0)）。
		costExploreInterval: defaultCostExploreInterval,
		exploreLast:         map[string]time.Time{},
	}
	if stateFp != "" {
		p.load()
		p.startFlusher()
	}
	return p
}

// Close 停止后台落盘 goroutine 并做最后一次落盘（幂等）。
// 进程退出前调用，消除 startFlusher 的 goroutine 泄漏；不调用也不影响正确性
// （进程退出即回收），仅是生命周期卫生。
func (p *Pool) Close() {
	if p.stopCh == nil {
		return
	}
	p.closeOnce.Do(func() {
		close(p.stopCh)
	})
	p.Flush()
}

// SetBreaker 注入熔断器参数（main 从 config 解析后调用）。非正值保留原值（用默认）。
func (p *Pool) SetBreaker(threshold int, cooldown, cooldownMax time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if threshold > 0 {
		p.breakerThreshold = threshold
	}
	if cooldown > 0 {
		p.breakerCooldown = cooldown
	}
	if cooldownMax > 0 {
		p.breakerCooldownMax = cooldownMax
	}
}

// SetEwma 注入 EWMA 表现回灌参数（ADR-005 D-502，main 从 config 解析后调用）。
// enabled=false 时权重因子恒 1（完全回到现状行为）；alpha 非 (0,1] 保留默认；
// minSamples<=0 保留默认。热生效（与 pool.* 其余 Set* 同口径）。
func (p *Pool) SetEwma(enabled bool, alpha float64, minSamples int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ewmaEnabled = enabled
	if alpha > 0 && alpha <= 1 {
		p.ewmaAlpha = alpha
	}
	if minSamples > 0 {
		p.ewmaMinSamples = int64(minSamples)
	}
}

// SetBreakerProbeGap 注入熔断 half-open 试探间隔（ADR-003 D-400）。
// 0（或负）= 关闭 half-open，退化为「到期即全量放回」的既有行为。
func (p *Pool) SetBreakerProbeGap(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d < 0 {
		d = 0
	}
	p.breakerProbeGap = d
}

// SetGlobalMaxInFlight 注入全局在途上限（ADR-003 D-401）。0（或负）= 不限。
func (p *Pool) SetGlobalMaxInFlight(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n < 0 {
		n = 0
	}
	p.globalMaxInFlight = n
}

// GlobalInFlight 返回当前全局在途请求数（ADR-003 D-401，/status 与准入判定用）。
func (p *Pool) GlobalInFlight() int64 {
	return p.globalInFlight.Load()
}

// InFlightTotal 返回全部账号的在途请求总数（/metrics 的账号维度 gauge）。
//
// 与 GlobalInFlight 的区别：GlobalInFlight 是**网关级**在途（含尚未选到号的
// 请求），本方法是**账号级**在途之和（选到号并 Acquire 成功的那些）。两者都
// 有观测价值：前者回答「网关整体负载」，后者回答「账号池被压到什么程度」。
func (p *Pool) InFlightTotal() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var total int64
	for _, e := range p.byUID {
		total += e.inFlight.Load()
	}
	return total
}

// AcquireGlobal 全局准入判定（ADR-003 D-401）：未配置上限恒放行；否则在
// 「当前在途 < 上限」时占用一个名额并返回 true。占用成功后**必须**配对调用
// ReleaseGlobal（handler 用 defer 保证）。
//
// 为什么用 CAS 循环而不是先读后写：并发下「读-判-写」会超发（多个 goroutine
// 同时读到 n-1 都以为还有名额）。CAS 保证不超发，且无需锁。
func (p *Pool) AcquireGlobal() bool {
	limit := p.globalMaxInFlight
	if limit <= 0 {
		return true // 未配置上限：恒放行（零回归）
	}
	for {
		cur := p.globalInFlight.Load()
		if cur >= int64(limit) {
			return false
		}
		if p.globalInFlight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// ReleaseGlobal 释放一个全局在途名额（与 AcquireGlobal 严格配对）。
// 未配置上限时是空操作（因为 AcquireGlobal 没有真的占名额）。
func (p *Pool) ReleaseGlobal() {
	if p.globalMaxInFlight <= 0 {
		return
	}
	// 防御性下溢保护：正常配对下不会负；异常路径（重复 Release）只记不崩。
	if v := p.globalInFlight.Add(-1); v < 0 {
		p.globalInFlight.Store(0)
		log.Printf("WARN: [pool] globalInFlight 下溢（Release 不配对？）已钳 0")
	}
}

// SetSoftRateMax 注入软冷却指数退避的封顶时长（main 从 config 解析后调用）。
// 非正值保留原值（用默认 2h），风格同 SetBreaker。
func (p *Pool) SetSoftRateMax(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d > 0 {
		p.softRateMax = d
	}
}

// SetCostExploreInterval 注入 costTier 条件探索窗口（main 从 config 解析后调用，
// issue #136）。0 = 关停（完全回到现状行为）；正值覆盖默认 30m。
// 注意：与 SetSoftRateMax「非正值保留默认」不同，0 在这里是**合法值**（关停开关，
// 与 config 的 "0" 关停语义对齐）——不设 0 语义就无法关停探索。
func (p *Pool) SetCostExploreInterval(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if d < 0 {
		return // 负值非法，保留现值
	}
	p.costExploreInterval = d
}

// CostExploreStatus 透出探索台账（/status 用）：累计探索事件数 + 各 (域, 模型)
// 的最近探索时刻（键内 \x1f 分隔符输出为 "|"，与 model_costs 行对照即可读出
// 「探索→毕业」全链路）。RLock 只读遍历；map 大小受「服务过的 (域, 模型)」集合
// 约束（与 modelCost 同界，天然有界）。
func (p *Pool) CostExploreStatus() (events int64, last map[string]time.Time) {
	p.exploreMu.Lock()
	defer p.exploreMu.Unlock()
	last = make(map[string]time.Time, len(p.exploreLast))
	for k, ts := range p.exploreLast {
		// 键 realm+"\x1f"+model → 输出 "|"（JSON 安全可读；\x1f 不可打印）。
		last[strings.ReplaceAll(k, "\x1f", "|")] = ts
	}
	return p.costExploreEvents, last
}

// SetWeights 注入加权路由的闲置补偿参数。非正值保留原值（用默认）。
func (p *Pool) SetWeights(idlePerHour, idleMax float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idlePerHour > 0 {
		p.idleWeightPerHour = idlePerHour
	}
	if idleMax > 0 {
		p.idleWeightMax = idleMax
	}
}

// SetPreferExpiring 注入最早到期优先路由开关（main 从 config 解析后调用）。
func (p *Pool) SetPreferExpiring(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preferExpiring = enabled
}

// SetDegrade 注入连败降权参数（main 从 config 解析后调用，issue #114）。
// 非正值保留原值（用默认，见 defaultDegrade*），风格同 SetBreaker/SetSoftRateMax。
func (p *Pool) SetDegrade(threshold int, cooldown, cooldownMax time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if threshold > 0 {
		p.degradeThreshold = threshold
	}
	if cooldown > 0 {
		p.degradeCooldown = cooldown
	}
	if cooldownMax > 0 {
		p.degradeCooldownMax = cooldownMax
	}
}

// CreditFloor 透出生效的积分保底值（/status 用）。0 = 关闭。
func (p *Pool) CreditFloor() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.creditFloor
}

// SetCreditFloor 注入积分保底线（main 从 config 解析后调用）。
// 0 = 关闭（缺省即现状，零回归）；负值非法保留原值（0）。
// 语义见 Pool.creditFloor 字段注释。
func (p *Pool) SetCreditFloor(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= 0 {
		p.creditFloor = n
	}
}

// SetModelRateOf 注入上游目录积分倍率查表（main 用 upstream.Client.ModelRate 装配）。
// 供积分保底兜底判定「未实测过的模型是否收费」——本地台账无观测时，不能因为
// 「没学过」就放行，否则高价新模型会把触底号一次性打穿（kimi-k3-1 实案：
// 全池无观测 → 保底全部放行 → 两笔扣 111 分打穿到 0 并硬冷却到次日 04:00）。
// fn 可为 nil（清注入）；回调只在持 p.mu 时被调用，不得反向调用 Pool 方法。
func (p *Pool) SetModelRateOf(fn func(realm, model string) string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.modelRateOf = fn
}

// SetMaxInFlight 注入单账号最大在途请求数；0 = 不限。负值保留原值。
func (p *Pool) SetMaxInFlight(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= 0 {
		p.maxInFlight = n
	}
}

// SetMaxInFlightGlobal 注入 global 域单账号在途上限（WAF 403 修复 P1-1 分档）；
// 0 = 未设置，global 账号回落 maxInFlight（不分档）。负值保留原值。
func (p *Pool) SetMaxInFlightGlobal(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n >= 0 {
		p.maxInFlightGlobal = n
	}
}

// inFlightLimit 报告账号的生效在途上限（global 分档优先，回落 maxInFlight）；
// 0 = 不限。调用方需已持 p.mu（或快照过 limit，见 Acquire）。
func (p *Pool) inFlightLimit(e *entry) int {
	if p.maxInFlightGlobal > 0 && e.a.Realm() == "global" {
		return p.maxInFlightGlobal
	}
	return p.maxInFlight
}

// SetStore 注入池状态快照镜像（redisstore.Store）。nil 表示不镜像（纯本地恢复）。
// 必须在 SyncToDir 之前调用，使"择新恢复"发生在账号对齐之前。
func (p *Pool) SetStore(s StoreSnapshotter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store = s
}

// RestoreFromSnapshot 择新恢复：比较本地 state.json 与 Redis 快照，采用较新者。
// 无快照、快照无 savedAt、或本地不存在/不可读时，都会被判定为"本地优先/跳过快照"，
// 同时打一条恢复来源日志。必须在 SyncToDir 之前调用（SyncToDir 只增删不入值）。
// Acquire 为 uid 占一个在途名额（会话粘性命中后调用）；池上限内返回 true。
// 名额用 entry.inFlight 原子自增，满额返回 false。上限按账号 realm 分档
// （global 档 maxInFlightGlobal，P1-1；未设置回落 maxInFlight）。
func (p *Pool) Acquire(uid string) bool {
	p.mu.RLock()
	e, ok := p.byUID[uid]
	if !ok {
		p.mu.RUnlock()
		return false
	}
	limit := p.inFlightLimit(e)
	p.mu.RUnlock()
	if limit <= 0 {
		// 不限：计数仍累加（供状态观测），但永不拒绝。
		e.inFlight.Add(1)
		return true
	}
	for {
		cur := e.inFlight.Load()
		if cur >= int64(limit) {
			return false
		}
		if e.inFlight.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Release 释放一个在途名额。幂等减到 0 为止（防重复释放扣成负数）。
func (p *Pool) Release(uid string) {
	p.mu.RLock()
	e, ok := p.byUID[uid]
	p.mu.RUnlock()
	if !ok {
		return
	}
	for {
		cur := e.inFlight.Load()
		if cur <= 0 {
			return
		}
		if e.inFlight.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

// SetRandomSource 仅供测试注入确定性随机源；生产代码不应调用。
// 注入源取 n∈[0,n) 后，pickWeighted 的抽签结果完全可预测。
func (p *Pool) SetRandomSource(fn func(n int64) int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.randInt64N = fn
}

// Add 加入账号；已存在则保留原状态、更新凭证（upsert 单账号，不影响其他账号）。
func (p *Pool) Add(a *auth.Auth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.upsertLocked(a)
}

// SyncToDir 用最新扫描结果对齐池：新账号加入、消失的账号剔除（状态保留）。
// 剔除结果持久化回 state.json，避免已删账号在下次启动时被 load() 复活。
func (p *Pool) SyncToDir(auths []*auth.Auth) {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := make(map[string]bool, len(auths))
	for _, a := range auths {
		seen[a.UID] = true
		p.upsertLocked(a)
	}
	changed := false
	for uid := range p.byUID {
		if !seen[uid] {
			delete(p.byUID, uid)
			changed = true
		}
	}
	if changed {
		p.saveLocked()
	}
}

// Remove 从池中移除账号并立即落盘（管理面板用）。返回被移除账号的凭证
// （含 FilePath，供调用方删除 auth 文件）；uid 不存在返回 nil。
// 在途请求的 Release 对已删条目是 no-op，无需等待。
func (p *Pool) Remove(uid string) *auth.Auth {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return nil
	}
	delete(p.byUID, uid)
	p.dirty.Store(true)
	p.saveLocked()
	return e.a
}

// upsertLocked 更新或插入单个账号；已存在则只换凭证、保留 credits/cooling 状态。
// 调用方必须已持有 p.mu；Add 与 SyncToDir 共用此 upsert 逻辑。
func (p *Pool) upsertLocked(a *auth.Auth) {
	if e, ok := p.byUID[a.UID]; ok {
		e.a = a // 保留 credits/cooling 状态
		return
	}
	p.byUID[a.UID] = &entry{a: a}
}

// Pick 返回 healthy 中积分最高的账号；无可用返回 nil。
