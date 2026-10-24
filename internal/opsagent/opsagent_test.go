// opsagent 单测：规则触发/不触发、unknown 幂等登记、白名单护栏 teeth、
// Run 的 ctx 退出、历史环上限。覆盖锚定 check-depth 的 minCoverage 棘轮。
package opsagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/evidence"
	"yg-gateway/internal/unknown"
)

// fakeSink 收集建议（断言输出口被调用）。
type fakeSink struct{ got []Suggestion }

func (f *fakeSink) Suggest(s Suggestion) { f.got = append(f.got, s) }

// fixedObs 固定观察面：可变字段供各用例注入。
type fixedObs struct {
	counts   PoolCounts
	accounts []AccountView
}

func (o fixedObs) Counts() PoolCounts      { return o.counts }
func (o fixedObs) Accounts() []AccountView { return o.accounts }

var fixedNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local)

// TestRoundCapacitySuggest 容量规则：healthy/total < 阈值且池有规模 → 建议 + 审计四态。
func TestRoundCapacitySuggest(t *testing.T) {
	reg := unknown.NewRegistry()
	sink := &fakeSink{}
	obs := fixedObs{counts: PoolCounts{Total: 4, Healthy: 1, Cooling: 2, Disabled: 1}}
	a := New(Config{HealthyFrac: 0.5, MaxRounds: 10}, Observers{
		Counts:   obs.Counts,
		Accounts: obs.Accounts,
		Now:      func() time.Time { return fixedNow },
	}, reg, sink)

	rd := a.Round()
	if len(rd.Suggestions) != 1 {
		t.Fatalf("建议数 = %d, want 1（suggestions=%+v）", len(rd.Suggestions), rd.Suggestions)
	}
	if rd.Suggestions[0].Rule != "capacity" || sink.got[0].Rule != "capacity" {
		t.Fatalf("规则名 = %q / sink %q, want capacity", rd.Suggestions[0].Rule, sink.got[0].Rule)
	}
	// 审计：轮 Facts 含池级计数 observation；建议自带占比 derivation（依据随建议走）。
	var hasObs bool
	for _, f := range rd.Facts {
		if f.Kind == evidence.KindObservation && f.Basis == "pool.CountsDetailed" {
			hasObs = true
		}
	}
	if !hasObs {
		t.Fatalf("轮审计缺池级 observation（facts=%+v）", rd.Facts)
	}
	var hasDeriv bool
	for _, f := range rd.Suggestions[0].Facts {
		if f.Kind == evidence.KindDerivation && strings.Contains(f.Basis, "healthy/total") {
			hasDeriv = true
		}
	}
	if !hasDeriv {
		t.Fatalf("建议缺占比 derivation（facts=%+v）", rd.Suggestions[0].Facts)
	}
	if len(rd.Rejected) != 0 {
		t.Fatalf("不应有被拒动作：%v", rd.Rejected)
	}
	if rd.At != fixedNow {
		t.Fatalf("At = %v, want %v（注入时钟）", rd.At, fixedNow)
	}
}

// TestRoundNoCapacitySuggestWhenHealthy 健康占比达标 / 池太小 → 不触发。
func TestRoundNoCapacitySuggestWhenHealthy(t *testing.T) {
	for name, counts := range map[string]PoolCounts{
		"健康占比达标": {Total: 4, Healthy: 3},
		"池太小是噪声": {Total: 2, Healthy: 0},
	} {
		sink := &fakeSink{}
		obs := fixedObs{counts: counts}
		a := New(Config{HealthyFrac: 0.5}, Observers{
			Counts: obs.Counts, Accounts: obs.Accounts,
			Now: func() time.Time { return fixedNow },
		}, unknown.NewRegistry(), sink)
		rd := a.Round()
		if len(rd.Suggestions) != 0 || len(sink.got) != 0 {
			t.Fatalf("%s：不应建议（rd=%+v sink=%d）", name, rd.Suggestions, len(sink.got))
		}
	}
}

// TestRoundHardCoolContradictionRegistersUnknown 402-余额矛盾：登记 unknown（requires 非空）
// + 建议；第二轮幂等（同一 (question,requires) 不新增条目）。
func TestRoundHardCoolContradictionRegistersUnknown(t *testing.T) {
	reg := unknown.NewRegistry()
	sink := &fakeSink{}
	obs := fixedObs{counts: PoolCounts{Total: 2, Healthy: 2}, accounts: []AccountView{
		{UID: "uid-1234567890abcdef", Nickname: "nickA", Credits: 42, CoolKind: "hard",
			CoolUntil: fixedNow.Add(time.Hour)},
		{UID: "uid-b", Credits: 42, CoolKind: "soft"}, // 非 hard：不触发
		{UID: "uid-c", Credits: 0, CoolKind: "hard"},  // 余额非正：不触发
		{UID: "uid-d", Credits: 42, CoolKind: ""},     // 无冷却：不触发
	}}
	a := New(Config{}, Observers{
		Counts: obs.Counts, Accounts: obs.Accounts,
		Now: func() time.Time { return fixedNow },
	}, reg, sink)

	rd := a.Round()
	if len(rd.Unknowns) != 1 || reg.Len() != 1 {
		t.Fatalf("unknown 登记 = %d/%d, want 1/1", len(rd.Unknowns), reg.Len())
	}
	if rd.Unknowns[0].Requires == "" {
		t.Fatal("requires 必须非空（unknown 包 fail-closed 契约）")
	}
	if len(rd.Suggestions) != 1 || rd.Suggestions[0].Rule != "credit-402-contradiction" {
		t.Fatalf("建议 = %+v, want 1 条 credit-402-contradiction", rd.Suggestions)
	}
	if rd.Suggestions[0].UID != "uid-1234567890abcdef" {
		t.Fatalf("建议应带 UID：%+v", rd.Suggestions[0])
	}

	// 第二轮：同 (question, requires) 幂等——registry 不涨，建议照发（状态仍在）。
	rd2 := a.Round()
	if reg.Len() != 1 {
		t.Fatalf("第二轮后 registry = %d, want 1（幂等）", reg.Len())
	}
	if len(rd2.Unknowns) != 1 || len(rd2.Suggestions) != 1 {
		t.Fatalf("第二轮应保持 1 unknown + 1 建议（got %d/%d）", len(rd2.Unknowns), len(rd2.Suggestions))
	}
}

// TestGuardRejectsNonWhitelistAction 护栏 teeth：非白名单动作必须被拒且可见（不静默）。
// MVP 规则不产出 cost-explore，白盒直接打 emit（同包测试）模拟未来规则的违规形态。
func TestGuardRejectsNonWhitelistAction(t *testing.T) {
	sink := &fakeSink{}
	a := New(Config{}, Observers{Now: func() time.Time { return fixedNow }}, nil, sink)
	rd := Round{}
	a.emit(&rd, Suggestion{Rule: "future-rule", Title: "x", Action: ActionCostExplore})
	if len(rd.Rejected) != 1 || !strings.Contains(rd.Rejected[0], "cost-explore") {
		t.Fatalf("非白名单动作必须落入 Rejected：%v", rd.Rejected)
	}
	if len(rd.Suggestions) != 0 || len(sink.got) != 0 {
		t.Fatal("被拒动作不得输出到 Sink")
	}
	// suggest 白名单成员：正常输出。
	a.emit(&rd, Suggestion{Rule: "r", Title: "ok", Action: ActionSuggest})
	if len(rd.Suggestions) != 1 || len(sink.got) != 1 {
		t.Fatalf("suggest 应放行（suggestions=%d sink=%d）", len(rd.Suggestions), len(sink.got))
	}
}

// TestRunExitsOnCancel R9：ctx 取消后 Run 返回（无泄漏），期间至少跑了一轮。
func TestRunExitsOnCancel(t *testing.T) {
	obs := fixedObs{counts: PoolCounts{Total: 4, Healthy: 1, Cooling: 3}}
	a := New(Config{Interval: 5 * time.Millisecond, MaxRounds: 10}, Observers{
		Counts: obs.Counts, Accounts: obs.Accounts,
		Now: func() time.Time { return fixedNow },
	}, nil, &fakeSink{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()
	select {
	case <-done:
		if len(a.History()) == 0 {
			t.Fatal("取消前应至少跑了一轮")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未随 ctx 取消退出（R9 违规）")
	}
}

// TestHistoryRingCap 历史环：超过 MaxRounds 只留最近 N 条（旧→新）。
func TestHistoryRingCap(t *testing.T) {
	obs := fixedObs{counts: PoolCounts{Total: 4, Healthy: 1, Cooling: 3}}
	a := New(Config{MaxRounds: 2, HealthyFrac: 0.5}, Observers{
		Counts: obs.Counts, Accounts: obs.Accounts,
		Now: func() time.Time { return fixedNow },
	}, nil, &fakeSink{})
	for i := 0; i < 4; i++ {
		rd := a.Round()
		rd.At = fixedNow.Add(time.Duration(i) * time.Minute)
		// 直接替换末位以构造可区分时间线。
		a.mu.Lock()
		a.hist[len(a.hist)-1] = rd
		a.mu.Unlock()
	}
	h := a.History()
	if len(h) != 2 {
		t.Fatalf("历史 = %d 条, want 2（MaxRounds）", len(h))
	}
	if !h[0].At.Before(h[1].At) {
		t.Fatalf("历史应旧→新：%v, %v", h[0].At, h[1].At)
	}
	// 副本语义：改副本不动内部。
	h[0].At = time.Time{}
	if a.History()[0].At.IsZero() {
		t.Fatal("History 必须返回副本")
	}
}

// TestNewDefaults 零值 Config 与 nil 观察面：fail-soft 到默认值（New 不 panic）。
func TestNewDefaults(t *testing.T) {
	a := New(Config{}, Observers{}, nil, nil)
	if a.cfg.Interval != defaultInterval || a.cfg.MaxRounds != defaultMaxRounds || a.cfg.HealthyFrac != defaultHealthyFrac {
		t.Fatalf("默认值 = %+v", a.cfg)
	}
	rd := a.Round() // nil Counts/Accounts → 零值观察：不 panic、不误报
	if len(rd.Suggestions) != 0 {
		t.Fatalf("零值观察面不应产出建议：%+v", rd.Suggestions)
	}
}
