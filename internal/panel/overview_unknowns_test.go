package panel

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/unknown"
)

// TestPanelOverviewSurfacesUnknowns 钉住面板「有值态」的数据通路（B1b / A-7）。
//
// 前端 `unkBadge(s.unknowns)` 与 `unkNoteText(accounts)` 的渲染逻辑已有 JS 级测试
// （`TestAppJSUnknownBadgeAndNote`），但**没人验证过 overview 到底带不带这个字段**：
// 一旦它被漏掉，徽章永远停在空态，而「空态」与「没有未知」在面板上不可区分——
// 正是 AGENTS.md「没覆盖 = 未知，不是没有」要防的那类静默失真。
func TestPanelOverviewSurfacesUnknowns(t *testing.T) {
	a := &auth.Auth{UID: "u-overview-unknown-0001", Nickname: "未知号", AccessToken: "tok"}
	pl := pool.New("")
	pl.Add(a)
	reg := unknown.NewRegistry()
	pl.SetUnknownRegistry(reg)
	pl.TrackCostUnknown(a.UID, "glm-5.2")

	p := New(Config{Version: "test", Pool: pl})
	rec := httptest.NewRecorder()
	p.overview(rec, httptest.NewRequest("GET", "/panel/api/overview", nil))

	if rec.Code != 200 {
		t.Fatalf("status=%d want 200", rec.Code)
	}
	var resp struct {
		Accounts []struct {
			UID      string         `json:"uid"`
			Unknowns []unknown.Item `json:"unknowns"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("overview 不是合法 JSON: %v", err)
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("accounts=%d want 1", len(resp.Accounts))
	}
	unk := resp.Accounts[0].Unknowns
	if len(unk) != 1 {
		t.Fatalf("accounts[0].unknowns=%d want 1（面板徽章靠这个字段；缺失即永远空态）", len(unk))
	}
	it := unk[0]
	if !strings.Contains(it.Question, "实际成本未知") {
		t.Errorf("question=%q want 含「实际成本未知」（面板悬停直接展示）", it.Question)
	}
	if it.Requires == "" {
		t.Error("requires 为空：悬停提示会缺「需要什么才能消解」")
	}
	if it.Revision < 1 || it.Resolved {
		t.Errorf("revision=%d resolved=%v want ≥1/false（未消解条目）", it.Revision, it.Resolved)
	}

	// 消解后必须从 overview 里消失（否则「有值态」会永远挂着一条已经解决的未知）。
	if _, err := reg.Resolve(it.ID, it.Revision, "note_model_cost:glm-5.2"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	rec2 := httptest.NewRecorder()
	p.overview(rec2, httptest.NewRequest("GET", "/panel/api/overview", nil))
	var resp2 struct {
		Accounts []struct {
			Unknowns []unknown.Item `json:"unknowns"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("overview(2) 不是合法 JSON: %v", err)
	}
	if n := len(resp2.Accounts[0].Unknowns); n != 0 {
		t.Fatalf("消解后 unknowns=%d want 0", n)
	}
}
