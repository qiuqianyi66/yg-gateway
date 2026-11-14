package quota

import (
	"testing"

	"yg-gateway/internal/provider"
)

func TestMeteredRemainingSumsBalanceAndFree(t *testing.T) {
	m := Metered{Balance: 100, FreeRemaining: 50}
	v, unknown := m.Remaining()
	if unknown {
		t.Fatal("两个维度都已知时 unknown 应为 false")
	}
	if v != 150 {
		t.Errorf("Remaining = %d，want 150", v)
	}
}

func TestMeteredUnknownBalance(t *testing.T) {
	m := Metered{Balance: -1, FreeRemaining: 50}
	v, unknown := m.Remaining()
	if !unknown {
		t.Fatal("余额未知时 unknown 应为 true（未知不是 0）")
	}
	if v != 0 {
		t.Errorf("unknown 时 value 应回 0，实得 %d", v)
	}
}

func TestMeteredUnknownFree(t *testing.T) {
	m := Metered{Balance: 100, FreeRemaining: -1}
	if _, unknown := m.Remaining(); !unknown {
		t.Fatal("免费额度未知时 unknown 应为 true（不拼凑半个数）")
	}
}

func TestMeteredExhausted(t *testing.T) {
	if !(Metered{Balance: 0, FreeRemaining: 0}).Exhausted() {
		t.Error("余额为 0 时应耗尽")
	}
	if (Metered{Balance: 10, FreeRemaining: 0}).Exhausted() {
		t.Error("余额 10 不应耗尽")
	}
	// 牙齿：unknown 时 Exhausted 必须 false（未知不是耗尽，宁可继续选）。
	if (Metered{Balance: -1, FreeRemaining: 0}).Exhausted() {
		t.Error("余额未知时 Exhausted 应为 false")
	}
}

func TestMeteredKind(t *testing.T) {
	if (Metered{}).Kind() != provider.QuotaMetered {
		t.Errorf("Kind = %v，want metered", (Metered{}).Kind())
	}
}

func TestWindowRemainingAndExhausted(t *testing.T) {
	if v, unknown := (Window{RemainingInWindow: 60}).Remaining(); unknown || v != 60 {
		t.Errorf("Window.Remaining = (%d,%v)，want (60,false)", v, unknown)
	}
	if _, unknown := (Window{RemainingInWindow: -1}).Remaining(); !unknown {
		t.Error("窗口剩余未知时 unknown 应为 true")
	}
	if !(Window{RemainingInWindow: 0}).Exhausted() {
		t.Error("窗口剩余 0 应耗尽")
	}
	if (Window{RemainingInWindow: -1}).Exhausted() {
		t.Error("窗口剩余未知时 Exhausted 应为 false")
	}
	if (Window{}).Kind() != provider.QuotaWindow {
		t.Errorf("Window.Kind 应为 window")
	}
}
