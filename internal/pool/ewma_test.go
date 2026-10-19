// EWMA 表现回灌测试（ADR-005 D-502）。
package pool

import (
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestEwmaDisabledRegression ewma 关闭（默认）时 routingWeightOf 与旧 weightOf
// 逐位一致——「默认关闭 = 零回归」的守门测试。去掉 factor 门槛逻辑（恒 1）即 FAIL。
func TestEwmaDisabledRegression(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 100, 0)
	p.mu.RLock()
	defer p.mu.RUnlock()
	e := p.byUID["u1"]
	if w := p.routingWeightOf(e, 100, time.Now(), 1234); w != p.weightOf(e, 100, time.Now()) {
		t.Fatalf("ewma 关闭时 factor 必须恒 1：routing=%v weight=%v", w, p.weightOf(e, 100, time.Now()))
	}
}

// TestEwmaWeightFactorNeutralColdStart 样本不足 → 成功率/延迟因子都恒 1（不惩罚
// 新号/没流量号）。把 min-samples 门槛去掉 → 0 样本账号被当 sr=1.0 加分 → 本测试 FAIL。
func TestEwmaWeightFactorNeutralColdStart(t *testing.T) {
	p := New("")
	p.SetEwma(true, 0.1, 5)
	e := &entry{}
	if f := p.ewmaWeightFactor(e, 1000); f != 1.0 {
		t.Fatalf("0 样本成功率因子应恒 1, got %v", f)
	}
	e.noteEwmaLatency(0.1, 5000)
	if f := p.ewmaWeightFactor(e, 1000); f != 1.0 {
		t.Fatalf("延迟样本不足时因子应恒 1, got %v", f)
	}
}

// TestEwmaSuccessFactorBounds 成功率因子边界：sr=1 → +15%，sr=0 → -15%，单调。
// 钳制失效（sr=0 把权重清零）→ 本测试 FAIL（饿死防护守门）。
// 浮点比较用容差（±1e-6）：边界值本身是二进制不精确的 0.95/0.15 运算结果。
func TestEwmaSuccessFactorBounds(t *testing.T) {
	p := New("")
	p.SetEwma(true, 0.1, 1)
	warm := func(sr float64) *entry {
		e := &entry{ewmaSuccessSamples: 10}
		// 直接把信号拨到目标值（因子只读信号值，与观测历史无关）。
		e.ewmaSuccess = sr
		return e
	}
	closeEnough := func(got, want float64) bool { return got >= want-1e-6 && got <= want+1e-6 }
	if f := p.ewmaWeightFactor(warm(1.0), 0); !closeEnough(f, 1.15) {
		t.Fatalf("sr=1 factor 应为 1.15, got %v", f)
	}
	if f := p.ewmaWeightFactor(warm(0.0), 0); !closeEnough(f, 0.85) {
		t.Fatalf("sr=0 factor 应为 0.85, got %v", f)
	}
	if f := p.ewmaWeightFactor(warm(0.95), 0); !closeEnough(f, 1.0) {
		t.Fatalf("sr=基线 factor 应为 1.0, got %v", f)
	}
	// 单调：sr 越高 factor 越高。
	lo, mid, hi := p.ewmaWeightFactor(warm(0.5), 0), p.ewmaWeightFactor(warm(0.8), 0), p.ewmaWeightFactor(warm(1.0), 0)
	if !(lo < mid && mid < hi) {
		t.Fatalf("成功率因子应随 sr 单调：lo=%v mid=%v hi=%v", lo, mid, hi)
	}
}

// TestEwmaLatencyFactor 延迟因子：仅惩罚 > 中位数 × 1.5 的慢账号，线性升到 -30% 封顶；
// 低于阈值不罚（含等于阈值）。封顶失效 → 本测试 FAIL。
func TestEwmaLatencyFactor(t *testing.T) {
	p := New("")
	p.SetEwma(true, 0.1, 1)
	warm := func(ms float64) *entry {
		e := &entry{ewmaLatencySamples: 10, ewmaLatencyMs: ms}
		return e
	}
	median := 1000.0
	if f := p.ewmaWeightFactor(warm(1000), median); f != 1.0 {
		t.Fatalf("等于中位数不罚, got %v", f)
	}
	if f := p.ewmaWeightFactor(warm(1499), median); f != 1.0 {
		t.Fatalf("低于慢阈值不罚, got %v", f)
	}
	// ratio=2 → pen=(2-1.5)/1.5*0.3=0.1 → 0.9
	if f := p.ewmaWeightFactor(warm(2000), median); f < 0.89 || f > 0.91 {
		t.Fatalf("2× 中位数 factor 应为 ~0.9, got %v", f)
	}
	// ratio=4 → pen=0.5 → 封顶 0.3 → 0.7（不归零：饿死防护）
	if f := p.ewmaWeightFactor(warm(4000), median); f < 0.69 || f > 0.71 {
		t.Fatalf("4× 中位数 factor 应封顶 0.7, got %v", f)
	}
	if f := p.ewmaWeightFactor(warm(4000), 0); f != 1.0 {
		t.Fatalf("median=0（无池基准）不罚, got %v", f)
	}
}

// TestEwmaFeedsFromNotes 喂入契约：NoteSuccess 抬 sr、NoteError/NoteFailures 压 sr、
// RecordTokenUsage 喂延迟（首个观测直接赋值）。任一条喂入未生效 → 本测试 FAIL。
func TestEwmaFeedsFromNotes(t *testing.T) {
	p := New("")
	p.SetEwma(true, 0.1, 5)
	p.Add(&auth.Auth{UID: "u1"})

	p.NoteSuccess("u1")
	p.mu.RLock()
	sr := p.byUID["u1"].ewmaSuccess
	p.mu.RUnlock()
	if sr != 1.0 || p.byUID["u1"].ewmaSuccessSamples != 1 {
		t.Fatalf("首次成功 sr 应保持 1.0 samples=1, got sr=%v", sr)
	}

	p.NoteError("u1")
	p.mu.RLock()
	sr = p.byUID["u1"].ewmaSuccess
	p.mu.RUnlock()
	if sr >= 1.0 {
		t.Fatalf("NoteError 后 sr 应下降, got %v", sr)
	}
	if want := 1 - 0.1; sr != want {
		t.Fatalf("NoteError 后 sr 应为 %v, got %v", want, sr)
	}

	p.NoteFailures("u1")
	p.mu.RLock()
	sr = p.byUID["u1"].ewmaSuccess
	p.mu.RUnlock()
	if want := 0.9 - 0.1*0.9; sr != want {
		t.Fatalf("NoteFailures 后 sr 应为 %v, got %v", want, sr)
	}

	p.RecordTokenUsage("u1", TokenUsageDelta{HasLatencyMs: true, LatencyMs: 300})
	p.RecordTokenUsage("u1", TokenUsageDelta{HasLatencyMs: true, LatencyMs: 500})
	p.mu.RLock()
	lat := p.byUID["u1"].ewmaLatencyMs
	p.mu.RUnlock()
	if lat != 320 { // 300 + 0.1*(500-300)
		t.Fatalf("延迟 EWMA 应为 320, got %v", lat)
	}
}

// TestEwmaPickShiftsWeight 加权生效：两号 credits/闲置相同（初始齐用，idle 均 0），
// 坏号喂满失败样本后权重低于好号，确定性 r=0 恒选好号；但坏号权重不为零，
// r=末尾仍可选中（不饿死）。
func TestEwmaPickShiftsWeight(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.SetEwma(true, 0.1, 5)
	p.Add(&auth.Auth{UID: "good"})
	p.Add(&auth.Auth{UID: "bad"})
	p.SetCredits("good", 100, 0)
	p.SetCredits("bad", 100, 0)
	// 初始齐用：两号 idle 补偿相等（避免首轮后闲置补偿反超，干扰权重对比）。
	p.mu.Lock()
	p.byUID["good"].setLastUsed(time.Now())
	p.byUID["bad"].setLastUsed(time.Now())
	p.mu.Unlock()
	// 坏号 5 成 5 败（交替，绝不触发熔断：fails 每轮被成功清零，峰值 1 < 阈值 3）：
	// sr = 0.9^5 ≈ 0.59049，因子 ≈ 0.943。喂 20 连败会让熔断器直接出池，
	// EWMA 因子根本轮不到生效——本测试要的是「低于硬门槛的间歇失败」信号。
	for i := 0; i < 5; i++ {
		p.NoteSuccess("bad")
		p.NoteError("bad")
	}
	// 权重对比：好号 factor=1（0 样本中性），坏号 factor<1。
	p.mu.RLock()
	wGood := p.routingWeightOf(p.byUID["good"], 100, time.Now(), 0)
	wBad := p.routingWeightOf(p.byUID["bad"], 100, time.Now(), 0)
	p.mu.RUnlock()
	if wGood <= wBad {
		t.Fatalf("坏号不应高于好号：good=%v bad=%v", wGood, wBad)
	}
	if wBad <= 0 {
		t.Fatalf("坏号权重不得清零（饿死防护）：bad=%v", wBad)
	}

	p.SetRandomSource(func(n int64) int64 { return 0 })
	for i := 0; i < 10; i++ {
		if got := p.Pick(); got == nil || got.UID != "good" {
			t.Fatalf("iter %d: r=0 应恒选好号, got %+v", i, got)
		}
	}
	// r=total-1 → 命中坏号：惩罚未清零，坏号仍可被选中（不饿死）。
	p.SetRandomSource(func(n int64) int64 { return n - 1 })
	got := p.Pick()
	if got == nil || got.UID != "bad" {
		t.Fatalf("r=末尾应命中坏号（证明未饿死）, got %+v", got)
	}
}

// TestEwmaMedianLatency 池延迟中位数：仅统计达标样本，少于 2 个达标候选返回 0
// （因子中性）。中位数口径错误（如用均值）→ 慢号惩罚被取消 → 本测试 FAIL。
func TestEwmaMedianLatency(t *testing.T) {
	p := New("")
	p.SetEwma(true, 0.1, 5)
	for _, u := range []string{"a", "b", "c"} {
		p.Add(&auth.Auth{UID: u})
	}
	p.mu.Lock()
	for i, ms := range []int64{1000, 1000, 5000} {
		e := p.byUID[[]string{"a", "b", "c"}[i]]
		e.ewmaLatencySamples = 5
		e.ewmaLatencyMs = float64(ms)
	}
	cands := []*entry{p.byUID["a"], p.byUID["b"], p.byUID["c"]}
	median := p.poolMedianLatencyMs(cands)
	p.mu.Unlock()
	if median != 1000 {
		t.Fatalf("中位数应为 1000, got %v", median)
	}
	p.mu.Lock()
	cands = cands[:2] // 只剩 2 个达标候选，仍有中位数
	median = p.poolMedianLatencyMs(cands)
	p.mu.Unlock()
	if median != 1000 {
		t.Fatalf("2 候选时中位数应为 1000, got %v", median)
	}
	p.mu.Lock()
	for _, e := range []*entry{p.byUID["a"], p.byUID["b"]} {
		e.ewmaLatencySamples = 1 // 低于 minSamples → 不计入
	}
	median = p.poolMedianLatencyMs(cands)
	p.mu.Unlock()
	if median != 0 {
		t.Fatalf("无达标样本应返回 0（中性）, got %v", median)
	}
}

// TestEwmaLatencyPenalizesSlowInPick 延迟惩罚经 pick 生效：三号 credits 相同、
// 时间冻结，权重严格可排序；慢号（5s vs 中位 1s）被罚后 r=0 恒选快号；r=末尾仍可
// 命中慢号（惩罚未清零、不饿死）。
//
// 为什么改成注入时钟（T5 治 flake）：旧版靠 time.Now() 的**闲置补偿差**来打破
// fast/mid 的权重并列，而 Windows 的 time.Now() 精度约 1ms——setup 恰好跨毫秒
// 边界时（约 1/8 概率）三个 lastUsed 落在不同毫秒：pick 会把中选号（fast）的
// lastUsed 刷新到 now，于是第 2 轮起 mid 的闲置补偿反超 fast → r=0 选中 mid →
// FAIL（实测 -count=20 必现）。修法是**消除时间依赖**而不是放宽断言：SetClock
// 把时间冻结在 base（闲置补偿恒 0），并列只由 pick 的稳定兜底（UID 字典序）决出。
//
// 牙齿（三处断言各自能红）：
//  1. 删掉 ewmaWeightFactor 的延迟惩罚分支 → slow 的未罚权重 11.5 反超 fast/mid 的
//     11（slow 久置 1h 的补偿刻意留着，见下）→ 加权断言与两条 pick 断言 FAIL；
//  2. 把 pick/pickWeighted 的 p.now() 改回 time.Now()（注入时钟失效）→ 写回的
//     lastUsed 不等于冻结时刻 → lastUsed 断言 FAIL（不需要等墙钟漂移到「恰好出错」）。
func TestEwmaLatencyPenalizesSlowInPick(t *testing.T) {
	withNoPickGap(t)
	// 冻结时钟：now 恒为 base，fast/mid 的 lastUsed 也是 base → 闲置补偿恒 0。
	// base 取哪个时刻无关紧要（只有「冻结」这件事有意义）；不要改成 time.Now()。
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	p := New("")
	p.SetClock(func() time.Time { return base })
	p.SetEwma(true, 0.1, 5)
	for _, u := range []string{"fast", "mid", "slow"} {
		p.Add(&auth.Auth{UID: u})
		p.SetCredits(u, 100, 0)
	}
	p.mu.Lock()
	p.byUID["fast"].setLastUsed(base)
	p.byUID["mid"].setLastUsed(base)
	// slow 刻意久置 1h：其**未罚**权重（11.5）高于 fast/mid（11），因此「被罚后垫底」
	// 只能由延迟惩罚解释；顺带钉住「惩罚足以压过闲置补偿」这条语义边界。
	p.byUID["slow"].setLastUsed(base.Add(-time.Hour))
	// 快/中 1s、慢 5s，各 5 样本 → 池中位 1s → slow(5s) > 1.5s → 被罚。
	for _, u := range []string{"fast", "mid", "slow"} {
		e := p.byUID[u]
		e.ewmaLatencySamples = 5
		if u == "slow" {
			e.ewmaLatencyMs = 5000
		} else {
			e.ewmaLatencyMs = 1000
		}
	}
	eFast, eMid, eSlow := p.byUID["fast"], p.byUID["mid"], p.byUID["slow"]
	median := p.poolMedianLatencyMs([]*entry{eFast, eMid, eSlow})
	p.mu.Unlock()

	if median != 1000 {
		t.Fatalf("池延迟中位数应为 1000（下中位; 样本 1000/1000/5000）, got %v", median)
	}
	p.mu.RLock()
	rawFast := p.weightOf(eFast, 100, base)
	rawMid := p.weightOf(eMid, 100, base)
	rawSlow := p.weightOf(eSlow, 100, base)
	wFast := p.routingWeightOf(eFast, 100, base, median)
	wMid := p.routingWeightOf(eMid, 100, base, median)
	wSlow := p.routingWeightOf(eSlow, 100, base, median)
	p.mu.RUnlock()
	if rawSlow <= rawFast || rawSlow <= rawMid {
		t.Fatalf("前提失效：slow 的未罚权重应最高（久置补偿）, raw: fast=%v mid=%v slow=%v",
			rawFast, rawMid, rawSlow)
	}
	if wSlow >= wMid || wSlow >= wFast {
		t.Fatalf("延迟惩罚未生效：slow 被罚后应低于中位账号, weighted: fast=%v mid=%v slow=%v",
			wFast, wMid, wSlow)
	}

	p.SetRandomSource(func(n int64) int64 { return 0 })
	for i := 0; i < 10; i++ {
		if got := p.Pick(); got == nil || got.UID != "fast" {
			t.Fatalf("iter %d: 慢号被罚后 r=0 应选快号, got %+v", i, got)
		}
	}
	// 注入时钟必须**真的**被 pick 使用（否则权重重新被墙钟污染 → 本用例复发 flake）：
	// 选中写回的 lastUsed 必须恰好是冻结时刻。把 pick/pickWeighted 的 p.now() 改回
	// time.Now() 时此断言 FAIL（无需等墙钟漂移到「恰好出错」的那一刻）。
	p.mu.RLock()
	gotLastUsed := p.byUID["fast"].lastUsedTime()
	p.mu.RUnlock()
	if !gotLastUsed.Equal(base) {
		t.Fatalf("pick 应使用注入时钟写回 lastUsed: got %v want %v", gotLastUsed, base)
	}
	// r=末尾 → 权重最低的慢号仍可被选中（惩罚未清零、不饿死）。
	p.SetRandomSource(func(n int64) int64 { return n - 1 })
	got := p.Pick()
	if got == nil || got.UID != "slow" {
		t.Fatalf("r=末尾应命中慢号（证明未饿死）, got %+v", got)
	}
}

// TestEwmaStatusExposure /status 透出契约：关闭不输出、开启且有样本才输出。
// 透出丢失 → 运维看不到 EWMA 是否在工作 → 本测试 FAIL。
func TestEwmaStatusExposure(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	st, _ := p.Status("u1")
	if st.EwmaSuccess != nil || st.EwmaLatencyMs != nil || st.EwmaSamples != 0 {
		t.Fatalf("ewma 关闭时不应透出 EWMA 字段: %+v", st)
	}
	p.SetEwma(true, 0.1, 5)
	p.NoteSuccess("u1")
	p.RecordTokenUsage("u1", TokenUsageDelta{HasLatencyMs: true, LatencyMs: 300})
	st, _ = p.Status("u1")
	if st.EwmaSuccess == nil || *st.EwmaSuccess != 1.0 {
		t.Fatalf("开启+1 成功后 ewma_success 应=1.0, got %+v", st.EwmaSuccess)
	}
	if st.EwmaLatencyMs == nil || *st.EwmaLatencyMs != 300 {
		t.Fatalf("开启+1 观测后 ewma_latency_ms 应=300, got %+v", st.EwmaLatencyMs)
	}
	if st.EwmaSamples != 1 {
		t.Fatalf("ewma_samples 应=1, got %v", st.EwmaSamples)
	}
}

// TestPickConcurrentRLockSmoke pick 改读锁后的并发冒烟：N 个 goroutine 同时选号，
// 全部有结果、落在已知账号、基本打散。本机无 gcc 跑不了 -race，本测试抓「读锁路径
// 结构性崩溃」（panic/死锁/全 nil）；数据竞争由 CI 的 Linux race job 兜底。
func TestPickConcurrentRLockSmoke(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	for i := 0; i < 10; i++ {
		p.Add(&auth.Auth{UID: string(rune('a' + i))})
	}
	const N = 200
	got := make([]string, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if a := p.Pick(); a != nil {
				got[idx] = a.UID
			}
		}(i)
	}
	wg.Wait()
	p.mu.RLock()
	defer p.mu.RUnlock()
	counts := map[string]int{}
	for i, u := range got {
		if u == "" {
			t.Fatalf("pick %d 返回 nil", i)
		}
		if _, ok := p.byUID[u]; !ok {
			t.Fatalf("未知 uid %s", u)
		}
		counts[u]++
	}
	if len(counts) < 3 {
		t.Fatalf("并发 pick 过度集中（读锁路径异常?）: %v", counts)
	}
}

// TestEwmaFirstFailureIsOnlyOneStep 首个事件就是失败时 sr 必须只掉一步（0.9）。
// 缺口来源（2026-10-19 变异 EW2 存活）：把失败分支的懒初始化从 1.0 改成 0.0 仍全绿，
// 因为既有用例都是「先成功再失败」，从未走过 samples==0 的失败路径。
func TestEwmaFirstFailureIsOnlyOneStep(t *testing.T) {
	p := New("")
	p.SetEwma(true, 0.1, 5)
	p.Add(&auth.Auth{UID: "u1"})

	p.NoteError("u1")
	p.mu.RLock()
	sr, n := p.byUID["u1"].ewmaSuccess, p.byUID["u1"].ewmaSuccessSamples
	p.mu.RUnlock()
	if n != 1 {
		t.Fatalf("samples=%d want 1", n)
	}
	if want := 0.9; sr != want {
		t.Fatalf("首个事件即失败：sr=%v want %v（懒初始化 1.0 后掉一步，不能直接归零）", sr, want)
	}
}
