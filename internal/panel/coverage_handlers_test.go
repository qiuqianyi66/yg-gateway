package panel

// coverage_handlers_test.go —— Task 5 覆盖率补强：panel 包 handler / 路由层。
//
// 边界（有意为之，勿动）：
//   - 全部断言都经真实路由（p.ServeHTTP + httptest），覆盖鉴权层 → handler → JSON 输出；
//   - 上游一律用注入的 RoundTripper 桩（chStub）拦下，不联网、不等真实时间；
//   - 期望值全部是手写字面量（状态码 / 字段值 / 出站路径 / 请求体原文），
//     不复用被测函数算出的值；
//   - helper 统一 `ch` 前缀，避免与同包并行任务的 helper 重名（重名会让整包编译失败）。
//
// 不覆盖（需真实上游或会 panic，见交付报告）：p.cfg.Pool == nil 时 overview/logs/
// models/packages 等直连接口无 nil 守卫（nil *pool.Pool 会 panic，非 501）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/livecfg"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/scheduler"
	"yg-gateway/internal/session"
	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// ---------------------------------------------------------------------------
// 桩与公共辅助
// ---------------------------------------------------------------------------

// chAPIKey 本文件所有测试用的面板管理员密钥。
const chAPIKey = "ch-panel-key"

// chResourceBody 上游「个人资源包」响应（余额 400 / 总额 1000）。
// 同一 path 同时服务 get-user-resource 与 get-enterprise-user-usage 之外的逐包查询。
const chResourceBody = `{"code":0,"data":{"Response":{"Data":{"Accounts":[` +
	`{"PackageName":"首登赠送包","CapacitySize":1000,"CapacityRemain":400,"CycleEndTime":"2999-01-01 00:00:00"}]}}}}`

// chCaptured 一次被桩拦下的出站请求快照（可在并发下安全读取）。
type chCaptured struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   string
}

// chStub 可编程上游桩：handler 返回 (status, body)；未设置时回 200 {"code":0,"data":{}}。
type chStub struct {
	mu      sync.Mutex
	reqs    []chCaptured
	handler func(c chCaptured) (int, string)
}

// chStubOK 恒定响应桩。
func chStubOK(body string) *chStub {
	return &chStub{handler: func(chCaptured) (int, string) { return 200, body }}
}

// chStubFn 按请求定制的响应桩。
func chStubFn(fn func(c chCaptured) (int, string)) *chStub { return &chStub{handler: fn} }

func (s *chStub) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
	}
	c := chCaptured{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body}
	s.mu.Lock()
	s.reqs = append(s.reqs, c)
	fn := s.handler
	s.mu.Unlock()

	status, resp := 200, `{"code":0,"data":{}}`
	if fn != nil {
		status, resp = fn(c)
	}
	if resp == "" {
		resp = `{"code":0,"data":{}}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(resp)),
		Request:    r,
	}, nil
}

func (s *chStub) snapshot() []chCaptured {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]chCaptured, len(s.reqs))
	copy(out, s.reqs)
	return out
}

// count 统计命中指定 METHOD+path 的次数。
func (s *chStub) count(method, path string) int {
	n := 0
	for _, r := range s.snapshot() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// paths 返回全部命中路径（按发生顺序），供「调用顺序」断言。
func (s *chStub) paths() []string {
	out := []string{}
	for _, r := range s.snapshot() {
		out = append(out, r.Path)
	}
	return out
}

// chUpstream 构造只打桩的 upstream.Client（各 base 都是 .test 假域，便于断言 realm 路由）。
func chUpstream(stub *chStub, globalEnabled bool) *upstream.Client {
	hc := &http.Client{Transport: stub}
	return &upstream.Client{
		HTTP:          hc,
		ChatHTTP:      hc,
		ChatBaseCN:    "https://chat.test",
		BillingBaseCN: "https://billing.test",
		WebBaseCN:     "https://web.test",
		GlobalEnabled: globalEnabled,
	}
}

// chAcct 造一个可用凭证（CN；global=true 时显式落 realm=global）。
func chAcct(t *testing.T, uid string, global bool) *auth.Auth {
	t.Helper()
	a := &auth.Auth{
		UID:          uid,
		Nickname:     "nick-" + uid,
		AccessToken:  "at-" + uid,
		RefreshToken: "rt-" + uid,
		Domain:       "codebuddy.cn",
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	}
	if global {
		a.Domain = "www.workbuddy.ai"
		if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
			t.Fatalf("backfill realm global: %v", err)
		}
	}
	return a
}

// chForceGlobal 显式设定（并还原）auth 包的 global 路由开关：
// 同包其它测试可能改过它，本文件不依赖测试执行顺序。
func chForceGlobal(t *testing.T, enabled bool) {
	t.Helper()
	old := auth.GlobalEnabled()
	auth.SetGlobalEnabled(enabled)
	t.Cleanup(func() { auth.SetGlobalEnabled(old) })
}

// chPanel 建面板（默认带管理员密钥）。
func chPanel(t *testing.T, cfg Config) *Panel {
	t.Helper()
	if cfg.APIKey == "" {
		cfg.APIKey = chAPIKey
	}
	if cfg.Version == "" {
		cfg.Version = "9.9.9-test"
	}
	return New(cfg)
}

// chDo 经真实路由发一次带管理员密钥的请求。
func chDo(t *testing.T, p *Panel, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+chAPIKey)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

// chWant 断言状态码并解析 JSON 对象响应。
func chWant(t *testing.T, rec *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body.String())
	}
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("body 不是 JSON 对象: %v; body=%s", err, rec.Body.String())
		}
	}
	return out
}

// chErrMsg 取错误响应的 error 字段。
func chErrMsg(t *testing.T, out map[string]any) string {
	t.Helper()
	if ok, _ := out["ok"].(bool); ok {
		t.Fatalf("期望错误响应，拿到 ok=true: %+v", out)
	}
	msg, _ := out["error"].(string)
	if msg == "" {
		t.Fatalf("错误响应缺 error 字段: %+v", out)
	}
	return msg
}

func chNum(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("期望数字，拿到 %T (%v)", v, v)
	}
	return f
}

func chStr(t *testing.T, v any) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("期望字符串，拿到 %T (%v)", v, v)
	}
	return s
}

func chRows(t *testing.T, v any) []map[string]any {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("期望数组，拿到 %T (%v)", v, v)
	}
	out := make([]map[string]any, 0, len(arr))
	for i, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("第 %d 项不是对象: %T", i, it)
		}
		out = append(out, m)
	}
	return out
}

// chMultipart 构造 multipart/form-data 请求体（字段名 file）。
func chMultipart(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close form writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// chSwapLoginHTTP 把 login.go 的包级 client 换成桩（doJSON 唯一出口），测试结束还原。
func chSwapLoginHTTP(t *testing.T, stub *chStub) {
	t.Helper()
	old := loginHTTP
	loginHTTP = &http.Client{Transport: stub}
	t.Cleanup(func() { loginHTTP = old })
}

// chSpeedUpGaps 把面板内「对齐上游节奏」的真实等待压到毫秒级，测试结束还原。
func chSpeedUpGaps(t *testing.T) {
	t.Helper()
	oldReport, oldAccept, oldPoll, oldMP := reportGap, acceptBatchGap, claimPollGap, mpActionGap
	reportGap, acceptBatchGap, claimPollGap, mpActionGap = time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		reportGap, acceptBatchGap, claimPollGap, mpActionGap = oldReport, oldAccept, oldPoll, oldMP
	})
}

// ---------------------------------------------------------------------------
// panel.go 只读端点：overview / logs / request_metrics / request_logs
// ---------------------------------------------------------------------------

// TestPanelHandlersLiveKeyOverridesStatic 覆盖「密钥热加载」与「auth_required 口径」：
// Live 快照优先于静态字段（面板里改了 api_key，下一个请求即用新值，无需重启）。
func TestPanelHandlersLiveKeyOverridesStatic(t *testing.T) {
	live := livecfg.New(livecfg.Snapshot{APIKey: "live-key"})
	p := New(Config{Version: "v", APIKey: "static-key", Live: live, Pool: pool.New("")})

	// 旧的静态 key 失效 → 401。
	reqOld := httptest.NewRequest("GET", "/panel/api/overview", nil)
	reqOld.Header.Set("Authorization", "Bearer static-key")
	recOld := httptest.NewRecorder()
	p.ServeHTTP(recOld, reqOld)
	if recOld.Code != http.StatusUnauthorized {
		t.Errorf("旧静态 key 应 401，实际 %d", recOld.Code)
	}

	// 新的 Live key 生效 → 200。
	reqNew := httptest.NewRequest("GET", "/panel/api/overview", nil)
	reqNew.Header.Set("Authorization", "Bearer live-key")
	recNew := httptest.NewRecorder()
	p.ServeHTTP(recNew, reqNew)
	out := chWant(t, recNew, http.StatusOK)
	if out["auth_required"] != true {
		t.Errorf("Live 里配了 api_key，auth_required 应为 true: %+v", out)
	}

	// Live 存在但密钥为空且无多 Key → 保持 2.0.0 旧语义（不鉴权，auth_required=false）。
	liveEmpty := livecfg.New(livecfg.Snapshot{})
	p2 := New(Config{Version: "v", APIKey: "static-key", Live: liveEmpty, Pool: pool.New("")})
	rec := httptest.NewRecorder()
	p2.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/overview", nil))
	out = chWant(t, rec, http.StatusOK)
	if out["auth_required"] != false {
		t.Errorf("Live 密钥为空时 auth_required 应为 false（旧语义）: %+v", out)
	}
}

func TestPanelHandlersOverviewCountsAndSticky(t *testing.T) {
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-a", false))
	pl.Add(chAcct(t, "uid-b", false))
	pl.Disable("uid-a", "manual disable (panel)")
	pl.Pause("uid-b")

	// 1) StickyCount / SessionSnapshot 都为 nil：0 与 null（不崩、不编造）。
	p := chPanel(t, Config{Pool: pl, RedisMode: "upstash"})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/overview", ""), http.StatusOK)

	if got := chStr(t, out["version"]); got != "9.9.9-test" {
		t.Errorf("version = %q, want %q", got, "9.9.9-test")
	}
	if got := chStr(t, out["redis_mode"]); got != "upstash" {
		t.Errorf("redis_mode = %q, want %q", got, "upstash")
	}
	if got := out["auth_required"]; got != true {
		t.Errorf("auth_required = %v, want true（配了 api_key）", got)
	}
	if got := chNum(t, out["total"]); got != 2 {
		t.Errorf("total = %v, want 2", got)
	}
	if got := chNum(t, out["disabled"]); got != 1 {
		t.Errorf("disabled = %v, want 1", got)
	}
	if got := chNum(t, out["paused"]); got != 1 {
		t.Errorf("paused = %v, want 1（paused 必须与 disabled 分开计数）", got)
	}
	if got := chNum(t, out["healthy"]); got != 0 {
		t.Errorf("healthy = %v, want 0", got)
	}
	if got := chNum(t, out["sticky_sessions"]); got != 0 {
		t.Errorf("sticky_sessions = %v, want 0（StickyCount 未注入）", got)
	}
	if out["sticky_list"] != nil {
		t.Errorf("sticky_list = %v, want null（SessionSnapshot 未注入）", out["sticky_list"])
	}
	if out["model_locks"] != nil {
		t.Errorf("model_locks = %v, want null（无模型锁）", out["model_locks"])
	}
	if rows := chRows(t, out["accounts"]); len(rows) != 2 {
		t.Errorf("accounts 长度 = %d, want 2", len(rows))
	}
	if got := chNum(t, out["uptime_sec"]); got < 0 {
		t.Errorf("uptime_sec = %v, want >= 0", got)
	}

	// 2) 注入粘性快照：绑定原样透出，last_active 逐字对应（固定时间，不依赖真实时钟）。
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	pl2 := pool.New("")
	pl2.Add(chAcct(t, "uid-c", false))
	p2 := chPanel(t, Config{
		Pool: pl2,
		StickyCount: func() int {
			return 2
		},
		SessionSnapshot: func() []session.SessionBinding {
			return []session.SessionBinding{
				{Key: "sess-1", UID: "uid-c", LastActive: at},
				{Key: "sess-2", UID: "uid-d", LastActive: at.Add(time.Minute)},
			}
		},
	})
	out = chWant(t, chDo(t, p2, "GET", "/panel/api/overview", ""), http.StatusOK)
	if got := chNum(t, out["sticky_sessions"]); got != 2 {
		t.Errorf("sticky_sessions = %v, want 2", got)
	}
	list := chRows(t, out["sticky_list"])
	if len(list) != 2 {
		t.Fatalf("sticky_list 长度 = %d, want 2: %+v", len(list), list)
	}
	if got := chStr(t, list[0]["key"]); got != "sess-1" {
		t.Errorf("sticky_list[0].key = %q, want sess-1", got)
	}
	if got := chStr(t, list[0]["uid"]); got != "uid-c" {
		t.Errorf("sticky_list[0].uid = %q, want uid-c", got)
	}
	if got := chStr(t, list[0]["last_active"]); got != at.Format(time.RFC3339) {
		t.Errorf("sticky_list[0].last_active = %q, want %q（固定时间原样透出）", got, at.Format(time.RFC3339))
	}
}

// TestPanelHandlersLogsRing 日志环快照：频道分类（task/chat）+ 行首时间戳剥离后原样透出。
func TestPanelHandlersLogsRing(t *testing.T) {
	p := chPanel(t, Config{Pool: pool.New("")})
	if _, err := p.Logs().Write([]byte(
		"2026/01/02 03:04:05 panel: 队列启动：1 项\n" +
			"2026/01/02 03:04:06 | #1 | cn:glm-5.2 | ok\n")); err != nil {
		t.Fatalf("ring write: %v", err)
	}
	out := chWant(t, chDo(t, p, "GET", "/panel/api/logs", ""), http.StatusOK)
	if _, ok := out["entries"]; !ok {
		t.Fatalf("响应缺 entries: %+v", out)
	}
	rows := chRows(t, out["entries"])
	if len(rows) != 2 {
		t.Fatalf("entries 长度 = %d, want 2; body=%+v", len(rows), rows)
	}
	if got := chStr(t, rows[0]["text"]); got != "panel: 队列启动：1 项" {
		t.Errorf("第 1 行 text = %q（行首时间戳应被剥离）", got)
	}
	if got := chStr(t, rows[0]["ch"]); got != ChTask {
		t.Errorf("第 1 行频道 = %q, want %q", got, ChTask)
	}
	if got := chStr(t, rows[1]["ch"]); got != ChChat {
		t.Errorf("第 2 行频道 = %q, want %q", got, ChChat)
	}
}

func TestPanelHandlersRequestMetricsRecorder(t *testing.T) {
	// 未注入 RequestLog → 501（两个端点都要）。
	p := chPanel(t, Config{Pool: pool.New("")})
	for _, path := range []string{"/panel/api/request_metrics", "/panel/api/request_logs"} {
		out := chWant(t, chDo(t, p, "GET", path, ""), http.StatusNotImplemented)
		if got := chErrMsg(t, out); !strings.Contains(got, "request logger not available") {
			t.Errorf("%s error = %q", path, got)
		}
	}

	// 注入后：completed/succeeded 与 recent（时间降序）都要对。
	rec := reqlog.New(reqlog.Config{Enabled: false})
	rec.Begin()
	rec.Record(reqlog.Event{RequestID: "req-1", Status: 200, OK: true, DurationMs: 10})
	rec.Record(reqlog.Event{RequestID: "req-2", Status: 502, OK: false, DurationMs: 30})

	p2 := chPanel(t, Config{Pool: pool.New(""), RequestLog: rec})
	out := chWant(t, chDo(t, p2, "GET", "/panel/api/request_metrics", ""), http.StatusOK)
	if got := chNum(t, out["completed"]); got != 2 {
		t.Errorf("completed = %v, want 2", got)
	}
	if got := chNum(t, out["succeeded"]); got != 1 {
		t.Errorf("succeeded = %v, want 1", got)
	}
	if got := chNum(t, out["in_flight"]); got != 0 {
		t.Errorf("in_flight = %v, want 0（Begin 已被 Record 归还）", got)
	}
	recent := chRows(t, out["recent"])
	if len(recent) != 2 || chStr(t, recent[0]["request_id"]) != "req-2" {
		t.Fatalf("recent 应为最新在前 [req-2, req-1]，实际 %+v", recent)
	}
}

func TestPanelHandlersRequestLogsLimitAndFilters(t *testing.T) {
	rec := reqlog.New(reqlog.Config{Enabled: true, Dir: t.TempDir(), MaxBytes: 1 << 20, RetentionDays: 7})
	now := time.Now()
	rec.Record(reqlog.Event{Time: now, RequestID: "keep", Model: "glm-5.2", Account: "nick(abcd1234)",
		ClientIP: "10.1.2.3", UserAgent: "curl/8.0", Status: 200, OK: true, Outcome: reqlog.OutcomeSuccess})
	rec.Record(reqlog.Event{Time: now, RequestID: "drop", Model: "gpt-5.5", Account: "other(99998888)",
		ClientIP: "192.168.1.9", UserAgent: "Mozilla/5.0", Status: 429, OK: false, Outcome: reqlog.OutcomeHTTPError})
	rec.Close()

	p := chPanel(t, Config{Pool: pool.New(""), RequestLog: rec})

	// 过滤：model 包含匹配 + client_ip 前缀 → 只留 keep。
	out := chWant(t, chDo(t, p, "GET", "/panel/api/request_logs?model=glm&client_ip=10.1", ""), http.StatusOK)
	rows := chRows(t, out["entries"])
	if len(rows) != 1 || chStr(t, rows[0]["request_id"]) != "keep" {
		t.Fatalf("按 model=glm&client_ip=10.1 过滤应只留 keep，实际 %+v", rows)
	}
	if got := chNum(t, out["limit"]); got != 200 {
		t.Errorf("默认 limit = %v, want 200", got)
	}

	// limit 上限钳制 1000；非法/非正值回落默认 200。
	if got := chNum(t, chWant(t, chDo(t, p, "GET", "/panel/api/request_logs?limit=5000", ""), http.StatusOK)["limit"]); got != 1000 {
		t.Errorf("limit=5000 → %v, want 1000（上限钳制）", got)
	}
	for _, q := range []string{"limit=abc", "limit=0", "limit=-3"} {
		if got := chNum(t, chWant(t, chDo(t, p, "GET", "/panel/api/request_logs?"+q, ""), http.StatusOK)["limit"]); got != 200 {
			t.Errorf("%s → limit %v, want 200（非法值回落默认）", q, got)
		}
	}

	// outcome 过滤命中失败那条。
	out = chWant(t, chDo(t, p, "GET", "/panel/api/request_logs?outcome=http_error", ""), http.StatusOK)
	rows = chRows(t, out["entries"])
	if len(rows) != 1 || chStr(t, rows[0]["request_id"]) != "drop" {
		t.Fatalf("按 outcome=http_error 过滤应只留 drop，实际 %+v", rows)
	}
}

// ---------------------------------------------------------------------------
// panel.go 账号运维端点
// ---------------------------------------------------------------------------

func TestPanelHandlersAccountDisableRevivePauseResume(t *testing.T) {
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-x", false))
	p := chPanel(t, Config{Pool: pl})

	// 未知 uid：四个端点都要 404（不得静默成功）。
	for _, path := range []string{
		"/panel/api/accounts/nope/revive", "/panel/api/accounts/nope/disable",
		"/panel/api/accounts/nope/pause", "/panel/api/accounts/nope/resume",
	} {
		out := chWant(t, chDo(t, p, "POST", path, ""), http.StatusNotFound)
		if got := chErrMsg(t, out); got != "account not found" {
			t.Errorf("%s error = %q, want %q", path, got, "account not found")
		}
	}

	// disable → status.Disabled 且带人工原因；revive → 清掉。
	if out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-x/disable", ""), http.StatusOK); out["ok"] != true {
		t.Fatalf("disable 响应 = %+v", out)
	}
	st, ok := pl.Status("uid-x")
	if !ok || !st.Disabled {
		t.Fatalf("disable 后 status = %+v ok=%v, want disabled", st, ok)
	}
	if st.DisabledReason != "manual disable (panel)" {
		t.Errorf("disabled_reason = %q, want %q", st.DisabledReason, "manual disable (panel)")
	}
	chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-x/revive", ""), http.StatusOK)
	if st, _ := pl.Status("uid-x"); st.Disabled {
		t.Error("revive 后账号仍 disabled")
	}

	// pause → Paused=true 且不写 disabled_reason（暂停≠判死）；resume → 复位。
	chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-x/pause", ""), http.StatusOK)
	st, _ = pl.Status("uid-x")
	if !st.Paused {
		t.Fatal("pause 后 Paused 应为 true")
	}
	if st.Disabled || st.DisabledReason != "" {
		t.Errorf("pause 不该动 disabled 语义: %+v", st)
	}
	chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-x/resume", ""), http.StatusOK)
	if st, _ := pl.Status("uid-x"); st.Paused {
		t.Error("resume 后仍 Paused")
	}
}

func TestPanelHandlersAccountRemove(t *testing.T) {
	dir := t.TempDir()
	credRaw := `{"uid":"uid-rm","access_token":"at-rm","refresh_token":"rt-rm","domain":"codebuddy.cn"}`
	credPath := filepath.Join(dir, "workbuddy-uid-rm.json")
	if err := os.WriteFile(credPath, []byte(credRaw), 0o600); err != nil {
		t.Fatal(err)
	}

	pl := pool.New("")
	a := chAcct(t, "uid-rm", false)
	a.FilePath = credPath
	pl.Add(a)
	// 第二个号：FilePath 指向「非空目录」——os.Remove 必然失败 → file_error 分支。
	dirPath := filepath.Join(dir, "blocked-dir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := chAcct(t, "uid-rm2", false)
	b.FilePath = dirPath
	pl.Add(b)
	// 第三个号：FilePath 为空 → 只出池，不碰文件系统。
	pl.Add(chAcct(t, "uid-rm3", false))

	p := chPanel(t, Config{Pool: pl})

	if out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/nope/remove", ""), http.StatusNotFound); chErrMsg(t, out) != "account not found" {
		t.Errorf("未知 uid error = %q", chErrMsg(t, out))
	}

	// 正常移除：出池 + 凭证文件删除。
	out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-rm/remove", ""), http.StatusOK)
	if out["ok"] != true || out["file_error"] != nil {
		t.Fatalf("remove 响应 = %+v", out)
	}
	if _, exist := pl.Status("uid-rm"); exist {
		t.Error("remove 后账号仍在池中")
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Errorf("凭证文件应被删除，stat err = %v", err)
	}

	// 文件删除失败：仍出池，但如实报 file_error（200 + 提示，不当致命错误）。
	out = chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-rm2/remove", ""), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("remove(file_error) 响应 = %+v", out)
	}
	if msg, _ := out["file_error"].(string); msg == "" {
		t.Error("目录删除失败时应回 file_error")
	}
	if _, exist := pl.Status("uid-rm2"); exist {
		t.Error("remove（文件失败）后账号仍在池中")
	}

	// 无 FilePath：不报错。
	out = chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-rm3/remove", ""), http.StatusOK)
	if out["file_error"] != nil {
		t.Errorf("无 FilePath 不该有 file_error: %+v", out)
	}
}

func TestPanelHandlersAccountBalance(t *testing.T) {
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-bal", false))
	stub := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/billing/meter/get-user-resource" {
			return 200, chResourceBody
		}
		return 404, `{"code":404,"msg":"not found"}`
	})
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false)})

	if out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/nope/balance", ""), http.StatusNotFound); chErrMsg(t, out) != "account not found" {
		t.Errorf("未知 uid error = %q", chErrMsg(t, out))
	}

	out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-bal/balance", ""), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("balance 响应 = %+v", out)
	}
	if got := chNum(t, out["credits"]); got != 400 {
		t.Errorf("credits = %v, want 400", got)
	}
	if got := chNum(t, out["credits_total"]); got != 1000 {
		t.Errorf("credits_total = %v, want 1000", got)
	}
	if st, _ := pl.Status("uid-bal"); st.Credits != 400 || st.CreditsTotal != 1000 {
		t.Errorf("池内 credits = %d/%d, want 400/1000（SetCreditsDetailed 应回写）", st.Credits, st.CreditsTotal)
	}

	// 上游错误 → 502 且带上下文前缀。
	bad := chStubOK(`{"code":14001,"msg":"boom"}`)
	bad = chStubFn(func(chCaptured) (int, string) { return 400, `{"code":14001,"msg":"boom"}` })
	p2 := chPanel(t, Config{Pool: pl, Upstream: chUpstream(bad, false)})
	out = chWant(t, chDo(t, p2, "POST", "/panel/api/accounts/uid-bal/balance", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.HasPrefix(got, "user resource: ") {
		t.Errorf("error = %q, want 前缀 %q", got, "user resource: ")
	}
}

func TestPanelHandlersAccountCheckin(t *testing.T) {
	chForceGlobal(t, true)

	// (1) 正常 CN：签到成功 + 余额刷新（checkin_done=true，无 checkin_message）。
	checkinOK := func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/billing/meter/daily-checkin":
			return 200, `{"code":0,"data":{}}`
		case "/v2/billing/meter/get-user-resource":
			return 200, chResourceBody
		}
		return 404, `{"code":404,"msg":"nf"}`
	}
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-ck", false))
	stub := chStubFn(checkinOK)
	// 注入调度器（带快过期窗口）：签到/余额查询应带上该窗口做分桶。
	sched := scheduler.New(scheduler.Config{
		Pool: pl, Upstream: chUpstream(stub, false), ExpiringSoonWindow: 2 * time.Hour,
	})
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false), Scheduler: sched})
	if got := p.expiringSoonWindow(); got != 2*time.Hour {
		t.Fatalf("expiringSoonWindow = %v, want 2h（面板必须用调度器当前生效窗口）", got)
	}
	if got := chPanel(t, Config{Pool: pool.New("")}).expiringSoonWindow(); got != 0 {
		t.Fatalf("无调度器时窗口应为 0，实际 %v", got)
	}

	if out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/nope/checkin", ""), http.StatusNotFound); chErrMsg(t, out) != "account not found" {
		t.Errorf("未知 uid error = %q", chErrMsg(t, out))
	}

	out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-ck/checkin", ""), http.StatusOK)
	if out["ok"] != true || out["checkin_done"] != true {
		t.Fatalf("checkin 响应 = %+v", out)
	}
	if out["checkin_message"] != nil {
		t.Errorf("签到成功不该有 checkin_message: %+v", out)
	}
	if got := chNum(t, out["credits"]); got != 400 {
		t.Errorf("credits = %v, want 400", got)
	}
	if st, _ := pl.Status("uid-ck"); !st.CheckinDone {
		t.Error("签到成功后池内 CheckinDone 应为 true")
	}
	if n := stub.count("POST", "/v2/billing/meter/daily-checkin"); n != 1 {
		t.Errorf("daily-checkin 命中 %d 次, want 1", n)
	}

	// (2) 上游「今天已签到」幂等拒绝：checkin_done 仍为 true，并把原文透出。
	already := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/billing/meter/daily-checkin" {
			return 200, `{"code":14001,"msg":"今天已签到"}`
		}
		return 200, chResourceBody
	})
	pl2 := pool.New("")
	pl2.Add(chAcct(t, "uid-ck", false))
	p2 := chPanel(t, Config{Pool: pl2, Upstream: chUpstream(already, false)})
	out = chWant(t, chDo(t, p2, "POST", "/panel/api/accounts/uid-ck/checkin", ""), http.StatusOK)
	if out["checkin_done"] != true {
		t.Errorf("幂等「已签到」应记为 checkin_done=true: %+v", out)
	}
	if msg := chStr(t, out["checkin_message"]); !strings.Contains(msg, "已签到") {
		t.Errorf("checkin_message = %q, want 含 %q", msg, "已签到")
	}

	// (3) 其它业务错误（4xx）：不阻塞余额刷新（credits 仍更新），checkin_done=false。
	otherErr := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/billing/meter/daily-checkin" {
			return 400, `{"code":11140,"msg":"request illegal"}`
		}
		return 200, chResourceBody
	})
	pl3 := pool.New("")
	pl3.Add(chAcct(t, "uid-ck", false))
	p3 := chPanel(t, Config{Pool: pl3, Upstream: chUpstream(otherErr, false)})
	out = chWant(t, chDo(t, p3, "POST", "/panel/api/accounts/uid-ck/checkin", ""), http.StatusOK)
	if out["checkin_done"] != false {
		t.Errorf("业务错误不该记为已签到: %+v", out)
	}
	if got := chNum(t, out["credits"]); got != 400 {
		t.Errorf("签到失败时仍应刷新余额，credits = %v, want 400", got)
	}

	// (4) 企业版：不发签到请求，改走企业额度端点，enterprise=true。
	entStub := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/billing/meter/get-enterprise-user-usage" {
			return 200, `{"code":0,"data":{"credit":812.45,"limitNum":2000,"cycleEndTime":"2999-01-01 00:00:00"}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl4 := pool.New("")
	ent := chAcct(t, "uid-ent", false)
	ent.EnterpriseID = "ent-1"
	pl4.Add(ent)
	p4 := chPanel(t, Config{Pool: pl4, Upstream: chUpstream(entStub, false)})
	out = chWant(t, chDo(t, p4, "POST", "/panel/api/accounts/uid-ent/checkin", ""), http.StatusOK)
	if out["enterprise"] != true {
		t.Errorf("企业号响应应带 enterprise=true: %+v", out)
	}
	if msg := chStr(t, out["checkin_message"]); !strings.Contains(msg, "企业账号无签到体系") {
		t.Errorf("checkin_message = %q", msg)
	}
	if got := chNum(t, out["credits"]); got != 1188 {
		t.Errorf("企业额度 credits = %v, want 1188", got)
	}
	if n := entStub.count("POST", "/v2/billing/meter/daily-checkin"); n != 0 {
		t.Errorf("企业号不该调签到端点，命中 %d 次", n)
	}

	// (5) 余额查询失败：仍 200 + ok=true，把失败写进 balance_error。
	resErr := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/billing/meter/daily-checkin" {
			return 200, `{"code":0,"data":{}}`
		}
		return 400, `{"code":14001,"msg":"resource down"}`
	})
	pl5 := pool.New("")
	pl5.Add(chAcct(t, "uid-ck", false))
	p5 := chPanel(t, Config{Pool: pl5, Upstream: chUpstream(resErr, false)})
	out = chWant(t, chDo(t, p5, "POST", "/panel/api/accounts/uid-ck/checkin", ""), http.StatusOK)
	if out["ok"] != true || out["checkin_done"] != true {
		t.Fatalf("签到成功 + 余额失败：响应 = %+v", out)
	}
	if msg := chStr(t, out["balance_error"]); !strings.Contains(msg, "resource down") {
		t.Errorf("balance_error = %q", msg)
	}
	if out["credits"] != nil {
		t.Errorf("余额未取到时不该回 credits 字段: %+v", out)
	}
}

// ---------------------------------------------------------------------------
// panel.go 批量触发端点（Scheduler）
// ---------------------------------------------------------------------------

func TestPanelHandlersBatchTriggers(t *testing.T) {
	// 1) Scheduler 为 nil → 五个端点全部 501（不得假装已触发）。
	bare := chPanel(t, Config{Pool: pool.New("")})
	for _, path := range []string{
		"/panel/api/checkin_all", "/panel/api/travel_all", "/panel/api/activity_all",
		"/panel/api/keepalive_all", "/panel/api/balance_all",
	} {
		out := chWant(t, chDo(t, bare, "POST", path, ""), http.StatusNotImplemented)
		if got := chErrMsg(t, out); !strings.Contains(got, "scheduler not available") {
			t.Errorf("%s error = %q", path, got)
		}
	}

	// 2) 注入调度器（空池 → 各任务为空转，不发上游请求）。
	pl := pool.New("")
	stub := chStubOK(`{"code":0,"data":{}}`)
	sched := scheduler.New(scheduler.Config{Pool: pl, Upstream: chUpstream(stub, false)})
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false), Scheduler: sched})

	for _, path := range []string{
		"/panel/api/checkin_all", "/panel/api/travel_all",
		"/panel/api/activity_all", "/panel/api/keepalive_all",
	} {
		out := chWant(t, chDo(t, p, "POST", path, ""), http.StatusOK)
		if out["ok"] != true || out["started"] != true {
			t.Errorf("%s 响应 = %+v", path, out)
		}
	}
	// balance_all 是同步语义：返回当前池快照。
	out := chWant(t, chDo(t, p, "POST", "/panel/api/balance_all", ""), http.StatusOK)
	if out["ok"] != true {
		t.Errorf("balance_all 响应 = %+v", out)
	}
	if rows := chRows(t, out["accounts"]); len(rows) != 0 {
		t.Errorf("空池 balance_all accounts = %d, want 0", len(rows))
	}
}

// TestPanelHandlersBalanceAllSyncNicknames 覆盖 balance_all 的同步链路：
// 先刷余额，再逐号拉资料同步昵称（上游改名免重登，issue #94）。
func TestPanelHandlersBalanceAllSyncNicknames(t *testing.T) {
	chForceGlobal(t, true)
	pl := pool.New("")
	a := chAcct(t, "uid-nick", false)
	a.Nickname = ""
	pl.Add(a)

	stub := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/billing/meter/get-user-resource":
			return 200, chResourceBody
		case "/console/account":
			return 200, `{"code":0,"data":{"uid":"uid-nick","nickname":"改过名的号","phoneNumber":"13800000000"}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	sched := scheduler.New(scheduler.Config{Pool: pl, Upstream: chUpstream(stub, false)})
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false), Scheduler: sched})

	out := chWant(t, chDo(t, p, "POST", "/panel/api/balance_all", ""), http.StatusOK)
	rows := chRows(t, out["accounts"])
	if len(rows) != 1 {
		t.Fatalf("accounts = %d, want 1", len(rows))
	}
	if got := chStr(t, rows[0]["nickname"]); got != "改过名的号" {
		t.Errorf("nickname = %q, want %q（余额刷新后应同步上游资料）", got, "改过名的号")
	}
	if got := chNum(t, rows[0]["credits"]); got != 400 {
		t.Errorf("credits = %v, want 400", got)
	}
	// 敏感字段（手机号）不得泄进面板响应。
	if strings.Contains(out2Raw(t, out), "13800000000") {
		t.Error("面板响应不该出现上游手机号")
	}
	if n := stub.count("GET", "/console/account"); n != 1 {
		t.Errorf("资料同步命中 %d 次, want 1", n)
	}
}

// out2Raw 把响应对象重新编码成字符串用于「不该出现某子串」的断言。
func out2Raw(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// panel.go 用量 / 积分包 / 券码
// ---------------------------------------------------------------------------

func TestPanelHandlersUsageMonthlyAndSave(t *testing.T) {
	// 未注入 Usage → 三个端点全 501。
	bare := chPanel(t, Config{Pool: pool.New("")})
	for _, c := range []struct{ method, path string }{
		{"GET", "/panel/api/usage"},
		{"GET", "/panel/api/usage/monthly"},
		{"POST", "/panel/api/usage/save"},
	} {
		out := chWant(t, chDo(t, bare, c.method, c.path, ""), http.StatusNotImplemented)
		if got := chErrMsg(t, out); !strings.Contains(got, "usage recorder not available") {
			t.Errorf("%s %s error = %q", c.method, c.path, got)
		}
	}

	// 注入记录器：当月 token 按 Key 归集；usage/save 立即落盘不报错。
	rec := usage.New(filepath.Join(t.TempDir(), "usage.json"))
	rec.Add(time.Now(), "cn", "uid-u", "glm-5.2", usage.Delta{
		KeyID: "k1", TotalTokens: 1234, HasTotal: true,
	}, true)
	p := chPanel(t, Config{Pool: pool.New(""), Usage: rec})

	out := chWant(t, chDo(t, p, "GET", "/panel/api/usage/monthly", ""), http.StatusOK)
	if month := chStr(t, out["month"]); len(month) != len("2026-01") {
		t.Errorf("month = %q, want YYYY-MM", month)
	}
	keys := chRows2Int(t, out["keys"])
	if keys["k1"] != 1234 {
		t.Errorf("keys[k1] = %d, want 1234", keys["k1"])
	}

	if out := chWant(t, chDo(t, p, "POST", "/panel/api/usage/save", ""), http.StatusOK); out["ok"] != true {
		t.Errorf("usage/save 响应 = %+v", out)
	}
}

// chRows2Int 把 {"k":123} 解成 map[string]int64。
func chRows2Int(t *testing.T, v any) map[string]int64 {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("期望对象，拿到 %T", v)
	}
	out := make(map[string]int64, len(m))
	for k, val := range m {
		out[k] = int64(chNum(t, val))
	}
	return out
}

// TestPanelHandlersUsageKeyDimension 覆盖 keyNameMap（livecfg 快照 → by_key/credit_by_key 展示名）。
func TestPanelHandlersUsageKeyDimension(t *testing.T) {
	rec := usage.New("")
	rec.Add(time.Now(), "cn", "uid-u", "glm-5.2", usage.Delta{
		KeyID: "k1", TotalTokens: 2000, HasTotal: true,
		Credit: 1, HasCredit: true,
		PromptTokens: 1000, HasPromptTokens: true,
	}, true)

	live := livecfg.New(livecfg.Snapshot{
		APIKey:  chAPIKey,
		APIKeys: []httpauth.Key{{ID: "k1", Name: "生产 Key"}},
	})
	// Pricing 注入后应折算美元（纯展示层）：1000 prompt token × $1/1M = $0.001。
	p := chPanel(t, Config{
		Pool: pool.New(""), Usage: rec, Live: live, CreditCNYRate: 0.1,
		Pricing: map[string]usage.USDPrice{"glm-5.2": {Input: 1}},
	})

	out := chWant(t, chDo(t, p, "GET", "/panel/api/usage", ""), http.StatusOK)
	if got := chNum(t, out["cny_rate"]); got != 0.1 {
		t.Errorf("cny_rate = %v, want 0.1", got)
	}
	totals := out["totals"].(map[string]any)
	if got := chNum(t, totals["usd_value"]); got != 0.001 {
		t.Errorf("totals.usd_value = %v, want 0.001（按注入定价折算）", got)
	}

	// hours 上限钳制：>1440 一律按 1440（避免一次拉全量把面板拖死）。
	out = chWant(t, chDo(t, p, "GET", "/panel/api/usage?hours=99999", ""), http.StatusOK)
	if got := chNum(t, out["buckets"]); got != 1 {
		t.Errorf("buckets = %v, want 1（窗口内唯一小时桶）", got)
	}

	byKey := chRows(t, out["by_key"])
	if len(byKey) != 1 {
		t.Fatalf("by_key 行数 = %d, want 1: %+v", len(byKey), byKey)
	}
	if got := chStr(t, byKey[0]["key"]); got != "k1" {
		t.Errorf("by_key key = %q, want k1", got)
	}
	if got := chStr(t, byKey[0]["extra"]); got != "生产 Key" {
		t.Errorf("by_key 展示名 = %q, want %q（keyNameMap 未生效）", got, "生产 Key")
	}
}

func TestPanelHandlersPackages(t *testing.T) {
	chForceGlobal(t, true)
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-pa", false))
	pl.Add(chAcct(t, "uid-pb", false))

	// uid-pa 上游报错；uid-pb 返回一个 900 面额包 → 排序后 pb 在前。
	stub := chStubFn(func(c chCaptured) (int, string) {
		if strings.Contains(c.Header.Get("Authorization"), "at-uid-pa") {
			return 400, `{"code":14001,"msg":"packages down"}`
		}
		return 200, `{"code":0,"data":{"Response":{"Data":{"Accounts":[{"PackageName":"活动包",` +
			`"CapacitySize":1000,"CapacityRemain":900,"CycleEndTime":"2999-01-01 00:00:00"}]}}}}`
	})
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false)})

	out := chWant(t, chDo(t, p, "GET", "/panel/api/packages", ""), http.StatusOK)
	rows := chRows(t, out["accounts"])
	if len(rows) != 2 {
		t.Fatalf("accounts = %d, want 2", len(rows))
	}
	if got := chStr(t, rows[0]["uid"]); got != "uid-pb" {
		t.Errorf("首行 uid = %q, want uid-pb（余额降序）", got)
	}
	if got := chNum(t, rows[0]["remain"]); got != 900 {
		t.Errorf("uid-pb remain = %v, want 900", got)
	}
	if got := chNum(t, rows[0]["size"]); got != 1000 {
		t.Errorf("uid-pb size = %v, want 1000", got)
	}
	packs := chRows(t, rows[0]["packages"])
	if len(packs) != 1 || chStr(t, packs[0]["name"]) != "活动包" {
		t.Errorf("逐包构成 = %+v, want 1 个「活动包」", packs)
	}
	if got := chStr(t, rows[1]["uid"]); got != "uid-pa" {
		t.Fatalf("次行 uid = %q, want uid-pa", got)
	}
	if errStr, _ := rows[1]["error"].(string); !strings.Contains(errStr, "packages down") {
		t.Errorf("uid-pa error = %q（单号失败只标该行）", errStr)
	}
}

func TestPanelHandlersSchoolVouchers(t *testing.T) {
	chForceGlobal(t, true)
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-cn", false))
	pl.Add(chAcct(t, "uid-gl", true))
	pl.Add(chAcct(t, "uid-dis", false))
	pl.Disable("uid-dis", "manual")

	stub := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/portal/activity/school/vouchers" {
			return 200, `{"code":0,"data":{"items":[{"grant_id":7,"sku_code":"kfc_ice_cream",` +
				`"prize_name":"肯德基冰淇淋","code":"KFC-123","valid_to":"2026-10-24"}]}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, true)})

	out := chWant(t, chDo(t, p, "GET", "/panel/api/school/vouchers", ""), http.StatusOK)
	rows := chRows(t, out["accounts"])
	if len(rows) != 2 {
		t.Fatalf("accounts = %d, want 2（禁用号必须整行压掉）: %+v", len(rows), rows)
	}
	byUID := map[string]map[string]any{}
	for _, r := range rows {
		byUID[chStr(t, r["uid"])] = r
	}
	cn, ok := byUID["uid-cn"]
	if !ok {
		t.Fatalf("缺 uid-cn 行: %+v", byUID)
	}
	vs := chRows(t, cn["vouchers"])
	if len(vs) != 1 || chStr(t, vs[0]["code"]) != "KFC-123" || chNum(t, vs[0]["grant_id"]) != 7 {
		t.Errorf("券码 = %+v, want 1 张 KFC-123", vs)
	}
	gl, ok := byUID["uid-gl"]
	if !ok {
		t.Fatalf("缺 uid-gl 行: %+v", byUID)
	}
	if errStr, _ := gl["error"].(string); !strings.Contains(errStr, "无开学季活动") {
		t.Errorf("global 行 error = %q", errStr)
	}
	if n := stub.count("GET", "/portal/activity/school/vouchers"); n != 1 {
		t.Errorf("券码端点命中 %d 次, want 1（global/禁用号不得发起上游调用）", n)
	}
}

// ---------------------------------------------------------------------------
// panel.go models（502 / 503 / 成功 + 实测成本合并）
// ---------------------------------------------------------------------------

const chEnterpriseModelsBody = `{"code":0,"data":{"models":[` +
	`{"id":"glm-5.2","name":"GLM-5.2","descriptionZh":"智谱旗舰","credits":"0.5","vendor":"zhipu",` +
	`"maxInputTokens":200000,"maxOutputTokens":8192,"isDefault":true,"supportsToolCall":true,` +
	`"reasoning":{"supportedEfforts":["high","xhigh"],"defaultEffort":"high"}}],` +
	`"agents":[{"name":"cli","models":["glm-5.2","ghost-model"]}]}}`

// chV3ConfigBody /v3/config（主路）响应：模型 + 限时优惠（promo 字段透出面板）。
// 优惠无 schedule → 全天生效（不依赖当前时刻）。
const chV3ConfigBody = `{"code":0,"data":{"models":[` +
	`{"id":"glm-5.2","name":"GLM-5.2","descriptionZh":"智谱旗舰","credits":"0.5","vendor":"zhipu",` +
	`"tags":["推理"],"maxInputTokens":200000,"maxOutputTokens":8192,"maxAllowedSize":1048576,` +
	`"isDefault":true,"supportsToolCall":true,"onlyReasoning":true,"supportsReasoning":true,` +
	`"reasoning":{"effort":"high","summary":"auto","defaultEffort":"high",` +
	`"supportedEfforts":["high","xhigh"],"canDisableThinking":true}}],` +
	`"modelPromotions":[{"enabled":true,"priority":100,"modelIds":["glm-5.2"],` +
	`"badge":{"label":"夜间五折"},"hover":{"textZh":"23:00-07:50 五折"},` +
	`"discount":{"discountedCredits":"0.25","factor":0.5}}]}}`

func TestPanelHandlersModels(t *testing.T) {
	chForceGlobal(t, true)

	// 1) 无任何可用账号 → 503（并给出可操作的提示）。
	stub := chStubOK(`{"code":0,"data":{}}`)
	p := chPanel(t, Config{Pool: pool.New(""), Upstream: chUpstream(stub, false)})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/models", ""), http.StatusServiceUnavailable)
	if got := chErrMsg(t, out); !strings.Contains(got, "没有可用账号") {
		t.Errorf("error = %q", got)
	}

	// 2) 有 CN 账号但上游全失败 → 502，错误里带域前缀。
	failStub := chStubFn(func(chCaptured) (int, string) { return 400, `{"code":14001,"msg":"models down"}` })
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-m", false))
	p2 := chPanel(t, Config{Pool: pl, Upstream: chUpstream(failStub, false)})
	out = chWant(t, chDo(t, p2, "GET", "/panel/api/models", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.HasPrefix(got, "fetch models: cn: ") {
		t.Errorf("error = %q, want 前缀 %q", got, "fetch models: cn: ")
	}

	// 3) 成功：/v3/config（主路，含限时优惠）∪ 企业端点（补缺）合并输出，并验证：
	//    realm 前缀、远端窗口/输出上限权威、effort 档位、promo/成本合并。
	okStub := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/console/enterprises/personal/models":
			return 200, chEnterpriseModelsBody
		case "/v3/config":
			return 200, chV3ConfigBody
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl3 := pool.New("")
	pl3.Add(chAcct(t, "uid-m", false))
	usageRec := usage.New("")
	// 72h 窗口内的实测成本样本：0.5 积分 / 1000 token → 500 积分每 1M token。
	usageRec.Add(time.Now(), "cn", "uid-m", "glm-5.2", usage.Delta{
		Credit: 0.5, HasCredit: true, TotalTokens: 1000, HasTotal: true,
	}, true)
	p3 := chPanel(t, Config{Pool: pl3, Upstream: chUpstream(okStub, false), Usage: usageRec})

	out = chWant(t, chDo(t, p3, "GET", "/panel/api/models", ""), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("models 响应 = %+v", out)
	}
	models := chRows(t, out["models"])
	if len(models) != 1 {
		t.Fatalf("models 条数 = %d, want 1（agents[cli] 未列出的 ghost-model 必须剔除）: %+v", len(models), models)
	}
	m := models[0]
	if got := chStr(t, m["id"]); got != "cn:glm-5.2" {
		t.Errorf("id = %q, want cn:glm-5.2（realm 前缀即调用值）", got)
	}
	if got := chNum(t, m["context_length"]); got != 200000 {
		t.Errorf("context_length = %v, want 200000（远端权威）", got)
	}
	if got := chNum(t, m["max_output_tokens"]); got != 8192 {
		t.Errorf("max_output_tokens = %v, want 8192", got)
	}
	efforts := chStrings(t, m["supported_efforts"])
	if strings.Join(efforts, ",") != "high,xhigh" {
		t.Errorf("supported_efforts = %v, want [high xhigh]", efforts)
	}
	if got := chStr(t, m["default_effort"]); got != "high" {
		t.Errorf("default_effort = %q, want high", got)
	}
	if m["supports_tool_call"] != true || m["is_default"] != true {
		t.Errorf("透传字段缺失: %+v", m)
	}
	if got := chStr(t, m["credits"]); got != "0.5" {
		t.Errorf("credits = %q, want 0.5（牌价原文透出）", got)
	}
	if got := chNum(t, m["cost_ppm"]); got != 500 {
		t.Errorf("cost_ppm = %v, want 500（0.5 积分/1000 token）", got)
	}
	if got := chNum(t, m["cost_samples"]); got != 1 {
		t.Errorf("cost_samples = %v, want 1", got)
	}
	if !strings.Contains(strings.Join(okStub.paths(), ","), "/v3/config") {
		t.Error("应并发探测 /v3/config（单路失败只降级）")
	}
	// 限时优惠与展示字段（前端「生效价 + 标签 + 划线牌价」依赖这些键）。
	if got := chNum(t, m["promo_factor"]); got != 0.5 {
		t.Errorf("promo_factor = %v, want 0.5", got)
	}
	if got := chStr(t, m["promo_credits"]); got != "0.25" {
		t.Errorf("promo_credits = %q, want 0.25（生效价）", got)
	}
	if got := chStr(t, m["promo_label"]); got != "夜间五折" {
		t.Errorf("promo_label = %q", got)
	}
	if got := chStr(t, m["promo_note"]); got != "23:00-07:50 五折" {
		t.Errorf("promo_note = %q", got)
	}
	if got := chNum(t, m["max_allowed_size"]); got != 1048576 {
		t.Errorf("max_allowed_size = %v, want 1048576", got)
	}
	if got := chStr(t, m["reasoning_summary"]); got != "auto" {
		t.Errorf("reasoning_summary = %q, want auto", got)
	}
	if m["can_disable_thinking"] != true || m["only_reasoning"] != true {
		t.Errorf("reasoning 能力字段缺失: %+v", m)
	}

	// 4) global 域：条目一律带 global 前缀（CN/global 双域同源输出，前缀即调用值）。
	plGl := pool.New("")
	plGl.Add(chAcct(t, "uid-gl", true))
	pGl := chPanel(t, Config{Pool: plGl, Upstream: chUpstream(okStub, true)})
	out = chWant(t, chDo(t, pGl, "GET", "/panel/api/models", ""), http.StatusOK)
	glModels := chRows(t, out["models"])
	if len(glModels) == 0 {
		t.Fatalf("global 池 models 为空: %+v", out)
	}
	sawGLM := false
	for _, gm := range glModels {
		id := chStr(t, gm["id"])
		if !strings.HasPrefix(id, "global:") {
			t.Errorf("global 域条目 id = %q, want global: 前缀", id)
		}
		if id == "global:glm-5.2" {
			sawGLM = true
			if len(chStrings(t, gm["supported_efforts"])) == 0 {
				t.Errorf("global 条目 supported_efforts 不应为空: %+v", gm)
			}
		}
	}
	if !sawGLM {
		t.Errorf("global 域缺 global:glm-5.2: %+v", glModels)
	}

	// 5) global 探测失败：整域报错汇总进 502（不静默成空列表）。
	failGlobal := chStubFn(func(c chCaptured) (int, string) {
		return 404, `{"code":404,"msg":"nf"}`
	})
	pGlFail := chPanel(t, Config{Pool: plGl, Upstream: chUpstream(failGlobal, true)})
	out = chWant(t, chDo(t, pGlFail, "GET", "/panel/api/models", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.Contains(got, "global: 上游未返回可用模型") {
		t.Errorf("error = %q, want 含 global 域失败原因", got)
	}

	// 6) 只有 CN 账号且 global 开关开着：不得因 global 空域改变 CN 结果。
	plMix := pool.New("")
	plMix.Add(chAcct(t, "uid-m", false))
	pMix := chPanel(t, Config{Pool: plMix, Upstream: chUpstream(okStub, true)})
	out = chWant(t, chDo(t, pMix, "GET", "/panel/api/models", ""), http.StatusOK)
	if models := chRows(t, out["models"]); len(models) != 1 {
		t.Errorf("纯 CN 池 models = %d, want 1", len(models))
	}
}

func chStrings(t *testing.T, v any) []string {
	t.Helper()
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("期望字符串数组，拿到 %T (%v)", v, v)
	}
	out := make([]string, 0, len(arr))
	for _, it := range arr {
		out = append(out, chStr(t, it))
	}
	return out
}

// ---------------------------------------------------------------------------
// config.go：getConfig / saveConfig
// ---------------------------------------------------------------------------

func TestPanelHandlersGetConfig(t *testing.T) {
	// 未注入 LoadConfig → 501。
	p := chPanel(t, Config{Pool: pool.New("")})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/config", ""), http.StatusNotImplemented)
	if got := chErrMsg(t, out); !strings.Contains(got, "config api not available") {
		t.Errorf("error = %q", got)
	}

	// LoadConfig 报错 → 500 且带前缀（不是 200 空配置）。
	bad := chPanel(t, Config{Pool: pool.New(""),
		LoadConfig: func() (any, error) { return nil, fmt.Errorf("boom") }})
	out = chWant(t, chDo(t, bad, "GET", "/panel/api/config", ""), http.StatusInternalServerError)
	if got := chErrMsg(t, out); got != "load config: boom" {
		t.Errorf("error = %q, want %q", got, "load config: boom")
	}

	// 成功：回显 path + 注入的配置对象。
	ok := chPanel(t, Config{Pool: pool.New(""), ConfigPath: `C:\yg\config.json`,
		LoadConfig: func() (any, error) {
			return map[string]any{"api_key": "sk-x", "port": 7863}, nil
		}})
	out = chWant(t, chDo(t, ok, "GET", "/panel/api/config", ""), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("响应 = %+v", out)
	}
	if got := chStr(t, out["path"]); got != `C:\yg\config.json` {
		t.Errorf("path = %q", got)
	}
	cfg := out["config"].(map[string]any)
	if got := chStr(t, cfg["api_key"]); got != "sk-x" {
		t.Errorf("config.api_key = %q", got)
	}
	if got := chNum(t, cfg["port"]); got != 7863 {
		t.Errorf("config.port = %v", got)
	}
}

func TestPanelHandlersSaveConfig(t *testing.T) {
	// 未注入 SaveConfig → 501。
	bare := chPanel(t, Config{Pool: pool.New("")})
	out := chWant(t, chDo(t, bare, "POST", "/panel/api/config", `{}`), http.StatusNotImplemented)
	if got := chErrMsg(t, out); !strings.Contains(got, "config api not available") {
		t.Errorf("error = %q", got)
	}

	body := `{"api_key":"sk-x","port":7863}`

	// 成功 + 闭包返回 nil 列表：restart_required 必须是 []（不是 null —— 前端会按
	// 「null 表示未计算」走错分支）。
	var gotRaw []byte
	ok := chPanel(t, Config{Pool: pool.New(""), SaveConfig: func(raw []byte) ([]string, error) {
		gotRaw = append([]byte(nil), raw...)
		return nil, nil
	}})
	rec := chDo(t, ok, "POST", "/panel/api/config", body)
	out = chWant(t, rec, http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("响应 = %+v", out)
	}
	if string(gotRaw) != body {
		t.Errorf("SaveConfig 收到的原文 = %q, want %q（body 必须原样透传）", string(gotRaw), body)
	}
	if !strings.Contains(rec.Body.String(), `"restart_required":[]`) {
		t.Errorf("body = %s, want restart_required:[]", rec.Body.String())
	}
	if rows := chRows(t, out["restart_required"]); len(rows) != 0 {
		t.Errorf("restart_required = %+v, want 空数组", out["restart_required"])
	}

	// 成功 + 有需重启字段：原样回传。
	list := chPanel(t, Config{Pool: pool.New(""), SaveConfig: func([]byte) ([]string, error) {
		return []string{"port", "api_key"}, nil
	}})
	out = chWant(t, chDo(t, list, "POST", "/panel/api/config", body), http.StatusOK)
	got := chStrings(t, out["restart_required"])
	if strings.Join(got, ",") != "port,api_key" {
		t.Errorf("restart_required = %v, want [port api_key]", got)
	}

	// 校验失败 → 400 且错误原文透出，成功体（ok/restart_required）一律不得出现。
	rejected := chPanel(t, Config{Pool: pool.New(""), SaveConfig: func(raw []byte) ([]string, error) {
		gotRaw = append([]byte(nil), raw...)
		return nil, fmt.Errorf("api_key 不能为空")
	}})
	rec = chDo(t, rejected, "POST", "/panel/api/config", body)
	out = chWant(t, rec, http.StatusBadRequest)
	if got := chErrMsg(t, out); got != "api_key 不能为空" {
		t.Errorf("error = %q", got)
	}
	if strings.Contains(rec.Body.String(), "restart_required") {
		t.Errorf("失败响应不该带 restart_required: %s", rec.Body.String())
	}
	if string(gotRaw) != body {
		t.Errorf("失败路径也必须把原文交给校验闭包（由它决定不写盘）: %q", string(gotRaw))
	}
}

// ---------------------------------------------------------------------------
// import.go：importCockpit / validImportUID
// ---------------------------------------------------------------------------

func TestPanelHandlersImportCockpitBadRequests(t *testing.T) {
	pl := pool.New("")
	p := chPanel(t, Config{Pool: pl, AuthDir: t.TempDir(), Upstream: chUpstream(chStubOK(""), false)})

	// 非 multipart → 400 parse form。
	rec := chDo(t, p, "POST", "/panel/api/import/cockpit", `{}`)
	out := chWant(t, rec, http.StatusBadRequest)
	if got := chErrMsg(t, out); !strings.HasPrefix(got, "parse form: ") {
		t.Errorf("error = %q", got)
	}

	// multipart 但缺 file 字段 → 400 missing file field。
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("other", "x")
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/panel/api/import/cockpit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+chAPIKey)
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	out = chWant(t, rec, http.StatusBadRequest)
	if got := chErrMsg(t, out); !strings.HasPrefix(got, "missing file field: ") {
		t.Errorf("error = %q", got)
	}

	// 内容不是 JSON → 400 invalid json；不是数组（对象）同样非法。
	for _, payload := range []string{`{not json`, `{"uid":"x"}`} {
		body, ct := chMultipart(t, "cockpit.json", payload)
		req = httptest.NewRequest("POST", "/panel/api/import/cockpit", body)
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+chAPIKey)
		rec = httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		out = chWant(t, rec, http.StatusBadRequest)
		if got := chErrMsg(t, out); !strings.HasPrefix(got, "invalid json: ") {
			t.Errorf("payload=%q error = %q", payload, got)
		}
	}

	// 空数组 → 400 empty accounts array（不能让「导入 0 个」看起来像成功）。
	body, ct := chMultipart(t, "cockpit.json", `[]`)
	req = httptest.NewRequest("POST", "/panel/api/import/cockpit", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+chAPIKey)
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	out = chWant(t, rec, http.StatusBadRequest)
	if got := chErrMsg(t, out); got != "empty accounts array" {
		t.Errorf("error = %q", got)
	}
}

func TestPanelHandlersImportCockpit(t *testing.T) {
	chForceGlobal(t, true)
	dir := t.TempDir()
	pl := pool.New("")

	stub := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/billing/meter/get-user-resource", "/billing/meter/get-user-resource":
			return 200, chResourceBody
		case "/v2/billing/meter/daily-checkin", "/billing/meter/daily-checkin":
			return 200, `{"code":0,"data":{}}`
		case "/billing/ide/trial":
			return 200, `{"code":0,"data":{}}`
		}
		if strings.HasPrefix(c.Path, "/auth/realms/") {
			return 200, `{"code":200,"msg":"register success"}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	p := chPanel(t, Config{Pool: pl, AuthDir: dir, Upstream: chUpstream(stub, true)})

	payload := `[
	  {"id":"1","uid":"uid-imp-cn","email":"cn@test","access_token":"at-cn","refresh_token":"rt-cn",
	   "expires_at":1800000000000,"domain":"codebuddy.cn"},
	  {"id":"2","uid":"uid-imp-gl","email":"gl@test","access_token":"at-gl","refresh_token":"rt-gl",
	   "expires_at":1800000000000,"domain":"workbuddy.ai"},
	  {"id":"3","uid":"../evil","access_token":"at-x","refresh_token":"rt-x"},
	  {"id":"4","uid":"uid-no-refresh","access_token":"at-y"}
	]`
	body, ct := chMultipart(t, "cockpit.json", payload)
	req := httptest.NewRequest("POST", "/panel/api/import/cockpit", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+chAPIKey)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	out := chWant(t, rec, http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("响应 = %+v", out)
	}
	if got := chNum(t, out["total"]); got != 4 {
		t.Errorf("total = %v, want 4", got)
	}
	if got := chNum(t, out["imported"]); got != 2 {
		t.Errorf("imported = %v, want 2", got)
	}
	if got := chNum(t, out["skipped"]); got != 2 {
		t.Errorf("skipped = %v, want 2", got)
	}
	errs := chStrings(t, out["errors"])
	joined := strings.Join(errs, " | ")
	if !strings.Contains(joined, "invalid uid (id=3)") {
		t.Errorf("errors 应记录非法 uid: %q", joined)
	}
	if !strings.Contains(joined, "missing required fields (id=4)") {
		t.Errorf("errors 应记录缺字段: %q", joined)
	}

	// 落盘：只有两个合法账号有凭证文件，路径穿越的 uid 不得产生任何文件。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "workbuddy-uid-imp-cn.json,workbuddy-uid-imp-gl.json" {
		t.Fatalf("落盘文件 = %v, want 两个合法 uid 的凭证文件", names)
	}
	rawCN, err := os.ReadFile(filepath.Join(dir, "workbuddy-uid-imp-cn.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := auth.Parse(rawCN)
	if err != nil {
		t.Fatalf("导入的凭证文件不可解析: %v", err)
	}
	if parsed.UID != "uid-imp-cn" || parsed.RefreshToken != "rt-cn" {
		t.Errorf("凭证内容 = uid %q rt %q, want uid-imp-cn / rt-cn", parsed.UID, parsed.RefreshToken)
	}
	if parsed.ExpiresAt != 1800000000 {
		t.Errorf("ExpiresAt = %d, want 1800000000（毫秒 → 秒）", parsed.ExpiresAt)
	}
	// nickname 缺省回落 email。
	if parsed.Nickname != "cn@test" {
		t.Errorf("nickname = %q, want 回落 email %q", parsed.Nickname, "cn@test")
	}

	// 入池：realm 按 domain 推断；余额刷新写回 credits。
	stCN, ok := pl.Status("uid-imp-cn")
	if !ok {
		t.Fatal("uid-imp-cn 未进池")
	}
	if stCN.Realm != "cn" {
		t.Errorf("uid-imp-cn realm = %q, want cn", stCN.Realm)
	}
	if stCN.Credits != 400 {
		t.Errorf("uid-imp-cn credits = %d, want 400（导入后应刷新余额）", stCN.Credits)
	}
	stGL, ok := pl.Status("uid-imp-gl")
	if !ok {
		t.Fatal("uid-imp-gl 未进池")
	}
	if stGL.Realm != "global" {
		t.Errorf("uid-imp-gl realm = %q, want global（domain 推断）", stGL.Realm)
	}
	if n := stub.count("POST", "/billing/ide/trial"); n != 1 {
		t.Errorf("global 导入应领 trial，命中 %d 次", n)
	}
	if n := stub.count("POST", "/v2/billing/meter/daily-checkin"); n != 1 {
		t.Errorf("CN 导入应签到，命中 %d 次, want 1（global 不得走 CN 签到）", n)
	}
}

// validImportUID 白名单边界：它是「导入 uid 拼文件名」的唯一闸门，
// 放行一个 . 就能写成 auths/../evil.json。
func TestValidImportUIDBoundaries(t *testing.T) {
	cases := []struct {
		uid  string
		want bool
	}{
		{"", false},
		{"a", true},
		{"abc_123-XYZ", true},
		{"11111111-2222-4333-8444-555555555555", true},
		{"-", true},
		{"a..b", false},                 // 点号不放行（路径穿越最小单位）
		{"../evil", false},              // 相对路径
		{"..\\..\\evil", false},         // Windows 反斜杠
		{"a/b", false},                  // 分隔符
		{"a b", false},                  // 空格
		{"uid\n", false},                // 换行（日志/头注入面）
		{"uid\x00", false},              // NUL
		{"中文uid", false},                // 非 ASCII
		{"café", false},                 // 组合字符
		{"émile", false},                // Latin-1 之外
		{strings.Repeat("a", 64), true}, // 恰好 64
		{strings.Repeat("a", 65), false},
	}
	for _, c := range cases {
		if got := validImportUID(c.uid); got != c.want {
			t.Errorf("validImportUID(%q) = %v, want %v", c.uid, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// login.go：doJSON / loginStart / loginPoll / joinMsg / loginRegions
// ---------------------------------------------------------------------------

func TestPanelHandlersLoginStart(t *testing.T) {
	okBody := `{"code":0,"data":{"state":"st-abcdef123","authUrl":"https://auth.test/go"}}`

	// (1) 缺省 realm=cn：打到 tencent 域，Origin/Referer 是 codebuddy.cn，UA 恒定。
	stub := chStubOK(okBody)
	chSwapLoginHTTP(t, stub)
	p := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	out := chWant(t, chDo(t, p, "POST", "/panel/api/login/start", ""), http.StatusOK)
	if out["ok"] != true || chStr(t, out["realm"]) != "cn" {
		t.Fatalf("响应 = %+v", out)
	}
	if got := chStr(t, out["url"]); got != "https://auth.test/go" {
		t.Errorf("url = %q", got)
	}
	if got := chStr(t, out["state"]); got != "st-abcdef123" {
		t.Errorf("state = %q", got)
	}
	reqs := stub.snapshot()
	if len(reqs) != 1 {
		t.Fatalf("出站请求数 = %d, want 1", len(reqs))
	}
	r0 := reqs[0]
	if r0.Path != "/v2/plugin/auth/state" || r0.Query.Get("platform") != "CLI" {
		t.Errorf("state 端点 = %s?platform=%s", r0.Path, r0.Query.Get("platform"))
	}
	if got := r0.Header.Get("Origin"); got != "https://www.codebuddy.cn" {
		t.Errorf("Origin = %q, want https://www.codebuddy.cn", got)
	}
	if got := r0.Header.Get("Referer"); got != "https://www.codebuddy.cn/" {
		t.Errorf("Referer = %q", got)
	}
	if got := r0.Header.Get("User-Agent"); got != "CLI/2.63.2 CodeBuddy/2.63.2" {
		t.Errorf("User-Agent = %q（三段式指纹不得漂移）", got)
	}
	if got := r0.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := r0.Header.Get("X-Requested-With"); got != "XMLHttpRequest" {
		t.Errorf("X-Requested-With = %q", got)
	}
	if got := r0.Header.Get("Authorization"); got != "" {
		t.Errorf("auth/state 不该带 Bearer，实际 %q", got)
	}
	if r0.Body != "{}" {
		t.Errorf("body = %q, want {}", r0.Body)
	}
	// state 已登记，realm 记 cn。
	p.loginMu.Lock()
	sess, known := p.logins["st-abcdef123"]
	p.loginMu.Unlock()
	if !known || sess.realm != "cn" {
		t.Errorf("登录会话 = %+v known=%v", sess, known)
	}

	// (2) realm=global：切到 workbuddy.ai 域。
	gstub := chStubOK(okBody)
	chSwapLoginHTTP(t, gstub)
	pg := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	out = chWant(t, chDo(t, pg, "POST", "/panel/api/login/start", `{"realm":"global"}`), http.StatusOK)
	if got := chStr(t, out["realm"]); got != "global" {
		t.Fatalf("realm = %q, want global", got)
	}
	gre := gstub.snapshot()[0]
	if got := gre.Header.Get("Origin"); got != "https://www.workbuddy.ai" {
		t.Errorf("global Origin = %q", got)
	}

	// (3) body 非法 JSON：按缺省 cn 处理（不得 500）。
	badJSON := chStubOK(okBody)
	chSwapLoginHTTP(t, badJSON)
	pb := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	out = chWant(t, chDo(t, pb, "POST", "/panel/api/login/start", `{`), http.StatusOK)
	if got := chStr(t, out["realm"]); got != "cn" {
		t.Errorf("非法 body 应回落 cn，实际 %q", got)
	}

	// (4) 上游 5xx → 502，错误里带上游状态码。
	e5 := chStubFn(func(chCaptured) (int, string) { return 500, `{"code":10000,"msg":"down"}` })
	chSwapLoginHTTP(t, e5)
	pe := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	out = chWant(t, chDo(t, pe, "POST", "/panel/api/login/start", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.Contains(got, "auth state (upstream 500)") {
		t.Errorf("error = %q", got)
	}

	// (5) 上游 200 但缺 state/authUrl → 502（不能让前端拿到半个授权链接）。
	miss := chStubOK(`{"code":0,"data":{"state":"st-1"}}`)
	chSwapLoginHTTP(t, miss)
	pm := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	out = chWant(t, chDo(t, pm, "POST", "/panel/api/login/start", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); got != "auth state: missing state or authUrl" {
		t.Errorf("error = %q", got)
	}

	// (6) 回收过期会话：发起新登录时顺手清掉超过 loginTTL 的旧 state。
	rec := chStubOK(okBody)
	chSwapLoginHTTP(t, rec)
	pr := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	pr.loginMu.Lock()
	pr.logins["st-stale"] = loginSession{created: time.Now().Add(-loginTTL - time.Minute), realm: "cn"}
	pr.logins["st-fresh"] = loginSession{created: time.Now(), realm: "cn"}
	pr.loginMu.Unlock()
	chWant(t, chDo(t, pr, "POST", "/panel/api/login/start", ""), http.StatusOK)
	pr.loginMu.Lock()
	_, stale := pr.logins["st-stale"]
	_, fresh := pr.logins["st-fresh"]
	_, newOne := pr.logins["st-abcdef123"]
	pr.loginMu.Unlock()
	if stale {
		t.Error("超过 loginTTL 的 state 应被回收")
	}
	if !fresh || !newOne {
		t.Errorf("未过期的 state 不该被误删: fresh=%v new=%v", fresh, newOne)
	}
}

// TestPanelHandlersLoginPoll 覆盖轮询的全部错误分支与成功落盘（免重启热加载）。
func TestPanelHandlersLoginPoll(t *testing.T) {
	// 缺 state → 400；未知/过期 state → 404。
	stub := chStubOK(`{"code":0,"data":{}}`)
	chSwapLoginHTTP(t, stub)
	p := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/login/poll", ""), http.StatusBadRequest)
	if got := chErrMsg(t, out); got != "missing state" {
		t.Errorf("error = %q", got)
	}
	out = chWant(t, chDo(t, p, "GET", "/panel/api/login/poll?state=st-nope", ""), http.StatusNotFound)
	if got := chErrMsg(t, out); !strings.Contains(got, "unknown or expired state") {
		t.Errorf("error = %q", got)
	}

	seed := func(p *Panel, state, realm string) {
		p.loginMu.Lock()
		p.logins[state] = loginSession{created: time.Now(), realm: realm}
		p.loginMu.Unlock()
	}
	hasLogin := func(p *Panel, state string) bool {
		p.loginMu.Lock()
		defer p.loginMu.Unlock()
		_, ok := p.logins[state]
		return ok
	}

	// pending：上游业务 code 非 0 → 200 done=false + message（前端继续轮询）。
	pending := chStubOK(`{"code":211,"msg":"login ing"}`)
	chSwapLoginHTTP(t, pending)
	pp := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	seed(pp, "st-p", "cn")
	out = chWant(t, chDo(t, pp, "GET", "/panel/api/login/poll?state=st-p", ""), http.StatusOK)
	if out["done"] != false {
		t.Fatalf("pending 响应 = %+v", out)
	}
	if msg := chStr(t, out["message"]); !strings.Contains(msg, "login ing") {
		t.Errorf("message = %q", msg)
	}
	if !hasLogin(pp, "st-p") {
		t.Error("pending 时不该删除会话（否则前端永远轮询不到完成）")
	}

	// 已发 token 但 accessToken 为空 → done=false waiting for login。
	noTok := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/plugin/auth/token" {
			return 200, `{"code":0,"data":{"refreshToken":"rt-partial"}}`
		}
		return 200, `{"code":0,"data":{}}`
	})
	chSwapLoginHTTP(t, noTok)
	pn := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	seed(pn, "st-n", "cn")
	out = chWant(t, chDo(t, pn, "GET", "/panel/api/login/poll?state=st-n", ""), http.StatusOK)
	if out["done"] != false || chStr(t, out["message"]) != "waiting for login" {
		t.Errorf("半截 token 响应 = %+v", out)
	}

	// token 已发但拿不到 uid → 502（不得落盘无名凭证）。
	noUID := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/plugin/auth/token":
			return 200, `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600}}`
		case "/v2/plugin/login/account":
			return 200, `{"code":0,"data":{"nickname":"无 uid"}}`
		}
		return 200, `{"code":0,"data":{}}`
	})
	chSwapLoginHTTP(t, noUID)
	pu := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	seed(pu, "st-u", "cn")
	out = chWant(t, chDo(t, pu, "GET", "/panel/api/login/poll?state=st-u", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.Contains(got, "no uid") {
		t.Errorf("error = %q", got)
	}

	// 上游返回路径穿越 uid → 502 且不落盘（防 auths/../evil.json）。
	evilUID := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/plugin/auth/token":
			return 200, `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600}}`
		case "/v2/plugin/login/account":
			return 200, `{"code":0,"data":{"uid":"../../evil","nickname":"坏人"}}`
		}
		return 200, `{"code":0,"data":{}}`
	})
	dirEvil := t.TempDir()
	chSwapLoginHTTP(t, evilUID)
	pev := chPanel(t, Config{Pool: pool.New(""), AuthDir: dirEvil})
	seed(pev, "st-e", "cn")
	out = chWant(t, chDo(t, pev, "GET", "/panel/api/login/poll?state=st-e", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.Contains(got, "含非法字符") {
		t.Errorf("error = %q", got)
	}
	files, err := os.ReadDir(dirEvil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("非法 uid 不该落盘任何文件，实际 %v", files)
	}

	// AuthDir 为空 → MkdirAll 失败 → 500（不得静默当成功）。
	// 用「uid 合法」的桩，确保走到落盘那一步才失败（不是被 uid 校验拦下）。
	validUIDStub := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/plugin/auth/token":
			return 200, `{"code":0,"data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":3600}}`
		case "/v2/plugin/login/account":
			return 200, `{"code":0,"data":{"uid":"uid-empty-dir","nickname":"无目录"}}`
		}
		return 200, `{"code":0,"data":{}}`
	})
	chSwapLoginHTTP(t, validUIDStub)
	pEmpty := chPanel(t, Config{Pool: pool.New(""), AuthDir: ""})
	seed(pEmpty, "st-m", "cn")
	out = chWant(t, chDo(t, pEmpty, "GET", "/panel/api/login/poll?state=st-m", ""), http.StatusInternalServerError)
	if got := chErrMsg(t, out); !strings.Contains(got, "mkdir auth dir") {
		t.Errorf("error = %q", got)
	}

	// ---- 成功路径（CN）：凭证落盘 + 热加载进池 + 顺带签到/余额 ----
	dir := t.TempDir()
	successCN := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/plugin/auth/token":
			return 200, `{"code":0,"data":{"accessToken":"at-login","refreshToken":"rt-login","expiresIn":3600,"domain":"codebuddy.cn"}}`
		case "/v2/plugin/login/account":
			return 200, `{"code":0,"data":{"uid":"uid-login","enterpriseId":"","nickname":"登录号"}}`
		case "/v2/billing/meter/daily-checkin":
			return 200, `{"code":0,"data":{}}`
		case "/v2/billing/meter/get-user-resource":
			return 200, chResourceBody
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	chSwapLoginHTTP(t, successCN)
	pl := pool.New("")
	before := time.Now()
	ps := chPanel(t, Config{Pool: pl, AuthDir: dir, Upstream: chUpstream(successCN, true)})
	seed(ps, "st-ok", "cn")
	out = chWant(t, chDo(t, ps, "GET", "/panel/api/login/poll?state=st-ok", ""), http.StatusOK)
	if out["done"] != true {
		t.Fatalf("成功响应 = %+v", out)
	}
	if got := chStr(t, out["uid"]); got != "uid-login" {
		t.Errorf("uid = %q", got)
	}
	if got := chStr(t, out["nickname"]); got != "登录号" {
		t.Errorf("nickname = %q", got)
	}
	if got := chStr(t, out["realm"]); got != "cn" {
		t.Errorf("realm = %q, want cn", got)
	}
	if got := chNum(t, out["credits"]); got != 400 {
		t.Errorf("credits = %v, want 400", got)
	}
	if got := chNum(t, out["credits_total"]); got != 1000 {
		t.Errorf("credits_total = %v, want 1000", got)
	}
	if msg, _ := out["checkin_message"].(string); msg != "" {
		t.Errorf("CN 签到成功不该有 checkin_message: %q", msg)
	}
	if hasLogin(ps, "st-ok") {
		t.Error("完成登录后应从 logins 中剔除该 state")
	}

	// 凭证文件：可解析、字段正确、expiresIn 换算成秒。
	credPath := filepath.Join(dir, "workbuddy-uid-login.json")
	raw, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("登录完成应落盘凭证: %v", err)
	}
	parsed, err := auth.Parse(raw)
	if err != nil {
		t.Fatalf("凭证不可解析: %v", err)
	}
	wantExpMin := before.Add(3590 * time.Second).Unix()
	wantExpMax := time.Now().Add(3610 * time.Second).Unix()
	if parsed.ExpiresAt < wantExpMin || parsed.ExpiresAt > wantExpMax {
		t.Errorf("ExpiresAt = %d, want ∈ [%d,%d]（expiresIn=3600 换算）", parsed.ExpiresAt, wantExpMin, wantExpMax)
	}
	if parsed.UID != "uid-login" || parsed.RefreshToken != "rt-login" {
		t.Errorf("凭证 = uid %q rt %q", parsed.UID, parsed.RefreshToken)
	}
	if parsed.Realm() != "cn" {
		t.Errorf("凭证 realm = %q, want cn（CN 也显式补 realm 键）", parsed.Realm())
	}

	// 热加载进池：余额已刷新（登录完成不做本地签到标记——签到标记走 pool.NoteCheckinDone，
	// 由 accountCheckin / 调度器维护；此处只钉「登录即完成余额刷新」）。
	st, ok := pl.Status("uid-login")
	if !ok {
		t.Fatal("登录完成后账号应已在池中")
	}
	if st.Credits != 400 {
		t.Errorf("池内 credits = %d, want 400（登录完成应刷新余额）", st.Credits)
	}
	if st.Realm != "cn" {
		t.Errorf("池内 realm = %q, want cn", st.Realm)
	}

	// 出站细节：account 端点带 Bearer 取到的新 accessToken；签到走 CN v2 路径。
	var acctReq, checkinReq *chCaptured
	for _, r := range successCN.snapshot() {
		switch r.Path {
		case "/v2/plugin/login/account":
			rr := r
			acctReq = &rr
		case "/v2/billing/meter/daily-checkin":
			rr := r
			checkinReq = &rr
		}
	}
	if acctReq == nil {
		t.Fatal("未取账号资料（uid/nickname 来源）")
	}
	if got := acctReq.Header.Get("Authorization"); got != "Bearer at-login" {
		t.Errorf("account 端点 Authorization = %q, want Bearer at-login", got)
	}
	if got := acctReq.Query.Get("state"); got != "st-ok" {
		t.Errorf("account 端点 state = %q", got)
	}
	if checkinReq == nil {
		t.Fatal("CN 登录完成应顺带签到")
	}

	// ---- 成功路径（global）：注册激活 + trial，不签到，落盘 realm=global ----
	gdir := t.TempDir()
	successGL := chStubFn(func(c chCaptured) (int, string) {
		switch {
		case c.Path == "/v2/plugin/auth/token":
			return 200, `{"code":0,"data":{"accessToken":"at-gl","refreshToken":"rt-gl","expiresIn":3600,"domain":"workbuddy.ai"}}`
		case c.Path == "/v2/plugin/login/account":
			return 200, `{"code":0,"data":{"uid":"uid-glogin","nickname":"国际号"}}`
		case strings.HasPrefix(c.Path, "/auth/realms/"):
			return 200, `{"code":200,"msg":"register success"}`
		case c.Path == "/billing/ide/trial":
			return 200, `{"code":0,"data":{}}`
		case c.Path == "/billing/meter/get-user-resource", c.Path == "/v2/billing/meter/get-user-resource":
			return 200, chResourceBody
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	chSwapLoginHTTP(t, successGL)
	plG := pool.New("")
	pg := chPanel(t, Config{Pool: plG, AuthDir: gdir, Upstream: chUpstream(successGL, true)})
	seed(pg, "st-gl", "global")
	out = chWant(t, chDo(t, pg, "GET", "/panel/api/login/poll?state=st-gl", ""), http.StatusOK)
	if out["done"] != true || chStr(t, out["realm"]) != "global" {
		t.Fatalf("global 响应 = %+v", out)
	}
	if msg, _ := out["checkin_message"].(string); msg != "" {
		t.Errorf("global 激活/trial 成功不该有 checkin_message: %q", msg)
	}
	if n := successGL.count("POST", "/v2/billing/meter/daily-checkin"); n != 0 {
		t.Errorf("global 登录不该走 CN 签到，命中 %d 次", n)
	}
	rawGL, err := os.ReadFile(filepath.Join(gdir, "workbuddy-uid-glogin.json"))
	if err != nil {
		t.Fatalf("global 凭证应落盘: %v", err)
	}
	parsedGL, err := auth.Parse(rawGL)
	if err != nil {
		t.Fatal(err)
	}
	if parsedGL.Realm() != "global" {
		t.Errorf("global 凭证 realm = %q, want global（Realm() 按落盘键判域）", parsedGL.Realm())
	}
}

// joinMsg 多段提示拼接（trial 失败文案依赖它，空段必须跳过）。
func TestJoinMsg(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{""}, ""},
		{[]string{"a"}, "a"},
		{[]string{"", "b", ""}, "b"},
		{[]string{"a", "b"}, "a；b"},
		{[]string{"a", "", "b", "c"}, "a；b；c"},
	}
	for _, c := range cases {
		if got := joinMsg(c.in...); got != c.want {
			t.Errorf("joinMsg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPanelHandlersLoginRegions(t *testing.T) {
	chSwapLoginHTTP(t, chStubOK(`{"code":0,"data":{}}`))
	p := chPanel(t, Config{Pool: pool.New(""), AuthDir: t.TempDir()})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/login/regions", ""), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("响应 = %+v", out)
	}
	rows := chRows(t, out["regions"])
	want := []struct{ code, name string }{
		{"HK", "Hong Kong"}, {"MO", "Macao"}, {"SG", "Singapore"}, {"TH", "Thailand"},
		{"PH", "Philippines"}, {"MY", "Malaysia"}, {"ID", "Indonesia"},
	}
	if len(rows) != len(want) {
		t.Fatalf("regions 数量 = %d, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if rows[i]["code"] != w.code || rows[i]["name"] != w.name {
			t.Errorf("regions[%d] = %v, want %s/%s（顺序即 web 展示顺序）", i, rows[i], w.code, w.name)
		}
	}
}

// ---------------------------------------------------------------------------
// tasks.go：任务列表 / 接受 / 领取
// ---------------------------------------------------------------------------

const chTasksBody = `{"code":0,"data":{"tasks":[` +
	`{"task_code":"chat_5","title":"完成 5 次对话","target":5,"current":0,"accept_status":"not_accepted"},` +
	`{"task_code":"RichMeow_Chat","title":"与猫猫对话","target":1,"current":1,"accept_status":"accepted"}]}}`

const chTasksMPBody = `{"code":0,"data":{"tasks":[` +
	`{"task_code":"chat_5","title":"完成 5 次对话","target":5,"current":0,"accept_status":"not_accepted"},` +
	`{"task_code":"school_season","title":"校园日","target":1,"current":0,"accept_status":"not_accepted"}]}}`

// chGrowthStub 按 mp 头分流 growth 任务列表的桩。
func chGrowthStub(defaultBody, mpBody string) *chStub {
	return chStubFn(func(c chCaptured) (int, string) {
		if c.Path != "/v2/activity/growth/tasks" {
			return 404, `{"code":404,"msg":"nf"}`
		}
		if c.Header.Get("X-Client-Platform") == "miniprogram" {
			return 200, mpBody
		}
		return 200, defaultBody
	})
}

func TestPanelHandlersAccountTasks(t *testing.T) {
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-t", false))
	stub := chGrowthStub(chTasksBody, chTasksMPBody)
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false)})

	out := chWant(t, chDo(t, p, "GET", "/panel/api/accounts/nope/tasks", ""), http.StatusNotFound)
	if got := chErrMsg(t, out); got != "account not found" {
		t.Errorf("error = %q", got)
	}

	out = chWant(t, chDo(t, p, "GET", "/panel/api/accounts/uid-t/tasks", ""), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("响应 = %+v", out)
	}
	tasks := chRows(t, out["tasks"])
	codes := []string{}
	for _, tk := range tasks {
		codes = append(codes, chStr(t, tk["task_code"]))
	}
	// 默认口径 + mp 口径合并，按 task_code 去重（chat_5 只出现一次）。
	if strings.Join(codes, ",") != "chat_5,RichMeow_Chat,school_season" {
		t.Fatalf("tasks 合并结果 = %v, want [chat_5 RichMeow_Chat school_season]", codes)
	}
	if tasks[1]["claimable"] != true {
		t.Errorf("RichMeow_Chat (1/1) 应 claimable: %+v", tasks[1])
	}

	// 默认口径失败 → 502（不得回空列表让前端以为「没有任务」）。
	bad := chStubFn(func(chCaptured) (int, string) { return 400, `{"code":14001,"msg":"tasks down"}` })
	p2 := chPanel(t, Config{Pool: pl, Upstream: chUpstream(bad, false)})
	out = chWant(t, chDo(t, p2, "GET", "/panel/api/accounts/uid-t/tasks", ""), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.HasPrefix(got, "list tasks: ") {
		t.Errorf("error = %q", got)
	}
}

func TestPanelHandlersAccountTaskAccept(t *testing.T) {
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-t", false))
	stub := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/activity/growth/tasks/accept" {
			return 200, `{"code":0,"data":{}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false)})

	// 空 task_codes → 400（不空转上游）。
	for _, body := range []string{`{}`, `{"task_codes":[]}`, `{`, `{"task_codes":null}`} {
		out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-t/tasks/accept", body), http.StatusBadRequest)
		if got := chErrMsg(t, out); got != "task_codes required" {
			t.Errorf("body=%q error = %q", body, got)
		}
	}
	if n := stub.count("POST", "/v2/activity/growth/tasks/accept"); n != 0 {
		t.Fatalf("参数非法时不该打上游，命中 %d 次", n)
	}

	out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-t/tasks/accept", `{"task_codes":["chat_5","school_season"]}`), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("响应 = %+v", out)
	}
	reqs := stub.snapshot()
	if len(reqs) != 1 || string(reqs[0].Body) != `{"task_codes":["chat_5","school_season"]}` {
		t.Fatalf("上游收到 body = %q（数组顺序必须保持一致）", reqs[0].Body)
	}
	if got := reqs[0].Header.Get("X-Client-Platform"); got != "" {
		t.Errorf("普通 accept 不该带 mp 头，实际 %q", got)
	}

	// 上游拒绝 → 502。
	bad := chStubFn(func(chCaptured) (int, string) { return 400, `{"code":14001,"msg":"accept denied"}` })
	p2 := chPanel(t, Config{Pool: pl, Upstream: chUpstream(bad, false)})
	out = chWant(t, chDo(t, p2, "POST", "/panel/api/accounts/uid-t/tasks/accept", `{"task_codes":["chat_5"]}`), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.HasPrefix(got, "accept: ") {
		t.Errorf("error = %q", got)
	}
}

func TestPanelHandlersTaskAcceptAll(t *testing.T) {
	chSpeedUpGaps(t)
	chForceGlobal(t, true)

	// (1) 全部已接受 → accepted 0 + 说明文案，不发 accept 请求。
	doneTasks := `{"code":0,"data":{"tasks":[` +
		`{"task_code":"chat_5","target":5,"current":5,"accept_status":"accepted"},` +
		`{"task_code":"RichMeow_Chat","target":1,"current":1,"accept_status":"claimed"},` +
		`{"task_code":"Sequential_Tasks_4","target":1,"current":0,"accept_status":"not_accepted","locked":true}]}}`
	emptyMP := `{"code":0,"data":{"tasks":[]}}`
	stub := chGrowthStub(doneTasks, emptyMP)
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-a", false))
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false)})

	out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-a/tasks/accept_all", ""), http.StatusOK)
	if got := chNum(t, out["accepted"]); got != 0 {
		t.Errorf("accepted = %v, want 0（已接受/已领/locked 一律跳过）", got)
	}
	if got := chStr(t, out["message"]); got != "所有任务均已接受" {
		t.Errorf("message = %q", got)
	}
	if n := stub.count("POST", "/v2/activity/growth/tasks/accept"); n != 0 {
		t.Errorf("无待接受任务时不该打上游，命中 %d 次", n)
	}

	// (2) 25 个待接受 → 按 20 分片提交（两批：20 + 5），计数来自实际分片。
	var items []string
	for i := 0; i < 25; i++ {
		items = append(items, fmt.Sprintf(`{"task_code":"task_%02d","target":1,"current":0,"accept_status":"not_accepted"}`, i))
	}
	manyTasks := `{"code":0,"data":{"tasks":[` + strings.Join(items, ",") + `]}}`
	many := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/activity/growth/tasks":
			if c.Header.Get("X-Client-Platform") == "miniprogram" {
				return 200, emptyMP
			}
			return 200, manyTasks
		case "/v2/activity/growth/tasks/accept":
			return 200, `{"code":0,"data":{}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl2 := pool.New("")
	pl2.Add(chAcct(t, "uid-a", false))
	p2 := chPanel(t, Config{Pool: pl2, Upstream: chUpstream(many, false)})
	out = chWant(t, chDo(t, p2, "POST", "/panel/api/accounts/uid-a/tasks/accept_all", ""), http.StatusOK)
	if got := chNum(t, out["accepted"]); got != 25 {
		t.Errorf("accepted = %v, want 25", got)
	}
	if out["failed"] != nil {
		t.Errorf("failed = %v, want 空（无失败时不输出该列）", out["failed"])
	}
	if out["message"] != nil {
		t.Errorf("全部成功时不该有 message: %+v", out)
	}
	var batches [][]string
	for _, r := range many.snapshot() {
		if r.Path != "/v2/activity/growth/tasks/accept" {
			continue
		}
		var body struct {
			TaskCodes []string `json:"task_codes"`
		}
		if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
			t.Fatalf("上游 accept body 解析失败: %v (%s)", err, r.Body)
		}
		batches = append(batches, body.TaskCodes)
	}
	if len(batches) != 2 || len(batches[0]) != 20 || len(batches[1]) != 5 {
		t.Fatalf("accept 分片 = %d 批（%v），want [20, 5]（每批上限 20）", len(batches), batches)
	}
	if batches[0][0] != "task_00" || batches[1][0] != "task_20" {
		t.Errorf("分片边界 = %q / %q, want task_00 / task_20（顺序不得乱）", batches[0][0], batches[1][0])
	}

	// (3) mp 口径任务单独接受；两路都失败 → failed 汇聚 + 提示可重试。
	cnTasks := `{"code":0,"data":{"tasks":[{"task_code":"chat_5","target":5,"current":0,"accept_status":"not_accepted"}]}}`
	mpTasks := `{"code":0,"data":{"tasks":[{"task_code":"school_season","target":1,"current":0,"accept_status":"not_accepted"}]}}`
	fail := chStubFn(func(c chCaptured) (int, string) {
		if c.Path == "/v2/activity/growth/tasks" {
			if c.Header.Get("X-Client-Platform") == "miniprogram" {
				return 200, mpTasks
			}
			return 200, cnTasks
		}
		if c.Path == "/v2/activity/growth/tasks/accept" {
			return 400, `{"code":14001,"msg":"accept denied"}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl3 := pool.New("")
	pl3.Add(chAcct(t, "uid-a", false))
	p3 := chPanel(t, Config{Pool: pl3, Upstream: chUpstream(fail, false)})
	out = chWant(t, chDo(t, p3, "POST", "/panel/api/accounts/uid-a/tasks/accept_all", ""), http.StatusOK)
	if got := chNum(t, out["accepted"]); got != 0 {
		t.Errorf("accepted = %v, want 0", got)
	}
	failed := chStrings(t, out["failed"])
	if strings.Join(failed, ",") != "chat_5,school_season" {
		t.Errorf("failed = %v, want [chat_5 school_season]", failed)
	}
	if got := chStr(t, out["message"]); !strings.Contains(got, "部分任务接受失败") {
		t.Errorf("message = %q", got)
	}

	// 未知账号 → 404。
	out = chWant(t, chDo(t, p3, "POST", "/panel/api/accounts/nope/tasks/accept_all", ""), http.StatusNotFound)
	if chErrMsg(t, out) != "account not found" {
		t.Errorf("未知账号 error = %q", chErrMsg(t, out))
	}
}

func TestPanelHandlersAccountTaskClaim(t *testing.T) {
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-t", false))

	// 缺 task_code → 400。
	stub := chStubOK(`{"code":0,"data":{"credit":100,"energy":5}}`)
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, false)})
	for _, body := range []string{`{}`, `{"task_code":""}`, `{`} {
		out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-t/tasks/claim", body), http.StatusBadRequest)
		if got := chErrMsg(t, out); got != "task_code required" {
			t.Errorf("body=%q error = %q", body, got)
		}
	}
	if n := len(stub.snapshot()); n != 0 {
		t.Fatalf("参数非法时不该打上游，命中 %d 次", n)
	}

	// 普通任务：走 Web 域领奖端点，返回本次到账额度。
	out := chWant(t, chDo(t, p, "POST", "/panel/api/accounts/uid-t/tasks/claim", `{"task_code":"chat_5"}`), http.StatusOK)
	if got := chNum(t, out["credit"]); got != 100 {
		t.Errorf("credit = %v, want 100", got)
	}
	if got := chNum(t, out["energy"]); got != 5 {
		t.Errorf("energy = %v, want 5", got)
	}
	reqs := stub.snapshot()
	if len(reqs) != 1 || reqs[0].Path != "/activity/growth/tasks/chat_5/claim" {
		t.Fatalf("领奖出站 = %+v, want 单次 POST /activity/growth/tasks/chat_5/claim", reqs)
	}
	if got := reqs[0].Header.Get("x-client-platform"); got != "web" {
		t.Errorf("Web 领奖应带 x-client-platform: web，实际 %q", got)
	}

	// 已领过（credit/energy 全 0）→ already_claimed 语义，不是失败。
	already := chStubOK(`{"code":0,"data":{"already_claimed":true,"credit":0,"energy":0}}`)
	p2 := chPanel(t, Config{Pool: pl, Upstream: chUpstream(already, false)})
	out = chWant(t, chDo(t, p2, "POST", "/panel/api/accounts/uid-t/tasks/claim", `{"task_code":"chat_5"}`), http.StatusOK)
	if out["already_claimed"] != true || out["ok"] != true {
		t.Fatalf("重复领取响应 = %+v", out)
	}
	if got := chStr(t, out["message"]); got != "该奖励此前已领取" {
		t.Errorf("message = %q", got)
	}

	// mp 专属任务：先打 chat 域 mp 领奖，400 时降级 Web 域。
	mp := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/activity/growth/tasks/school_season/claim":
			if c.Header.Get("X-Client-Platform") == "miniprogram" {
				return 400, `{"code":14001,"msg":"task not found"}`
			}
			return 200, `{"code":0,"data":{"credit":200,"energy":5}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	p3 := chPanel(t, Config{Pool: pl, Upstream: chUpstream(mp, false)})
	out = chWant(t, chDo(t, p3, "POST", "/panel/api/accounts/uid-t/tasks/claim", `{"task_code":"school_season"}`), http.StatusOK)
	if got := chNum(t, out["credit"]); got != 200 {
		t.Errorf("mp 领奖 credit = %v, want 200（chat 域 400 后应降级 Web 域）", got)
	}
	var sawMP, sawWeb bool
	for _, r := range mp.snapshot() {
		if r.Path != "/activity/growth/tasks/school_season/claim" {
			continue
		}
		if r.Header.Get("X-Client-Platform") == "miniprogram" {
			sawMP = true
		} else {
			sawWeb = true
		}
	}
	if !sawMP || !sawWeb {
		t.Errorf("mp 任务应两段尝试（mp 域 → web 域），实际 %+v", mp.paths())
	}

	// 上游始终拒绝 → 502。
	deny := chStubFn(func(chCaptured) (int, string) { return 400, `{"code":14001,"msg":"task not completed"}` })
	p4 := chPanel(t, Config{Pool: pl, Upstream: chUpstream(deny, false)})
	out = chWant(t, chDo(t, p4, "POST", "/panel/api/accounts/uid-t/tasks/claim", `{"task_code":"chat_5"}`), http.StatusBadGateway)
	if got := chErrMsg(t, out); !strings.HasPrefix(got, "claim: ") {
		t.Errorf("error = %q", got)
	}

	// 未知账号 → 404。
	out = chWant(t, chDo(t, p3, "POST", "/panel/api/accounts/nope/tasks/claim", `{"task_code":"chat_5"}`), http.StatusNotFound)
	if chErrMsg(t, out) != "account not found" {
		t.Errorf("未知账号 error = %q", chErrMsg(t, out))
	}
}

// ---------------------------------------------------------------------------
// taskcenter.go：扫描 / 队列状态机
// ---------------------------------------------------------------------------

func TestPanelHandlersTasksScanAll(t *testing.T) {
	chForceGlobal(t, true)
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-cn", false))
	ent := chAcct(t, "uid-ent", false)
	ent.EnterpriseID = "ent-1"
	pl.Add(ent)
	pl.Add(chAcct(t, "uid-gl", true))
	pl.Add(chAcct(t, "uid-dis", false))
	pl.Disable("uid-dis", "manual")

	cnTasks := `{"code":0,"data":{"tasks":[` +
		`{"task_code":"chat_5","title":"完成 5 次对话","target":5,"current":1,"accept_status":"accepted"},` +
		`{"task_code":"RichMeow_Chat","target":1,"current":1,"accept_status":"claimed"},` +
		`{"task_code":"template_5","target":5,"current":5,"accept_status":"accepted"},` +
		`{"task_code":"Sequential_Tasks_4","target":1,"current":0,"accept_status":"not_accepted","locked":true},` +
		`{"task_code":"no_such_action","target":1,"current":0,"accept_status":"not_accepted"}]}}`
	cnMP := `{"code":0,"data":{"tasks":[` +
		`{"task_code":"chat_5","target":5,"current":1,"accept_status":"accepted"},` +
		`{"task_code":"school_season","title":"校园日","target":1,"current":0,"accept_status":"not_accepted"}]}}`
	stub := chGrowthStub(cnTasks, cnMP)
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, true)})

	out := chWant(t, chDo(t, p, "POST", "/panel/api/tasks/scan_all", ""), http.StatusOK)
	if out["ok"] != true {
		t.Fatalf("响应 = %+v", out)
	}
	items := chRows(t, out["accounts"])
	byUID := map[string]map[string]any{}
	for _, it := range items {
		byUID[chStr(t, it["uid"])] = it
	}
	cn := byUID["uid-cn"]
	if cn == nil {
		t.Fatalf("缺 uid-cn: %+v", byUID)
	}
	codes := []string{}
	for _, tk := range chRows(t, cn["growth"]) {
		codes = append(codes, chStr(t, tk["task_code"]))
	}
	// 待办筛选：claimed / locked / 无自动动作 都排除；mp 表按 code 去重后并入。
	if strings.Join(codes, ",") != "chat_5,template_5,school_season" {
		t.Errorf("uid-cn 待办 = %v, want [chat_5 template_5 school_season]", codes)
	}
	if got := chNum(t, out["pending_count"]); got != 3 {
		t.Errorf("pending_count = %v, want 3", got)
	}
	// 企业版 / global 域零上游调用（否则每次扫描都白打上游）。
	if errStr, _ := byUID["uid-ent"]["growth_error"].(string); errStr != "" {
		t.Errorf("企业版不该发起上游调用: %q", errStr)
	}
	if byUID["uid-ent"]["growth"] != nil {
		t.Errorf("企业版不该有待办任务: %+v", byUID["uid-ent"]["growth"])
	}
	if byUID["uid-gl"]["growth"] != nil {
		t.Errorf("global 号不该有待办任务: %+v", byUID["uid-gl"]["growth"])
	}
	if n := stub.count("GET", "/v2/activity/growth/tasks"); n != 2 {
		t.Errorf("growth 列表命中 %d 次, want 2（仅 uid-cn 的默认 + mp 两口径）", n)
	}
	if _, ok := byUID["uid-dis"]; ok {
		t.Error("禁用账号不该出现在扫描结果里")
	}
}

func TestPanelHandlersTasksQueueLifecycle(t *testing.T) {
	chSpeedUpGaps(t)
	chForceGlobal(t, true)

	// 队列空态：items 必须是 []（不是 null），running/seq/conc 都要如实为 0。
	bare := chPanel(t, Config{Pool: pool.New(""), Upstream: chUpstream(chStubOK(""), false)})
	rec := chDo(t, bare, "GET", "/panel/api/tasks/queue", "")
	out := chWant(t, rec, http.StatusOK)
	if out["running"] != false || chNum(t, out["total"]) != 0 || chNum(t, out["seq"]) != 0 || out["started"] != false {
		t.Errorf("空队列状态 = %+v", out)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("空队列 items 应为 []，body=%s", rec.Body.String())
	}

	// 执行链路：1 个账号 1 项 growth 任务，动作是 automation_1（仅一次 report 上报）；
	// 回读第二次起变为达标 → 自动领奖。
	var listCalls int
	var mu sync.Mutex
	stub := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/activity/growth/tasks":
			if c.Header.Get("X-Client-Platform") == "miniprogram" {
				return 200, `{"code":0,"data":{"tasks":[]}}`
			}
			mu.Lock()
			listCalls++
			n := listCalls
			mu.Unlock()
			cur, status := 0, "accepted"
			if n >= 2 {
				cur = 1
			}
			return 200, fmt.Sprintf(`{"code":0,"data":{"tasks":[{"task_code":"automation_1","title":"定时任务",`+
				`"target":1,"current":%d,"accept_status":%q}]}}`, cur, status)
		case "/v2/report":
			return 200, `{"code":0,"data":{}}`
		case "/activity/growth/tasks/automation_1/claim":
			return 200, `{"code":0,"data":{"credit":100,"energy":5}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl := pool.New("")
	pl.Add(chAcct(t, "uid-q", false))
	p := chPanel(t, Config{Pool: pl, Upstream: chUpstream(stub, true)})

	// 已在执行 → 409（重复点击不得并跑两遍）。
	p.queue().mu.Lock()
	p.queue().running = true
	p.queue().mu.Unlock()
	out = chWant(t, chDo(t, p, "POST", "/panel/api/tasks/run_queue", `{"concurrency":9,"growth":true}`), http.StatusConflict)
	if got := chErrMsg(t, out); !strings.Contains(got, "队列正在执行中") {
		t.Errorf("409 error = %q", got)
	}
	p.queue().mu.Lock()
	p.queue().running = false
	p.queue().mu.Unlock()

	out = chWant(t, chDo(t, p, "POST", "/panel/api/tasks/run_queue", `{"concurrency":9,"growth":true}`), http.StatusOK)
	if out["ok"] != true || out["started"] != true {
		t.Fatalf("run_queue 响应 = %+v", out)
	}
	if got := chNum(t, out["total"]); got != 1 {
		t.Errorf("total = %v, want 1", got)
	}
	if got := chNum(t, out["seq"]); got != 1 {
		t.Errorf("seq = %v, want 1", got)
	}

	// 等待执行结束（桩即时返回；上限 5s 兜底，避免挂死）。
	deadline := time.Now().Add(5 * time.Second)
	var st map[string]any
	for {
		st = chWant(t, chDo(t, p, "GET", "/panel/api/tasks/queue", ""), http.StatusOK)
		if st["running"] == false {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("队列未在 5s 内结束: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
	items := chRows(t, st["items"])
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1 项", items)
	}
	if got := chStr(t, items[0]["status"]); got != "done" {
		t.Errorf("条目状态 = %q, want done（message=%v）", got, items[0]["message"])
	}
	if got := chStr(t, items[0]["uid"]); got != "uid-q" {
		t.Errorf("条目 uid = %q", got)
	}
	if got := chStr(t, items[0]["code"]); got != "automation_1" {
		t.Errorf("条目 code = %q", got)
	}
	if msg := chStr(t, items[0]["message"]); !strings.Contains(msg, "自动领奖 +100 分 +5 能") {
		t.Errorf("条目 message = %q, want 含自动领奖结果", msg)
	}
	if got := chNum(t, st["conc"]); got != 4 {
		t.Errorf("conc = %v, want 4（入参 9 应被夹到上限 4）", got)
	}

	// 无可执行待办：全部任务已 claimed → started=false + 说明（不是 200 假成功）。
	claimed := chGrowthStub(`{"code":0,"data":{"tasks":[{"task_code":"automation_1","target":1,"current":1,"accept_status":"claimed"}]}}`,
		`{"code":0,"data":{"tasks":[]}}`)
	pl2 := pool.New("")
	pl2.Add(chAcct(t, "uid-q2", false))
	p2 := chPanel(t, Config{Pool: pl2, Upstream: chUpstream(claimed, true)})
	out = chWant(t, chDo(t, p2, "POST", "/panel/api/tasks/run_queue", `{}`), http.StatusOK)
	if out["ok"] != true || out["started"] != false {
		t.Fatalf("无待办响应 = %+v", out)
	}
	if got := chStr(t, out["message"]); got != "全部账号没有待办任务" {
		t.Errorf("message = %q", got)
	}
	if q := p2.queue(); q.running {
		t.Error("无待办时 running 必须回滚为 false（否则后续永远 409）")
	}

	// 调度器时点回调同管线：无待办时安全跳过（不 panic、不启动队列）。
	p2.RunGrowthQueueOnce()
	if q := p2.queue(); q.running {
		t.Error("定时回调无可执行待办时不该留下 running=true")
	}

	// 账号锁被占用（单任务动作正在跑）→ 队列条目标 skipped（不并发跑两遍）。
	lockStub := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/activity/growth/tasks":
			if c.Header.Get("X-Client-Platform") == "miniprogram" {
				return 200, `{"code":0,"data":{"tasks":[]}}`
			}
			return 200, `{"code":0,"data":{"tasks":[{"task_code":"automation_1","target":1,"current":0,"accept_status":"accepted"}]}}`
		case "/v2/report":
			return 200, `{"code":0,"data":{}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl3 := pool.New("")
	pl3.Add(chAcct(t, "uid-lock", false))
	p3 := chPanel(t, Config{Pool: pl3, Upstream: chUpstream(lockStub, true)})
	if !p3.tryLockAccount("uid-lock") {
		t.Fatal("预置账号任务锁失败")
	}
	t.Cleanup(func() { p3.unlockAccount("uid-lock") })

	out = chWant(t, chDo(t, p3, "POST", "/panel/api/tasks/run_queue", `{}`), http.StatusOK)
	if out["started"] != true {
		t.Fatalf("run_queue 响应 = %+v", out)
	}
	st = chWaitQueueIdle(t, p3)
	items = chRows(t, st["items"])
	if len(items) != 1 || chStr(t, items[0]["status"]) != "skipped" {
		t.Fatalf("锁冲突时条目应 skipped: %+v", items)
	}
	if msg := chStr(t, items[0]["message"]); !strings.Contains(msg, "该账号有其它任务动作在执行") {
		t.Errorf("skipped 原因 = %q", msg)
	}
	if n := lockStub.count("POST", "/v2/report"); n != 0 {
		t.Errorf("跳过时不该执行任务动作，report 命中 %d 次", n)
	}

	// 队列前置 accept：not_accepted 任务先报名（成功路径 + reportGap 节流）。
	acceptStub := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/activity/growth/tasks":
			if c.Header.Get("X-Client-Platform") == "miniprogram" {
				return 200, `{"code":0,"data":{"tasks":[]}}`
			}
			return 200, `{"code":0,"data":{"tasks":[{"task_code":"automation_1","target":1,"current":1,"accept_status":"not_accepted"}]}}`
		case "/v2/activity/growth/tasks/accept":
			return 200, `{"code":0,"data":{}}`
		case "/v2/report":
			return 200, `{"code":0,"data":{}}`
		case "/activity/growth/tasks/automation_1/claim":
			return 200, `{"code":0,"data":{"credit":100,"energy":5}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl4 := pool.New("")
	pl4.Add(chAcct(t, "uid-ac", false))
	p4 := chPanel(t, Config{Pool: pl4, Upstream: chUpstream(acceptStub, true)})
	out = chWant(t, chDo(t, p4, "POST", "/panel/api/tasks/run_queue", `{"concurrency":1}`), http.StatusOK)
	if out["started"] != true {
		t.Fatalf("run_queue 响应 = %+v", out)
	}
	st = chWaitQueueIdle(t, p4)
	items = chRows(t, st["items"])
	if len(items) != 1 || chStr(t, items[0]["status"]) != "done" {
		t.Fatalf("accept 后应正常执行: %+v", items)
	}
	if n := acceptStub.count("POST", "/v2/activity/growth/tasks/accept"); n != 1 {
		t.Errorf("队列前置 accept 命中 %d 次, want 1", n)
	}
	if got := chNum(t, st["conc"]); got != 1 {
		t.Errorf("conc = %v, want 1（下限夹取值）", got)
	}

	// accept 失败不阻塞任务执行（行为事件才是进度判据）。
	acceptFail := chStubFn(func(c chCaptured) (int, string) {
		switch c.Path {
		case "/v2/activity/growth/tasks":
			if c.Header.Get("X-Client-Platform") == "miniprogram" {
				return 200, `{"code":0,"data":{"tasks":[]}}`
			}
			return 200, `{"code":0,"data":{"tasks":[{"task_code":"automation_1","target":1,"current":1,"accept_status":"not_accepted"}]}}`
		case "/v2/activity/growth/tasks/accept":
			return 400, `{"code":14001,"msg":"accept denied"}`
		case "/v2/report":
			return 200, `{"code":0,"data":{}}`
		case "/activity/growth/tasks/automation_1/claim":
			return 200, `{"code":0,"data":{"credit":100,"energy":5}}`
		}
		return 404, `{"code":404,"msg":"nf"}`
	})
	pl5 := pool.New("")
	pl5.Add(chAcct(t, "uid-af", false))
	p5 := chPanel(t, Config{Pool: pl5, Upstream: chUpstream(acceptFail, true)})
	if out = chWant(t, chDo(t, p5, "POST", "/panel/api/tasks/run_queue", `{}`), http.StatusOK); out["started"] != true {
		t.Fatalf("run_queue 响应 = %+v", out)
	}
	st = chWaitQueueIdle(t, p5)
	items = chRows(t, st["items"])
	if len(items) != 1 || chStr(t, items[0]["status"]) != "done" {
		t.Fatalf("accept 失败不该阻塞执行: %+v", items)
	}
	if n := acceptFail.count("POST", "/v2/activity/growth/tasks/accept"); n != 1 {
		t.Errorf("应尝试过 accept，命中 %d 次", n)
	}
}

// chWaitQueueIdle 轮询队列状态直到 running=false（桩即时返回；5s 兜底防挂死）。
func chWaitQueueIdle(t *testing.T, p *Panel) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := chWant(t, chDo(t, p, "GET", "/panel/api/tasks/queue", ""), http.StatusOK)
		if st["running"] == false {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("队列未在 5s 内结束: %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
