package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
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

// TestReqlogArchiveRecordsCacheMissUnknown 端到端补证（PLAN B1）：**真实请求 → 真实归档文件**。
//
// 为什么需要它：仓里已有两个测试，但都不覆盖这段链路——
//   - `TestCacheTokensMissUnknownIsNotZero` 只到 `chatStatsReader.CacheTokens()`（解析器）；
//   - `TestReqlogCacheMissUnknownIsAdditive` 只到 `reqlog.Event → JSON`（序列化器）。
//
// 中间「chatrun 把状态填进 Event、Recorder 异步写入 *.jsonl」这一段没有任何测试，
// 而产线的 `cache_miss_unknown` 至今**一次都没观测到**（`requests-*.jsonl` 6916 行里 0 次）。
// 没有这两端连起来的证据，「产线会打这个标记」就只是推断。
//
// 覆盖两个方向（双向牙齿，只测一个方向会被「永远写 true」的假实现骗过）：
//
//	非流式 · 上游只给 hit（无 miss、无 prompt_tokens） → 落盘必须带 cache_miss_unknown:true，且**不得**出现 cache_miss_tokens
//	流式   · 同上（末帧 usage 同形态）                → 同上
//	非流式 · 上游显式给 miss                          → 落盘必须带 cache_miss_tokens 数字，且**不得**出现 cache_miss_unknown
//
// 还原什么会让它 FAIL：把 `chatrun.go` 里 `st.cacheHit, st.cacheMissUnknown = hit, true` 改回
// 旧实现（miss 未知时写 0 并当成观察）→ 前两条红；把标记改成无条件 true → 第三条红。
func TestReqlogArchiveRecordsCacheMissUnknown(t *testing.T) {
	// 夹具要点：本仓**上游恒走 SSE**（CodeBuddy 只吐 event-stream），非流式是 handler 聚合出来的
	// ——`chatflow_test.go` 的子用例 1 就是「非流式请求 + SSE 上游」。所以三个用例的上游体都是 SSE，
	// 只有**请求**的 stream 字段不同。
	const (
		sseUnknown = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_cache_hit_tokens\":800}}\n\n" +
			"data: [DONE]\n\n"
		sseKnown = "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
			"data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_cache_hit_tokens\":100,\"prompt_cache_miss_tokens\":900,\"prompt_tokens\":1000}}\n\n" +
			"data: [DONE]\n\n"
	)

	cases := []struct {
		name        string
		upstreamBdy string
		reqStream   bool // 只有它决定 handler 走聚合还是透传
		wantUnknown bool
		wantMissTok string // "" = 该字段不得出现
	}{
		{name: "非流式·miss 未知", upstreamBdy: sseUnknown, reqStream: false, wantUnknown: true},
		{name: "流式·miss 未知", upstreamBdy: sseUnknown, reqStream: true, wantUnknown: true},
		{name: "非流式·miss 已观测", upstreamBdy: sseKnown, reqStream: false, wantUnknown: false, wantMissTok: `"cache_miss_tokens":900`},
		{name: "流式·miss 已观测", upstreamBdy: sseKnown, reqStream: true, wantUnknown: false, wantMissTok: `"cache_miss_tokens":900`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := chatThenReadArchiveLine(t, tc.upstreamBdy, tc.reqStream)

			if got := strings.Contains(line, `"cache_miss_unknown":true`); got != tc.wantUnknown {
				t.Errorf("cache_miss_unknown 出现=%v，want %v；行=%s", got, tc.wantUnknown, line)
			}
			if tc.wantUnknown {
				if strings.Contains(line, `"cache_miss_tokens"`) {
					t.Errorf("miss 未知时不得写 cache_miss_tokens（0 会被读成全部命中）；行=%s", line)
				}
			}
			if tc.wantMissTok != "" && !strings.Contains(line, tc.wantMissTok) {
				t.Errorf("miss 已观测时必须落盘 %s；行=%s", tc.wantMissTok, line)
			}
		})
	}
}

// chatThenReadArchiveLine 打一次真实 chat 请求，返回归档文件里那一行（已脱敏的 Event JSON）。
// upstreamBody 恒为 SSE 体（上游协议使然），仅请求侧区分流式/非流式。
func chatThenReadArchiveLine(t *testing.T, upstreamBody string, reqStream bool) string {
	t.Helper()
	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Dir: dir, Enabled: true})
	t.Cleanup(rl.Close) // close 带 sync.Once，重复调用安全

	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream:   newFakeUpstream(t, func(string) (int, string, bool) { return 200, upstreamBody, true }),
		RequestLog: rl,
	})

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":false}`
	if reqStream {
		body = `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	}
	if rec := doChat(t, h, "", body); rec.Code != 200 {
		t.Fatalf("status=%d want 200，body=%s", rec.Code, rec.Body.String())
	}

	rl.Close() // 刷盘（显式关一次，不依赖 t.Cleanup 的时机）
	matches, err := filepath.Glob(filepath.Join(dir, "requests-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("归档文件应恰好 1 个，got %v (err=%v)", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("读归档失败：%v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("应恰好 1 行事件，got %d 行：%s", len(lines), raw)
	}
	return lines[0]
}
