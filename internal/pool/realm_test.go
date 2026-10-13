package pool

import (
	"reflect"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// realmPool 构造一个含 cn/global 账号的池，并确保 globalEnabled 开关开启（缺省）。
func realmPool(t *testing.T) *Pool {
	t.Helper()
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := New("")
	// CN 账号（显式 cn realm 或空 realm+cn domain 均可）→ Realm()=="cn"
	p.Add(&auth.Auth{UID: "cn1", Domain: "www.codebuddy.cn"})
	p.Add(&auth.Auth{UID: "cn2", Domain: ""}) // 空 domain → cn
	// global 账号 → Realm()=="global"
	p.Add(&auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"})
	p.Add(&auth.Auth{UID: "g2", Domain: "workbuddy.ai"})
	return p
}

// realmOf 直接读账号 realm（等价 e.a.Realm()）。
func realmOf(a *auth.Auth) string { return a.Realm() }

func TestAvailableUIDsForRealm(t *testing.T) {
	p := realmPool(t)

	// 全部 healthy → cn 集合只有 cn1/cn2，global 集合只有 g1/g2。
	got := p.AvailableUIDsForRealm("cn")
	want := []string{"cn1", "cn2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForRealm(cn)=%v want %v", got, want)
	}
	got = p.AvailableUIDsForRealm("global")
	want = []string{"g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForRealm(global)=%v want %v", got, want)
	}

	// realm=="" 退化为现状（等价 AvailableUIDs：全部 healthy）。
	got = p.AvailableUIDsForRealm("")
	want = []string{"cn1", "cn2", "g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForRealm()=%v want %v", got, want)
	}
}

func TestAvailableUIDsForModelRealm(t *testing.T) {
	p := realmPool(t)
	// 6004 模型冷却只落在 cn-1 上 → AvailableUIDsForModelRealm("glm-5.2","cn") 排除它，
	// 但 cn 集合至少还保底 cn-2；global 集合不受 cn 冷却影响。
	p.CooldownSoftForModel("cn1", 10*time.Minute, time.Now().Add(30*time.Minute), "glm-5.2", "model 6004")

	got := p.AvailableUIDsForModelRealm("glm-5.2", "cn")
	if len(got) != 1 || got[0] != "cn2" {
		t.Errorf("AvailableUIDsForModelRealm(glm-5.2,cn)=%v want [cn2]", got)
	}
	got = p.AvailableUIDsForModelRealm("glm-5.2", "global")
	want := []string{"g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForModelRealm(glm-5.2,global)=%v want %v", got, want)
	}
	// realm=="" → 等价 AvailableUIDsForModel（模型豁免照常：cn1 仍被该模型冷却排除）。
	got = p.AvailableUIDsForModelRealm("glm-5.2", "")
	want = []string{"cn2", "g1", "g2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AvailableUIDsForModelRealm(glm-5.2)=%v want %v", got, want)
	}
}

func TestWeightedAvailableUIDsForModelRealm(t *testing.T) {
	p := realmPool(t)
	now := time.Now()
	p.SetCreditsDetailed("cn2", 100, 100, 100, now.Add(time.Hour), 100)

	got := p.WeightedAvailableUIDsForModelRealm("glm-5.2", "cn")
	want := []string{"cn1", "cn2", "cn2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("weighted cn candidates=%v want %v", got, want)
	}

	// 在途满载账号不进入候选，快过期权重不得突破现有并发上限。
	p.SetMaxInFlight(1)
	if !p.Acquire("cn2") {
		t.Fatal("failed to acquire cn2")
	}
	got = p.WeightedAvailableUIDsForModelRealm("glm-5.2", "cn")
	want = []string{"cn1"}
	p.Release("cn2")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("in-flight-full weighted candidates=%v want %v", got, want)
	}

	// 关闭总开关后逐账号只出现一次，旧行为完全恢复。
	p.SetPreferExpiring(false)
	got = p.WeightedAvailableUIDsForModelRealm("glm-5.2", "cn")
	want = []string{"cn1", "cn2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("disabled weighted cn candidates=%v want %v", got, want)
	}
}

func TestPickExcludingForRealm(t *testing.T) {
	p := realmPool(t)
	// realm=cn → 只从 cn 集合选。
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		a := p.PickExcludingForRealm(nil, "", "cn")
		if a == nil {
			t.Fatal("PickExcludingForRealm(cn) returned nil")
		}
		seen[a.UID] = true
		if realmOf(a) != "cn" {
			t.Fatalf("PickExcludingForRealm(cn) returned global account %s", a.UID)
		}
	}
	// global 同样只从 global 集合。
	for i := 0; i < 50; i++ {
		a := p.PickExcludingForRealm(nil, "", "global")
		if a == nil {
			t.Fatal("PickExcludingForRealm(global) returned nil")
		}
		if realmOf(a) != "global" {
			t.Fatalf("PickExcludingForRealm(global) returned cn account %s", a.UID)
		}
	}
	// realm=="" → 退化现状：所有 healthy 都能选。
	for i := 0; i < 50; i++ {
		a := p.PickExcludingForRealm(nil, "", "")
		if a == nil {
			t.Fatal("PickExcludingForRealm() returned nil")
		}
	}
}

func TestPickExcludingForRealmTried(t *testing.T) {
	p := realmPool(t)
	// tried 排除在 realm 过滤之后（维持请求级轮换语义）。
	a := p.PickExcludingForRealm(map[string]bool{"cn1": true}, "", "cn")
	if a == nil {
		t.Fatal("tried cn1 → nil")
	}
	if a.UID == "cn1" {
		t.Fatalf("tried cn1 still picked: %s", a.UID)
	}
	if realmOf(a) != "cn" {
		t.Fatalf("picked non-cn %s", a.UID)
	}
}

// TestPickGroupFilter：group 非空时候选集只含同组账号；空 group = 不限池（零回归）。
// P1-B 账号池分组。
func TestPickGroupFilter(t *testing.T) {
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := New("")
	// 4 个号：team-a 两个，team-b 一个，未分组一个。
	p.Add(&auth.Auth{UID: "a1", Domain: "www.codebuddy.cn", Group: "team-a"})
	p.Add(&auth.Auth{UID: "a2", Domain: "www.codebuddy.cn", Group: "team-a"})
	p.Add(&auth.Auth{UID: "b1", Domain: "www.codebuddy.cn", Group: "team-b"})
	p.Add(&auth.Auth{UID: "plain", Domain: "www.codebuddy.cn"}) // 空 group

	// group=team-a → 只能选 a1/a2。
	for i := 0; i < 50; i++ {
		a := p.PickExcludingForRealmGroup(nil, "", "cn", "team-a")
		if a == nil {
			t.Fatal("team-a 组应有号，返回 nil")
		}
		if a.Group != "team-a" {
			t.Fatalf("group=team-a 却选到 %s（group=%q）", a.UID, a.Group)
		}
	}
	// group=team-b → 只能选 b1。
	for i := 0; i < 20; i++ {
		a := p.PickExcludingForRealmGroup(nil, "", "cn", "team-b")
		if a == nil || a.UID != "b1" {
			t.Fatalf("group=team-b 应只选 b1, got %v", a)
		}
	}
	// group=空 → 所有 cn 号都能选（零回归）。
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		a := p.PickExcludingForRealmGroup(nil, "", "cn", "")
		if a == nil {
			t.Fatal("group 空应能选到号")
		}
		seen[a.UID] = true
	}
	for _, uid := range []string{"a1", "a2", "b1", "plain"} {
		if !seen[uid] {
			t.Fatalf("group 空时 %s 从未被选到（候选集不对）", uid)
		}
	}
	// group=不存在的组 → 无候选，返回 nil。
	if a := p.PickExcludingForRealmGroup(nil, "", "cn", "no-such-group"); a != nil {
		t.Fatalf("不存在的组应返回 nil, got %s", a.UID)
	}
}

// TestPickGroupWithRealm：realm 与 group 过滤叠加——两个谓词都要满足。
func TestPickGroupWithRealm(t *testing.T) {
	withNoPickGap(t)
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })
	p := New("")
	// cn + team-a；global + team-a；cn + team-b。
	p.Add(&auth.Auth{UID: "cn-a1", Domain: "www.codebuddy.cn", Group: "team-a"})
	p.Add(&auth.Auth{UID: "gl-a1", Domain: "www.workbuddy.ai", Group: "team-a"})
	p.Add(&auth.Auth{UID: "cn-b1", Domain: "www.codebuddy.cn", Group: "team-b"})

	// realm=cn + group=team-a → 只能选 cn-a1。
	for i := 0; i < 20; i++ {
		a := p.PickExcludingForRealmGroup(nil, "", "cn", "team-a")
		if a == nil || a.UID != "cn-a1" {
			t.Fatalf("realm=cn+group=team-a 应只选 cn-a1, got %v", a)
		}
	}
	// realm=global + group=team-a → 只能选 gl-a1。
	for i := 0; i < 20; i++ {
		a := p.PickExcludingForRealmGroup(nil, "", "global", "team-a")
		if a == nil || a.UID != "gl-a1" {
			t.Fatalf("realm=global+group=team-a 应只选 gl-a1, got %v", a)
		}
	}
}
