package provider

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// validDescriptor 构造一个字段齐全的合法描述符（测试基线）。
func validDescriptor(id string) Descriptor {
	return Descriptor{
		ID:    id,
		Kind:  KindOpenAICompat,
		Quota: QuotaMetered,
	}
}

func TestRegisterGetValid(t *testing.T) {
	r := NewRegistry()
	d := Descriptor{
		ID:          "ark-main",
		Kind:        KindArk,
		Quota:       QuotaMetered,
		ChatBaseURL: "https://ark.example/api/v3",
		Models:      []string{"doubao-seed-2-1-pro", "doubao-seed-2-1-lite"},
		Enabled:     true,
	}
	if err := r.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("ark-main")
	if !ok {
		t.Fatal("Get(ark-main) 未命中")
	}
	if got.Kind != KindArk || got.Quota != QuotaMetered || !got.Enabled {
		t.Errorf("Get 返回字段不符: %+v", got)
	}
	if len(got.Models) != 2 || got.Models[0] != "doubao-seed-2-1-pro" {
		t.Errorf("Models 不符: %v", got.Models)
	}
}

func TestRegisterRejectsEmptyID(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Descriptor{Kind: KindArk, Quota: QuotaMetered}); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("空 ID 应返回 ErrEmptyID，实得 %v", err)
	}
	if r.Len() != 0 {
		t.Errorf("校验失败不得写入，Len=%d", r.Len())
	}
}

func TestRegisterRejectsInvalidKind(t *testing.T) {
	r := NewRegistry()
	err := r.Register(Descriptor{ID: "x", Kind: Kind("bogus"), Quota: QuotaMetered})
	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("非法 Kind 应返回 ErrInvalidKind，实得 %v", err)
	}
}

func TestRegisterRejectsInvalidQuota(t *testing.T) {
	r := NewRegistry()
	err := r.Register(Descriptor{ID: "x", Kind: KindArk, Quota: QuotaKind("bogus")})
	if !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("非法 Quota 应返回 ErrInvalidQuota，实得 %v", err)
	}
}

func TestRegisterRejectsDuplicateID(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(validDescriptor("dup")); err != nil {
		t.Fatalf("首次登记失败: %v", err)
	}
	// 同 ID 不同字段也算重复：注册表以 ID 为唯一键，不做静默覆盖。
	err := r.Register(Descriptor{ID: "dup", Kind: KindArk, Quota: QuotaCredits})
	if !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("重复 ID 应返回 ErrDuplicateID，实得 %v", err)
	}
	if r.Len() != 1 {
		t.Errorf("重复登记不得写入，Len=%d", r.Len())
	}
}

func TestGetUnknownID(t *testing.T) {
	r := NewRegistry()
	got, ok := r.Get("nope")
	if ok {
		t.Fatal("未知 ID 不应命中")
	}
	if got.ID != "" {
		t.Errorf("未知 ID 应返回零值 Descriptor，实得 %+v", got)
	}
}

func TestListSortedByID(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"zeta", "alpha", "mike"} {
		if err := r.Register(validDescriptor(id)); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}
	got := r.List()
	if len(got) != 3 {
		t.Fatalf("List 长度 = %d，want 3", len(got))
	}
	want := []string{"alpha", "mike", "zeta"}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("List[%d] = %q，want %q（按 ID 稳定排序）", i, got[i].ID, id)
		}
	}
}

// TestModelsDeepCopy 守住「注册表内容不被外部持有者改写」这条契约：
// Register 前改原切片、List/Get 后改返回切片，都不得影响已登记内容。
func TestModelsDeepCopy(t *testing.T) {
	r := NewRegistry()
	models := []string{"m1", "m2"}
	d := validDescriptor("deep")
	d.Models = models
	if err := r.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}

	models[0] = "mutated-by-caller"
	got, _ := r.Get("deep")
	if got.Models[0] != "m1" {
		t.Errorf("调用方改原切片影响了注册表: %v", got.Models)
	}

	got.Models[0] = "mutated-by-reader"
	again, _ := r.Get("deep")
	if again.Models[0] != "m1" {
		t.Errorf("调用方改 Get 返回值影响了注册表: %v", again.Models)
	}

	listed := r.List()
	listed[0].Models[0] = "mutated-by-list"
	fresh, _ := r.Get("deep")
	if fresh.Models[0] != "m1" {
		t.Errorf("调用方改 List 返回值影响了注册表: %v", fresh.Models)
	}
}

// TestConcurrentRegisterAndList 并发写 + 并发读不得 panic / 丢条目。
// 本机无 gcc，-race 由 CI 独占（AGENTS.md），此处只守「不崩、不丢」。
func TestConcurrentRegisterAndList(t *testing.T) {
	r := NewRegistry()
	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("p-%03d", i)
			if err := r.Register(validDescriptor(id)); err != nil {
				t.Errorf("并发 Register(%s): %v", id, err)
				return
			}
			if _, ok := r.Get(id); !ok {
				t.Errorf("登记后立即 Get(%s) 未命中", id)
			}
			if len(r.List()) == 0 {
				t.Errorf("List 返回空（已有登记）")
			}
		}(i)
	}
	wg.Wait()
	if r.Len() != n {
		t.Errorf("Len = %d，want %d（并发登记丢条目）", r.Len(), n)
	}
	if len(r.List()) != n {
		t.Errorf("List 长度 = %d，want %d", len(r.List()), n)
	}
}
