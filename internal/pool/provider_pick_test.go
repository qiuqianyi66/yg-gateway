package pool

import (
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// arkOnlyRoute 注入一个「ark-model → ark，其余未命中」的路由（ADR-026 装配语义）。
func arkOnlyRoute(m string) (string, bool) {
	if m == "ark-model" {
		return "ark", true
	}
	return "", false
}

func TestPickRoutesByProvider(t *testing.T) {
	p := New("")
	p.SetProviderOfModel(arkOnlyRoute)
	p.Add(&auth.Auth{UID: "cb"}) // provider 空 = codebuddy
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	got := p.PickExcludingForModel(nil, "ark-model")
	if got == nil || got.UID != "a1" {
		t.Fatalf("命中 ark 的模型应选 ark 账号，got %+v", got)
	}
}

func TestPickNonCreditsSkipsInFlightFull(t *testing.T) {
	p := New("")
	p.SetMaxInFlight(1)
	p.SetProviderOfModel(arkOnlyRoute)
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	p.Add(&auth.Auth{UID: "a2", Provider: "ark"})
	if !p.Acquire("a1") {
		t.Fatal("Acquire a1 失败")
	}
	got := p.PickExcludingForModel(nil, "ark-model")
	if got == nil || got.UID != "a2" {
		t.Fatalf("a1 在途满应选 a2，got %+v", got)
	}
}

func TestPickNonCreditsNoCrossProvider(t *testing.T) {
	p := New("")
	p.SetProviderOfModel(arkOnlyRoute)
	p.Add(&auth.Auth{UID: "cb"})
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	p.Cooldown("a1", CoolHard, time.Hour, "test")
	if got := p.PickExcludingForModel(nil, "ark-model"); got != nil {
		t.Fatalf("ark 账号全冷却应返回 nil，不得跨到 codebuddy，got %+v", got)
	}
}

func TestPickUnmatchedModelOnlyCodebuddy(t *testing.T) {
	p := New("")
	p.SetProviderOfModel(arkOnlyRoute)
	p.Add(&auth.Auth{UID: "cb"})
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	// 未命中 ark 白名单的模型：走 credits 路径，ark 账号必须被排除。
	got := p.PickExcludingForModel(nil, "other-model")
	if got == nil || got.UID != "cb" {
		t.Fatalf("未命中 provider 的模型应只选 codebuddy，got %+v", got)
	}
}

func TestPickProviderNilZeroRegression(t *testing.T) {
	p := New("") // 不注入 providerOfModel（nil）
	p.Add(&auth.Auth{UID: "cb"})
	got := p.Pick()
	if got == nil || got.UID != "cb" {
		t.Fatalf("nil 路由下 credits 路径应照常，got %+v", got)
	}
}

// TestPickNonCreditsRequiresDerivedProvider：牙齿——ark 账号若 provider 字段未从
// auth 凭证派生（空），命中 ark 的请求不得选它（否则会漏到 credits 语义，或反向
// 把无 provider 账号误当 codebuddy 用）。
func TestPickNonCreditsRequiresDerivedProvider(t *testing.T) {
	p := New("")
	p.SetProviderOfModel(arkOnlyRoute)
	// 这个账号本应属于 ark，但 Provider 字段没写（模拟 auth 未派生/旧文件无 provider）。
	p.Add(&auth.Auth{UID: "orphan"})
	if got := p.PickExcludingForModel(nil, "ark-model"); got != nil {
		t.Fatalf("provider 未派生的账号不得被 ark 路由选中，got %+v", got)
	}
}
