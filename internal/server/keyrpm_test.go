package server

import (
	"testing"

	"yg-gateway/internal/httpauth"
)

func TestKeyRPMGuardAllow(t *testing.T) {
	g := newKeyRPMGuard()
	limited := &httpauth.Key{ID: "k1", RPM: 2}
	unlimited := &httpauth.Key{ID: "k2", RPM: 0}

	// unlimited: RPM=0 恒放行，打 100 次都过。
	for i := 0; i < 100; i++ {
		if !g.allow(unlimited) {
			t.Fatalf("RPM=0 的 key 应恒放行（第 %d 次被拒）", i)
		}
	}

	// limited: 上限 2，前 2 次过，第 3 次拒。
	if !g.allow(limited) {
		t.Fatal("第 1 次应放行")
	}
	if !g.allow(limited) {
		t.Fatal("第 2 次应放行")
	}
	if g.allow(limited) {
		t.Fatal("第 3 次应被拒（超过 RPM=2）")
	}

	// nil key（未启用鉴权）恒放行。
	if !g.allow(nil) {
		t.Fatal("nil key 应恒放行")
	}
}

func TestKeyRPMGuardIsolation(t *testing.T) {
	g := newKeyRPMGuard()
	k1 := &httpauth.Key{ID: "k1", RPM: 1}
	k2 := &httpauth.Key{ID: "k2", RPM: 1}

	// k1 打满后，k2 不受影响（计数按 keyID 隔离）。
	if !g.allow(k1) {
		t.Fatal("k1 第 1 次应放行")
	}
	if g.allow(k1) {
		t.Fatal("k1 第 2 次应被拒")
	}
	if !g.allow(k2) {
		t.Fatal("k2 不受 k1 影响，第 1 次应放行")
	}
}
