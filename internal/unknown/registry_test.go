package unknown

import (
	"errors"
	"testing"
)

// TestRecordRequiresNonEmpty 登记时必须说明「需要什么才能消解」。
// 说不清需要什么，就不是一个可跟踪的未知，只是一句抱怨。
//
// 牙齿：把 requires 校验去掉 → 本测试 FAIL。
func TestRecordRequiresNonEmpty(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Record("账号 u1 的 tier 是什么", ""); err == nil {
		t.Fatal("requires 为空必须报错（说不清需要什么就不能登记）")
	}
	if _, err := r.Record("", "一次带 credit 的调用"); err == nil {
		t.Fatal("question 为空必须报错")
	}
	it, err := r.Record("账号 u1 的 tier 是什么", "一次带 credit 的真实调用")
	if err != nil {
		t.Fatalf("合法登记报错: %v", err)
	}
	if it.ID == "" {
		t.Error("登记后必须有稳定 ID")
	}
	if it.Revision != 1 {
		t.Errorf("首次登记 revision=%d want 1", it.Revision)
	}
	if it.Resolved {
		t.Error("刚登记不应是已消解")
	}
}

// TestResolveStaleRevision 用过期 revision 调 Resolve 必须报错——
// 强制调用方重读后重试，禁止盲重试（rea 的 update_unknown 语义）。
//
// 牙齿：把 revision 校验去掉 → 本测试 FAIL。
func TestResolveStaleRevision(t *testing.T) {
	r := NewRegistry()
	it, _ := r.Record("q", "需要一次真实调用")

	// 用一个过期的 revision
	_, err := r.Resolve(it.ID, it.Revision+1, "ev-1")
	if !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale revision err=%v want ErrStaleRevision", err)
	}
	// 原条目状态不变
	got, ok := r.Get(it.ID)
	if !ok {
		t.Fatal("Get 找不到条目")
	}
	if got.Resolved {
		t.Error("stale 调用不得改动条目状态")
	}
}

// TestResolveNeedsEvidence 消解必须由**证据**完成，不能空手消解。
//
// 牙齿：把 evidenceID 校验去掉 → 本测试 FAIL。
func TestResolveNeedsEvidence(t *testing.T) {
	r := NewRegistry()
	it, _ := r.Record("q", "需要一次真实调用")
	if _, err := r.Resolve(it.ID, it.Revision, ""); err == nil {
		t.Fatal("evidenceID 为空必须报错（消解只能由合格观察证据完成）")
	}
}

// TestResolveSuccess 正确 revision + 证据 ID → 消解成功，revision 递增。
func TestResolveSuccess(t *testing.T) {
	r := NewRegistry()
	it, _ := r.Record("账号 u1 的 tier 是什么", "一次带 credit 的真实调用")

	resolved, err := r.Resolve(it.ID, it.Revision, "ev-credit-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !resolved.Resolved {
		t.Error("消解后 Resolved 应为 true")
	}
	if resolved.Revision != it.Revision+1 {
		t.Errorf("消解后 revision=%d want %d", resolved.Revision, it.Revision+1)
	}
	if len(resolved.Evidence) != 1 || resolved.Evidence[0] != "ev-credit-1" {
		t.Errorf("证据未记录: %v", resolved.Evidence)
	}
	// 再查：状态持久
	got, _ := r.Get(it.ID)
	if !got.Resolved {
		t.Error("Get 应反映已消解状态")
	}
}

// TestResolveUnknownID 消解不存在的 ID 报错（不静默成功）。
func TestResolveUnknownID(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Resolve("no-such-id", 1, "ev-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
}

// TestListOnlyOpen List 只返回**未消解**的未知——已消解的不该继续占据注意力。
//
// 牙齿：把 List 改成返回全部 → 本测试 FAIL。
func TestListOnlyOpen(t *testing.T) {
	r := NewRegistry()
	a, _ := r.Record("q-a", "req-a")
	_, _ = r.Record("q-b", "req-b")

	if got := len(r.List()); got != 2 {
		t.Fatalf("List len=%d want 2", got)
	}
	if _, err := r.Resolve(a.ID, a.Revision, "ev-1"); err != nil {
		t.Fatal(err)
	}
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("消解一条后 List len=%d want 1（已消解不再列出）", len(list))
	}
	if list[0].Question != "q-b" {
		t.Errorf("剩下的是 %q want q-b", list[0].Question)
	}
	// All 仍能看到全部（审计用）
	if got := len(r.All()); got != 2 {
		t.Errorf("All len=%d want 2（含已消解，供审计）", got)
	}
}

// TestRecordSameQuestionDedup 同一 (question, requires) 重复登记应复用同一条目，
// 而不是每次调用都造一条新的——否则面板会被同一个未知刷屏。
//
// 牙齿：去掉去重 → 本测试 FAIL。
func TestRecordSameQuestionDedup(t *testing.T) {
	r := NewRegistry()
	a, _ := r.Record("同一个问题", "同一个条件")
	b, _ := r.Record("同一个问题", "同一个条件")
	if a.ID != b.ID {
		t.Errorf("重复登记应复用条目：%s vs %s", a.ID, b.ID)
	}
	if got := len(r.All()); got != 1 {
		t.Errorf("All len=%d want 1（已去重）", got)
	}
}

// TestResolveIdempotentAfterResolved 已消解的条目再次 Resolve（同 revision）不报错、
// 幂等返回——避免调用方因为重放而炸。
func TestResolveIdempotentAfterResolved(t *testing.T) {
	r := NewRegistry()
	it, _ := r.Record("q", "req")
	first, err := r.Resolve(it.ID, it.Revision, "ev-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Resolve(it.ID, first.Revision, "ev-2")
	if err != nil {
		t.Fatalf("已消解条目再次 resolve 应幂等，got err=%v", err)
	}
	if !again.Resolved {
		t.Error("幂等返回仍应是已消解")
	}
}
