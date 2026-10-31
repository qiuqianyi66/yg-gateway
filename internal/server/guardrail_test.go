// guardrail_test.go 上下文护栏测试（REQ-context-guardrail-2026-10-31 / ADR-019）。
//
// 分层：单元层直接测 Guardrail.Check 语义（零回归 / 阈值边界 / 会话限流窗口 /
// 散客 / 统计 / 对照样本 / 惰性 GC）；HTTP 语义层测 applyGuardrail 的
// 413/429/X-Gateway-Warning/事件标记（脱离完整 chat 链路，同 finalErrorOf 测试风格）。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/session"
	"yg-gateway/internal/upstream"
)

// newGuardrailForTest 构造护栏并注入可控时钟（窗口翻转测试用）。
func newGuardrailForTest(now *time.Time) *Guardrail {
	g := NewGuardrail()
	g.now = func() time.Time { return *now }
	return g
}

// ---- 单元层：Guardrail.Check ----

func TestGuardrailAllZeroPasses(t *testing.T) {
	// G1：全 0 配置恒放行——任何估算值都不触发 warn/blocked/rateLimited（零回归）。
	now := time.Now()
	g := newGuardrailForTest(&now)
	d := g.Check("sess", 900_000) // 即使超大请求也不拦（护栏关闭 = 旧行为）
	if d.Result != GuardrailPass {
		t.Fatalf("全 0 配置 Result = %v，want GuardrailPass", d.Result)
	}
	if d.RateLimited {
		t.Fatalf("全 0 配置 RateLimited = true，want false")
	}
	if d.WarnTokens != 0 || d.MaxTokens != 0 {
		t.Fatalf("全 0 配置阈值 = (%d,%d)，want (0,0)", d.WarnTokens, d.MaxTokens)
	}
	if gs := g.Snapshot(); gs.WarnHits+gs.BlockedHits+gs.SessionRPMHits != 0 {
		t.Fatalf("全 0 配置命中统计非零：%+v", gs)
	}
}

func TestGuardrailCheckNil(t *testing.T) {
	// nil 护栏（未装配）恒放行，调用方无 nil 恐慌。
	var g *Guardrail
	d := g.Check("sess", 900_000)
	if d.Result != GuardrailPass || d.RateLimited {
		t.Fatalf("nil 护栏 Check = %+v，want 全放行", d)
	}
	if s := g.Snapshot(); s.WarnHits != 0 {
		t.Fatalf("nil 护栏 Snapshot = %+v", s)
	}
}

func TestGuardrailWarnNotBlocking(t *testing.T) {
	// G2：超 warn 阈值 → Warn 且不阻断（RateLimited=false），命中计数 +1。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetContextWarnTokens(100)
	g.SetContextMaxTokens(200)

	d := g.Check("sess", 150)
	if d.Result != GuardrailWarn {
		t.Fatalf("est=150 warn=100 → Result = %v，want GuardrailWarn", d.Result)
	}
	if d.RateLimited {
		t.Fatalf("warn 命中不得限流")
	}
	if d.WarnTokens != 100 || d.MaxTokens != 200 {
		t.Fatalf("Decision 阈值 = (%d,%d)，want (100,200)", d.WarnTokens, d.MaxTokens)
	}
	gs := g.Snapshot()
	if gs.WarnHits != 1 || gs.BlockedHits != 0 {
		t.Fatalf("命中统计 = %+v，want warn=1 blocked=0", gs)
	}
	if gs.LastHitAt.IsZero() {
		t.Fatalf("LastHitAt 未记录")
	}
}

func TestGuardrailBlockedWinsOverWarn(t *testing.T) {
	// G3 语义：同时超 warn 与 max 时 blocked 优先（拦截是更重的决定）。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetContextWarnTokens(100)
	g.SetContextMaxTokens(200)
	d := g.Check("sess", 250)
	if d.Result != GuardrailBlocked {
		t.Fatalf("est=250 max=200 → Result = %v，want GuardrailBlocked", d.Result)
	}
	gs := g.Snapshot()
	if gs.BlockedHits != 1 || gs.WarnHits != 0 {
		t.Fatalf("命中统计 = %+v，want blocked=1 warn=0", gs)
	}
}

func TestGuardrailThresholdBoundary(t *testing.T) {
	// G8：阈值边界——恰等于命中，差 1 不命中。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetContextWarnTokens(100)
	g.SetContextMaxTokens(200)

	cases := []struct {
		est  int64
		want GuardrailResult
	}{
		{99, GuardrailPass},  // 差 1 不 warn
		{100, GuardrailWarn}, // 恰等于 warn
		{199, GuardrailWarn}, // max 未到
		{200, GuardrailBlocked},
	}
	for _, c := range cases {
		if got := g.Check("sess", c.est).Result; got != c.want {
			t.Fatalf("est=%d → %v，want %v", c.est, got, c.want)
		}
	}
}

func TestGuardrailSessionRPMWindow(t *testing.T) {
	// G6：固定窗口语义（同 keyRPM）——先 record 后 allow，第 RPM+1 次被拒；
	// 分钟翻转后窗口重置（被拒那次也计入窗口 = 统计真实速率）。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetSessionRPM(2)

	// 窗口内：第 1、2 次放行，第 3 次拒。
	for i := 1; i <= 3; i++ {
		d := g.Check("sess-a", 10)
		if i <= 2 && d.RateLimited {
			t.Fatalf("第 %d 次应放行，被拒", i)
		}
		if i == 3 && !d.RateLimited {
			t.Fatalf("第 3 次应被拒（RPM=2）")
		}
	}
	gs := g.Snapshot()
	if gs.SessionRPMHits != 1 {
		t.Fatalf("SessionRPMHits = %d，want 1", gs.SessionRPMHits)
	}

	// 分钟翻转：窗口重置，第 1 次又放行。
	now = now.Add(time.Minute + time.Second)
	d := g.Check("sess-a", 10)
	if d.RateLimited {
		t.Fatalf("窗口翻转后第 1 次应放行")
	}
}

func TestGuardrailSessionRPMSeparateSessions(t *testing.T) {
	// G6：不同会话互不影响（按 sessKey 独立窗口）。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetSessionRPM(1)
	g.Check("sess-a", 10) // a 用掉额度
	d := g.Check("sess-b", 10)
	if d.RateLimited {
		t.Fatalf("不同会话被同一窗口牵连：sess-b 应放行")
	}
}

func TestGuardrailSessionRPMScatterIgnored(t *testing.T) {
	// G6：散客（sessKey 空）不计入会话限流，也不因限流被拒。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetSessionRPM(1)
	for i := 0; i < 5; i++ {
		if d := g.Check("", 10); d.RateLimited {
			t.Fatalf("散客第 %d 次被会话限流误拒", i+1)
		}
	}
	if gs := g.Snapshot(); gs.SessionRPMHits != 0 {
		t.Fatalf("散客不应产生 SessionRPMHits：%+v", gs)
	}
}

func TestGuardrailSessionRPMDisableClears(t *testing.T) {
	// 关闭会话限流（0）后窗口表清空，旧窗口不残留。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetSessionRPM(1)
	g.Check("sess-a", 10)
	g.SetSessionRPM(0)
	g.SetSessionRPM(1)
	d := g.Check("sess-a", 10)
	if d.RateLimited {
		t.Fatalf("关闭再开后旧窗口应已清空（sess-a 第 1 次放行）")
	}
}

func TestGuardrailSweepBounds(t *testing.T) {
	// 惰性 GC：窗口表超阈值时清理超 1 分钟的陈旧键（同 keyRPM 策略），防无限增长。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetSessionRPM(1)
	for i := 0; i < guardrailSessionMax+20; i++ {
		g.Check("old-"+string(rune('a'+i%26))+string(rune('0'+i%10)), 10)
	}
	// 全部窗口在同一分钟内（未过期），表不会缩到阈值以下。
	now = now.Add(time.Minute + time.Second) // 全部过期
	for i := 0; i < 5; i++ {
		g.Check("fresh", 10) // 触发 sweep
	}
	g.mu.Lock()
	live := 0
	for _, w := range g.sessions {
		if now.Sub(w.start) < time.Minute {
			live++
		}
	}
	g.mu.Unlock()
	if live != 1 {
		t.Fatalf("sweep 后活动窗口 = %d，want 1（只有 fresh）", live)
	}
}

func TestGuardrailCalibrationFIFO(t *testing.T) {
	// G2/G8：对照样本 ≤20 条 FIFO；上游缺失（0）不补（unknown 不冒充 0）。
	now := time.Now()
	g := newGuardrailForTest(&now)
	for i := int64(1); i <= 25; i++ {
		g.RecordCalibration(i*1000, i*1000+50)
	}
	gs := g.Snapshot()
	if len(gs.Calibration) != guardrailCalibrationMax {
		t.Fatalf("Calibration 长度 = %d，want %d", len(gs.Calibration), guardrailCalibrationMax)
	}
	if gs.Calibration[0].Est != 6*1000 {
		t.Fatalf("FIFO 截断错误：首条 Est = %d，want 6000", gs.Calibration[0].Est)
	}
	if gs.Calibration[guardrailCalibrationMax-1].Est != 25*1000 {
		t.Fatalf("尾条 Est = %d，want 25000", gs.Calibration[guardrailCalibrationMax-1].Est)
	}
	g.RecordCalibration(999, 0) // 上游缺失不补
	if n := len(g.Snapshot().Calibration); n != guardrailCalibrationMax {
		t.Fatalf("上游缺失样本不应入表：len=%d", n)
	}
}

func TestGuardrailConcurrentSafe(t *testing.T) {
	// 并发安全（go test -race 由 CI 独占；本测试至少验证无死锁/竞态 panic 路径）。
	now := time.Now()
	g := newGuardrailForTest(&now)
	g.SetContextWarnTokens(100)
	g.SetSessionRPM(5)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				g.Check("sess-"+string(rune('a'+n%3)), int64(j%300))
			}
		}(i)
	}
	wg.Wait()
	_ = g.Snapshot()
}

// ---- HTTP 语义层：applyGuardrail ----

// guardrailTestRequest 构造带请求 trace 的请求（事件标记断言用）。
func guardrailTestRequest(t *testing.T, body string) (*http.Request, *requestTrace) {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	tr := &requestTrace{id: "test", start: time.Now()}
	ctx := context.WithValue(req.Context(), requestTraceKey{}, tr)
	return req.WithContext(ctx), tr
}

func TestApplyGuardrailNilHandlerPasses(t *testing.T) {
	// 未装配护栏（nil）：applyGuardrail 恒不处理，行为与改动前逐位一致。
	h := NewHandler(Config{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if h.applyGuardrail(rec, req, "sess", reqViewForTest(), nil) {
		t.Fatalf("nil 护栏不应处理请求")
	}
}

func TestApplyGuardrailBlocked413(t *testing.T) {
	// G3 HTTP 语义：413 + code context_length_exceeded + Retry-After: 60 + 事件 blocked。
	g := NewGuardrail()
	g.SetContextWarnTokens(100)
	g.SetContextMaxTokens(200)
	h := NewHandler(Config{Guardrail: g})
	view, body := viewWithMessages(t, strings.Repeat("一", 300)) // 300 CJK ≈ 300 tokens
	req, tr := guardrailTestRequest(t, body)

	rec := httptest.NewRecorder()
	if !h.applyGuardrail(rec, req, "sess", view, nil) {
		t.Fatalf("blocked 命中应返回 handled=true")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("HTTP = %d，want 413", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q，want 60", got)
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("响应非 JSON：%v", err)
	}
	if e.Error.Code != "context_length_exceeded" {
		t.Fatalf("error.code = %q，want context_length_exceeded", e.Error.Code)
	}
	if tr.guardrailHit != "blocked" {
		t.Fatalf("事件标记 = %q，want blocked", tr.guardrailHit)
	}
}

func TestApplyGuardrailWarnHeader(t *testing.T) {
	// G2 HTTP 语义：warn 不阻断（handled=false）+ X-Gateway-Warning 头 + 事件 warn +
	// run.guardrailEst 置位（供对照样本）。
	g := NewGuardrail()
	g.SetContextWarnTokens(100)
	h := NewHandler(Config{Guardrail: g})
	view, body := viewWithMessages(t, strings.Repeat("一", 150))
	req, tr := guardrailTestRequest(t, body)
	run := &chatRun{}

	rec := httptest.NewRecorder()
	if h.applyGuardrail(rec, req, "sess", view, run) {
		t.Fatalf("warn 命中不得阻断（handled=false）")
	}
	if got := rec.Header().Get("X-Gateway-Warning"); !strings.Contains(got, "exceed warn threshold 100") {
		t.Fatalf("X-Gateway-Warning = %q，want 含阈值 100", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("warn 不应写响应体（Code=%d，want 200 默认）", rec.Code)
	}
	if tr.guardrailHit != "warn" {
		t.Fatalf("事件标记 = %q，want warn", tr.guardrailHit)
	}
	// 估算 = 150 个 CJK（计 1）+ role 值 "user"（4 字符 other → 1）= 151。
	if run.guardrailEst != 151 {
		t.Fatalf("run.guardrailEst = %d，want 151（150 CJK + user 信封 1）", run.guardrailEst)
	}
}

func TestApplyGuardrailSessionRPM429(t *testing.T) {
	// G6 HTTP 语义：会话限流 429 + session_rate_limit + Retry-After + 事件 session_rpm。
	g := NewGuardrail()
	g.SetSessionRPM(1)
	h := NewHandler(Config{Guardrail: g})
	view, body := viewWithMessages(t, "hi")

	req1, _ := guardrailTestRequest(t, body)
	rec1 := httptest.NewRecorder()
	if h.applyGuardrail(rec1, req1, "sess", view, nil) {
		t.Fatalf("RPM=1 第 1 次不得拒绝")
	}

	req2, tr2 := guardrailTestRequest(t, body)
	rec2 := httptest.NewRecorder()
	if !h.applyGuardrail(rec2, req2, "sess", view, nil) {
		t.Fatalf("RPM=1 第 2 次应拒绝")
	}
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("HTTP = %d，want 429", rec2.Code)
	}
	if got := rec2.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q，want 60", got)
	}
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &e); err != nil {
		t.Fatalf("响应非 JSON：%v", err)
	}
	if e.Error.Code != "session_rate_limit" {
		t.Fatalf("error.code = %q，want session_rate_limit", e.Error.Code)
	}
	if tr2.guardrailHit != "session_rpm" {
		t.Fatalf("事件标记 = %q，want session_rpm", tr2.guardrailHit)
	}
}

func TestStatusOmitsGuardrailStatsWhenNoHits(t *testing.T) {
	// G7：无任何命中时 /status 不输出 guardrail_stats 键（未知表达，与 agent_profile 同源）。
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: upstream.New(), Guardrail: NewGuardrail()})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status 非 JSON：%v", err)
	}
	if _, ok := body["guardrail_stats"]; ok {
		t.Fatalf("无命中时不应输出 guardrail_stats 键")
	}
}

func TestStatusShowsGuardrailStatsAfterHits(t *testing.T) {
	// G7：命中后 /status 输出 guardrail_stats（计数 + 对照样本）。
	g := NewGuardrail()
	g.SetContextWarnTokens(100)
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: upstream.New(), Guardrail: g})
	g.Check("sess", 150) // 1 次 warn
	g.RecordCalibration(150, 160)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status 非 JSON：%v", err)
	}
	gs, ok := body["guardrail_stats"].(map[string]any)
	if !ok {
		t.Fatalf("guardrail_stats 缺失或非对象")
	}
	if gs["warn_hits"].(float64) != 1 {
		t.Fatalf("warn_hits = %v，want 1", gs["warn_hits"])
	}
	samples, ok := gs["calibration_samples"].([]any)
	if !ok || len(samples) != 1 {
		t.Fatalf("calibration_samples = %v，want 1 条", gs["calibration_samples"])
	}
}

// ---- 测试辅助 ----

// viewWithMessages 构造带 messages 的 RequestView，返回视图与其原始 body。
func viewWithMessages(t *testing.T, text string) (*session.RequestView, string) {
	t.Helper()
	body := `{"model":"test","messages":[{"role":"user","content":"` + text + `"}]}`
	v, ok := session.ParseRequestView([]byte(body))
	if !ok {
		t.Fatalf("ParseRequestView 失败")
	}
	return &v, body
}

func reqViewForTest() *session.RequestView {
	v, _ := session.ParseRequestView([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	return &v
}
