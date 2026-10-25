// unknown_all_test.go AllOpenUnknowns 的双向覆盖（独立复审 🟡-1 修复：
// 原测试只覆盖「resolved 被排除」半边——实现若退化为 return nil 也全绿）。
package pool

import (
	"testing"

	"yg-gateway/internal/unknown"
)

func TestAllOpenUnknownsIncludesOpenExcludesResolved(t *testing.T) {
	p := New("")
	reg := unknown.NewRegistry()
	p.SetUnknownRegistry(reg)

	openIt, err := reg.Record("开条目（未消解）", "需要某观察")
	if err != nil {
		t.Fatal(err)
	}
	resIt, err := reg.Record("闭条目（已消解）", "需要另一观察")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Resolve(resIt.ID, resIt.Revision, "ev-x"); err != nil {
		t.Fatal(err)
	}

	got := p.AllOpenUnknowns()
	if len(got) != 1 {
		t.Fatalf("AllOpenUnknowns = %d 条, want 1（开条目必须在场）：%+v", len(got), got)
	}
	if got[0].ID != openIt.ID || got[0].Resolved {
		t.Fatalf("在场条目错：%+v", got[0])
	}

	// 全量口径与来源无关：agent 登记的条目（无 unknownSeen 键）同样可见——
	// 这正是 2026-10-24 实测缺口（openCostUnknownsLocked 看不到 agent 条目）的回归锚。
	agIt, err := reg.Record("agent 登记的口径矛盾", "权威对账")
	if err != nil {
		t.Fatal(err)
	}
	got = p.AllOpenUnknowns()
	if len(got) != 2 {
		t.Fatalf("agent 来源条目应在全量口径可见 = %d, want 2", len(got))
	}
	found := false
	for _, it := range got {
		if it.ID == agIt.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("agent 登记条目未出现在全量口径")
	}

	// nil registry（未注入）安全返回 nil。
	var bare Pool
	if got := bare.AllOpenUnknowns(); got != nil {
		t.Fatalf("nil registry 应返回 nil，got %+v", got)
	}
}
