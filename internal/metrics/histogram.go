// Package metrics 轻量固定分桶延迟直方图（ADR-003 D-403）。
//
// 为什么自实现而不是引 hdrhistogram：项目哲学是「最少依赖」（当前仅 12 个模块），
// 而固定分桶对本场景完全够用——延迟量级跨 4 个数量级（1ms~120s），需要的是
// 「P95 比 P50 慢多少」这种量级判断，不是纳秒级精度。不为一个可自实现的观测
// 组件引入新依赖。
//
// 设计要点：
//   - 桶边界固定（指数 + 细化混合），覆盖网关关心的 10ms~120s；
//   - 分位数估算用**桶内线性插值**（不是取桶上界——那会系统性高估）；
//   - 每桶一个 atomic.Int64，累加无锁；读取时逐桶原子读（快照非严格一致，
//     对观测足够——分位数不需要跨桶快照一致性）；
//   - 零分配热路径（Add 只做一次原子加）。
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// bucketBoundsMs 桶上界（毫秒），升序。最后隐含一个 +Inf 桶（索引 len(bounds)）。
//
// 取值理由：网关实测延迟分布——正常 200ms~3s、慢 5~30s、超时 60~120s。
// 低端加密（10/25/50/100ms）是为了看清「快速请求」的占比；
// 高端到 120s（覆盖 header_timeout 默认 60s 与 idle 300s 前的量级）。
var bucketBoundsMs = []int64{
	10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000,
}

// bucketCount 桶总数（含 +Inf）。写成显式常量而不是 len()+1：
// 数组类型 [bucketCount]T 的长度必须是编译期常量，len(slice) 不是常量。
const bucketCount = 14 // = len(bucketBoundsMs) + 1

// Histogram 一个延迟直方图。零值可用（无需构造）。
type Histogram struct {
	// buckets[i] 是落在 (bounds[i-1], bounds[i]] 的样本数；最后一个桶是 +Inf。
	// 用 atomic 是因为 Add 会在多个请求 goroutine 上并发调用。
	buckets [bucketCount]atomic.Int64
	// sumMs / count 用于算均值与校验（count 与 sum(buckets) 应相等）。
	sumMs atomic.Int64
	count atomic.Int64
}

// 编译期自检：bucketCount 必须等于边界数 + 1（改动 bucketBoundsMs 时若忘记
// 同步 bucketCount，这里直接编译失败，而不是运行时数组越界）。
var _ = [1]struct{}{}[len(bucketBoundsMs)+1-bucketCount]

// Add 记录一次观测（毫秒）。热路径：一次桶定位 + 一次原子加，零分配。
func (h *Histogram) Add(d time.Duration) {
	if h == nil {
		return
	}
	ms := d.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	h.sumMs.Add(ms)
	h.count.Add(1)
	h.buckets[bucketIndex(ms)].Add(1)
}

// bucketIndex 返回 ms 落在哪个桶（线性查找；桶少（14 个）且有界，
// 二分收益可忽略，而线性对 L1 缓存更友好）。
func bucketIndex(ms int64) int {
	for i, b := range bucketBoundsMs {
		if ms <= b {
			return i
		}
	}
	return len(bucketBoundsMs) // +Inf 桶
}

// Count 样本总数。
func (h *Histogram) Count() int64 {
	if h == nil {
		return 0
	}
	return h.count.Load()
}

// MeanMs 均值（毫秒）。无样本返回 0。
func (h *Histogram) MeanMs() float64 {
	if h == nil {
		return 0
	}
	n := h.count.Load()
	if n == 0 {
		return 0
	}
	return float64(h.sumMs.Load()) / float64(n)
}

// Quantile 估算分位数（q ∈ [0,1]）。无样本返回 0。
//
// 估算方法：找到含第 k 个样本的桶（k = q*n），在该桶的**上下界之间线性插值**。
//   - 桶内假设均匀分布（无更好信息时的标准做法）；
//   - 上界用桶的上界（bucketBoundsMs[i]），+Inf 桶用前一桶上界的 2 倍估算
//     （避免返回无穷大）；
//   - 首桶下界取 0。
//
// 为什么不取桶上界：那会系统性高估（快请求全被算成桶上界），
// 让 P50 看起来比实际慢——而面板要回答的正是"典型请求多快"。
func (h *Histogram) Quantile(q float64) float64 {
	if h == nil {
		return 0
	}
	n := h.count.Load()
	if n == 0 {
		return 0
	}
	if q <= 0 {
		q = 0
	}
	if q >= 1 {
		q = 1
	}
	target := int64(q * float64(n))
	if target < 1 {
		target = 1
	}
	if target > n {
		target = n
	}
	var cum int64
	for i := 0; i < bucketCount; i++ {
		c := h.buckets[i].Load()
		if cum+c >= target {
			// 目标落在本桶。桶内线性插值。
			lower := float64(0)
			if i > 0 {
				lower = float64(bucketBoundsMs[i-1])
			}
			upper := bucketUpperMs(i)
			if c <= 0 {
				return upper // 空桶（理论不可达）：退化为上界
			}
			// 桶内位置：目标在本桶的第 (target-cum) 个样本（1-based）
			frac := float64(target-cum) / float64(c)
			if frac > 1 {
				frac = 1
			}
			return lower + (upper-lower)*frac
		}
		cum += c
	}
	// 理论不可达（cum 应等于 n）；防御性返回 +Inf 桶上界。
	return bucketUpperMs(bucketCount - 1)
}

// bucketUpperMs 桶 i 的上界（+Inf 桶用前一桶的 2 倍估算，避免无穷）。
func bucketUpperMs(i int) float64 {
	if i < len(bucketBoundsMs) {
		return float64(bucketBoundsMs[i])
	}
	// +Inf 桶：用最后边界 ×2 作为"典型上界"（更精确需要更细的桶，不值得）。
	return float64(bucketBoundsMs[len(bucketBoundsMs)-1]) * 2
}

// Snapshot 一次读取的分位数快照（面板 / /status 用）。
// Histogram 返回本快照对应的直方图本体（Prometheus 暴露用：/metrics 要的是
// 各桶样本数，而 Snapshot 只有分位数——同一份数据两种口径，故给个回指）。
//
// 回指安全：RegistrySnapshot 由 Registry.Snapshot() 构造，那里持有的是
// *Histogram 指针本身（不是拷贝），因此这里的 h 始终指向活对象——抓取时读到的
// 是「抓取时刻」的桶计数（原子读，快照非严格一致，对指标足够）。
type Snapshot struct {
	Count  int64   `json:"count"`
	MeanMs float64 `json:"mean_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P90Ms  float64 `json:"p90_ms"`
	P95Ms  float64 `json:"p95_ms"`
	P99Ms  float64 `json:"p99_ms"`
	MaxMs  float64 `json:"max_ms"`

	hist *Histogram
}

// UnsafeHistogram 返回底层直方图指针（仅供同包 /metrics 暴露使用）。
func (s Snapshot) UnsafeHistogram() *Histogram { return s.hist }

// Snapshot 返回分位数快照。MaxMs 用最高非空桶的上界（近似——精确 max 需要
// 额外记录，而分桶的价值在分位数不在精确极值）。
func (h *Histogram) Snapshot() Snapshot {
	if h == nil {
		return Snapshot{}
	}
	return Snapshot{
		Count:  h.Count(),
		MeanMs: round1(h.MeanMs()),
		P50Ms:  round1(h.Quantile(0.50)),
		P90Ms:  round1(h.Quantile(0.90)),
		P95Ms:  round1(h.Quantile(0.95)),
		P99Ms:  round1(h.Quantile(0.99)),
		MaxMs:  round1(h.maxMs()),
		hist:   h,
	}
}

// maxMs 最高非空桶的上界。
func (h *Histogram) maxMs() float64 {
	for i := bucketCount - 1; i >= 0; i-- {
		if h.buckets[i].Load() > 0 {
			return bucketUpperMs(i)
		}
	}
	return 0
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// ---------------------------------------------------------------------------
// Registry：全局 + 按模型两个维度
// ---------------------------------------------------------------------------

// Registry 延迟观测注册表（全局 + 按模型）。
//
// 为什么只做「全局 + 按模型」：模型数有界（上游目录几十个），账号数也有界，
// 但账号维度对"网关整体快不快"的答案贡献小——按模型才能回答"哪个模型慢"。
// 按账号按需再加（避免一开始就承担无界增长风险）。
type Registry struct {
	global Histogram
	// byModel 模型名 → 直方图。模型名来自上游目录（有界），但为防御异常输入
	// （伪造 model 名打请求）设上限。
	//
	// 锁策略：读多写极少——已有 model 的读走 RLock，只在"首次见到该 model"时
	// 才取写锁创建。稳态下热路径几乎无锁竞争（Add 本身是原子的，不经锁）。
	mu        sync.RWMutex
	byModel   map[string]*Histogram
	maxModels int
}

// defaultMaxModels byModel 的条目上限（防御伪造 model 名导致的无界增长）。
const defaultMaxModels = 200

// NewRegistry 构造注册表。
func NewRegistry() *Registry {
	return &Registry{byModel: map[string]*Histogram{}, maxModels: defaultMaxModels}
}

// Observe 记录一次请求延迟（毫秒级）。model 为空时只记全局。
//
// 并发安全：global 用原子桶；byModel 的 map 本身需要保护，但用 RWMutex 会让
// 热路径有锁开销——这里用「首次写入时加锁创建，之后只操作已有 *Histogram」
// 的模式（Go map 读在无写入时是安全的；写入只发生在首次见到的 model）。
func (r *Registry) Observe(model string, d time.Duration) {
	if r == nil {
		return
	}
	r.global.Add(d)
	if model == "" {
		return
	}
	if h := r.modelHist(model); h != nil {
		h.Add(d)
	}
}

// modelHist 取（必要时创建）模型直方图。
func (r *Registry) modelHist(model string) *Histogram {
	r.mu.RLock()
	h, ok := r.byModel[model]
	r.mu.RUnlock()
	if ok {
		return h
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.byModel[model]; ok {
		return h
	}
	if len(r.byModel) >= r.maxModels {
		return nil // 超上限：丢弃该模型维度（全局仍记），防无界增长
	}
	h = &Histogram{}
	r.byModel[model] = h
	return h
}

// Global 返回全局直方图本体（Prometheus 暴露用：需要各桶计数而非分位数）。
// 返回的是活对象指针，调用方只做原子读（Histogram.Add 本身无锁）。
func (r *Registry) Global() *Histogram {
	if r == nil {
		return nil
	}
	return &r.global
}

// ---------------------------------------------------------------------------
// KindCounters：按离散标签计数（上游错误分类等）
// ---------------------------------------------------------------------------

// KindCounters 按标签计数的计数器集合。
//
// 为什么需要上界：标签值来自上游错误分类（有界枚举），但防御异常输入导致无界
// 增长（与 Registry.byModel 同原则）。超上界后新标签**丢弃计数**——宁可少一个
// 标签维度，也不让内存随输入无界膨胀。
type KindCounters struct {
	mu      sync.Mutex
	m       map[string]*atomic.Int64
	maxKeys int
}

// defaultMaxKinds 标签上限。
const defaultMaxKinds = 64

// NewKindCounters 构造标签计数器集合。
func NewKindCounters() *KindCounters {
	return &KindCounters{m: map[string]*atomic.Int64{}, maxKeys: defaultMaxKinds}
}

// Inc 给 key 加 1。key 为空按 "(none)" 计（避免空标签无法区分「未分类」与「缺失」）。
func (k *KindCounters) Inc(key string) {
	if k == nil {
		return
	}
	if key == "" {
		key = "(none)"
	}
	if c := k.counter(key); c != nil {
		c.Add(1)
	}
}

// counter 取（必要时创建）key 的计数器；超上界返回 nil（丢弃该标签的计数）。
func (k *KindCounters) counter(key string) *atomic.Int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	if c, ok := k.m[key]; ok {
		return c
	}
	if len(k.m) >= k.maxKeys {
		return nil
	}
	c := &atomic.Int64{}
	k.m[key] = c
	return c
}

// Snapshot 返回当前各标签计数（快照非严格一致，对指标足够）。
func (k *KindCounters) Snapshot() map[string]int64 {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]int64, len(k.m))
	for key, c := range k.m {
		out[key] = c.Load()
	}
	return out
}

// Snapshot 返回全量快照：全局 + 各模型。
func (r *Registry) Snapshot() RegistrySnapshot {
	if r == nil {
		return RegistrySnapshot{}
	}
	r.mu.RLock()
	models := make(map[string]*Histogram, len(r.byModel))
	for k, v := range r.byModel {
		models[k] = v
	}
	r.mu.RUnlock()

	out := RegistrySnapshot{
		Global:  r.global.Snapshot(),
		ByModel: make(map[string]Snapshot, len(models)),
	}
	for k, h := range models {
		out.ByModel[k] = h.Snapshot()
	}
	return out
}

// RegistrySnapshot 全量延迟快照。
type RegistrySnapshot struct {
	Global  Snapshot            `json:"global"`
	ByModel map[string]Snapshot `json:"by_model,omitempty"`
}
