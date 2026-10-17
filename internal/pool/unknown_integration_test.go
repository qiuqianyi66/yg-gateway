package pool

import (
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/unknown"
)

// newPoolWithUnknowns 建一个注入了 unknown registry 的池。
func newPoolWithUnknowns(t *testing.T) (*Pool, *unknown.Registry) {
	t.Helper()
	p := New("")
	reg := unknown.NewRegistry()
	p.SetUnknownRegistry(reg)
	return p, reg
}

// TestTrackCostUnknownRecordsOnce 未知成本被跟踪一次且幂等去重。
//
// 为什么：某 (账号,模型) 还没有 credit 观测时，它的成本是**未知**——
// 而未知恰恰是养号决策最需要的（tier 未知时收费模型会被放行，可能打穿余额）。
// 旧实现把这个未知压成 tier 1 就没人跟了。
//
// 牙齿：去掉 LoadOrStore 去重 → 第二次调用会多记一条 → 本测试 FAIL。
func TestTrackCostUnknownRecordsOnce(t *testing.T) {
	p, reg := newPoolWithUnknowns(t)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)

	// 无任何 NoteModelCost → 成本未知
	p.TrackCostUnknown("u1", "hy4-preview")
	p.TrackCostUnknown("u1", "hy4-preview") // 重复：应去重
	p.TrackCostUnknown("u1", "hy4-preview")

	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("unknowns len=%d want 1（同一组合只登记一条）", len(list))
	}
	if list[0].Requires == "" {
		t.Error("unknown 必须说明需要什么才能消解")
	}
}

// TestTrackCostUnknownSkipsKnown 已有 credit 观测的组合不再登记为未知。
//
// 牙齿：把 tier 判定去掉（有观测也登记）→ 本测试 FAIL。
func TestTrackCostUnknownSkipsKnown(t *testing.T) {
	p, reg := newPoolWithUnknowns(t)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)

	p.NoteModelCost("u1", "hy4-preview", 2.9, 1000) // 有观测了
	p.TrackCostUnknown("u1", "hy4-preview")

	if got := len(reg.List()); got != 0 {
		t.Fatalf("已有观测的组合不该被登记为未知，got %d 条", got)
	}
}

// TestNoteModelCostResolvesUnknown 实测到 credit 后自动消解对应的未知条目——
// 这就是"消解只能由合格的观察证据完成"的落地。
//
// 牙齿：把 NoteModelCost 里的 resolve 去掉 → 本测试 FAIL。
func TestNoteModelCostResolvesUnknown(t *testing.T) {
	p, reg := newPoolWithUnknowns(t)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)

	p.TrackCostUnknown("u1", "hy4-preview")
	if len(reg.List()) != 1 {
		t.Fatal("前置条件：应有一条未消解未知")
	}

	// 一次真实观测 → 消解
	p.NoteModelCost("u1", "hy4-preview", 2.9, 1000)

	if got := len(reg.List()); got != 0 {
		t.Fatalf("实测 credit 后未知应被消解，仍剩 %d 条", got)
	}
	// 审计：All 里仍能看到它被消解过
	all := reg.All()
	if len(all) != 1 || !all[0].Resolved {
		t.Fatalf("All 应保留已消解条目供审计: %+v", all)
	}
	if len(all[0].Evidence) == 0 {
		t.Error("消解必须留下证据 ID")
	}
}

// TestUnknownRegistryOptional 未注入 registry 时全部 no-op（零回归）。
//
// 牙齿：把 nil 判据去掉 → 本测试 panic（nil 解引用）。
func TestUnknownRegistryOptional(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)

	// 不调 SetUnknownRegistry
	p.TrackCostUnknown("u1", "hy4-preview") // 必须不 panic
	p.NoteModelCost("u1", "hy4-preview", 2.9, 1000)

	st := statusOf(t, p, "u1")
	if st.Unknowns != nil {
		t.Errorf("未注入 registry 时 /status 不该有 unknowns 段，got %+v", st.Unknowns)
	}
}

// TestExpiredCostObservationReopensUnknown 成本观测过期 → 该组合重新变回未知。
//
// 为什么：消解是单向闩锁，而 modelCost 有 TTL（modelCostTTL）。观测过期后选号侧
// 已经按"未知"处理（credit_floor 不再信本地实测），若 registry 里那条一直 Resolved，
// /metrics 的 unknowns_open 就读 0——指标从"还有多少没摸清"退化成"永远没问题"。
//
// 牙齿：把 pruneAllLocked 里的 reopenCostUnknown 调用去掉 → 第 4 步读到 0 → FAIL。
func TestExpiredCostObservationReopensUnknown(t *testing.T) {
	p, reg := newPoolWithUnknowns(t)
	// 注意：modelCost 的 LastSeen 由 NoteModelCost 直接用 time.Now() 写入（未走 p.now()），
	// 所以这里按**真实时钟**推进，不冻结时间——冻结会让台账永远显示"刚观测过"。
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)

	// 1) 首次选号 → 登记为未知
	p.TrackCostUnknown("u1", "hy4-preview")
	if got := len(reg.List()); got != 1 {
		t.Fatalf("前置：应有 1 条未消解，got %d", got)
	}
	// 2) 实测到 credit → 消解
	p.NoteModelCost("u1", "hy4-preview", 2.9, 1000)
	if got := len(reg.List()); got != 0 {
		t.Fatalf("实测后应消解，仍剩 %d 条", got)
	}
	// 3) 时间推进越过 TTL，触发台账过期清理（落盘路径，不在选号热路径）
	p.mu.Lock()
	p.pruneAllLocked(time.Now().Add(modelCostTTL + time.Hour))
	p.mu.Unlock()

	// 4) 观测过期 = 重新变未知（指标恢复说真话）
	if got := len(reg.List()); got != 1 {
		t.Fatalf("观测过期后该组合应重新变未知，got %d 条（0 表示指标会撒谎）", got)
	}
	// 5) 再次观测 → 再次消解（闭环，且 revision 契约没被破坏）
	p.NoteModelCost("u1", "hy4-preview", 3.1, 1000)
	if got := len(reg.List()); got != 0 {
		t.Fatalf("再次观测后应再次消解，仍剩 %d 条", got)
	}
}

// TestStatusExposesUnknowns /status 暴露未消解未知（供面板/agent 消费）。
func TestStatusExposesUnknowns(t *testing.T) {
	p, _ := newPoolWithUnknowns(t)
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)

	p.TrackCostUnknown("u1", "hy4-preview")

	st := statusOf(t, p, "u1")
	if len(st.Unknowns) != 1 {
		t.Fatalf("unknowns len=%d want 1", len(st.Unknowns))
	}
	if st.Unknowns[0].Question == "" || st.Unknowns[0].Requires == "" {
		t.Errorf("unknowns 条目缺 question/requires: %+v", st.Unknowns[0])
	}
}
