package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/session"
)

// captureStdout 重定向 os.Stdout（连同 chatLogOut，见 SetChatLogOutput 的注入点）
// 并捕获 fn 期间的全部输出。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	oldOut := chatLogOut
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	chatLogOut = w
	fn()
	os.Stdout = old
	chatLogOut = oldOut
	_ = w.Close()
	raw, _ := io.ReadAll(r)
	return string(raw)
}

// withChatLog 临时开启聊天表格日志（TestMain 默认关闭），测试结束后恢复。
// 仅供断言表格行输出的用例使用。
func withChatLog(t *testing.T) {
	t.Helper()
	old := chatLogEnabled
	chatLogEnabled = true
	t.Cleanup(func() { chatLogEnabled = old })
}

func TestChatStatsReaderTokensFromUsage(t *testing.T) {
	r := newChatStatsReaderSince(strings.NewReader(sseOK), time.Now())
	// 保证 TTFB 跨过时钟粒度：Windows 上纯内存读取不足 1ms，time.Since 可能取 0。
	time.Sleep(3 * time.Millisecond)
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("copy: %v", err)
	}
	toks, ok := r.Tokens()
	if !ok || toks != 1 {
		t.Fatalf("tokens=%d ok=%v, want 1/true (from usage, not rune count)", toks, ok)
	}
	usage := r.Usage()
	if !usage.HasPromptTokens || usage.PromptTokens != 1 || !usage.HasCompletionTokens || usage.CompletionTokens != 1 || !usage.HasTotalTokens || usage.TotalTokens != 2 {
		t.Fatalf("usage=%+v, want prompt=1 completion=1 total=2", usage)
	}
	if r.TTFB() <= 0 {
		t.Errorf("ttfb=%v want >0", r.TTFB())
	}
}

func TestChatStatsReaderNoUsage(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	r := newChatStatsReaderSince(strings.NewReader(sse), time.Now())
	_, _ = io.Copy(io.Discard, r)
	if toks, ok := r.Tokens(); ok || toks != 0 {
		t.Errorf("tokens=%d ok=%v, want 0/false for missing usage", toks, ok)
	}
}

func TestChatStatsReaderLastFrameUsageWins(t *testing.T) {
	sse := "data: {\"usage\":{\"completion_tokens\":5}}\n\n" +
		"data: {\"usage\":{\"completion_tokens\":12}}\n\n" +
		"data: [DONE]\n\n"
	r := newChatStatsReaderSince(strings.NewReader(sse), time.Now())
	_, _ = io.Copy(io.Discard, r)
	toks, ok := r.Tokens()
	if !ok || toks != 12 {
		t.Fatalf("tokens=%d ok=%v, want 12 (last frame wins)", toks, ok)
	}
}

func TestChatStatsReaderTTFBOnlyOnDataFrame(t *testing.T) {
	start := time.Now().Add(-2 * time.Second)
	var s chatStatsReader
	s.start = start
	s.parseSSELine("event: ping")
	if s.TTFB() != 0 {
		t.Errorf("non-data line must not set TTFB: %v", s.TTFB())
	}
	s.parseSSELine("data: {\"choices\":[]}")
	first := s.TTFB()
	if first < time.Second {
		t.Errorf("first data frame TTFB=%v want >=2s", first)
	}
	s.parseSSELine("data: {\"choices\":[]}")
	if s.TTFB() != first {
		t.Errorf("second frame changed TTFB: %v -> %v", first, s.TTFB())
	}
}

func TestChatStatsReaderBytesPassthrough(t *testing.T) {
	sse := "data: {\"content\":\"你好\"}\n\ndata: [DONE]\n\n"
	r := newChatStatsReaderSince(strings.NewReader(sse), time.Now())
	out, _ := io.ReadAll(r)
	if string(out) != sse {
		t.Errorf("passthrough mismatch:\n got %q\nwant %q", out, sse)
	}
}

func TestRequestMetricsRecordsStream(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:   up,
		RequestLog: reqLog,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("X-Request-Id"); !strings.HasPrefix(got, "req-") {
		t.Fatalf("X-Request-Id=%q", got)
	}
	s := reqLog.Snapshot()
	if s.Completed != 1 || s.Succeeded != 1 || s.Failed != 0 || len(s.Recent) != 1 {
		t.Fatalf("metrics = %+v", s)
	}
	e := s.Recent[0]
	if e.Outcome != reqlog.OutcomeSuccess || e.Status != http.StatusOK || !e.OK || e.Model != "glm-5.2" || e.TotalTokens != 2 || e.Attempts != 1 {
		t.Fatalf("event = %+v", e)
	}
	// kind 维度（V3-8）：chat 路径的事件必须显式打标 chat——历史行的「缺失=未知」
	// 三值语义只对旧归档成立，新事件缺标 = 生产方漏打，直接判失败。
	if e.Kind != reqlog.KindChat {
		t.Fatalf("event kind = %q, want %q", e.Kind, reqlog.KindChat)
	}
}

// 调用来源必须进归档事件：命中受信任代理时采信 X-Forwarded-For 首段（反代后真实
// 客户端）+ 截断后的 UA。F-002 起，未配置受信任代理时不再无条件采信 XFF。
func TestRequestMetricsCapturesClientInfo(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:             testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:         up,
		RequestLog:       reqLog,
		RecordClientInfo: true,
		// 受信任代理：来源落在 10.0.0.0/8 内才采信 XFF（反代部署）
		TrustedProxy: NewTrustedProxyMatcher("10.0.0.0/8"),
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	req.RemoteAddr = "10.0.0.1:443" // 来源是受信任反代
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
	req.Header.Set("User-Agent", "python-requests/2.31.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	s := reqLog.Snapshot()
	if len(s.Recent) != 1 {
		t.Fatalf("recent = %+v", s.Recent)
	}
	if got := s.Recent[0].ClientIP; got != "203.0.113.7" {
		t.Errorf("client ip = %q want 203.0.113.7 (XFF first hop)", got)
	}
	if got := s.Recent[0].UserAgent; got != "python-requests/2.31.0" {
		t.Errorf("user agent = %q", got)
	}
}

// TestRequestMetricsIgnoresForgedXFF 未配置受信任代理时，伪造 XFF 不进归档（回落 RemoteAddr）。
// 这是 F-002 的端到端牙齿：直连部署下客户端伪造 XFF 不得污染日志与透传。
func TestRequestMetricsIgnoresForgedXFF(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:             testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:         up,
		RequestLog:       reqLog,
		RecordClientInfo: true,
		// 不设 TrustedProxy → 不信任任何代理头
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	req.RemoteAddr = "198.51.100.9:54321" // 直连客户端
	req.Header.Set("X-Forwarded-For", "169.254.169.254")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	s := reqLog.Snapshot()
	if len(s.Recent) != 1 {
		t.Fatalf("recent = %+v", s.Recent)
	}
	if got := s.Recent[0].ClientIP; got != "198.51.100.9" {
		t.Errorf("client ip = %q want 198.51.100.9 (伪造 XFF 不得采信，回落 RemoteAddr)", got)
	}
}

// 开关关闭时不采集来源（归档里不出现 IP/UA），但请求指标照常记录。
func TestRequestMetricsClientInfoDisabled(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:   up,
		RequestLog: reqLog,
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	req.Header.Set("User-Agent", "python-requests/2.31.0")
	h.ServeHTTP(httptest.NewRecorder(), req)
	s := reqLog.Snapshot()
	if len(s.Recent) != 1 {
		t.Fatalf("recent = %+v", s.Recent)
	}
	if s.Recent[0].ClientIP != "" || s.Recent[0].UserAgent != "" {
		t.Fatalf("client info recorded while disabled: %+v", s.Recent[0])
	}
	if s.Recent[0].Model != "glm-5.2" {
		t.Errorf("model = %q, metrics should be unaffected", s.Recent[0].Model)
	}
}

// 无代理头时回落到 TCP 对端地址——直连部署下这是唯一来源线索。
func TestClientIPForLogFallsBackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "198.51.100.9:54321"
	if got := clientIPForLog(req); got != "198.51.100.9" {
		t.Errorf("got %q want 198.51.100.9", got)
	}
	// 代理头优先（X-Real-IP 兜底 XFF）。
	req.Header.Set("X-Real-IP", "192.0.2.5")
	if got := clientIPForLog(req); got != "192.0.2.5" {
		t.Errorf("X-Real-IP got %q want 192.0.2.5", got)
	}
	req.Header.Set("X-Forwarded-For", "192.0.2.9, 10.1.1.1")
	if got := clientIPForLog(req); got != "192.0.2.9" {
		t.Errorf("XFF got %q want 192.0.2.9", got)
	}
	if got := clientIPForLog(nil); got != "" {
		t.Errorf("nil request got %q want empty", got)
	}
}

// 超长 UA 落盘前截断：UA 是客户端可控自由文本，不截断会把归档行撑爆。
func TestCaptureClientInfoTruncatesUserAgent(t *testing.T) {
	tr := &requestTrace{}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.RemoteAddr = "198.51.100.9:1234"
	req.Header.Set("User-Agent", strings.Repeat("A", 5000))
	tr.captureClientInfo(req, nil)
	if len(tr.userAgent) != maxUserAgentLen {
		t.Fatalf("ua len = %d want %d", len(tr.userAgent), maxUserAgentLen)
	}
}

func TestRequestMetricsDetectsStreamErrorFrame(t *testing.T) {
	const sseErr = "data: {\"error\":{\"message\":\"upstream failed\"}}\n\ndata: [DONE]\n\n"
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseErr, true
	})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:   up,
		RequestLog: reqLog,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))
	s := reqLog.Snapshot()
	if len(s.Recent) != 1 || s.Recent[0].Outcome != reqlog.OutcomeStreamError || s.Recent[0].OK {
		t.Fatalf("stream error metrics = %+v", s.Recent)
	}
}

func TestParseModelFromBody(t *testing.T) {
	if got := parseModelFromBody([]byte(`{"model":"deepseek-v4-flash","stream":true}`)); got != "deepseek-v4-flash" {
		t.Errorf("got %q", got)
	}
	if got := parseModelFromBody([]byte(`{}`)); got != "-" {
		t.Errorf("got %q want -", got)
	}
	if got := parseModelFromBody([]byte(`not json`)); got != "-" {
		t.Errorf("got %q want -", got)
	}
}

func TestCompletionTokensExtraction(t *testing.T) {
	got := completionTokens(map[string]any{
		"usage": map[string]any{"prompt_tokens": 10.0, "completion_tokens": 234.0, "total_tokens": 244.0},
	})
	if got != 234 {
		t.Errorf("got %d want 234", got)
	}
	if got := completionTokens(map[string]any{}); got != -1 {
		t.Errorf("missing usage: got %d want -1", got)
	}
	if got := completionTokens(map[string]any{"usage": map[string]any{}}); got != -1 {
		t.Errorf("missing completion_tokens: got %d want -1", got)
	}
}

func TestUIDPrefix(t *testing.T) {
	if got := uidPrefix("00e26541abcdef012345"); got != "00e26541" {
		t.Errorf("long uid -> %q", got)
	}
	if got := uidPrefix("abc"); got != "abc" {
		t.Errorf("short uid -> %q", got)
	}
	if got := uidPrefix(""); got != "-" {
		t.Errorf("empty uid -> %q", got)
	}
}

func TestLogChatRowFormat(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRow(412*time.Millisecond, 27100*time.Millisecond, "deepseek-v4-flash", "stream", "00e26541abcdef", "示例号", http.StatusOK, 1234)
	})
	for _, want := range []string{
		"| #", "deepseek-v4", "| stream |", "| 200 |", "示例号(00e26541)", "TTFB=412ms", "tok=1234", "tok/s   |", "total=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("row missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "00e26541abcdef") {
		t.Errorf("full uid leaked: %s", out)
	}
}

func TestLogChatRowNoUsageShowsDash(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRow(0, time.Second, "glm-5.2", "sync", "s1", "", http.StatusServiceUnavailable, -1)
	})
	for _, want := range []string{"TTFB=-", "tok=-", "tok=-      |", "| 503 |"} {
		if !strings.Contains(out, want) {
			t.Errorf("row missing %q:\n%s", want, out)
		}
	}
}

func TestLogChatRowExtendedFields(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRowEx(10*time.Millisecond, 2*time.Second, "glm-5.3", "stream", "u123456789", "示例号",
			http.StatusOK, 42, "req-abc123", reqlog.OutcomeSuccess, 2, 1.25, true, "", "")
	})
	for _, want := range []string{"rid=req-abc123", "out=success", "try=2", "credit=1.2500"} {
		if !strings.Contains(out, want) {
			t.Errorf("extended row missing %q:\n%s", want, out)
		}
	}
	// 来源未采集时不追加 src 段（旧行格式不变，方便既有日志解析脚本继续工作）。
	if strings.Contains(out, "src=") {
		t.Errorf("empty source must not add src field:\n%s", out)
	}
}

// 调用来源必须落进流水行：IP 用裸值，UA 用 ShortUA 压缩后的客户端标签。
func TestLogChatRowSourceFields(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRowEx(10*time.Millisecond, 2*time.Second, "glm-5.3", "stream", "u123456789", "示例号",
			http.StatusOK, 42, "req-abc123", reqlog.OutcomeSuccess, 1, 0, false,
			"203.0.113.7", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	})
	for _, want := range []string{`src=203.0.113.7`, `ua="Chrome/120.0.0.0"`} {
		if !strings.Contains(out, want) {
			t.Errorf("source row missing %q:\n%s", want, out)
		}
	}
	// 完整浏览器 UA 不该整段铺进流水行（只留客户端标签）。
	if strings.Contains(out, "AppleWebKit") {
		t.Errorf("full UA leaked into row:\n%s", out)
	}
}

// 只有 IP 没有 UA（非浏览器客户端不带头）时，UA 列显示 "-"，IP 照常展示。
func TestLogChatRowSourcePartial(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRow(0, time.Second, "m", "sync", "u", "", 200, 1)
	})
	if strings.Contains(out, "src=") {
		t.Errorf("logChatRow has no source, want no src field:\n%s", out)
	}
}

func TestLogChatRowSeqIncrements(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRow(0, time.Second, "m", "sync", "u", "", 200, 1)
		logChatRow(0, time.Second, "m", "sync", "u", "", 200, 1)
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), out)
	}
	first := strings.Fields(lines[0])[1]
	second := strings.Fields(lines[1])[1]
	if !strings.HasPrefix(first, "#") || !strings.HasPrefix(second, "#") {
		t.Fatalf("seq columns missing: %q %q", first, second)
	}
	if first == second {
		t.Errorf("seq not incremented: %q == %q", first, second)
	}
}

func TestChatLogsStreamRow(t *testing.T) {
	withChatLog(t)
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	out := captureStdout(t, func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("code=%d", rec.Code)
		}
	})
	for _, want := range []string{"| stream |", "| 200 |", "| u1 ", "TTFB=", "tok=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("stream row missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "tok=1") {
		t.Errorf("tok: want precise usage completion_tokens: %s", out)
	}
}

func TestChatLogsSyncRowTTFBDash(t *testing.T) {
	withChatLog(t)
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream: up,
	})
	out := captureStdout(t, func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`))
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("code=%d", rec.Code)
		}
	})
	for _, want := range []string{"| sync |", "| 200 |", "TTFB=-", "tok=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("sync row missing %q:\n%s", want, out)
		}
	}
}

func TestChatLogsErrorRow(t *testing.T) {
	withChatLog(t)
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 402, `{"code":1,"msg":"余额不足"}`, false
	})
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: up})
	out := captureStdout(t, func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`))
		h.ServeHTTP(rec, req)
		if rec.Code != 503 {
			t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
		}
	})
	for _, want := range []string{"| u1 ", "| 503 |", "tok=-"} {
		if !strings.Contains(out, want) {
			t.Errorf("error row missing %q:\n%s", want, out)
		}
	}
}

func TestHealthzDoesNotLogTableRow(t *testing.T) {
	withChatLog(t) // 日志开启也应无表格行：非 chat 路由根本不走 logChatRow
	p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	h := NewHandler(Config{Pool: p, Upstream: newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	})})
	out := captureStdout(t, func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
		if rec.Code != 200 {
			t.Fatalf("code=%d", rec.Code)
		}
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, httptest.NewRequest("GET", "/v1/models", nil))
		rec3 := httptest.NewRecorder()
		h.ServeHTTP(rec3, httptest.NewRequest("GET", "/status", nil))
	})
	if strings.Contains(out, "| #") {
		t.Errorf("healthz/models/status must not emit table rows:\n%s", out)
	}
}

// V3-10 G1：带 metadata.conversation_id 的 chat 请求，reqlog 事件必须打出
// SessionFP（= session.RequestIDForKey(sessKey)，可复算），UA 分类进 AgentKind。
// G2：UA 未采集（RecordClientInfo=false）时 AgentKind 省略（unknown，不冒充 other）。
func TestRequestMetricsTagsSessionFPAndAgentKind(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:             testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:         up,
		RequestLog:       reqLog,
		RecordClientInfo: true,
	})
	rec := httptest.NewRecorder()
	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}],"metadata":{"conversation_id":"test-conv-xyz"}}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("User-Agent", "claude-cli/1.0.0")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	e := reqLog.Snapshot().Recent[0]
	wantFP := session.RequestIDForKey("test-conv-xyz")
	if e.SessionFP != wantFP {
		t.Errorf("session_fp = %q want %q (sha256(salt|sessKey)[:16])", e.SessionFP, wantFP)
	}
	if e.AgentKind != "claude_code" {
		t.Errorf("agent_kind = %q want claude_code", e.AgentKind)
	}
}

// V3-10 G2 反向：RecordClientInfo=false 时 UA 不采集，AgentKind 必须空串（unknown），
// 不能兜底成 "other"。SessionFP 仍应打（会话键来自 body 解析，与 UA 开关无关）。
func TestRequestMetricsAgentKindOmittedWhenClientInfoOff(t *testing.T) {
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		return 200, sseOK, true
	})
	reqLog := reqlog.New(reqlog.Config{})
	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
		Upstream:   up,
		RequestLog: reqLog,
		// RecordClientInfo 零值 false → 不采 UA
	})
	rec := httptest.NewRecorder()
	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}],"metadata":{"conversation_id":"c2"}}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("User-Agent", "claude-cli/1.0.0")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	e := reqLog.Snapshot().Recent[0]
	if e.AgentKind != "" {
		t.Errorf("agent_kind = %q want empty (UA not collected → unknown, not other)", e.AgentKind)
	}
	if e.SessionFP == "" {
		t.Errorf("session_fp must still be set from body metadata (UA switch is orthogonal)")
	}
}
