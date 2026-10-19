package pool

import (
	"testing"

	"yg-gateway/internal/auth"
)

// TestPickSkipsInFlightFullAccount 钉住「在途占满的号不参与选号」（同账号不被并发超发）。
//
// 缺口来源（2026-10-19 全清单复跑）：把 pick.go 的 `if p.inFlightFull(e) { continue }`
// 改成 `if false && ...` 仍然全绿——也就是这条限制此前**没有任何测试盯着**，
// 而它保护的正是「同一账号被并发超发」这种只在高并发下才显形的故障。
func TestPickSkipsInFlightFullAccount(t *testing.T) {
	withNoPickGap(t)
	p := New("")
	p.SetMaxInFlight(1)
	p.SetRandomSource(func(int64) int64 { return 0 }) // 固定随机源：权重最高者被选中

	full := &auth.Auth{UID: "u-full", Nickname: "满"}
	free := &auth.Auth{UID: "u-free", Nickname: "空"}
	auth.BackfillRealmFor(full, "cn")
	auth.BackfillRealmFor(free, "cn")
	p.Add(full)
	p.Add(free)
	p.SetCredits("u-full", 1000, 0) // 权重最高：没被在途挡住时一定选它
	p.SetCredits("u-free", 100, 0)

	if !p.Acquire("u-full") {
		t.Fatal("Acquire(u-full) 应成功（maxInFlight=1 且此前无在途）")
	}
	defer p.Release("u-full")

	got := p.PickExcluding(nil)
	if got == nil {
		t.Fatal("还有空闲号可选，Pick 不应返回 nil")
	}
	if got.UID == "u-full" {
		t.Fatalf("Pick 选中了在途占满的号（uid=%s）：在途判定失效，同一账号会被并发超发", got.UID)
	}

	// 对照：释放后同一个号必须能被选中——证明上一步的排除来自「在途占满」，不是权重或健康度。
	p.Release("u-full")
	if got2 := p.PickExcluding(nil); got2 == nil || got2.UID != "u-full" {
		t.Fatalf("释放后应选中权重最高的 u-full，got %v", got2)
	}
}
