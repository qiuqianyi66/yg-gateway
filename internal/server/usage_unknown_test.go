package server

import (
	"encoding/json"
	"strings"
	"testing"

	"yg-gateway/internal/reqlog"
)

// TestUsageCreditTotalUnknownIsNotZero 钉住 REQ-usage-unknown-admission 的核心三态：
// **未经观察的值不得冒充 0**。非流式成本台账的入口是 usageCreditTotal，它必须能把
//
//	① 没有分母（total_tokens 缺失）→ 不记且不算未知
//	② credit 不可解析（类型错/缺失，有分母）→ **未知**，绝不能记成 credit=0 的观测样本
//	③ credit 真的是 0（真免费模型）→ **观察**，必须照常记账（防过度纠正）
//
// 还原什么会让它 FAIL：把 `c, okC := usage["credit"].(float64)` 改回 `c, _ := ...`
// （即旧实现）→ 第 ② 类断言立刻红，而它正是「把付费号判成免费号」的入口。
func TestUsageCreditTotalUnknownIsNotZero(t *testing.T) {
	cases := []struct {
		name        string
		resp        map[string]any
		wantOK      bool
		wantUnknown bool
		wantCredit  float64
		wantTotal   int
	}{
		{
			name:        "credit 是字符串（上游类型错）→ 未知，不记样本",
			resp:        map[string]any{"usage": map[string]any{"credit": "0.1", "total_tokens": float64(120)}},
			wantOK:      false,
			wantUnknown: true,
			wantTotal:   120,
		},
		{
			name:        "credit 字段缺失但有分母 → 未知",
			resp:        map[string]any{"usage": map[string]any{"total_tokens": float64(120)}},
			wantOK:      false,
			wantUnknown: true,
			wantTotal:   120,
		},
		{
			name:        "credit 是 null → 未知",
			resp:        map[string]any{"usage": map[string]any{"credit": nil, "total_tokens": float64(120)}},
			wantOK:      false,
			wantUnknown: true,
			wantTotal:   120,
		},
		{
			name:       "真免费模型 credit=0 → 观察（防过度纠正）",
			resp:       map[string]any{"usage": map[string]any{"credit": float64(0), "total_tokens": float64(120)}},
			wantOK:     true,
			wantCredit: 0,
			wantTotal:  120,
		},
		{
			name:       "正常 credit → 观察",
			resp:       map[string]any{"usage": map[string]any{"credit": 2.9, "total_tokens": float64(1000)}},
			wantOK:     true,
			wantCredit: 2.9,
			wantTotal:  1000,
		},
		{
			name:      "total_tokens 缺失 → 不记，且不算 credit 未知（没有分母）",
			resp:      map[string]any{"usage": map[string]any{"credit": float64(1)}},
			wantOK:    false,
			wantTotal: 0,
		},
		{
			name:      "total_tokens=0 → 不记",
			resp:      map[string]any{"usage": map[string]any{"credit": float64(1), "total_tokens": float64(0)}},
			wantOK:    false,
			wantTotal: 0,
		},
		{
			name:   "无 usage 段 → 不记",
			resp:   map[string]any{},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			credit, total, ok, unknown := usageCreditTotal(tc.resp)
			if ok != tc.wantOK || unknown != tc.wantUnknown {
				t.Fatalf("ok=%v unknown=%v，want ok=%v unknown=%v（未知≠0 是本条的全部意义）",
					ok, unknown, tc.wantOK, tc.wantUnknown)
			}
			if credit != tc.wantCredit {
				t.Errorf("credit=%v，want %v", credit, tc.wantCredit)
			}
			if total != tc.wantTotal {
				t.Errorf("total=%v，want %v", total, tc.wantTotal)
			}
		})
	}
}

// TestCacheTokensMissUnknownIsNotZero 钉住流式路径的四态返回：
// miss **未知**（既无显式 miss 字段、prompt 也不可用）必须与 miss=0（全部命中）区分开。
// 0 会被当成「全部命中」，把命中率抬到 100% 并压掉低命中率告警（issue #92 的信号）。
//
// 还原什么会让它 FAIL：把 `return hit, 0, false, true` 改回旧实现 `return hit, 0, true`
// （即 missKnown 恒 true）→ 第 ④ 类断言红。
func TestCacheTokensMissUnknownIsNotZero(t *testing.T) {
	cases := []struct {
		name      string
		s         *chatStatsReader
		wantOK    bool
		wantMissK bool
		wantHit   int64
		wantMiss  int64
	}{
		{
			name:      "上游显式 miss=0 → 观察（全命中是真的）",
			s:         &chatStatsReader{hasCacheHit: true, cacheHit: 900, hasCacheMiss: true, cacheMiss: 0, hasPromptTokens: true, promptTokens: 900},
			wantOK:    true,
			wantMissK: true,
			wantHit:   900,
			wantMiss:  0,
		},
		{
			name:      "上游显式 miss>0 → 观察",
			s:         &chatStatsReader{hasCacheHit: true, cacheHit: 100, hasCacheMiss: true, cacheMiss: 900, hasPromptTokens: true, promptTokens: 1000},
			wantOK:    true,
			wantMissK: true,
			wantHit:   100,
			wantMiss:  900,
		},
		{
			name:      "miss 缺失 + prompt 可用 → 推导（仍是观察）",
			s:         &chatStatsReader{hasCacheHit: true, cacheHit: 800, hasPromptTokens: true, promptTokens: 1000},
			wantOK:    true,
			wantMissK: true,
			wantHit:   800,
			wantMiss:  200,
		},
		{
			name:      "miss 缺失 + prompt 不可用 → 未知（不得当 0）",
			s:         &chatStatsReader{hasCacheHit: true, cacheHit: 800},
			wantOK:    true,
			wantMissK: false,
			wantHit:   800,
			wantMiss:  0,
		},
		{
			name:      "miss 缺失 + prompt < hit（上游数据自相矛盾）→ 未知",
			s:         &chatStatsReader{hasCacheHit: true, cacheHit: 800, hasPromptTokens: true, promptTokens: 700},
			wantOK:    true,
			wantMissK: false,
			wantHit:   800,
			wantMiss:  0,
		},
		{
			name:   "hit 不可得 → 不参与统计",
			s:      &chatStatsReader{hasPromptTokens: true, promptTokens: 1000},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit, miss, missKnown, ok := tc.s.CacheTokens()
			if ok != tc.wantOK || missKnown != tc.wantMissK {
				t.Fatalf("ok=%v missKnown=%v，want ok=%v missKnown=%v", ok, missKnown, tc.wantOK, tc.wantMissK)
			}
			if hit != tc.wantHit || miss != tc.wantMiss {
				t.Errorf("hit=%d miss=%d，want hit=%d miss=%d", hit, miss, tc.wantHit, tc.wantMiss)
			}
		})
	}
}

// TestReqlogCacheMissUnknownIsAdditive 钉住审计流的字段语义：未知打标记、不打数字，
// 且该标记是**纯增量**（不出现时，历史行与「观察到的 0」的形态完全不变）。
func TestReqlogCacheMissUnknownIsAdditive(t *testing.T) {
	unknown := reqlog.Event{Model: "m", CacheMissUnknown: true}
	raw, err := json.Marshal(unknown)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, `"cache_miss_unknown":true`) {
		t.Errorf("miss 未知时必须带 cache_miss_unknown 标记，got %s", got)
	}
	if strings.Contains(got, `"cache_miss_tokens"`) {
		t.Errorf("miss 未知时不得写 cache_miss_tokens 数字（0 会被读成全部命中），got %s", got)
	}

	observedZero := reqlog.Event{Model: "m", CacheHitTokens: 900, CacheMissTokens: 0}
	raw2, _ := json.Marshal(observedZero)
	if got2 := string(raw2); strings.Contains(got2, `"cache_miss_unknown"`) {
		t.Errorf("观察到的 miss=0 不得带未知标记，got %s", got2)
	}
}
