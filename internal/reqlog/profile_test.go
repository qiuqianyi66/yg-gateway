package reqlog

import (
	"testing"
	"time"
)

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestAggregateProfileEmpty(t *testing.T) {
	p := AggregateProfile(nil)
	if p.Window.Events != 0 {
		t.Errorf("events = %d want 0", p.Window.Events)
	}
	if len(p.ByAgent) != 0 {
		t.Errorf("by_agent = %v want empty", p.ByAgent)
	}
	if len(p.TopSessions) != 0 {
		t.Errorf("top_sessions = %v want empty", p.TopSessions)
	}
}

func TestAggregateProfileByAgentAndSessions(t *testing.T) {
	events := []Event{
		// Session A: 3 turns, claude_code, cache hit heavy.
		{Time: mustTime("2026-10-26T10:00:00Z"), SessionFP: "fpAAA", AgentKind: "claude_code",
			PromptTokens: 1000, CompletionTokens: 200, CacheHitTokens: 800, CacheMissTokens: 200},
		{Time: mustTime("2026-10-26T10:01:00Z"), SessionFP: "fpAAA", AgentKind: "claude_code",
			PromptTokens: 1200, CompletionTokens: 300, CacheHitTokens: 1000, CacheMissTokens: 200},
		{Time: mustTime("2026-10-26T10:02:00Z"), SessionFP: "fpAAA", AgentKind: "claude_code",
			PromptTokens: 1500, CompletionTokens: 400, CacheHitTokens: 1200, CacheMissTokens: 300},
		// Session B: 2 turns, browser.
		{Time: mustTime("2026-10-26T11:00:00Z"), SessionFP: "fpBBB", AgentKind: "browser",
			PromptTokens: 100, CompletionTokens: 50},
		{Time: mustTime("2026-10-26T11:05:00Z"), SessionFP: "fpBBB", AgentKind: "browser",
			PromptTokens: 200, CompletionTokens: 80},
		// No-FP scatters: still count in by_agent.requests but not sessions.
		{Time: mustTime("2026-10-26T12:00:00Z"), AgentKind: "browser",
			PromptTokens: 50, CompletionTokens: 10},
		// Unknown UA (not collected): goes to "unknown" bucket, not "other".
		{Time: mustTime("2026-10-26T12:30:00Z"), SessionFP: "fpCCC",
			PromptTokens: 300, CompletionTokens: 30},
	}
	p := AggregateProfile(events)

	if p.Window.Events != 7 {
		t.Errorf("events = %d want 7", p.Window.Events)
	}
	if p.Window.From.Format(time.RFC3339) != "2026-10-26T10:00:00Z" {
		t.Errorf("from = %v", p.Window.From)
	}
	if p.Window.To.Format(time.RFC3339) != "2026-10-26T12:30:00Z" {
		t.Errorf("to = %v", p.Window.To)
	}

	cc := p.ByAgent["claude_code"]
	if cc.Requests != 3 || cc.Sessions != 1 {
		t.Errorf("claude_code = %+v want requests=3 sessions=1", cc)
	}
	if cc.PromptTokens != 3700 || cc.CompletionTokens != 900 {
		t.Errorf("claude_code tokens = prompt=%d completion=%d", cc.PromptTokens, cc.CompletionTokens)
	}
	if cc.CacheHitTokens != 3000 || cc.CacheMissTokens != 700 {
		t.Errorf("claude_code cache = hit=%d miss=%d", cc.CacheHitTokens, cc.CacheMissTokens)
	}

	br := p.ByAgent["browser"]
	if br.Requests != 3 || br.Sessions != 1 {
		t.Errorf("browser = %+v want requests=3 sessions=1", br)
	}

	un := p.ByAgent["unknown"]
	if un.Requests != 1 || un.Sessions != 1 {
		t.Errorf("unknown = %+v want requests=1 sessions=1", un)
	}
	if _, ok := p.ByAgent["other"]; ok {
		t.Errorf("unknown-UA row must NOT land in other bucket; by_agent=%v", p.ByAgent)
	}

	// top_sessions: fpAAA turns=3 first, fpBBB turns=2, fpCCC turns=1.
	if len(p.TopSessions) != 3 {
		t.Fatalf("top_sessions len = %d want 3", len(p.TopSessions))
	}
	if p.TopSessions[0].FP != "fpAAA" || p.TopSessions[0].Turns != 3 {
		t.Errorf("top[0] = %+v", p.TopSessions[0])
	}
	if p.TopSessions[1].FP != "fpBBB" {
		t.Errorf("top[1] = %+v", p.TopSessions[1])
	}
	if p.TopSessions[2].FP != "fpCCC" || p.TopSessions[2].AgentKind != "" {
		t.Errorf("top[2] = %+v (agent_kind should be empty/unknown)", p.TopSessions[2])
	}
}

// G5: cache_miss_unknown=true 行的 miss 不累加（miss 未知 ≠ miss=0），hit 仍累加。
func TestAggregateProfileCacheMissUnknownNotCounted(t *testing.T) {
	events := []Event{
		{Time: mustTime("2026-10-26T10:00:00Z"), SessionFP: "fp1", AgentKind: "claude_code",
			CacheHitTokens: 500, CacheMissTokens: 100},
		{Time: mustTime("2026-10-26T10:01:00Z"), SessionFP: "fp1", AgentKind: "claude_code",
			CacheHitTokens: 300, CacheMissUnknown: true}, // miss 未知
	}
	p := AggregateProfile(events)
	b := p.ByAgent["claude_code"]
	if b.CacheHitTokens != 800 {
		t.Errorf("hit = %d want 800 (hit still accumulates)", b.CacheHitTokens)
	}
	if b.CacheMissTokens != 100 {
		t.Errorf("miss = %d want 100 (unknown-miss row must NOT add 0)", b.CacheMissTokens)
	}
	if got := p.TopSessions[0].CacheMissTokens; got != 100 {
		t.Errorf("session miss = %d want 100", got)
	}
}

// top_sessions 截断到 maxTopSessions。
func TestAggregateProfileTopSessionsTruncated(t *testing.T) {
	events := make([]Event, 0, 25)
	for i := 0; i < 25; i++ {
		fp := string(rune('a'+i)) + "...."
		events = append(events, Event{
			Time:         mustTime("2026-10-26T10:00:00Z"),
			SessionFP:    fp,
			AgentKind:    "other",
			PromptTokens: 10,
		})
	}
	p := AggregateProfile(events)
	if len(p.TopSessions) != maxTopSessions {
		t.Errorf("top_sessions len = %d want %d", len(p.TopSessions), maxTopSessions)
	}
}
