// canary_test.go 规则心跳测试：健康路径（规则全存活 → 无失败）与检测路径
// （人为弄死规则前提 → 心跳必须报 FAIL——心跳自己的牙齿）。
package opsagent

import (
	"testing"
	"time"
)

func TestCanaryAllRulesAlive(t *testing.T) {
	a := New(Config{HealthyFrac: 0.5}, Observers{Now: func() time.Time { return fixedNow }}, nil, nil)
	if failures := a.CanaryRound(fixedNow); len(failures) != 0 {
		t.Fatalf("合成观察应让全部规则存活：%v", failures)
	}
}

// TestCanaryDetectsDeadRule 检测路径：HealthyFrac 压到 0.01（healthy 1/4 = 0.25
// 永远达不到 → capacity 永假）——心跳必须抓到这个「静默死亡」。
func TestCanaryDetectsDeadRule(t *testing.T) {
	a := New(Config{HealthyFrac: 0.01}, Observers{Now: func() time.Time { return fixedNow }}, nil, nil)
	failures := a.CanaryRound(fixedNow)
	found := false
	for _, f := range failures {
		if len(f) > 8 && f[:8] == "capacity" {
			found = true
		}
	}
	if !found {
		t.Fatalf("心跳未抓到 capacity 死亡：%v", failures)
	}
	// contradiction 规则不受 HealthyFrac 影响：仍应存活。
	for _, f := range failures {
		if len(f) > 21 && f[:21] == "credit-402-contradict" {
			t.Fatalf("contradiction 规则不应被 HealthyFrac 杀死：%v", failures)
		}
	}
}
