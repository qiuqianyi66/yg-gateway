// attempts.go 重试放大率直方图 + 慢请求计数器（F-305）。
//
// 为什么不直接复用 Histogram（毫秒分桶）：放大率的观测单位是「次数」（整数），
// 不是毫秒。固定整数分桶与毫秒直方图是两种语义，硬塞进毫秒桶会让 le=10ms 的
// 桶位含义变成「≤10 次尝试」这种谁也读不懂的口径。
//
// 与 Histogram 同哲学：零第三方依赖、每桶一个 atomic.Int64、热路径零分配
// （Observe 只做一次桶定位 + 一次原子加）。
package metrics

import (
	"sync"
	"sync/atomic"
)

// attemptsBounds 单请求上游尝试次数的分桶上界（整数，升序）；末尾隐含 +Inf。
//
// 取值理由：放大率要回答的是「1 次（无换号重试）vs >1 次（发生重试/换号）」
// 的分布形状，不是精确均值——桶刻意粗。正常请求 1 次；MaxRotate 默认 3，
// 换号重试大多落在 2~3 次；4 次以上已是异常放大（账号池大面积抖动），尾部分
// 辨率无价值。
var attemptsBounds = []int64{1, 2, 3, 4, 6, 8}

// attemptsBucketCount 桶总数（含 +Inf）。写成显式常量而不是 len()+1：
// 数组类型 [attemptsBucketCount]T 的长度必须是编译期常量，切片 len 不是
// （同 bucketCount 的理由）。
const attemptsBucketCount = 7 // = len(attemptsBounds) + 1

// 编译期自检：AttemptsHistogram.buckets 长度必须与边界数同步。
var _ = [1]struct{}{}[attemptsBucketCount-(len(attemptsBounds)+1)]

// AttemptsHistogram 固定整数分桶的「单请求上游尝试次数」直方图。零值可用。
type AttemptsHistogram struct {
	// buckets[i] 是落在 (bounds[i-1], bounds[i]] 的样本数；最后一桶是 +Inf。
	buckets [attemptsBucketCount]atomic.Int64
	// total = 样本 attempts 之和（放大率均值分子）；count = 样本数。
	total atomic.Int64
	count atomic.Int64
}

// Observe 记录一次收口观测（n = 该用户请求实际触发的上游尝试次数）。
func (h *AttemptsHistogram) Observe(n int) {
	if h == nil {
		return
	}
	v := int64(n)
	if v < 0 {
		v = 0
	}
	h.total.Add(v)
	h.count.Add(1)
	h.buckets[attemptsIndex(v)].Add(1)
}

// attemptsIndex 返回 v 落在哪个桶（线性查找；桶极少，L1 缓存友好）。
func attemptsIndex(v int64) int {
	for i, b := range attemptsBounds {
		if v <= b {
			return i
		}
	}
	return len(attemptsBounds) // +Inf 桶
}

// Count 样本总数。
func (h *AttemptsHistogram) Count() int64 {
	if h == nil {
		return 0
	}
	return h.count.Load()
}

// Sum 样本 attempts 之和（放大率均值分子）。
func (h *AttemptsHistogram) Sum() int64 {
	if h == nil {
		return 0
	}
	return h.total.Load()
}

// BucketCounts 返回各桶（非累计）当前计数快照（测试用；非严格一致，对观测足够）。
func (h *AttemptsHistogram) BucketCounts() []int64 {
	if h == nil {
		return nil
	}
	out := make([]int64, attemptsBucketCount)
	for i := range out {
		out[i] = h.buckets[i].Load()
	}
	return out
}

// AttemptsStats 重试放大率注册表（全局 + 按模型；与 Registry 同范式）。
//
// 为什么允许模型标签：模型数有界（上游目录几十个），与 Registry.byModel 同原则；
// 上限 defaultMaxModels 防御伪造 model 名的无界增长。UID 绝不做标签（R2）。
type AttemptsStats struct {
	mu        sync.RWMutex
	global    AttemptsHistogram
	byModel   map[string]*AttemptsHistogram
	maxModels int
}

// NewAttemptsStats 构造注册表。
func NewAttemptsStats() *AttemptsStats {
	return &AttemptsStats{byModel: map[string]*AttemptsHistogram{}, maxModels: defaultMaxModels}
}

// Observe 记录一次收口观测。model 为空时只记全局。
func (s *AttemptsStats) Observe(model string, attempts int) {
	if s == nil {
		return
	}
	s.global.Observe(attempts)
	if model == "" {
		return
	}
	if h := s.modelHist(model); h != nil {
		h.Observe(attempts)
	}
}

// Total 全部请求的上游尝试总次数（放大率分子：配合 requests_total 算均值）。
func (s *AttemptsStats) Total() int64 {
	if s == nil {
		return 0
	}
	return s.global.total.Load()
}

// Global 返回全局直方图本体（Prometheus 暴露用：需要各桶计数而非均值）。
func (s *AttemptsStats) Global() *AttemptsHistogram {
	if s == nil {
		return nil
	}
	return &s.global
}

// modelHist 取（必要时创建）模型直方图（同 Registry.modelHist 的 RLock-then-Lock）。
func (s *AttemptsStats) modelHist(model string) *AttemptsHistogram {
	s.mu.RLock()
	h, ok := s.byModel[model]
	s.mu.RUnlock()
	if ok {
		return h
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.byModel[model]; ok {
		return h
	}
	if len(s.byModel) >= s.maxModels {
		return nil // 超上限：丢弃该模型维度（全局仍记），防无界增长
	}
	h = &AttemptsHistogram{}
	s.byModel[model] = h
	return h
}

// ModelSnapshot 返回按模型的直方图快照（非严格一致；仅供 /metrics 暴露）。
func (s *AttemptsStats) ModelSnapshot() map[string]*AttemptsHistogram {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]*AttemptsHistogram, len(s.byModel))
	for k, v := range s.byModel {
		out[k] = v
	}
	return out
}

// Counter 无标签单值计数器（F-305 慢请求信号）。
//
// 为什么不单列文件：慢请求就这一个全局信号，与 AttemptsHistogram 同批引入；
// 一个原子计数器不值得占一个文件名额。无标签是刻意的——模型维度已在
// request_duration_ms 直方图里有，慢请求计数只服务「超过阈值几次」这一个告警信号。
type Counter struct{ n atomic.Int64 }

// NewCounter 构造计数器。
func NewCounter() *Counter { return &Counter{} }

// Inc 加 1（nil 安全：未接线时零开销丢弃）。
func (c *Counter) Inc() {
	if c != nil {
		c.n.Add(1)
	}
}

// Value 当前计数。
func (c *Counter) Value() int64 {
	if c == nil {
		return 0
	}
	return c.n.Load()
}
