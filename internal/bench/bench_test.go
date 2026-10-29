package bench

import (
	"testing"
	"time"

	"yg-gateway/internal/reqlog"
)

func TestEvalPickBiasExpiringPreferred(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	// 账号 A expiring 高、B 低；事件全命中 A → bias > 1。
	// 事件账号用「昵称(uid8)」标签口径（真实 reqlog 形态），快照用完整 UID。
	events := []reqlog.Event{
		{Time: now.Add(-48 * time.Hour), Account: "秋(00c67396)", Model: "m1", OK: true},
		{Time: now.Add(-24 * time.Hour), Account: "秋(00c67396)", Model: "m1", OK: true},
		{Time: now, Account: "秋(00c67396)", Model: "m1", OK: false},
	}
	accounts := []AccountState{
		{UID: "00c67396-652e-4fa2-8904-41c82c3bbb3f", CreditsExpiring: 500},
		{UID: "71b4291d-0000-0000-0000-000000000000", CreditsExpiring: 10},
	}
	rep := Eval(events, accounts, now)
	if rep.PickBias.Kind != KindDerivation {
		t.Errorf("kind=%s want derivation", rep.PickBias.Kind)
	}
	if rep.PickBias.Samples != 1 {
		t.Errorf("samples=%d want 1（仅 A 命中快照）", rep.PickBias.Samples)
	}
	if rep.PickBias.Value < 1.0 {
		t.Errorf("bias=%v want >1（快过期账号被优先选中）", rep.PickBias.Value)
	}
	if rep.WindowDays != 3 {
		t.Errorf("window=%d want 3", rep.WindowDays)
	}
}

func TestAccountKeyParse(t *testing.T) {
	cases := []struct{ label, want string }{
		{"秋(00c67396)", "00c67396"},
		{"A", "A"},
		{"(12345678)", "12345678"},
		{"昵称(abcdefgh)尾巴", "abcdefgh"},
	}
	for _, c := range cases {
		if got := accountKey(c.label); got != c.want {
			t.Errorf("accountKey(%q)=%q want %q", c.label, got, c.want)
		}
	}
	if uidKey("00c67396-652e-4fa2") != "00c67396" {
		t.Errorf("uidKey truncation wrong")
	}
	if uidKey("A") != "A" {
		t.Errorf("uidKey short passthrough wrong")
	}
}

func TestEvalHealthAndRouting(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	events := []reqlog.Event{
		{Time: now.Add(-120 * time.Hour), Model: "m1", OK: true},
		{Time: now.Add(-96 * time.Hour), Model: "m1", OK: true},
		{Time: now.Add(-72 * time.Hour), Model: "m1", OK: true},
		{Time: now.Add(-48 * time.Hour), Model: "m1", OK: false},
		{Time: now.Add(-24 * time.Hour), Model: "m1", OK: false},
		{Time: now.Add(-120 * time.Hour), Model: "m2", OK: true},
		{Time: now.Add(-96 * time.Hour), Model: "m2", OK: true},
		{Time: now.Add(-72 * time.Hour), Model: "m2", OK: true},
		{Time: now.Add(-48 * time.Hour), Model: "m2", OK: true},
		{Time: now.Add(-24 * time.Hour), Model: "m2", OK: true},
	}
	accounts := []AccountState{
		{UID: "A", Disabled: false, Paused: false, Cooling: false},
		{UID: "B", Cooling: true},
		{UID: "C", Disabled: true},
	}
	rep := Eval(events, accounts, now)
	if rep.Health.SuccessRate7d.Kind != KindDerivation || rep.Health.SuccessRate7d.Value != 0.8 {
		t.Errorf("success7d=%v/%s want 0.8/derivation", rep.Health.SuccessRate7d.Value, rep.Health.SuccessRate7d.Kind)
	}
	if rep.Health.HealthyRatio.Value != 0.33 {
		t.Errorf("healthy=%v want 0.33", rep.Health.HealthyRatio.Value)
	}
	// m1: 3/5=0.6；m2: 5/5=1.0。
	if rep.Routing["m1"].Value != 0.6 || rep.Routing["m1"].Samples != 5 {
		t.Errorf("m1=%v/%d want 0.6/5", rep.Routing["m1"].Value, rep.Routing["m1"].Samples)
	}
	if rep.Routing["m2"].Value != 1.0 {
		t.Errorf("m2=%v want 1.0", rep.Routing["m2"].Value)
	}
}

func TestEvalEmptyInputs(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	rep := Eval(nil, nil, now)
	if rep.PickBias.Kind != KindUnknown || rep.Health.SuccessRate7d.Kind != KindUnknown {
		t.Errorf("empty events should be unknown, got %s/%s", rep.PickBias.Kind, rep.Health.SuccessRate7d.Kind)
	}
	if rep.Health.HealthyRatio.Kind != KindUnknown {
		t.Errorf("empty accounts healthy should be unknown, got %s", rep.Health.HealthyRatio.Kind)
	}
	if rep.Routing != nil && len(rep.Routing) != 0 {
		t.Errorf("empty routing should be empty map")
	}
}

func TestEvalRouteInsufficientSamplesUnknown(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	events := []reqlog.Event{
		{Time: now, Model: "rare", OK: true},
	}
	rep := Eval(events, nil, now)
	if rep.Routing["rare"].Kind != KindUnknown {
		t.Errorf("1-sample model should be unknown, got %s", rep.Routing["rare"].Kind)
	}
	if rep.Routing["rare"].Samples != 1 {
		t.Errorf("samples=%d want 1", rep.Routing["rare"].Samples)
	}
}

func TestEvalDeterministic(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.Local)
	events := []reqlog.Event{
		{Time: now.Add(-48 * time.Hour), Account: "A", Model: "m1", OK: true},
		{Time: now.Add(-24 * time.Hour), Account: "B", Model: "m1", OK: false},
		{Time: now, Account: "A", Model: "m2", OK: true},
	}
	accounts := []AccountState{{UID: "A", CreditsExpiring: 100}, {UID: "B", CreditsExpiring: 0}}
	r1 := Eval(events, accounts, now)
	r2 := Eval(events, accounts, now)
	// 重跑逐字段一致（纯函数无时钟/随机）。
	if r1.PickBias.Value != r2.PickBias.Value || r1.Health.SuccessRate7d.Value != r2.Health.SuccessRate7d.Value {
		t.Errorf("deterministic violation: %+v vs %+v", r1.PickBias, r2.PickBias)
	}
}
