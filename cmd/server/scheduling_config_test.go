package main

import (
	"testing"
	"time"
)

// 调度可靠性新配置字段（ADR-003）的解析测试。

// TestBreakerProbeGapConfig half-open 试探间隔解析：
//   - 缺省/空串 → 默认 30s
//   - "0" → 0（关闭，合法）
//   - 非法值 → fail fast
//   - 负值 → fail fast
//
// 牙齿：把 "0" 的处理改成"0 也回落默认" → TestBreakerProbeGapExplicitZero 会 FAIL。
func TestBreakerProbeGapConfig(t *testing.T) {
	// 缺省：空串 → 默认 30s
	c, err := loadWith(`{"api_key":"k"}`, t)
	if err != nil {
		t.Fatalf("缺省配置应可加载: %v", err)
	}
	if c.BreakerProbeGapDur != 30*time.Second {
		t.Errorf("缺省 breaker_probe_gap 应为 30s，got %v", c.BreakerProbeGapDur)
	}

	// 合法值
	c, err = loadWith(`{"api_key":"k","pool":{"breaker_probe_gap":"2m"}}`, t)
	if err != nil {
		t.Fatalf("合法值应可加载: %v", err)
	}
	if c.BreakerProbeGapDur != 2*time.Minute {
		t.Errorf("breaker_probe_gap 应为 2m，got %v", c.BreakerProbeGapDur)
	}

	// 非法值 → fail fast
	if _, err := loadWith(`{"api_key":"k","pool":{"breaker_probe_gap":"abc"}}`, t); err == nil {
		t.Error("非法 breaker_probe_gap 应 fail fast")
	}

	// 负值 → fail fast（0 是合法的"关闭"，负值无意义）
	if _, err := loadWith(`{"api_key":"k","pool":{"breaker_probe_gap":"-30s"}}`, t); err == nil {
		t.Error("负 breaker_probe_gap 应 fail fast")
	}
}

// TestBreakerProbeGapExplicitZero "0" 必须解析为 0（关闭），而不是回落默认。
//
// 这是 D-400「配置 0 = 关闭 half-open」契约的守门测试。
func TestBreakerProbeGapExplicitZero(t *testing.T) {
	c, err := loadWith(`{"api_key":"k","pool":{"breaker_probe_gap":"0"}}`, t)
	if err != nil {
		t.Fatalf("0 应合法（表示关闭）: %v", err)
	}
	if c.BreakerProbeGapDur != 0 {
		t.Fatalf("显式 0 应解析为 0（关闭 half-open），got %v", c.BreakerProbeGapDur)
	}
}

// TestGlobalMaxInFlightConfig 全局在途上限解析：缺省 0（不限）、显式正整数、负值归一。
func TestGlobalMaxInFlightConfig(t *testing.T) {
	// 缺省 → 0（不限，零回归）
	c, err := loadWith(`{"api_key":"k"}`, t)
	if err != nil {
		t.Fatalf("缺省配置应可加载: %v", err)
	}
	if c.Pool.GlobalMaxInFlight != 0 {
		t.Errorf("缺省 global_max_in_flight 应为 0，got %d", c.Pool.GlobalMaxInFlight)
	}

	// 显式正整数
	c, err = loadWith(`{"api_key":"k","pool":{"global_max_in_flight":50}}`, t)
	if err != nil {
		t.Fatalf("正整数应可加载: %v", err)
	}
	if c.Pool.GlobalMaxInFlight != 50 {
		t.Errorf("global_max_in_flight 应为 50，got %d", c.Pool.GlobalMaxInFlight)
	}
}

// TestSessionStickyDeriveTriState 内容派生会话键的三态（ADR-004）：
//   - 键缺失（nil）  → true（保持既有行为，零回归）
//   - 显式 true      → true
//   - 显式 false     → false（关闭派生）
//
// 牙齿：把 normalize 改成 `c.SessionStickyDerive = c.SessionSticky.Derive != nil`
// → 「键缺失」用例 FAIL（会变成 false，静默关闭所有部署的派生）。
func TestSessionStickyDeriveTriState(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"键缺失 → 默认开", `{"api_key":"k"}`, true},
		{"session_sticky 存在但无 derive → 默认开", `{"api_key":"k","session_sticky":{"enabled":true}}`, true},
		{"显式 true", `{"api_key":"k","session_sticky":{"derive":true}}`, true},
		{"显式 false", `{"api_key":"k","session_sticky":{"derive":false}}`, false},
	}
	for _, c := range cases {
		cfg, err := loadWith(c.body, t)
		if err != nil {
			t.Fatalf("%s: 加载失败 %v", c.name, err)
		}
		if cfg.SessionStickyDerive != c.want {
			t.Errorf("%s: SessionStickyDerive = %v, want %v", c.name, cfg.SessionStickyDerive, c.want)
		}
	}
}

// TestSessionStickyEnabledIsRestartRequired session_sticky 的装配期字段必须列入
// 重启项（R11：热生效字段要标注清楚）。
//
// 背景：sessRouter 只在启动时按 enabled 创建（main 的 session.New）；derive 则在
// NewHandler 时被复制进 handler 的 cfg，而 saveConfig 不重建 handler。两者运行期
// 改都不生效。此前只列了 ttl/gc_interval → 面板保存后显示"已立即生效"却不生效
// （用户以为开着粘性，其实没开）。
func TestSessionStickyEnabledIsRestartRequired(t *testing.T) {
	c := &Config{Listen: ":7863", AuthDir: "./auths", StateFile: "./data/state.json"}
	fields := restartRequiredFields(c)
	have := map[string]bool{}
	for _, f := range fields {
		have[f] = true
	}
	for _, want := range []string{
		"session_sticky.enabled",
		"session_sticky.derive",
		"session_sticky.ttl",
		"session_sticky.gc_interval",
	} {
		if !have[want] {
			t.Errorf("%s 应在重启项清单里，got %v", want, fields)
		}
	}
}
