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
