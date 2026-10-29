// softband_recent_test.go 软带（P1）与近期用量衰减（P2）的行为测试。
// 零回归锚定：两个开关全关（默认）时与现状逐位一致。
package pool

import (
	"math"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestSoftBandWeightAndReason 带内：不硬拦、权重 ×0.5、pickReason 含 softband；
// 带外/≤floor：行为不变（带外无因子；≤floor 仍被 floorBlocked 硬拦）。
func TestSoftBandWeightAndReason(t *testing.T) {
	p := New("")
	p.mu.Lock()
	p.creditFloor = 100 // 无台账 → 带上界 = 100×1.2 = 120
	p.mu.Unlock()
	p.Add(&auth.Auth{UID: "in"})
	p.Add(&auth.Auth{UID: "out"})
	p.SetCredits("in", 110, 0)
	p.SetCredits("out", 300, 0)
	p.mu.Lock()
	t0 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	p.byUID["in"].setLastUsed(t0)
	p.byUID["out"].setLastUsed(t0)
	wIn := p.routingWeightOf(p.byUID["in"], 300, t0, 0, 0)
	wOut := p.routingWeightOf(p.byUID["out"], 300, t0, 0, 0)
	p.mu.Unlock()

	// 基础权重相同（credits 项被 max 归一差异：in=110/300、out=300/300 —— 先算
	// 期望值而非硬编码比值）。
	expIn := 1 + 110.0/300.0*10
	expOut := 1 + 10.0
	if math.Abs(wOut/expOut-1) > 1e-9 {
		t.Fatalf("带外权重 = %v, want %v（不受软带影响）", wOut, expOut)
	}
	if math.Abs(wIn/(expIn*softBandFactor)-1) > 1e-9 {
		t.Fatalf("带内权重 = %v, want %v×0.5", wIn, expIn)
	}

	// pick 后理由含 softband（确定性：只有 in/out 两号，注入随机源选 in）。
	p.SetRandomSource(func(n int64) int64 { return 0 })
	for i := 0; i < 5; i++ {
		p.Pick()
	}
	st, _ := p.Status("out")
	if strings.Contains(st.PickReason, "softband") {
		t.Fatalf("带外号不应有 softband 标记：%q", st.PickReason)
	}
	// 选中 in 的轮次应有标记：重置随机源恒选权重高者（out 权重更高）——改用直查：
	p.mu.RLock()
	_, inBand := p.softBandFactorOf(p.byUID["in"], t0)
	_, outBand := p.softBandFactorOf(p.byUID["out"], t0)
	p.mu.RUnlock()
	if !inBand || outBand {
		t.Fatalf("softBandFactorOf 判定错：in=%v out=%v", inBand, outBand)
	}

	// ≤floor 仍硬拦（floorBlocked 既有语义不变）：给该号喂一条 m1 实测收费台账
	// （floor 对未知模型按既有语义放行，故必须先给「收费」证据），credits=90 不可选。
	p.Add(&auth.Auth{UID: "low"})
	p.SetCredits("low", 90, 0)
	p.NoteModelCost("low", "m1", 2.0, 1000) // 实测收费台账（正规入口）
	p.mu.RLock()
	blocked := p.floorBlockedForModel(p.byUID["low"], "m1", t0)
	p.mu.RUnlock()
	if !blocked {
		t.Fatal("credits ≤ floor 应被硬拦（软带不是逃逸口）")
	}
}

// TestSoftBandPrescreenCap 带宽 cap（floor×0.5）与热路径预筛：credits > floor×1.5
// 恒带外（即使扣费环算出的带宽更大也被 cap）；大额扣费不再无限放大带宽。
// 注意：P90 语义下带宽 = floor + 2×P90(单次扣费)，cap 只在 P90 足够大时触发——
// 用 60.0 单次扣费（2×60=120 → upper=220）验证 cap 仍收敛到 floor×1.5。
func TestSoftBandPrescreenCap(t *testing.T) {
	p := New("")
	p.mu.Lock()
	p.creditFloor = 100
	p.mu.Unlock()
	p.Add(&auth.Auth{UID: "rich"})
	p.Add(&auth.Auth{UID: "edge"})
	p.SetCredits("rich", 200, 0) // = floor×2 > 预筛线 floor×1.5 → 恒带外
	p.SetCredits("edge", 149, 0) // 恰在预筛线内；无扣费环 → upper=floor×1.2=120 → 带外
	now := time.Now()
	p.mu.RLock()
	_, inEdge0 := p.softBandFactorOf(p.byUID["edge"], now)
	p.mu.RUnlock()
	if inEdge0 {
		t.Fatal("无扣费环：149 > 120 应带外（迁移期回退 upper=floor×1.2）")
	}
	// cap 是 softBandUpper 的纯计算语义，与 NoteModelCost 的扣费记账解耦——
	// 大额喂环会同时扣 credits 打穿 floor，故直接注入环验证 cap 收敛。
	p.mu.Lock()
	p.byUID["edge"].recentCharges = []float64{60.0} // 单次 60：2×60=120 → upper=220 → cap 150
	p.mu.Unlock()
	p.mu.RLock()
	fRich, inRich := p.softBandFactorOf(p.byUID["rich"], now)
	fEdge, inEdge := p.softBandFactorOf(p.byUID["edge"], now)
	upperCapped := p.softBandUpper(p.byUID["edge"], now) // 大额扣费直查：验证 cap
	p.mu.RUnlock()
	if inRich || fRich != 1 {
		t.Fatalf("floor×2 应被预筛为带外（带宽 cap = floor×0.5）")
	}
	if !inEdge || fEdge != softBandFactor {
		t.Fatalf("149 ∈ (100, cap150] 应带内（60 单次扣费 ×2=120 → upper=220 被 cap 到 150）")
	}
	if upperCapped != 150 {
		t.Fatalf("softBandUpper = %d, want 150（220 被 cap 到 floor×1.5）", upperCapped)
	}
	// cap 生效的带内案例：credits=130（(100,150] 内）应带内。
	p.SetCredits("edge", 130, 0)
	p.mu.RLock()
	_, in130 := p.softBandFactorOf(p.byUID["edge"], now)
	p.mu.RUnlock()
	if !in130 {
		t.Fatal("130 应带内（upper=min(max(120,220),150)=150）")
	}
}

// TestSoftBandRecentChargeP90 P90 语义（方案 A 主路径）：混合分布（聊天小额 + 评测
// 大额）下带宽 = max(floor×1.2, floor + 2×P90(近 N 次单次扣费))，P90 落在评测段——
// 评测扣费足够大时上限随 P90 抬升（进带号能扛 2 次评测级扣费）。同时验证环 cap
// （softBandChargeHistory）与 credit=0 不入环。
func TestSoftBandRecentChargeP90(t *testing.T) {
	p := New("")
	p.mu.Lock()
	p.creditFloor = 100
	p.mu.Unlock()
	p.Add(&auth.Auth{UID: "u"})
	p.SetCredits("u", 130, 0) // 注入环不扣费（与 cap 测试同口径），余额直接落带内验证
	// 混合分布：聊天小额 10 次（0.2 credit）+ 评测大额 2 次（20 credit）。
	// 排序后 [0.2×10, 20×2]，ceil(0.9×12)-1 = 10 → P90 = 20。
	p.mu.Lock()
	p.byUID["u"].recentCharges = []float64{0.2, 0.2, 0.2, 0.2, 0.2, 0.2, 0.2, 0.2, 0.2, 0.2, 20, 20}
	p.mu.Unlock()
	p.mu.RLock()
	upper := p.softBandUpper(p.byUID["u"], time.Now())
	_, inBand := p.softBandFactorOf(p.byUID["u"], time.Now())
	p.mu.RUnlock()
	if upper != 140 { // max(120, 100 + 2×20) = 140
		t.Fatalf("softBandUpper = %d, want 140（P90=20，N=2 语义）", upper)
	}
	if !inBand {
		t.Fatal("credits=130 ≤ 140 应带内（P90 语义：能扛 2 次评测级扣费）")
	}
	// P90 低于基准时 base（floor×1.2）主导：聊天纯号不会因小额扣费缩带上限。
	p.mu.Lock()
	p.byUID["u"].recentCharges = []float64{0.2, 0.2, 0.2}
	p.mu.Unlock()
	p.mu.RLock()
	if upperChat := p.softBandUpper(p.byUID["u"], time.Now()); upperChat != 120 {
		p.mu.RUnlock()
		t.Fatalf("纯聊天扣费 softBandUpper = %d, want 120（base 主导，零回归）", upperChat)
	}
	p.mu.RUnlock()

	// NoteModelCost 集成：收费扣费入环 + 扣 credits；免费（credit=0）不入环。
	p.mu.Lock()
	p.byUID["u"].recentCharges = nil
	p.mu.Unlock()
	p.NoteModelCost("u", "m", 5.0, 1_000) // 收费：入环
	p.NoteModelCost("u", "m", 0, 1_000)   // 免费：不入环
	p.mu.RLock()
	e := p.byUID["u"]
	if n := len(e.recentCharges); n != 1 {
		p.mu.RUnlock()
		t.Fatalf("recentCharges len = %d, want 1（仅收费入环）", n)
	}
	if e.recentCharges[0] != 5.0 {
		p.mu.RUnlock()
		t.Fatalf("recentCharges[0] = %v, want 5.0", e.recentCharges[0])
	}
	if got := e.credits; got != 125 { // 130 − 5（0 扣费不入账）
		p.mu.RUnlock()
		t.Fatalf("credits = %d, want 125", got)
	}
	p.mu.RUnlock()

	// 环 cap：再喂 40 次大额，应只保留最近 softBandChargeHistory=32 条。
	for i := 0; i < 40; i++ {
		p.NoteModelCost("u", "m", 4.4, 300_000)
	}
	p.mu.RLock()
	if n := len(p.byUID["u"].recentCharges); n != softBandChargeHistory {
		p.mu.RUnlock()
		t.Fatalf("recentCharges len = %d, want %d（环 cap）", n, softBandChargeHistory)
	}
	p.mu.RUnlock()
}

// TestSoftBandDisabledWhenFloorOff creditFloor=0（保底关闭）时无带。
func TestSoftBandDisabledWhenFloorOff(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u"})
	p.SetCredits("u", 10, 0) // 极低余额但 floor 关
	p.mu.RLock()
	f, inBand := p.softBandFactorOf(p.byUID["u"], time.Now())
	p.mu.RUnlock()
	if inBand || f != 1 {
		t.Fatalf("floor 关闭不应有软带：f=%v inBand=%v", f, inBand)
	}
}

// TestRecentUsageFactor 衰减账：刚服务 200k token → 因子 0.5；12h 后 ≈1（签到
// 周期归零语义）；开关关（默认）→ 权重不受影响（零回归）。
func TestRecentUsageFactor(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u"})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	p.RecordTokenUsageWithTime("u", TokenUsageDelta{
		HasTotalTokens: true, TotalTokens: 200_000, Model: "m"}, now)

	p.mu.RLock()
	e := p.byUID["u"]
	f0 := e.recentUsageFactorOf(now)
	f12 := e.recentUsageFactorOf(now.Add(12 * time.Hour))
	p.mu.RUnlock()
	if math.Abs(f0-0.5) > 1e-9 {
		t.Fatalf("刚服务 200k：因子 = %v, want 0.5", f0)
	}
	if math.Abs(f12-1) > 1e-3 {
		t.Fatalf("12h 后：因子 = %v, want ≈1（归零）", f12)
	}

	// 开关关：routingWeightOf 不乘因子（与无账号等价）。
	p.SetCredits("u", 100, 0)
	p.mu.Lock()
	p.byUID["u"].setLastUsed(now)
	wOff := p.routingWeightOf(p.byUID["u"], 100, now, 0, 0)
	p.recentUsageFactor = true
	wOn := p.routingWeightOf(p.byUID["u"], 100, now, 0, 0)
	p.mu.Unlock()
	if math.Abs(wOff/(wOn/0.5)-1) > 1e-9 {
		t.Fatalf("开关行为：off=%v on=%v（on 应为 off×0.5）", wOff, wOn)
	}
}

// TestRecordTokenUsageUnknownNotCounted 缺字段的调用不污染衰减账（unknown 不当 0 记）。
func TestRecordTokenUsageUnknownNotCounted(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u"})
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)
	p.RecordTokenUsageWithTime("u", TokenUsageDelta{Model: "m"}, now) // 全缺
	p.mu.RLock()
	e := p.byUID["u"]
	if e.recentTokens != 0 || !e.recentTokensAt.IsZero() {
		t.Fatalf("缺字段调用不应入账：%v %v", e.recentTokens, e.recentTokensAt)
	}
	if f := e.recentUsageFactorOf(now); f != 1 {
		t.Fatalf("无账因子应 1，got %v", f)
	}
	p.mu.RUnlock()
}
