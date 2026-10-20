package upstream

// coverage_sequences_test.go 上游 HTTP 序列族补强（纯增量，不改生产代码）。
//
// 覆盖面：desktop / tasks / streak / school / global_register / global_models 探测 /
// blackcat HTTP 方法 / trial。风格与既有测试一致：复用 testClient / jsonResp / rtFunc /
// shortBillingRetry，全部走注入 transport（零真实网络）。
// 所有本文件定义的 helper 带前缀 upstreamhttp（避免与同包并行 agent 撞名）。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// ---------------------------------------------------------------------------
// 脚手架
// ---------------------------------------------------------------------------

// upstreamhttpUUIDRe clientToken 形状（前端 randomUUID 同款 8-4-4-4-12 hex）。
var upstreamhttpUUIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// upstreamhttpReq 被 stub 截获的一次出站请求快照（body 已预先读走）。
type upstreamhttpReq struct {
	Method   string
	Host     string
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// upstreamhttpRec 请求记录器（probeGlobalModels 并发四路，故加锁）。
type upstreamhttpRec struct {
	mu    sync.Mutex
	calls []upstreamhttpReq
}

func (r *upstreamhttpRec) add(q upstreamhttpReq) {
	r.mu.Lock()
	r.calls = append(r.calls, q)
	r.mu.Unlock()
}

func (r *upstreamhttpRec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *upstreamhttpRec) snapshot() []upstreamhttpReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]upstreamhttpReq, len(r.calls))
	copy(out, r.calls)
	return out
}

// one 断言恰好有一条命中 path 的请求并返回它（0 条或多条直接 Fatal）。
func (r *upstreamhttpRec) one(t *testing.T, path string) upstreamhttpReq {
	t.Helper()
	var hits []upstreamhttpReq
	for _, c := range r.snapshot() {
		if c.Path == path {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("path %q 命中 %d 次，want 1；实际全部请求=%v", path, len(hits), r.paths())
	}
	return hits[0]
}

func (r *upstreamhttpRec) paths() []string {
	var out []string
	for _, c := range r.snapshot() {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

// pathSet 只取 path（不含 method），供 containsJoin 这类包含判断使用。
func (r *upstreamhttpRec) pathSet() []string {
	var out []string
	for _, c := range r.snapshot() {
		out = append(out, c.Path)
	}
	return out
}

// upstreamhttpStub 把 respond 包成 rtFunc 并记录请求（body 读走后交回 respond 断言）。
func upstreamhttpStub(rec *upstreamhttpRec, respond func(upstreamhttpReq) (*http.Response, error)) rtFunc {
	return func(req *http.Request) (*http.Response, error) {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
			_ = req.Body.Close()
		}
		q := upstreamhttpReq{
			Method: req.Method, Host: req.URL.Host, Path: req.URL.Path,
			RawQuery: req.URL.RawQuery, Header: req.Header.Clone(), Body: body,
		}
		rec.add(q)
		return respond(q)
	}
}

// upstreamhttpFixed 单一固定响应的 stub（忽略请求内容）。
func upstreamhttpFixed(rec *upstreamhttpRec, status int, body string) rtFunc {
	return upstreamhttpStub(rec, func(upstreamhttpReq) (*http.Response, error) {
		return jsonResp(status, body), nil
	})
}

// upstreamhttpChunkedBody 每次 Read 吐一个 chunk 的 body（模拟真实 SSE 分片到达：
// 逐块扫描才还原流式行为，单块整包与真实链路不同）。
type upstreamhttpChunkedBody struct {
	chunks []string
	i      int
}

func (b *upstreamhttpChunkedBody) Read(p []byte) (int, error) {
	if b.i >= len(b.chunks) {
		return 0, io.EOF
	}
	n := copy(p, b.chunks[b.i])
	b.i++
	return n, nil
}

func (b *upstreamhttpChunkedBody) Close() error { return nil }

// upstreamhttpStreamResp 200 + 分片 body 的响应。
func upstreamhttpStreamResp(chunks ...string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &upstreamhttpChunkedBody{chunks: chunks},
	}
}

// upstreamhttpStreamStub 分片流式响应的 stub。
func upstreamhttpStreamStub(rec *upstreamhttpRec, chunks ...string) rtFunc {
	return upstreamhttpStub(rec, func(upstreamhttpReq) (*http.Response, error) {
		return upstreamhttpStreamResp(chunks...), nil
	})
}

// upstreamhttpErrBody 读取即失败的 body（模拟连接中断/截断）。
type upstreamhttpErrBody struct{}

func (upstreamhttpErrBody) Read([]byte) (int, error) {
	return 0, errors.New("unexpected EOF")
}

func (upstreamhttpErrBody) Close() error { return nil }

// upstreamhttpSameSet 无序比较（global 探测并集顺序不可依赖：见测试注释）。
func upstreamhttpSameSet(a, b []string) bool {
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	return upstreamhttpStringsEqual(as, bs)
}

// upstreamhttpEnvelope 拼 {code:0,msg:ok,data:<data>} 信封（data 为空则 null）。
func upstreamhttpEnvelope(data string) string {
	if strings.TrimSpace(data) == "" {
		data = "null"
	}
	return `{"code":0,"msg":"ok","data":` + data + `}`
}

func upstreamhttpObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("body 不是 JSON 对象: %v (body=%s)", err, raw)
	}
	return m
}

func upstreamhttpArray(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var a []map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("body 不是 JSON 对象数组: %v (body=%s)", err, raw)
	}
	return a
}

func upstreamhttpCNAuth() *auth.Auth {
	return &auth.Auth{AccessToken: "at-cn", UID: "u-cn", Nickname: "cn-user", Domain: "copilot.tencent.com"}
}

func upstreamhttpGlobalAuth() *auth.Auth {
	a := &auth.Auth{AccessToken: "at-global", UID: "u-global", Nickname: "global-user", Domain: "www.workbuddy.ai"}
	a.BackfillRealm()
	return a
}

// upstreamhttpErrStatus 取 *Error 的 HTTP 状态码（非 *Error 返回 -1）。
func upstreamhttpErrStatus(err error) int {
	var ue *Error
	if errors.As(err, &ue) {
		return ue.Status
	}
	return -1
}

// upstreamhttpStringsEqual 顺序敏感的字符串切片比较。
func upstreamhttpStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// desktop.go
// ---------------------------------------------------------------------------

// TestUpstreamHTTPDesktopBuddyAppSequence 五连「进入 Buddy 应用」事件：事件码顺序、
// 公共 buddy 字段注入、mode=LOCAL。
func TestUpstreamHTTPDesktopBuddyAppSequence(t *testing.T) {
	events := DesktopBuddyAppSequence("cb_y5Dy46tPQGGWtueMxXbe", "企鹅教师助手")
	want := []string{
		"buddyapp_discover_click", "buddyapp_show", "buddyapp_enter_click",
		"buddyapp_auth_confirm_click", "buddyapp_bindaccount_skip_click",
	}
	if len(events) != len(want) {
		t.Fatalf("events=%d want %d", len(events), len(want))
	}
	for i, code := range want {
		if events[i]["eventCode"] != code {
			t.Errorf("events[%d].eventCode=%v want %v", i, events[i]["eventCode"], code)
		}
		if events[i]["buddyId"] != "cb_y5Dy46tPQGGWtueMxXbe" || events[i]["buddyName"] != "企鹅教师助手" {
			t.Errorf("events[%d] 缺 buddy 公共字段: %v", i, events[i])
		}
		if events[i]["mode"] != "LOCAL" {
			t.Errorf("events[%d].mode=%v want LOCAL", i, events[i]["mode"])
		}
	}
	// 第一步（discover）无 extra 载荷：只能有公共 4 键。
	if len(events[0]) != 4 {
		t.Errorf("events[0]=%v want 仅 eventCode/mode/buddyId/buddyName 共 4 键，实为 %d 键", events[0], len(events[0]))
	}
	// enter_click 带 isFirstPage="1"（字符串）。
	if events[2]["isFirstPage"] != "1" {
		t.Errorf("enter_click.isFirstPage=%v want \"1\"", events[2]["isFirstPage"])
	}
}

// TestUpstreamHTTPDesktopAutomationCreateEvent 定时任务创建事件：点亮 automation_1 的
// 关键字段（once 调度 + 思考模型 + 手工来源）。
func TestUpstreamHTTPDesktopAutomationCreateEvent(t *testing.T) {
	ev := DesktopAutomationCreateEvent("每日签到")
	if ev["eventCode"] != "automated_task_create_suc" {
		t.Errorf("eventCode=%v want automated_task_create_suc", ev["eventCode"])
	}
	if ev["name"] != "每日签到" {
		t.Errorf("name=%v want 每日签到", ev["name"])
	}
	if ev["source"] != "manually" || ev["scheduleType"] != "once" || ev["mode"] != "LOCAL" {
		t.Errorf("source/scheduleType/mode=%v/%v/%v want manually/once/LOCAL", ev["source"], ev["scheduleType"], ev["mode"])
	}
	if ev["modelIsThinking"] != true || ev["modelId"] != "fast-model" {
		t.Errorf("model 字段=%v/%v want true/fast-model", ev["modelIsThinking"], ev["modelId"])
	}
	if len(ev) != 10 {
		t.Errorf("载荷键数=%d want 10（新增/丢失字段都要被看见）: %v", len(ev), ev)
	}
}

// TestUpstreamHTTPDesktopTemplateUseSequence 模板任务组 = chat 链(6) + 模板 2 事件。
func TestUpstreamHTTPDesktopTemplateUseSequence(t *testing.T) {
	events := DesktopTemplateUseSequence("conv-t", "req-t", "tpl-9", "周报模板")
	if len(events) != 8 {
		t.Fatalf("events=%d want 8（6 chat + 2 template）", len(events))
	}
	if events[0]["eventCode"] != "agent_task_created" {
		t.Errorf("events[0]=%v want chat 链在前的 agent_task_created", events[0]["eventCode"])
	}
	created := events[6]
	if created["eventCode"] != "agent_task_created_with_template" {
		t.Errorf("events[6].eventCode=%v want agent_task_created_with_template", created["eventCode"])
	}
	if created["id"] != "tpl-9" || created["name"] != "周报模板" || created["requestId"] != "req-t" || created["mode"] != "working" {
		t.Errorf("template 创建事件载荷=%v", created)
	}
	used := events[7]
	if used["eventCode"] != "template_used" || used["template_id"] != "tpl-9" || used["task_mode"] != "working" {
		t.Errorf("template_used 载荷=%v", used)
	}
	// 模板 sequence 的 chat 链 messageId 约定为 msg-<templateID>。
	if events[0]["messageId"] != "msg-tpl-9" {
		t.Errorf("chat 链 messageId=%v want msg-tpl-9", events[0]["messageId"])
	}
}

// TestUpstreamHTTPDesktopPlaybookPromptSequence 灵感同款组 = chat 链(6) + 3 个 playbook 事件。
func TestUpstreamHTTPDesktopPlaybookPromptSequence(t *testing.T) {
	events := DesktopPlaybookPromptSequence("conv-p", "req-p", "case-7", "案例名")
	if len(events) != 9 {
		t.Fatalf("events=%d want 9（6 chat + 3 playbook）", len(events))
	}
	click := events[6]
	if click["eventCode"] != "web_element_click" || click["elementId"] != "playbook_ctaClick" ||
		click["pageName"] != "playbook_detail" || click["elementName"] != "案例名" {
		t.Errorf("web_element_click 载荷=%v", click)
	}
	cta := events[7]
	if cta["eventCode"] != "playbook_cta_click" || cta["id"] != "case-7" || cta["type"] != "document" || cta["position"] != 0 {
		t.Errorf("playbook_cta_click 载荷=%v", cta)
	}
	send := events[8]
	if send["eventCode"] != "playbook_prompt_send" || send["conversationId"] != "conv-p" || send["requestId"] != "req-p" {
		t.Errorf("playbook_prompt_send 载荷=%v（JOIN 字段缺失则服务端不对账）", send)
	}
}

// TestUpstreamHTTPDesktopDesignCanvasSequence 设计画布组：canvas open 的 id 由 requestID
// 末 8 位派生（末段错位会让上游判据对不上）。
func TestUpstreamHTTPDesktopDesignCanvasSequence(t *testing.T) {
	events := DesktopDesignCanvasSequence("conv-c", "0123456789abcdef")
	if len(events) != 8 {
		t.Fatalf("events=%d want 8", len(events))
	}
	create := events[6]
	if create["eventCode"] != "wbx_design_canvas_task_create" || create["cost"] != 12000 ||
		create["source"] != "summon_keyword" || create["isSuccessful"] != true {
		t.Errorf("canvas_task_create 载荷=%v", create)
	}
	open := events[7]
	if open["eventCode"] != "wbx_design_canvas_open" {
		t.Errorf("events[7].eventCode=%v", open["eventCode"])
	}
	if open["id"] != "ardot-file-89abcdef" {
		t.Errorf("canvas_open.id=%v want ardot-file-89abcdef（requestID 末 8 位）", open["id"])
	}
	if open["cost"] != 13000 || open["type"] != "page" {
		t.Errorf("canvas_open cost/type=%v/%v want 13000/page", open["cost"], open["type"])
	}
}

// TestUpstreamHTTPReportWebEvent Web 域单事件上报：走 webBase + /v2/report、浏览器指纹头、
// body 为单元素数组、machineId 与桌面域派生盐不同。
func TestUpstreamHTTPReportWebEvent(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, `{"code":0,"msg":"OK"}`))
	c.WebBaseCN = "https://web.example"
	a := &auth.Auth{AccessToken: "at-w", UID: "u-web", Nickname: "webber", EnterpriseID: "ent-9"}

	if err := c.ReportWebEvent(a, "library_doc_intro_click", "https://www.workbuddy.cn/library/doc/1", "doc-1", "文档入门"); err != nil {
		t.Fatalf("ReportWebEvent: %v", err)
	}
	got := rec.one(t, "/v2/report")
	if got.Host != "web.example" {
		t.Errorf("host=%s want web.example（Web 事件必须走 webBase，不是 chatBase）", got.Host)
	}
	if got.Method != http.MethodPost {
		t.Errorf("method=%s want POST", got.Method)
	}
	if got.Header.Get("x-client-platform") != "web" {
		t.Errorf("x-client-platform=%q want web", got.Header.Get("x-client-platform"))
	}
	if got.Header.Get("Origin") != "https://web.example" {
		t.Errorf("Origin=%q want https://web.example", got.Header.Get("Origin"))
	}
	if got.Header.Get("Referer") != "https://www.workbuddy.cn/library/doc/1" {
		t.Errorf("Referer=%q want 页面 URL", got.Header.Get("Referer"))
	}
	if !strings.HasPrefix(got.Header.Get("User-Agent"), "Mozilla/5.0 (Windows NT 10.0; Win64; x64)") {
		t.Errorf("UA=%q want 浏览器形状", got.Header.Get("User-Agent"))
	}
	if got.Header.Get("Authorization") != "Bearer at-w" || got.Header.Get("X-User-Id") != "u-web" {
		t.Errorf("鉴权头=%q/%q", got.Header.Get("Authorization"), got.Header.Get("X-User-Id"))
	}
	arr := upstreamhttpArray(t, got.Body)
	if len(arr) != 1 {
		t.Fatalf("body 元素数=%d want 1", len(arr))
	}
	ev := arr[0]
	if ev["eventCode"] != "library_doc_intro_click" || ev["pageURL"] != "https://www.workbuddy.cn/library/doc/1" {
		t.Errorf("事件字段=%v", ev)
	}
	if ev["elementId"] != "doc-1" || ev["elementName"] != "文档入门" {
		t.Errorf("element 字段=%v/%v", ev["elementId"], ev["elementName"])
	}
	if ev["os"] != "Win32" || ev["userId"] != "u-web" || ev["userNickname"] != "webber" || ev["enterpriseId"] != "ent-9" {
		t.Errorf("指纹字段=%v", ev)
	}
	if want := deriveID(a, "webmachine"); ev["machineId"] != want {
		t.Errorf("machineId=%v want %v（web 盐）", ev["machineId"], want)
	}
	if ev["machineId"] == deriveID(a, "machine") {
		t.Error("web 事件误用了桌面 machineId 盐")
	}

	// 上游 500 → 错误透出（页面行为任务失败必须可观测）。
	recErr := &upstreamhttpRec{}
	cErr := testClient(upstreamhttpFixed(recErr, 500, `{"code":10000,"msg":"boom"}`))
	cErr.WebBaseCN = "https://web.example"
	if err := cErr.ReportWebEvent(a, "library_doc_intro_click", "https://www.workbuddy.cn/x", "e", "n"); err == nil {
		t.Error("500 want err")
	} else if upstreamhttpErrStatus(err) != 500 {
		t.Errorf("err status=%d want 500", upstreamhttpErrStatus(err))
	}
}

// TestUpstreamHTTPMarketExpertList 专家市场列表：POST chatBase + 分页/排序体 + expert_type
// 条件字段 + 响应字段解析（编造 id 不计数，故字段必须原样透出）。
func TestUpstreamHTTPMarketExpertList(t *testing.T) {
	rec := &upstreamhttpRec{}
	body := `{"code":0,"data":{"experts":[` +
		`{"expert_id":"ex_Abc123","expert_type":"team","display_name_zh":"开学季专家团","profession_zh":"教育","version":"2.1.0","categories":["edu","k12"]},` +
		`{"expert_id":"ex_Def456","expert_type":"agent","display_name_zh":"轻量云专家","profession_zh":"云","version":"","categories":[]}]}}`
	c := testClient(upstreamhttpFixed(rec, 200, body))
	a := upstreamhttpCNAuth()

	got, err := c.MarketExpertList(a, "team")
	if err != nil {
		t.Fatalf("MarketExpertList: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("experts=%d want 2", len(got))
	}
	if got[0].ExpertID != "ex_Abc123" || got[0].ExpertType != "team" || got[0].DisplayNameZH != "开学季专家团" ||
		got[0].ProfessionZH != "教育" || got[0].Version != "2.1.0" || len(got[0].Categories) != 2 {
		t.Errorf("experts[0]=%+v", got[0])
	}
	if got[1].ExpertID != "ex_Def456" || got[1].Version != "" {
		t.Errorf("experts[1]=%+v", got[1])
	}
	req := rec.one(t, "/portal/operation-platform/market/expert/list")
	if req.Host != "chat.example" {
		t.Errorf("host=%s want chat.example", req.Host)
	}
	if req.Header.Get("Authorization") != "Bearer at-cn" || req.Header.Get("X-User-Id") != "u-cn" ||
		req.Header.Get("X-Product") != "SaaS" || req.Header.Get("X-Domain") != "https://chat.example" {
		t.Errorf("头=%v", req.Header)
	}
	gotBody := upstreamhttpObject(t, req.Body)
	want := map[string]any{"page": float64(1), "page_size": float64(20), "sort_by": "reco_rank", "sort_order": "desc", "expert_type": "team"}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("body[%s]=%v want %v", k, gotBody[k], v)
		}
	}
	if len(gotBody) != len(want) {
		t.Errorf("body 键数=%d want %d (%v)", len(gotBody), len(want), gotBody)
	}

	// expertType 为空：不得下发 expert_type 键（否则上游按类型过滤，拿不到全量）。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, `{"code":0,"data":{"experts":[]}}`))
	if _, err := c2.MarketExpertList(a, ""); err != nil {
		t.Fatalf("MarketExpertList(empty type): %v", err)
	}
	if _, ok := upstreamhttpObject(t, rec2.one(t, "/portal/operation-platform/market/expert/list").Body)["expert_type"]; ok {
		t.Error("expertType 为空时不应下发 expert_type")
	}
}

// TestUpstreamHTTPDesktopReportErrorPaths 桌面上报/主题设置的错误路径：零事件本地报错
// （不打上游）、上游 500 必须作为错误返回。
func TestUpstreamHTTPDesktopReportErrorPaths(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, `{"code":0,"msg":"OK"}`))
	a := upstreamhttpCNAuth()

	if err := c.ReportDesktopEvent(a); err == nil {
		t.Error("零事件 want err")
	}
	if n := rec.count(); n != 0 {
		t.Errorf("零事件不应打上游，请求数=%d", n)
	}

	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 500, `{"code":10000,"msg":"boom"}`))
	if err := c2.SetAppearanceTheme(a, "theme-tkmw7j"); err == nil {
		t.Error("500 want err")
	} else if upstreamhttpErrStatus(err) != 500 {
		t.Errorf("err status=%d want 500（err=%v）", upstreamhttpErrStatus(err), err)
	}
}

// TestUpstreamHTTPMarketExpertListParseError 非 JSON 响应/上游错误必须报错
// （不能被当成"空市场"）。
func TestUpstreamHTTPMarketExpertListParseError(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, `{"code":0,"data":{"experts":"oops"}}`))
	if _, err := c.MarketExpertList(upstreamhttpCNAuth(), "agent"); err == nil {
		t.Fatal("experts 形状不对时应报解析错误，want err")
	}

	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 503, `{"code":10000,"msg":"down"}`))
	if _, err := c2.MarketExpertList(upstreamhttpCNAuth(), "agent"); err == nil {
		t.Fatal("503 want err")
	} else if upstreamhttpErrStatus(err) != 503 {
		t.Errorf("err status=%d want 503", upstreamhttpErrStatus(err))
	}
}

// TestUpstreamHTTPDesktopChatWithExpert 真实专家对话：走 chatBase /v2/chat/completions、
// 带 X-Expert-Id 与桌面头族；并从 SSE 流中抓**服务端** requestId——前面出现的不匹配 id
// 必须不阻塞后续匹配（旧实现不推进搜索偏移 → 读满 1MB 后误报"未找到"）。
func TestUpstreamHTTPDesktopChatWithExpert(t *testing.T) {
	const serverID = "0123456789abcdef0123456789abcdef"
	// 分三次读到：第一块里的 id 不是服务端 requestId —— 搜索偏移必须随块推进，
	// 否则第二块的合法 id 永远不被检查（旧实现读满 1MB 后误报"未找到"）。
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStreamStub(rec,
		"data: {\"id\":\"msg-abc-not-a-request\",\"object\":\"chat.completion.chunk\"}\n\n",
		"data: {\"id\":\""+serverID+"\",\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\n",
		"data: [DONE]\n\n",
	))

	conv, rid, err := c.DesktopChatWithExpert(upstreamhttpCNAuth(), "ex_Abc123")
	if err != nil {
		t.Fatalf("DesktopChatWithExpert: %v", err)
	}
	if rid != serverID {
		t.Fatalf("requestId=%q want %q（必须取服务端 id，自造 UUID 不计数）", rid, serverID)
	}
	if !strings.HasPrefix(conv, "wb2api-conv-") {
		t.Errorf("conversationId=%q want wb2api-conv- 前缀", conv)
	}
	req := rec.one(t, "/v2/chat/completions")
	if req.Host != "chat.example" {
		t.Errorf("host=%s want chat.example", req.Host)
	}
	if req.Header.Get("X-Expert-Id") != "ex_Abc123" {
		t.Errorf("X-Expert-Id=%q want ex_Abc123", req.Header.Get("X-Expert-Id"))
	}
	if req.Header.Get("X-Conversation-ID") != conv {
		t.Errorf("X-Conversation-ID=%q want %q（必与返回的 conv 一致）", req.Header.Get("X-Conversation-ID"), conv)
	}
	if req.Header.Get("Accept") != "text/event-stream" || req.Header.Get("x-codebuddy-request") != "1" {
		t.Errorf("流式/风控头缺失: %v", req.Header)
	}
	body := upstreamhttpObject(t, req.Body)
	if body["model"] != "fast-model" || body["stream"] != true || body["agent"] != "cli" {
		t.Errorf("chat body=%v want model=fast-model stream=true agent=cli", body)
	}
	// include_usage 决定上游回不回 usage：去掉它 = 成本观测静默断流（XC5 变异咬出来的缺口）。
	if so, ok := body["stream_options"].(map[string]any); !ok || so["include_usage"] != true {
		t.Errorf("stream_options=%v want include_usage=true（否则上游不回 usage）", body["stream_options"])
	}

	// 无 expertID：不下发 X-Expert-Id。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpStreamStub(rec2, "data: {\"id\":\""+serverID+"\"}\n\n"))
	if _, _, err := c2.DesktopChatWithExpert(upstreamhttpCNAuth(), ""); err != nil {
		t.Fatalf("DesktopChatWithExpert(no expert): %v", err)
	}
	if _, ok := rec2.one(t, "/v2/chat/completions").Header["X-Expert-Id"]; ok {
		t.Error("expertID 为空时不应下发 X-Expert-Id")
	}
}

// TestUpstreamHTTPDesktopChatWithExpertHTTPError 非 200 → 错误带状态码与 body 片段。
func TestUpstreamHTTPDesktopChatWithExpertHTTPError(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 500, `{"code":10000,"msg":"boom"}`))
	conv, rid, err := c.DesktopChatWithExpert(upstreamhttpCNAuth(), "ex_1")
	if err == nil {
		t.Fatal("want err on http 500")
	}
	if conv != "" || rid != "" {
		t.Errorf("失败时应返回空 conv/rid，got %q/%q", conv, rid)
	}
	if !strings.Contains(err.Error(), "chat http 500") {
		t.Errorf("err=%v want 含 http 500", err)
	}
}

// TestUpstreamHTTPDesktopChatWithExpertRedirectIsError 3xx 也是失败：判据必须是「非 200」，
// 不能放宽成「>=400」——否则重定向/异常状态会让解析器去读空 body，最终以
// 「未找到 requestId」的错误形态掩盖真实原因（XC2 变异咬出来的缺口）。
func TestUpstreamHTTPDesktopChatWithExpertRedirectIsError(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 302, ""))
	conv, rid, err := c.DesktopChatWithExpert(upstreamhttpCNAuth(), "ex_1")
	if err == nil {
		t.Fatal("want err on http 302")
	}
	if conv != "" || rid != "" {
		t.Errorf("失败时应返回空 conv/rid，got %q/%q", conv, rid)
	}
	if !strings.Contains(err.Error(), "chat http 302") {
		t.Errorf("err=%v want 含 chat http 302（不得退化成「未找到 requestId」）", err)
	}
}

// TestUpstreamHTTPDesktopChatWithExpertNoRequestID SSE 里没有合法 requestId → 报错
// （不得回落到自造 id，否则 expert_actual_use 白报）。
func TestUpstreamHTTPDesktopChatWithExpertNoRequestID(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStreamStub(rec,
		"data: {\"id\":\"msg-1\"}\n\n",
		"data: {\"id\":\"not-hex-32\"}\n\n",
		"data: [DONE]\n\n",
	))
	if _, _, err := c.DesktopChatWithExpert(upstreamhttpCNAuth(), "ex_1"); err == nil {
		t.Fatal("want err when no server-side requestId in SSE")
	} else if !strings.Contains(err.Error(), "未找到服务端 requestId") {
		t.Errorf("err=%v want 未找到服务端 requestId", err)
	}
}

// TestUpstreamHTTPDesktopExpertSummonSequence 召唤专家事件组：categories 首元素作 type、
// 非字符串/空 categories 回落 expert-all、version 空回落 1.0.0。
func TestUpstreamHTTPDesktopExpertSummonSequence(t *testing.T) {
	e := MarketExpert{
		ExpertID: "ex_1", ExpertType: "team", DisplayNameZH: "专家团", ProfessionZH: "教育",
		Version: "2.1.0", Categories: []any{"edu", "k12"},
	}
	events := DesktopExpertSummonSequence(e)
	if len(events) != 3 {
		t.Fatalf("events=%d want 3", len(events))
	}
	if events[0]["type"] != "edu" || events[0]["source"] != "ex_1" || events[0]["version"] != "2.1.0" ||
		events[0]["elementId"] != "expert_summon_click" {
		t.Errorf("web_element_click=%v", events[0])
	}
	if events[1]["eventCode"] != "expert_summon_click" || events[1]["id"] != "ex_1" ||
		events[1]["name"] != "专家团" || events[1]["expertTitle"] != "教育" ||
		events[1]["expertType"] != "team" || events[1]["type"] != "expert-all" ||
		events[1]["version"] != "2.1.0" || events[1]["mode"] != "LOCAL" {
		t.Errorf("expert_summon_click=%v", events[1])
	}
	if events[2]["eventCode"] != "expert_summoned" || events[2]["id"] != "ex_1" {
		t.Errorf("expert_summoned=%v", events[2])
	}

	// 空 Version + 非字符串首元素 categories → 兜底值。
	bare := DesktopExpertSummonSequence(MarketExpert{ExpertID: "ex_2", Categories: []any{map[string]any{"x": 1}}})
	if bare[0]["type"] != "expert-all" {
		t.Errorf("非字符串 categories 应回落 expert-all，got %v", bare[0]["type"])
	}
	if bare[0]["version"] != "1.0.0" || bare[1]["version"] != "1.0.0" {
		t.Errorf("空 version 应回落 1.0.0，got %v", bare[0]["version"])
	}
}

// TestUpstreamHTTPDesktopExpertActualUseVariants expert_actual_use 三形态：mode=craft /
// mode=LOCAL（Expert_lighthouse 判据）/ 公共载荷字段（cost/messageId 派生）。
func TestUpstreamHTTPDesktopExpertActualUseVariants(t *testing.T) {
	e := MarketExpert{
		ExpertID: "ex_9", ExpertType: "agent", DisplayNameZH: "轻量云专家", ProfessionZH: "云",
		Version: "3.0.0", Categories: []any{"cloud"},
	}
	const rid = "0123456789abcdef0123456789abcdef"

	craft := DesktopExpertActualUseEvent(e, "conv-1", rid)
	local := DesktopExpertActualUseLocal(e, "conv-1", rid)

	if craft["eventCode"] != "expert_actual_use" {
		t.Errorf("eventCode=%v", craft["eventCode"])
	}
	if craft["mode"] != "craft" {
		t.Errorf("DesktopExpertActualUseEvent.mode=%v want craft", craft["mode"])
	}
	if local["mode"] != "LOCAL" {
		t.Errorf("DesktopExpertActualUseLocal.mode=%v want LOCAL", local["mode"])
	}
	for _, ev := range []DesktopEvent{craft, local} {
		if ev["id"] != "ex_9" || ev["name"] != "轻量云专家" || ev["expertTitle"] != "云" ||
			ev["type"] != "cloud" || ev["expertType"] != "agent" || ev["source"] != "builtin" ||
			ev["version"] != "3.0.0" {
			t.Errorf("公共载荷=%v", ev)
		}
		if ev["cost"] != 9000 || ev["characterCount"] != 14 {
			t.Errorf("cost/characterCount=%v/%v want 9000/14", ev["cost"], ev["characterCount"])
		}
		if ev["conversationId"] != "conv-1" || ev["requestId"] != rid {
			t.Errorf("JOIN 字段=%v/%v", ev["conversationId"], ev["requestId"])
		}
		if ev["messageId"] != "msg-89abcdef" {
			t.Errorf("messageId=%v want msg-89abcdef（requestId 末 8 位）", ev["messageId"])
		}
		if ev["requestModelId"] != "fast-model" || ev["requestModelName"] != "fast-model" {
			t.Errorf("requestModel 字段=%v", ev)
		}
	}
	// 非字符串 categories / 空 version 的兜底（同一载荷函数被两个导出入口共用）。
	fallback := DesktopExpertActualUseEvent(MarketExpert{ExpertID: "ex_raw", Categories: []any{[]string{"x"}}}, "conv-2", rid)
	if fallback["type"] != "expert-all" {
		t.Errorf("非字符串 categories 应回落 expert-all，got %v", fallback["type"])
	}
	if fallback["version"] != "1.0.0" {
		t.Errorf("空 version 应回落 1.0.0，got %v", fallback["version"])
	}
	if fallback["expertType"] != "" {
		t.Errorf("空 expertType 应原样透出（不编造），got %v", fallback["expertType"])
	}
}

// ---------------------------------------------------------------------------
// tasks.go
// ---------------------------------------------------------------------------

// TestUpstreamHTTPListTasksDefaultVsMP 默认口径 vs 小程序口径：同一路径、差别只在
// X-Client-Platform 头；progress 对象覆盖平铺字段；claimable/claimed 本地推算。
func TestUpstreamHTTPListTasksDefaultVsMP(t *testing.T) {
	body := upstreamhttpEnvelope(`{"tasks":[` +
		`{"task_code":"RichMeow_Chat","title":"桌面对话","description":"指引","task_desc":"达成条件",` +
		`"reward_credit":100,"reward_energy":5,"has_reward":true,"task_type":"single","tag":"PC",` +
		`"jump_url":"workbuddy://x","locked":true,"accept_status":"not_accepted","status":"complete",` +
		`"target":1,"current":1,"progress":{"current":3,"target":3}},` +
		`{"task_code":"Sequential_Tasks_1","reward_credit":200,"accept_status":"claimed","target":1,"current":0}]}`)

	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, body))
	a := upstreamhttpCNAuth()

	tasks, err := c.ListTasks(a)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks=%d want 2", len(tasks))
	}
	got := tasks[0]
	if got.TaskCode != "RichMeow_Chat" || got.Title != "桌面对话" || got.Description != "指引" || got.TaskDesc != "达成条件" {
		t.Errorf("基础字段=%+v", got)
	}
	if got.Credit != 100 || got.Energy != 5 || !got.HasReward || got.TaskType != "single" || got.Tag != "PC" ||
		got.JumpURL != "workbuddy://x" || !got.Locked || got.Status != "complete" || got.AcceptStatus != "not_accepted" {
		t.Errorf("扩展字段=%+v", got)
	}
	// progress 对象（3/3）必须覆盖平铺的 1/1。
	if got.Current != 3 || got.Target != 3 {
		t.Errorf("progress 覆盖失败 current/target=%d/%d want 3/3", got.Current, got.Target)
	}
	if !got.Claimable || got.Claimed {
		t.Errorf("Claimable/Claimed=%v/%v want true/false", got.Claimable, got.Claimed)
	}
	// 已领取任务：即便 current(0) < target(1) 也不算 claimable。
	if tasks[1].Claimed != true || tasks[1].Claimable {
		t.Errorf("claimed 任务 Claimed/Claimable=%v/%v want true/false", tasks[1].Claimed, tasks[1].Claimable)
	}
	if tasks[1].Credit != 200 {
		t.Errorf("reward_credit=%d want 200（上游是 reward_ 前缀）", tasks[1].Credit)
	}

	req := rec.one(t, "/v2/activity/growth/tasks")
	if req.Method != http.MethodGet || req.Host != "chat.example" {
		t.Errorf("method/host=%s/%s want GET/chat.example", req.Method, req.Host)
	}
	if _, ok := req.Header["X-Client-Platform"]; ok {
		t.Errorf("默认口径不得带 X-Client-Platform，got %v", req.Header.Values("X-Client-Platform"))
	}
	if req.Header.Get("Authorization") != "Bearer at-cn" || req.Header.Get("X-User-Id") != "u-cn" {
		t.Errorf("鉴权头=%v/%v", req.Header.Get("Authorization"), req.Header.Get("X-User-Id"))
	}

	// 小程序口径：同路径 + miniprogram 头。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, body))
	if _, err := c2.ListTasksMP(a); err != nil {
		t.Fatalf("ListTasksMP: %v", err)
	}
	mpReq := rec2.one(t, "/v2/activity/growth/tasks")
	if mpReq.Header.Get("X-Client-Platform") != "miniprogram" {
		t.Errorf("X-Client-Platform=%q want miniprogram（缺头上游 task not found）", mpReq.Header.Get("X-Client-Platform"))
	}
}

// TestUpstreamHTTPAcceptTasksBodies accept 全量/小程序两口径的路径、方法与 body。
func TestUpstreamHTTPAcceptTasksBodies(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(`{}`)))
	a := upstreamhttpCNAuth()

	if err := c.AcceptTasks(a, []string{"chat_5", "black_cat"}); err != nil {
		t.Fatalf("AcceptTasks: %v", err)
	}
	req := rec.one(t, "/v2/activity/growth/tasks/accept")
	if req.Method != http.MethodPost || req.Host != "chat.example" {
		t.Errorf("method/host=%s/%s", req.Method, req.Host)
	}
	body := upstreamhttpObject(t, req.Body)
	codes, ok := body["task_codes"].([]any)
	if !ok || len(codes) != 2 || codes[0] != "chat_5" || codes[1] != "black_cat" {
		t.Errorf("body.task_codes=%v want [chat_5 black_cat]", body["task_codes"])
	}

	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, upstreamhttpEnvelope(`{}`)))
	if err := c2.AcceptTasksMP(a, []string{"Sequential_Tasks_1"}); err != nil {
		t.Fatalf("AcceptTasksMP: %v", err)
	}
	mp := rec2.one(t, "/v2/activity/growth/tasks/accept")
	if mp.Header.Get("X-Client-Platform") != "miniprogram" {
		t.Errorf("AcceptTasksMP 缺 mp 头: %v", mp.Header)
	}
	if mp.Body == nil || !strings.Contains(string(mp.Body), "Sequential_Tasks_1") {
		t.Errorf("AcceptTasksMP body=%s", mp.Body)
	}

	// 业务错误透出（accept 失败不能被静默吞掉）。
	rec3 := &upstreamhttpRec{}
	c3 := testClient(upstreamhttpFixed(rec3, 200, `{"code":12000,"msg":"internal"}`))
	if err := c3.AcceptTasks(a, []string{"x"}); err == nil {
		t.Fatal("业务码非 0 时 want err")
	}
}

// TestUpstreamHTTPClaimRewardMPSuccess 小程序领奖：chat 域路径 /activity/growth/tasks/
// <code>/claim + mp 头 + 无 body，credit/energy 从 data 解析。
func TestUpstreamHTTPClaimRewardMPSuccess(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(`{"already_claimed":false,"credit":200,"energy":5}`)))
	a := upstreamhttpCNAuth()

	credit, energy, err := c.ClaimRewardMP(a, "Sequential_Tasks_1")
	if err != nil {
		t.Fatalf("ClaimRewardMP: %v", err)
	}
	if credit != 200 || energy != 5 {
		t.Errorf("credit/energy=%d/%d want 200/5", credit, energy)
	}
	req := rec.one(t, "/activity/growth/tasks/Sequential_Tasks_1/claim")
	if req.Method != http.MethodPost || req.Host != "chat.example" {
		t.Errorf("method/host=%s/%s", req.Method, req.Host)
	}
	if req.Header.Get("X-Client-Platform") != "miniprogram" {
		t.Errorf("X-Client-Platform=%q want miniprogram", req.Header.Get("X-Client-Platform"))
	}
	if len(req.Body) != 0 {
		t.Errorf("claim 不应带 body，got %q", req.Body)
	}
}

// TestUpstreamHTTPClaimRewardMPFallsBackToWebOn400 chat 域 400 → Web 域降级领奖：
// 第二条请求必须打到 webBase，且带 x-client-platform: web。
func TestUpstreamHTTPClaimRewardMPFallsBackToWebOn400(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
		if q.Host == "chat.example" {
			return jsonResp(400, `{"code":400,"msg":"task not completed"}`), nil
		}
		if q.Host == "web.example" {
			return jsonResp(200, upstreamhttpEnvelope(`{"credit":50,"energy":1}`)), nil
		}
		return nil, fmt.Errorf("unexpected host %s", q.Host)
	}))
	c.WebBaseCN = "https://web.example"
	a := upstreamhttpCNAuth()
	a.EnterpriseID = "ent-1"

	credit, energy, err := c.ClaimRewardMP(a, "chat_5")
	if err != nil {
		t.Fatalf("400 应降级 Web 域领奖: %v", err)
	}
	if credit != 50 || energy != 1 {
		t.Errorf("降级后 credit/energy=%d/%d want 50/1", credit, energy)
	}
	if n := rec.count(); n != 2 {
		t.Fatalf("请求数=%d want 2（chat 400 → web 重试）: %v", n, rec.paths())
	}
	calls := rec.snapshot()
	if calls[0].Host != "chat.example" || calls[0].Header.Get("X-Client-Platform") != "miniprogram" {
		t.Errorf("首次请求应在 chat 域且带 mp 头: %s %v", calls[0].Host, calls[0].Header)
	}
	if calls[1].Host != "web.example" {
		t.Fatalf("第二条请求 host=%v want web.example（calls=%v）", calls[1].Host, rec.paths())
	}
	if calls[1].Header.Get("x-client-platform") != "web" {
		t.Errorf("Web 域请求 x-client-platform=%q want web", calls[1].Header.Get("x-client-platform"))
	}
	// 企业账号：Web 域领奖必须带企业头与域头（缺则上游按个人账号处理）。
	if calls[1].Header.Get("X-Enterprise-Id") != "ent-1" || calls[1].Header.Get("X-Tenant-Id") != "ent-1" {
		t.Errorf("Web 域企业头=%q/%q want ent-1/ent-1", calls[1].Header.Get("X-Enterprise-Id"), calls[1].Header.Get("X-Tenant-Id"))
	}
	if calls[1].Header.Get("X-Domain") != "copilot.tencent.com" {
		t.Errorf("Web 域 X-Domain=%q want copilot.tencent.com", calls[1].Header.Get("X-Domain"))
	}

	// 非 400 的错误不得降级（避免对限流/5xx 二次打上游）。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 429, `{"code":6004,"msg":"rate limited"}`))
	if _, _, err := c2.ClaimRewardMP(a, "chat_5"); err == nil {
		t.Fatal("429 时 want err")
	} else if upstreamhttpErrStatus(err) != 429 {
		t.Errorf("err status=%d want 429（非 *Error? %v）", upstreamhttpErrStatus(err), err)
	}
	if n := rec2.count(); n != 1 {
		t.Errorf("非 400 不应降级，请求数=%d want 1", n)
	}
}

// TestUpstreamHTTPParseClaimRewardAndGrowthTasks 纯解析函数：领奖 data 与任务列表
// 的边界形状（缺字段零值、非法 JSON、progress=null）。
func TestUpstreamHTTPParseClaimRewardAndGrowthTasks(t *testing.T) {
	credit, energy, err := parseClaimReward(json.RawMessage(`{"already_claimed":false,"credit":100,"energy":5}`))
	if err != nil || credit != 100 || energy != 5 {
		t.Errorf("parseClaimReward=%d/%d/%v want 100/5/nil", credit, energy, err)
	}
	if _, _, err := parseClaimReward(json.RawMessage(`not-json`)); err == nil {
		t.Error("非法 JSON want err")
	}
	if c, e, err := parseClaimReward(json.RawMessage(`{}`)); err != nil || c != 0 || e != 0 {
		t.Errorf("缺字段应零值无错，got %d/%d/%v", c, e, err)
	}

	tasks, err := parseGrowthTasks(json.RawMessage(`{"tasks":[` +
		`{"task_code":"a","target":5,"current":5,"progress":null},` +
		`{"task_code":"b","target":5,"current":4,"progress":{"current":1,"target":9}},` +
		`{"task_code":"c"}]}`))
	if err != nil {
		t.Fatalf("parseGrowthTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks=%d want 3", len(tasks))
	}
	// progress=null → 用平铺字段；5/5 → claimable。
	if tasks[0].Current != 5 || tasks[0].Target != 5 || !tasks[0].Claimable {
		t.Errorf("tasks[0]=%+v want 5/5 claimable", tasks[0])
	}
	// progress 对象 1/9 → 覆盖平铺 4/5，且未达标不可领。
	if tasks[1].Current != 1 || tasks[1].Target != 9 || tasks[1].Claimable {
		t.Errorf("tasks[1]=%+v want 1/9 不可领", tasks[1])
	}
	// 无 progress 无进度：target=0 → 永不可领（0 是有效值，不是"完成"）。
	if tasks[2].Current != 0 || tasks[2].Target != 0 || tasks[2].Claimable {
		t.Errorf("tasks[2]=%+v want 0/0 不可领", tasks[2])
	}
	if _, err := parseGrowthTasks(json.RawMessage(`{"tasks":"oops"}`)); err == nil {
		t.Error("tasks 形状不对 want err")
	}
}

// ---------------------------------------------------------------------------
// streak.go
// ---------------------------------------------------------------------------

// TestUpstreamHTTPGrowthStreakFull 连登完整状态解析（档位/补签卡/兑换状态嵌套结构）。
func TestUpstreamHTTPGrowthStreakFull(t *testing.T) {
	data := `{"streak":{"days":12,"month_total_days":20,"next_tier":"14d","next_tier_remaining":2},` +
		`"makeup_cards":{"balance":1,"max":3},` +
		`"redemption_status":{"tier_7d_status":"redeemed","tier_14d_status":"available","tier_28d_status":"locked",` +
		`"remaining_days":5,"tiers":[{"tier":"7d","days":7,"credit":100,"energy":5,"cards":1,"chances":2}]}}`
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(data)))

	out, err := c.GrowthStreakFull(upstreamhttpCNAuth())
	if err != nil {
		t.Fatalf("GrowthStreakFull: %v", err)
	}
	if out.Streak.Days != 12 || out.Streak.MonthTotalDays != 20 || out.Streak.NextTier != "14d" || out.Streak.NextTierRemaining != 2 {
		t.Errorf("streak=%+v", out.Streak)
	}
	if out.MakeupCards.Balance != 1 || out.MakeupCards.Max != 3 {
		t.Errorf("makeup_cards=%+v", out.MakeupCards)
	}
	rs := out.RedemptionStatus
	if rs.Tier7dStatus != "redeemed" || rs.Tier14dStatus != "available" || rs.Tier28dStatus != "locked" || rs.RemainingDays != 5 {
		t.Errorf("redemption_status=%+v", rs)
	}
	if len(rs.Tiers) != 1 || rs.Tiers[0].Tier != "7d" || rs.Tiers[0].Days != 7 || rs.Tiers[0].Credit != 100 ||
		rs.Tiers[0].Energy != 5 || rs.Tiers[0].Cards != 1 || rs.Tiers[0].Chances != 2 {
		t.Errorf("tiers=%+v", rs.Tiers)
	}
	req := rec.one(t, "/activity/growth/streak")
	if req.Method != http.MethodGet || req.Host != "chat.example" {
		t.Errorf("method/host=%s/%s want GET/chat.example", req.Method, req.Host)
	}

	// data 非对象 → 解析错误（不能把畸形当"连登 0 天"）。
	recBad := &upstreamhttpRec{}
	cBad := testClient(upstreamhttpFixed(recBad, 200, upstreamhttpEnvelope(`"oops"`)))
	if _, err := cBad.GrowthStreakFull(upstreamhttpCNAuth()); err == nil {
		t.Error("畸形 data want err")
	}
}

// TestUpstreamHTTPGrowthRedeemTier 兑换档位：body 带 tier + 幂等 client_token（UUID 形状），
// 未解锁 403 以 *Error 透出（调用方按 locked 跳过）。
func TestUpstreamHTTPGrowthRedeemTier(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(`{"credit":100}`)))
	if err := c.GrowthRedeemTier(upstreamhttpCNAuth(), "14d"); err != nil {
		t.Fatalf("GrowthRedeemTier: %v", err)
	}
	req := rec.one(t, "/activity/growth/redeem")
	if req.Method != http.MethodPost || req.Host != "chat.example" {
		t.Errorf("method/host=%s/%s", req.Method, req.Host)
	}
	body := upstreamhttpObject(t, req.Body)
	if body["tier"] != "14d" {
		t.Errorf("body.tier=%v want 14d", body["tier"])
	}
	token, _ := body["client_token"].(string)
	if !upstreamhttpUUIDRe.MatchString(token) {
		t.Errorf("client_token=%q want UUID 形状（幂等令牌）", token)
	}
	if len(body) != 2 {
		t.Errorf("body=%v want 仅 tier + client_token", body)
	}

	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 403, `{"code":403,"msg":"连续登录天数不足"}`))
	err := c2.GrowthRedeemTier(upstreamhttpCNAuth(), "28d")
	if err == nil {
		t.Fatal("403 want err")
	}
	if upstreamhttpErrStatus(err) != 403 {
		t.Errorf("err status=%d want 403（err=%v）", upstreamhttpErrStatus(err), err)
	}
}

// TestUpstreamHTTPLotteryChancesAndDraw 抽奖次数查询与抽奖：路径、方法、client_token，
// 以及 draw 返回原始 prize 载荷（形状由活动期决定，必须原样透传）。
func TestUpstreamHTTPLotteryChancesAndDraw(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
		if q.Path == "/activity/growth/lottery/summary" {
			return jsonResp(200, upstreamhttpEnvelope(`{"chances":3,"module":{"enabled":true}}`)), nil
		}
		if q.Path == "/activity/growth/lottery/draw" {
			return jsonResp(200, upstreamhttpEnvelope(`{"prize":{"name":"100 积分","type":"credit","amount":100}}`)), nil
		}
		return nil, fmt.Errorf("unexpected path %s", q.Path)
	}))
	a := upstreamhttpCNAuth()

	chances, err := c.LotteryChances(a)
	if err != nil {
		t.Fatalf("LotteryChances: %v", err)
	}
	if chances != 3 {
		t.Errorf("chances=%d want 3", chances)
	}
	if req := rec.one(t, "/activity/growth/lottery/summary"); req.Method != http.MethodGet {
		t.Errorf("summary method=%s want GET", req.Method)
	}

	// data 非对象 → 错误（不能把畸形当 0 次机会）。
	recBad := &upstreamhttpRec{}
	cBad := testClient(upstreamhttpFixed(recBad, 200, upstreamhttpEnvelope(`"oops"`)))
	if _, err := cBad.LotteryChances(a); err == nil {
		t.Error("畸形 data want err")
	}

	raw, err := c.LotteryDraw(a)
	if err != nil {
		t.Fatalf("LotteryDraw: %v", err)
	}
	var prize struct {
		Prize struct {
			Name   string `json:"name"`
			Type   string `json:"type"`
			Amount int64  `json:"amount"`
		} `json:"prize"`
	}
	if err := json.Unmarshal(raw, &prize); err != nil {
		t.Fatalf("draw 载荷原样透传失败: %v (raw=%s)", err, raw)
	}
	if prize.Prize.Name != "100 积分" || prize.Prize.Type != "credit" || prize.Prize.Amount != 100 {
		t.Errorf("prize=%+v", prize.Prize)
	}
	draw := rec.one(t, "/activity/growth/lottery/draw")
	if draw.Method != http.MethodPost {
		t.Errorf("draw method=%s want POST", draw.Method)
	}
	token, _ := upstreamhttpObject(t, draw.Body)["client_token"].(string)
	if !upstreamhttpUUIDRe.MatchString(token) {
		t.Errorf("draw body client_token=%q want UUID 形状", token)
	}
}

// ---------------------------------------------------------------------------
// school.go
// ---------------------------------------------------------------------------

// TestUpstreamHTTPSchoolVouchers 我的券码：billingBase + /portal/activity/school 前缀、
// Bearer/X-User-Id 头、items 字段解析。
func TestUpstreamHTTPSchoolVouchers(t *testing.T) {
	data := `{"items":[{"grant_id":7,"draw_uuid":"draw-1","sku_code":"kfc_ice_cream","prize_name":"肯德基冰淇淋",` +
		`"code":"KFC-1234-5678","valid_from":"","valid_to":"2026-10-24","granted_at":"2026-09-16T10:00:00+08:00"}]}`
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(data)))

	got, err := c.SchoolVouchers(upstreamhttpCNAuth())
	if err != nil {
		t.Fatalf("SchoolVouchers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("vouchers=%d want 1", len(got))
	}
	v := got[0]
	if v.GrantID != 7 || v.DrawUUID != "draw-1" || v.SKUCode != "kfc_ice_cream" || v.PrizeName != "肯德基冰淇淋" ||
		v.Code != "KFC-1234-5678" || v.ValidTo != "2026-10-24" || v.GrantedAt != "2026-09-16T10:00:00+08:00" {
		t.Errorf("voucher=%+v", v)
	}
	req := rec.one(t, "/portal/activity/school/vouchers")
	if req.Method != http.MethodGet || req.Host != "billing.example" {
		t.Errorf("method/host=%s/%s want GET/billing.example（school 域在 billingBase）", req.Method, req.Host)
	}
	if req.Header.Get("Authorization") != "Bearer at-cn" || req.Header.Get("X-User-Id") != "u-cn" ||
		req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Accept") != "application/json" {
		t.Errorf("school 域头缺失: %v", req.Header)
	}

	// data 形状不对 → 报错；业务码非 0 → 报错。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, upstreamhttpEnvelope(`"not-an-object"`)))
	if _, err := c2.SchoolVouchers(upstreamhttpCNAuth()); err == nil {
		t.Error("data 非对象时 want err")
	}
	rec3 := &upstreamhttpRec{}
	c3 := testClient(upstreamhttpFixed(rec3, 200, `{"code":403,"msg":"activity expired"}`))
	if _, err := c3.SchoolVouchers(upstreamhttpCNAuth()); err == nil {
		t.Error("业务码非 0 want err")
	}
}

// TestUpstreamHTTPReportMPEvent 小程序上报：BillingBaseCN + /v2/report、mp 指纹头族、
// 公共指纹注入且业务字段优先。
func TestUpstreamHTTPReportMPEvent(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, `{"code":0,"msg":"OK"}`))
	a := &auth.Auth{AccessToken: "at-mp", UID: "u-mp", Nickname: "小程序用户"}

	if err := c.ReportMPEvent(a,
		map[string]any{"eventCode": "chat_request_send"},
		map[string]any{"eventCode": "expert_actual_use", "product": "MP-OVERRIDE", "id": "ex_1"},
	); err != nil {
		t.Fatalf("ReportMPEvent: %v", err)
	}
	req := rec.one(t, "/v2/report")
	if req.Host != "billing.example" {
		t.Errorf("host=%s want billing.example（mp 上报走 codebuddy.cn 域）", req.Host)
	}
	for k, want := range map[string]string{
		"X-Client-Product":  "workbuddy-mp",
		"X-Client-Version":  "2.4.0",
		"X-Client-Platform": "mp-weixin",
		"X-Platform":        "wechatmp",
		"X-User-Id":         "u-mp",
		"Authorization":     "Bearer at-mp",
	} {
		if got := req.Header.Get(k); got != want {
			t.Errorf("header %s=%q want %q", k, got, want)
		}
	}
	arr := upstreamhttpArray(t, req.Body)
	if len(arr) != 2 {
		t.Fatalf("body 元素数=%d want 2", len(arr))
	}
	if arr[0]["ideType"] != "WorkBuddy_MP" || arr[0]["ideVersion"] != "2.4.0" || arr[0]["extName"] != "workbuddy-mp" ||
		arr[0]["platform"] != "mini_program" || arr[0]["userId"] != "u-mp" || arr[0]["userNickname"] != "小程序用户" {
		t.Errorf("mp 公共指纹=%v", arr[0])
	}
	if arr[1]["product"] != "MP-OVERRIDE" {
		t.Errorf("业务字段应覆盖指纹 product，got %v", arr[1]["product"])
	}
	if arr[1]["eventCode"] != "expert_actual_use" || arr[1]["id"] != "ex_1" {
		t.Errorf("arr[1]=%v", arr[1])
	}
	if _, ok := arr[1]["platform"].(string); !ok {
		t.Errorf("指纹字段 platform 丢失: %v", arr[1])
	}

	// 零事件：本地直接报错，不打上游。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, `{"code":0}`))
	if err := c2.ReportMPEvent(a); err == nil {
		t.Fatal("零事件 want err")
	}
	if n := rec2.count(); n != 0 {
		t.Errorf("零事件不应打上游，请求数=%d", n)
	}
}

// TestUpstreamHTTPSchoolSeasonAndMiniEvents 校园日 / mp 专家 / 模型对话 / 灵感事件形状。
func TestUpstreamHTTPSchoolSeasonAndMiniEvents(t *testing.T) {
	// 校园日：chat_request_send 同构 + activityId（缺 activityId 不点亮）。
	season := SchoolSeasonChatEvent("conv-season")
	if season["activityId"] != "school_open_day_2026" {
		t.Errorf("activityId=%v want school_open_day_2026", season["activityId"])
	}
	if season["eventCode"] != "chat_request_send" || season["conversationId"] != "conv-season" {
		t.Errorf("校园日事件=%v", season)
	}
	if _, ok := SchoolChatTimesEvents("conv-x")["activityId"]; ok {
		t.Error("SchoolChatTimesEvents 不应带 activityId（两套口径勿照抄）")
	}

	// mp 专家使用：extVersion 2.2.8、source=mini_program、type=send_message、
	// 且**不带** conversationId/activityId（真实事件就是不带）。
	ev := MiniExpertUseEvent("ex_real", "专家名", "")
	if ev["eventCode"] != "expert_actual_use" || ev["id"] != "ex_real" || ev["expertTitle"] != "专家名" ||
		ev["expertType"] != "agent" || ev["type"] != "send_message" || ev["source"] != "mini_program" ||
		ev["extVersion"] != "2.2.8" || ev["characterCount"] != 12 {
		t.Errorf("MiniExpertUseEvent=%v", ev)
	}
	for _, k := range []string{"conversationId", "activityId"} {
		if _, ok := ev[k]; ok {
			t.Errorf("MiniExpertUseEvent 不应带 %s", k)
		}
	}
	if bare := MiniExpertUseEvent("ex_only", "", "team"); bare["expertTitle"] != "ex_only" || bare["expertType"] != "team" {
		t.Errorf("空 name/显式 expertType 兜底失败: %v", bare)
	}

	// mp 模型对话：必须带 requestModelId/Name（模型任务载体）。
	model := MiniChatModelEvent("conv-m", "glm-5.2", "GLM-5.2")
	if model["requestModelId"] != "glm-5.2" || model["requestModelName"] != "GLM-5.2" {
		t.Errorf("MiniChatModelEvent=%v", model)
	}
	if model["eventCode"] != "chat_request_send" || model["conversationId"] != "conv-m" {
		t.Errorf("模型事件基座字段=%v", model)
	}

	// mp 灵感：两事件，cta position=1、prompt 带 conversationId 与 promptLength。
	events := MiniPlaybookEvents("case-mp", "灵感案例")
	if len(events) != 2 {
		t.Fatalf("playbook events=%d want 2", len(events))
	}
	cta, send := events[0], events[1]
	if cta["eventCode"] != "playbook_cta_click" || cta["position"] != 1 || cta["id"] != "case-mp" ||
		cta["name"] != "灵感案例" || cta["type"] != "document" || cta["extVersion"] != "2.2.8" {
		t.Errorf("cta=%v", cta)
	}
	if send["eventCode"] != "playbook_prompt_send" || send["promptLength"] != 96 || send["isOfficial"] != 1 ||
		send["id"] != "case-mp" || send["source"] != "discover" {
		t.Errorf("send=%v", send)
	}
	convID, _ := send["conversationId"].(string)
	if !strings.HasPrefix(convID, "wb2api-mp-pb-") {
		t.Errorf("playbook conversationId=%q want wb2api-mp-pb- 前缀", convID)
	}
}

// ---------------------------------------------------------------------------
// global_register.go
// ---------------------------------------------------------------------------

const upstreamhttpCountriesInner = `{"data":{"list":[` +
	`{"EnName":"United States","Name":"美国","IOS2":"US","IOS3":"USA","Code":"840"},` +
	`{"EnName":"Japan","Name":"日本","IOS2":"JP","IOS3":"JPN","Code":"392"},` +
	`{"EnName":"Indonesia","Name":"印度尼西亚","IOS2":"ID","IOS3":"IDN","Code":"360"},` +
	`{"EnName":"Malaysia","Name":"马来西亚","IOS2":"MY","IOS3":"MYS","Code":"458"},` +
	`{"EnName":"Philippines","Name":"菲律宾","IOS2":"PH","IOS3":"PHL","Code":"608"},` +
	`{"EnName":"Thailand","Name":"泰国","IOS2":"TH","IOS3":"THA","Code":"764"},` +
	`{"EnName":"Singapore","Name":"新加坡","IOS2":"SG","IOS3":"SGP","Code":"702"},` +
	`{"EnName":"Macao","Name":"中国澳门","IOS2":"MO","IOS3":"MAC","Code":"446"},` +
	`{"EnName":"Hong Kong","Name":"中国香港","IOS2":"HK","IOS3":"HKG","Code":"344"}]}}`

// upstreamhttpCountriesResp 双层信封：外层 data 是 **JSON 字符串**（真实上游形态）。
func upstreamhttpCountriesResp(t *testing.T, inner string) string {
	t.Helper()
	quoted, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}
	return `{"code":0,"msg":"ok","data":` + string(quoted) + `}`
}

// TestUpstreamHTTPGlobalFetchCountriesDoubleEnvelopeAndWhitelist 国家列表：双层信封
// 二次解析 + intlOnly 白名单过滤（顺序对齐 web 展示，不随上游返回序）。
func TestUpstreamHTTPGlobalFetchCountriesDoubleEnvelopeAndWhitelist(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpCountriesResp(t, upstreamhttpCountriesInner)))
	c.BillingBaseGlobal = "https://global.example"
	a := upstreamhttpGlobalAuth()

	all, err := c.GlobalFetchCountries(a, false)
	if err != nil {
		t.Fatalf("GlobalFetchCountries(all): %v", err)
	}
	if len(all) != 9 {
		t.Fatalf("全量 countries=%d want 9", len(all))
	}
	if all[0].IOS2 != "US" || all[0].EnName != "United States" || all[0].Name != "美国" ||
		all[0].IOS3 != "USA" || all[0].Code != "840" {
		t.Errorf("countries[0]=%+v（字段名必须对齐 EnName/IOS2/Code）", all[0])
	}
	req := rec.one(t, "/billing/area/get-country-code")
	if req.Method != http.MethodPost || req.Host != "global.example" {
		t.Errorf("method/host=%s/%s want POST/global.example", req.Method, req.Host)
	}
	if !strings.HasPrefix(req.Header.Get("User-Agent"), "Mozilla/5.0 (Windows NT 10.0; Win64; x64)") {
		t.Errorf("UA=%q want web 指纹（注册链路非 CLI 指纹）", req.Header.Get("User-Agent"))
	}
	if req.Header.Get("Origin") != "https://global.example" || req.Header.Get("Referer") != "https://global.example/" {
		t.Errorf("Origin/Referer=%q/%q want 同域 global base", req.Header.Get("Origin"), req.Header.Get("Referer"))
	}
	if req.Header.Get("Authorization") != "Bearer at-global" {
		t.Errorf("Authorization=%q", req.Header.Get("Authorization"))
	}
	body := upstreamhttpObject(t, req.Body)
	if len(body) != 1 || body["filterForbidden"] != float64(1) {
		t.Errorf("body=%v want {filterForbidden:1}", body)
	}

	intl, err := c.GlobalFetchCountries(a, true)
	if err != nil {
		t.Fatalf("GlobalFetchCountries(intl): %v", err)
	}
	var gotCodes []string
	for _, ctry := range intl {
		gotCodes = append(gotCodes, ctry.IOS2)
	}
	want := []string{"HK", "MO", "SG", "TH", "PH", "MY", "ID"}
	if !upstreamhttpStringsEqual(gotCodes, want) {
		t.Errorf("intlOnly 过滤结果=%v want %v（白名单顺序，US/JP 被剔除）", gotCodes, want)
	}
}

// TestUpstreamHTTPGlobalFetchCountriesGuards 非 global 账号与业务错误的守卫。
func TestUpstreamHTTPGlobalFetchCountriesGuards(t *testing.T) {
	// 未配置 global base → 缺省 workbuddy.ai（D5）。
	if got := (&Client{}).globalRegisterBase(); got != "https://www.workbuddy.ai" {
		t.Errorf("缺省 global base=%q want https://www.workbuddy.ai", got)
	}
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpCountriesResp(t, upstreamhttpCountriesInner)))
	c.BillingBaseGlobal = "https://global.example"

	// CN 账号：本地直接拒绝，零上游调用。
	if _, err := c.GlobalFetchCountries(upstreamhttpCNAuth(), true); err == nil {
		t.Fatal("CN 账号 want err")
	}
	if n := rec.count(); n != 0 {
		t.Errorf("CN 账号不应打上游，请求数=%d", n)
	}
	// nil auth 同样拒绝（防解引用 panic）。
	if _, err := c.GlobalFetchCountries(nil, true); err == nil {
		t.Fatal("nil auth want err")
	}

	// 业务码非 0 → 错误带 msg。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, `{"code":500,"msg":"region required"}`))
	c2.BillingBaseGlobal = "https://global.example"
	_, err := c2.GlobalFetchCountries(upstreamhttpGlobalAuth(), true)
	if err == nil || !strings.Contains(err.Error(), "region required") {
		t.Errorf("err=%v want 含 region required", err)
	}

	// 响应非 JSON → 解析错误（不能静默当空列表）。
	rec3 := &upstreamhttpRec{}
	c3 := testClient(upstreamhttpFixed(rec3, 200, `<html>502</html>`))
	c3.BillingBaseGlobal = "https://global.example"
	if _, err := c3.GlobalFetchCountries(upstreamhttpGlobalAuth(), true); err == nil {
		t.Error("非 JSON 响应 want err")
	}

	// data 是 JSON 字符串但内部不是合法 JSON 对象 → 二次解析报错（不得当空列表）。
	rec4 := &upstreamhttpRec{}
	c4 := testClient(upstreamhttpFixed(rec4, 200, `{"code":0,"msg":"ok","data":"not-json"}`))
	c4.BillingBaseGlobal = "https://global.example"
	if _, err := c4.GlobalFetchCountries(upstreamhttpGlobalAuth(), true); err == nil {
		t.Error("内层非对象 want err")
	}
}

// TestUpstreamHTTPGlobalRegisterStatus 注册状态三态：200 已激活 / 500 或缺地区文案需补 /
// 其他码原样透出（不得误判为已激活）。
func TestUpstreamHTTPGlobalRegisterStatus(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		wantActivated  bool
		wantNeedsReg   bool
		wantMsgContain string
	}{
		{"200 已激活", `{"code":200,"msg":"ok"}`, true, false, "register success"},
		{"500 需补地区", `{"code":500,"msg":"user region required"}`, false, true, "region required"},
		{"大小写不敏感文案", `{"code":1,"msg":"Region Required"}`, false, true, "Region Required"},
		{"其他码不误判", `{"code":403,"msg":"forbidden"}`, false, false, "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &upstreamhttpRec{}
			c := testClient(upstreamhttpFixed(rec, 200, tc.body))
			c.BillingBaseGlobal = "https://global.example"
			activated, needsRegion, msg, err := c.GlobalRegisterStatus(upstreamhttpGlobalAuth())
			if err != nil {
				t.Fatalf("GlobalRegisterStatus: %v", err)
			}
			if activated != tc.wantActivated || needsRegion != tc.wantNeedsReg {
				t.Errorf("activated/needsRegion=%v/%v want %v/%v", activated, needsRegion, tc.wantActivated, tc.wantNeedsReg)
			}
			if !strings.Contains(msg, tc.wantMsgContain) {
				t.Errorf("msg=%q want 含 %q", msg, tc.wantMsgContain)
			}
			req := rec.one(t, "/auth/realms/copilot/overseas/user/register")
			if req.Method != http.MethodGet {
				t.Errorf("method=%s want GET", req.Method)
			}
			if req.RawQuery != "userId=u-global" {
				t.Errorf("query=%q want userId=u-global", req.RawQuery)
			}
			if req.Header.Get("X-User-Id") != "u-global" {
				t.Errorf("X-User-Id=%q want u-global", req.Header.Get("X-User-Id"))
			}
			if _, ok := req.Header["X-Refresh-Token"]; ok {
				t.Error("出站请求不得携带 X-Refresh-Token")
			}
		})
	}

	// 非 global 账号：本地拒绝。
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, `{"code":200}`))
	c.BillingBaseGlobal = "https://global.example"
	if _, _, _, err := c.GlobalRegisterStatus(upstreamhttpCNAuth()); err == nil {
		t.Fatal("CN 账号 want err")
	}
	if n := rec.count(); n != 0 {
		t.Errorf("CN 账号不应打上游，请求数=%d", n)
	}

	// 传输层失败 → 错误（不得把无响应当"未激活"继续走注册流程）。
	cNet := testClient(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: refused") })
	cNet.BillingBaseGlobal = "https://global.example"
	if _, _, _, err := cNet.GlobalRegisterStatus(upstreamhttpGlobalAuth()); err == nil ||
		!strings.Contains(err.Error(), "refused") {
		t.Errorf("err=%v want 传输层错误透出", err)
	}
	// 响应非 JSON → 解析错误。
	recBad := &upstreamhttpRec{}
	cBad := testClient(upstreamhttpFixed(recBad, 200, `<html>502</html>`))
	cBad.BillingBaseGlobal = "https://global.example"
	if _, _, _, err := cBad.GlobalRegisterStatus(upstreamhttpGlobalAuth()); err == nil {
		t.Error("非 JSON body want err")
	}
	// token 为空 → 不发 Authorization（防伪造空 Bearer 头）。
	req, err := (&Client{}).globalRegisterReq(http.MethodGet, "https://global.example/x", "", nil)
	if err != nil {
		t.Fatalf("globalRegisterReq: %v", err)
	}
	if _, ok := req.Header["Authorization"]; ok {
		t.Errorf("空 token 不应发 Authorization，got %v", req.Header.Get("Authorization"))
	}
	if req.Header.Get("User-Agent") != globalWebUA {
		t.Errorf("UA=%q want globalWebUA", req.Header.Get("User-Agent"))
	}
}

// TestUpstreamHTTPGlobalSubmitRegion attributes 三元组映射（countryCode/FullName/Name
// 都是**单元素数组**，错形状上游静默丢弃）。
func TestUpstreamHTTPGlobalSubmitRegion(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, `{"code":0,"msg":"ok"}`))
	c.BillingBaseGlobal = "https://global.example"
	a := upstreamhttpGlobalAuth()

	country := GlobalCountry{EnName: "Hong Kong", Name: "中国香港", IOS2: "HK", IOS3: "HKG", Code: "344"}
	if err := c.GlobalSubmitRegion(a, country); err != nil {
		t.Fatalf("GlobalSubmitRegion: %v", err)
	}
	req := rec.one(t, "/console/login/account")
	if req.Method != http.MethodPost || req.Host != "global.example" {
		t.Errorf("method/host=%s/%s", req.Method, req.Host)
	}
	var got struct {
		Attributes map[string][]string `json:"attributes"`
	}
	if err := json.Unmarshal(req.Body, &got); err != nil {
		t.Fatalf("body 形状=%s: %v", req.Body, err)
	}
	want := map[string][]string{
		"countryCode":     {"344"},
		"countryFullName": {"Hong Kong"},
		"countryName":     {"HK"},
	}
	if len(got.Attributes) != len(want) {
		t.Fatalf("attributes=%v want %v", got.Attributes, want)
	}
	for k, v := range want {
		if len(got.Attributes[k]) != 1 || got.Attributes[k][0] != v[0] {
			t.Errorf("attributes[%s]=%v want %v", k, got.Attributes[k], v)
		}
	}

	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, `{"code":10001,"msg":"invalid country"}`))
	c2.BillingBaseGlobal = "https://global.example"
	if err := c2.GlobalSubmitRegion(a, country); err == nil || !strings.Contains(err.Error(), "invalid country") {
		t.Errorf("err=%v want 含 invalid country", err)
	}
	if err := c2.GlobalSubmitRegion(upstreamhttpCNAuth(), country); err == nil {
		t.Error("CN 账号 want err")
	}
}

// TestUpstreamHTTPGlobalCompleteRegistrationFlow 一键激活：status → countries → submit
// → **再次 status 验证**（顺序不可换），激活后返回 true。
func TestUpstreamHTTPGlobalCompleteRegistrationFlow(t *testing.T) {
	rec := &upstreamhttpRec{}
	var registerHits atomic.Int32
	c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
		switch q.Path {
		case "/auth/realms/copilot/overseas/user/register":
			if registerHits.Add(1) == 1 {
				return jsonResp(200, `{"code":500,"msg":"region required"}`), nil
			}
			return jsonResp(200, `{"code":200,"msg":"ok"}`), nil
		case "/billing/area/get-country-code":
			return jsonResp(200, upstreamhttpCountriesResp(t, upstreamhttpCountriesInner)), nil
		case "/console/login/account":
			return jsonResp(200, `{"code":0,"msg":"ok"}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", q.Path)
		}
	}))
	c.BillingBaseGlobal = "https://global.example"

	activated, err := c.GlobalCompleteRegistration(upstreamhttpGlobalAuth())
	if err != nil {
		t.Fatalf("GlobalCompleteRegistration: %v", err)
	}
	if !activated {
		t.Error("补地区后应已激活")
	}
	var order []string
	for _, call := range rec.snapshot() {
		order = append(order, call.Path)
	}
	wantOrder := []string{
		"/auth/realms/copilot/overseas/user/register",
		"/billing/area/get-country-code",
		"/console/login/account",
		"/auth/realms/copilot/overseas/user/register",
	}
	if !upstreamhttpStringsEqual(order, wantOrder) {
		t.Errorf("调用顺序=%v want %v", order, wantOrder)
	}
	// 提交的必须是白名单首个（HK），不是上游返回序首个（US）。
	submit := rec.one(t, "/console/login/account")
	if !strings.Contains(string(submit.Body), `"HK"`) || strings.Contains(string(submit.Body), `"US"`) {
		t.Errorf("提交地区 body=%s want HK（白名单首个）", submit.Body)
	}
}

// TestUpstreamHTTPGlobalCompleteRegistrationShortCircuits 已激活短路 / 未激活无地区需求
// 报错 / 无可用地区报错。
func TestUpstreamHTTPGlobalCompleteRegistrationShortCircuits(t *testing.T) {
	// 已激活：只打一次 status，零其它调用。
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, `{"code":200,"msg":"ok"}`))
	c.BillingBaseGlobal = "https://global.example"
	activated, err := c.GlobalCompleteRegistration(upstreamhttpGlobalAuth())
	if err != nil || !activated {
		t.Fatalf("已激活应短路返回 true/nil，got %v/%v", activated, err)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("请求数=%d want 1（已激活不得再打国家列表/提交）", n)
	}

	// 未激活且不需要补地区 → 错误带原 msg。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, `{"code":403,"msg":"account banned"}`))
	c2.BillingBaseGlobal = "https://global.example"
	if _, err := c2.GlobalCompleteRegistration(upstreamhttpGlobalAuth()); err == nil ||
		!strings.Contains(err.Error(), "register not activated") || !strings.Contains(err.Error(), "account banned") {
		t.Errorf("err=%v want register not activated: account banned", err)
	}

	// 需补地区但白名单过滤后无可用国家 → 明确报错（不静默提交空地区）。
	rec3 := &upstreamhttpRec{}
	c3 := testClient(upstreamhttpStub(rec3, func(q upstreamhttpReq) (*http.Response, error) {
		if q.Path == "/auth/realms/copilot/overseas/user/register" {
			return jsonResp(200, `{"code":500,"msg":"region required"}`), nil
		}
		return jsonResp(200, upstreamhttpCountriesResp(t, `{"data":{"list":[{"EnName":"United States","IOS2":"US","Code":"840"}]}}`)), nil
	}))
	c3.BillingBaseGlobal = "https://global.example"
	if _, err := c3.GlobalCompleteRegistration(upstreamhttpGlobalAuth()); err == nil ||
		!strings.Contains(err.Error(), "no countries available") {
		t.Errorf("err=%v want no countries available", err)
	}

	// 拉国家列表失败 → 错误带上下文，且不得继续提交地区。
	rec4 := &upstreamhttpRec{}
	c4 := testClient(upstreamhttpStub(rec4, func(q upstreamhttpReq) (*http.Response, error) {
		if q.Path == "/auth/realms/copilot/overseas/user/register" {
			return jsonResp(200, `{"code":500,"msg":"region required"}`), nil
		}
		return jsonResp(200, `{"code":403,"msg":"activity closed"}`), nil
	}))
	c4.BillingBaseGlobal = "https://global.example"
	if _, err := c4.GlobalCompleteRegistration(upstreamhttpGlobalAuth()); err == nil ||
		!strings.Contains(err.Error(), "fetch countries") {
		t.Errorf("err=%v want fetch countries: ...", err)
	}
	if containsJoin(rec4.pathSet(), "/console/login/account") {
		t.Errorf("拉列表失败后不得提交地区：%v", rec4.paths())
	}

	// 提交地区失败 → 错误带上下文。
	rec5 := &upstreamhttpRec{}
	var registerHits5 atomic.Int32
	c5 := testClient(upstreamhttpStub(rec5, func(q upstreamhttpReq) (*http.Response, error) {
		switch q.Path {
		case "/auth/realms/copilot/overseas/user/register":
			if registerHits5.Add(1) == 1 {
				return jsonResp(200, `{"code":500,"msg":"region required"}`), nil
			}
			return jsonResp(200, `{"code":200,"msg":"ok"}`), nil
		case "/billing/area/get-country-code":
			return jsonResp(200, upstreamhttpCountriesResp(t, upstreamhttpCountriesInner)), nil
		default:
			return jsonResp(200, `{"code":10001,"msg":"invalid country"}`), nil
		}
	}))
	c5.BillingBaseGlobal = "https://global.example"
	if _, err := c5.GlobalCompleteRegistration(upstreamhttpGlobalAuth()); err == nil ||
		!strings.Contains(err.Error(), "submit region") {
		t.Errorf("err=%v want submit region: ...", err)
	}
}

// ---------------------------------------------------------------------------
// global_models.go（探测路径）
// ---------------------------------------------------------------------------

// upstreamhttpV3ConfigBody /v3/config 按 UA 下发不同模型集（三路缺一不可）。
func upstreamhttpV3ConfigBody(ua string) string {
	switch {
	case strings.Contains(ua, "WorkBuddy AI"):
		return upstreamhttpEnvelope(`{"models":[` +
			`{"id":"gpt-6-sol","maxInputTokens":1000000,"maxOutputTokens":128000,"reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}},` +
			`{"id":"desktop-only","maxInputTokens":500000,"maxOutputTokens":64000}]}`)
	case strings.Contains(ua, "CodeBuddyIDE"):
		return upstreamhttpEnvelope(`{"models":[` +
			`{"id":"gpt-6-sol","maxInputTokens":176000,"maxOutputTokens":24000},` +
			`{"id":"ide-only","maxInputTokens":200000,"maxOutputTokens":32000}]}`)
	default:
		return upstreamhttpEnvelope(`{"models":[` +
			`{"id":"gpt-6-sol","maxInputTokens":272000,"maxOutputTokens":72000},` +
			`{"id":"cli-only","maxInputTokens":256000,"maxOutputTokens":32000}]}`)
	}
}

const upstreamhttpEnterpriseModelsBody = `{"code":0,"msg":"ok","data":{"models":[` +
	`{"id":"gpt-6-sol","maxInputTokens":1,"maxOutputTokens":999},` +
	`{"id":"gpt-5.3-codex","maxInputTokens":272000,"maxOutputTokens":72000,` +
	`"reasoning":{"supportedEfforts":["low","medium","high"],"defaultEffort":"medium"}}]}}`

// upstreamhttpGlobalModelsClient 装配 global 探测用 client（GlobalEnabled 必须显式打开，
// 否则 globalOn 兜底返回 nil 且零上游调用）。
func upstreamhttpGlobalModelsClient(rec *upstreamhttpRec) *Client {
	c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
		switch q.Path {
		case "/v3/config":
			return jsonResp(200, upstreamhttpV3ConfigBody(q.Header.Get("User-Agent"))), nil
		case "/v2/enterprises/personal/models":
			return jsonResp(200, upstreamhttpEnterpriseModelsBody), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", q.Path)
		}
	}))
	c.GlobalEnabled = true
	c.ChatBaseGlobal = "https://global-chat.example"
	return c
}

// TestUpstreamHTTPFetchGlobalModelsProbeAndCache 四路并发探测（v3 三 UA + 企业端点）并集
// 合并、v3 字段权威、effort 桶落 global 域，且第二次调用走缓存（零上游调用）。
func TestUpstreamHTTPFetchGlobalModelsProbeAndCache(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := upstreamhttpGlobalModelsClient(rec)
	a := upstreamhttpGlobalAuth()

	names := c.FetchGlobalModels(a)
	// v3 路内部顺序由 probeV3 的 map 迭代决定（infos 未排序）→ 只断言集合与
	// 「企业端点补缺项追加在末位」（后者由 mergeGlobalCatalog 保证稳定）。
	want := []string{"desktop-only", "gpt-6-sol", "ide-only", "cli-only", "gpt-5.3-codex"}
	if !upstreamhttpSameSet(names, want) {
		t.Fatalf("names=%v want 集合 %v", names, want)
	}
	if len(names) == 0 || names[len(names)-1] != "gpt-5.3-codex" {
		t.Errorf("names=%v want 末位为 gpt-5.3-codex（企业端点补缺追加）", names)
	}
	calls := rec.snapshot()
	if len(calls) != 4 {
		t.Fatalf("首次探测请求数=%d want 4（3 路 /v3/config + 1 企业端点）: %v", len(calls), rec.paths())
	}
	// 三路 UA 缺一不可。
	var v3UAs []string
	enterpriseHits := 0
	for _, call := range calls {
		switch call.Path {
		case "/v3/config":
			v3UAs = append(v3UAs, call.Header.Get("User-Agent"))
			if call.Header.Get("Authorization") != "Bearer at-global" {
				t.Errorf("v3/config Authorization=%q", call.Header.Get("Authorization"))
			}
		case "/v2/enterprises/personal/models":
			enterpriseHits++
		}
	}
	if enterpriseHits != 1 {
		t.Errorf("企业端点请求数=%d want 1", enterpriseHits)
	}
	sawDesktop, sawIDE, sawCLI := false, false, false
	for _, ua := range v3UAs {
		switch {
		case strings.Contains(ua, "WorkBuddy AI"):
			sawDesktop = true
		case strings.Contains(ua, "CodeBuddyIDE"):
			sawIDE = true
		default:
			sawCLI = true
		}
	}
	if !sawDesktop || !sawIDE || !sawCLI {
		t.Errorf("v3 三路 UA=%v want 桌面/IDE/CLI 各一（缺路会漏模型）", v3UAs)
	}
	if ua := c.defaultWorkBuddyUAFor(a); !strings.Contains(ua, "WorkBuddy AI") {
		t.Errorf("global 桌面端 UA=%q want 含 WorkBuddy AI", ua)
	}

	infos := c.FetchGlobalModelInfos(a)
	if len(infos) != len(want) {
		t.Fatalf("infos=%d want %d", len(infos), len(want))
	}
	var sol *ModelInfo
	for i := range infos {
		if infos[i].ID == "gpt-6-sol" {
			sol = &infos[i]
		}
	}
	if sol == nil {
		t.Fatal("infos 缺 gpt-6-sol")
	}
	// v3 桌面路字段权威：企业端点同 id 条目（1000000→1 / 128000→999）不得覆盖。
	if sol.ContextWindow != 1000000 || sol.MaxTokens != 128000 {
		t.Errorf("gpt-6-sol 窗口=%d/%d want 1000000/128000（v3 主路权威）", sol.ContextWindow, sol.MaxTokens)
	}
	if len(sol.Efforts) != 2 || sol.Efforts[0] != "low" || sol.Efforts[1] != "high" || sol.DefaultEffort != "high" {
		t.Errorf("gpt-6-sol efforts=%v default=%q", sol.Efforts, sol.DefaultEffort)
	}
	// effort 桶落 global 域（不是 cn）；企业端点补缺模型的档位也要并进桶。
	efforts, defaults := c.GlobalEffortSnapshot()
	if len(efforts["gpt-6-sol"]) != 2 || defaults["gpt-6-sol"] != "high" {
		t.Errorf("global effort 桶=%v/%v want gpt-6-sol:[low high]/high", efforts, defaults)
	}
	if len(efforts["gpt-5.3-codex"]) != 3 || defaults["gpt-5.3-codex"] != "medium" {
		t.Errorf("企业端点补缺模型的档位未并入: efforts=%v defaults=%v", efforts, defaults)
	}
	if _, ok := c.effortsSnapshot("cn")["gpt-6-sol"]; ok {
		t.Error("global 探测不得污染 cn effort 桶")
	}

	// 缓存：TTL 内第二次调用零上游请求。
	before := rec.count()
	if got := c.FetchGlobalModels(a); !upstreamhttpSameSet(got, want) {
		t.Errorf("缓存命中 names=%v want %v", got, want)
	}
	if after := rec.count(); after != before {
		t.Errorf("缓存命中仍打了上游：%d → %d", before, after)
	}
}

// TestUpstreamHTTPFetchGlobalModelsPartialFailure 单路失败降级：企业端点失败 → 只用 v3；
// v3 全失败 → 只用企业端点（两条降级分支互不拖累）。
func TestUpstreamHTTPFetchGlobalModelsPartialFailure(t *testing.T) {
	// 企业端点 500。
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
		if q.Path == "/v3/config" {
			return jsonResp(200, upstreamhttpV3ConfigBody(q.Header.Get("User-Agent"))), nil
		}
		return jsonResp(500, `{"code":10000,"msg":"boom"}`), nil
	}))
	c.GlobalEnabled = true
	c.ChatBaseGlobal = "https://global-chat.example"
	names := c.FetchGlobalModels(upstreamhttpGlobalAuth())
	want := []string{"desktop-only", "gpt-6-sol", "ide-only", "cli-only"}
	if !upstreamhttpSameSet(names, want) {
		t.Errorf("企业端点失败时 names=%v want 集合 %v", names, want)
	}
	if containsJoin(names, "gpt-5.3-codex") {
		t.Errorf("企业端点失败不得混入其独有模型: %v", names)
	}

	// v3 三路 500，企业端点在 /console fallback 也失败 → nil + 负缓存。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 500, `{"code":10000,"msg":"boom"}`))
	c2.GlobalEnabled = true
	c2.ChatBaseGlobal = "https://global-chat.example"
	if got := c2.FetchGlobalModels(upstreamhttpGlobalAuth()); got != nil {
		t.Errorf("全失败 want nil，got %v（不得回落静态名单）", got)
	}
	if !containsJoin(rec2.pathSet(), "/console/enterprises/personal/models") {
		t.Errorf("/v2 失败后应 fallback /console：%v", rec2.paths())
	}
	// 负缓存：冷却期内不再打上游。
	before := rec2.count()
	if got := c2.FetchGlobalModelInfos(upstreamhttpGlobalAuth()); got != nil {
		t.Errorf("负缓存期 want nil infos，got %v", got)
	}
	if after := rec2.count(); after != before {
		t.Errorf("负缓存期内仍打了上游：%d → %d", before, after)
	}

	// v3 全失败 + 企业端点成功 → 用企业端点结果。
	rec3 := &upstreamhttpRec{}
	c3 := testClient(upstreamhttpStub(rec3, func(q upstreamhttpReq) (*http.Response, error) {
		if q.Path == "/v2/enterprises/personal/models" {
			return jsonResp(200, upstreamhttpEnterpriseModelsBody), nil
		}
		return jsonResp(503, `{"code":10000,"msg":"down"}`), nil
	}))
	c3.GlobalEnabled = true
	c3.ChatBaseGlobal = "https://global-chat.example"
	got3 := c3.FetchGlobalModels(upstreamhttpGlobalAuth())
	if !upstreamhttpSameSet(got3, []string{"gpt-6-sol", "gpt-5.3-codex"}) {
		t.Errorf("v3 失败时 names=%v want 集合 [gpt-6-sol gpt-5.3-codex]", got3)
	}
}

// TestUpstreamHTTPFetchGlobalModelsBodyReadError 半截 body（读失败）必须当失败处理：
// 不进解析、不落成功缓存，并写负缓存止损。
func TestUpstreamHTTPFetchGlobalModelsBodyReadError(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStub(rec, func(upstreamhttpReq) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       upstreamhttpErrBody{},
		}, nil
	}))
	c.GlobalEnabled = true
	c.ChatBaseGlobal = "https://global-chat.example"
	a := upstreamhttpGlobalAuth()

	if got := c.FetchGlobalModels(a); got != nil {
		t.Errorf("读 body 失败 want nil，got %v", got)
	}
	before := rec.count()
	if got := c.FetchGlobalModels(a); got != nil {
		t.Errorf("负缓存期 want nil，got %v", got)
	}
	if after := rec.count(); after != before {
		t.Errorf("负缓存期内不应打上游：%d → %d", before, after)
	}
}

// TestUpstreamHTTPFetchGlobalModelsNonGlobalAccount 非 global / 逃生门关闭：不探测
// （globalOn 兜底），零上游调用。
func TestUpstreamHTTPFetchGlobalModelsNonGlobalAccount(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := upstreamhttpGlobalModelsClient(rec)

	if got := c.FetchGlobalModels(upstreamhttpCNAuth()); got != nil {
		t.Errorf("CN 账号 want nil，got %v", got)
	}
	if got := c.FetchGlobalModels(upstreamhttpGlobalAuth()); got == nil {
		t.Error("GlobalEnabled=true 的 global 账号应能探测")
	}
	// 逃生门：GlobalEnabled=false 时即便账号是 global 也不探测。
	rec2 := &upstreamhttpRec{}
	c2 := upstreamhttpGlobalModelsClient(rec2)
	c2.GlobalEnabled = false
	if got := c2.FetchGlobalModels(upstreamhttpGlobalAuth()); got != nil {
		t.Errorf("逃生门关闭 want nil，got %v", got)
	}
	if n := rec2.count(); n != 0 {
		t.Errorf("逃生门关闭时请求数=%d want 0", n)
	}
}

// containsJoin 判断切片里是否含指定元素。
func containsJoin(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestUpstreamHTTPExtractEffortsAndNarrowMerge effort 抽取（空 id 跳过）与窄表合并
// （企业端点只给 id 名单时不得编造对象字段）。
func TestUpstreamHTTPExtractEffortsAndNarrowMerge(t *testing.T) {
	names, infos, efforts, defaults := extractEfforts([]ModelInfo{
		{ID: ""}, // 空 id 条目必须跳过（否则会写出空模型名的档位）
		{ID: "a", Efforts: []string{"low"}, DefaultEffort: "low"},
		{ID: "b"},
	})
	if !upstreamhttpStringsEqual(names, []string{"a", "b"}) || len(infos) != 2 {
		t.Errorf("names=%v infos=%d want [a b]/2（空 id 被跳过）", names, len(infos))
	}
	if len(efforts) != 1 || efforts["a"][0] != "low" || len(defaults) != 1 || defaults["a"] != "low" {
		t.Errorf("efforts=%v defaults=%v want 仅 a 入桶", efforts, defaults)
	}

	// 窄表（secondaryInfos == nil）：只并 id，不编造字段。
	gotNames, gotInfos := mergeGlobalCatalog([]string{"a", "a", ""}, nil, []string{"b", "a", ""}, nil)
	if !upstreamhttpStringsEqual(gotNames, []string{"a", "b"}) {
		t.Errorf("窄表合并 names=%v want [a b]（去重 + 空串剔除）", gotNames)
	}
	if gotInfos != nil {
		t.Errorf("窄表合并不得编造 infos，got %v", gotInfos)
	}
	// secondary 为空 → 原样返回主路。
	gotNames, _ = mergeGlobalCatalog([]string{"a"}, nil, nil, nil)
	if !upstreamhttpStringsEqual(gotNames, []string{"a"}) {
		t.Errorf("secondary 为空时应原样返回主路，got %v", gotNames)
	}

	// 主路桶为空（nil）而 secondary 有档位 → 用 secondary 建桶（不得丢档）。
	sec := map[string][]string{"x": {"low"}}
	if got := mergeEffortBuckets(nil, sec); len(got) != 1 || got["x"][0] != "low" {
		t.Errorf("主路 nil 时应由 secondary 建桶，got %v", got)
	}
	// 主路已有同 id 档位 → 主路权威，secondary 只补缺失。
	prim := map[string][]string{"x": {"high"}}
	if got := mergeEffortBuckets(prim, sec); len(got["x"]) != 1 || got["x"][0] != "high" {
		t.Errorf("主路档位应权威，got %v", got)
	}
	secDef := map[string]string{"x": "low", "y": "medium"}
	if got := mergeEffortDefaults(nil, secDef); got["y"] != "medium" {
		t.Errorf("主路 nil 时应由 secondary 建桶，got %v", got)
	}
	if got := mergeEffortDefaults(map[string]string{"x": "high"}, secDef); got["x"] != "high" || got["y"] != "medium" {
		t.Errorf("主路默认档应权威且补缺，got %v", got)
	}
}

// TestUpstreamHTTPParseGlobalModelNames 多信封/多形态解析：对象数组（含 id 回退与
// disabled 剔除）、窄表字符串数组、顶层数组、result 键、混合数组宽松兜底、空列表报错。
func TestUpstreamHTTPParseGlobalModelNames(t *testing.T) {
	t.Run("对象数组全字段与id回退", func(t *testing.T) {
		raw := `{"code":0,"data":{"models":[` +
			`{"id":"gpt-6-sol","maxInputTokens":1000000,"maxOutputTokens":128000,` +
			`"reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}},` +
			`{"name":"named-only","maxInputTokens":200000},` +
			`{"id":"vid","disabled":true},` +
			`{"model":"loose-model","maxInputTokens":3000}]}}`
		names, infos, efforts, defaults, err := parseGlobalModelNames([]byte(raw))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !upstreamhttpStringsEqual(names, []string{"gpt-6-sol", "named-only", "loose-model"}) {
			t.Fatalf("names=%v want [gpt-6-sol named-only loose-model]（disabled 剔除 + name/model 回退）", names)
		}
		if len(infos) != 3 || infos[0].ContextWindow != 1000000 || infos[0].MaxTokens != 128000 {
			t.Errorf("infos=%+v", infos)
		}
		if infos[1].ID != "named-only" || infos[1].ContextWindow != 200000 {
			t.Errorf("name 兜底条目=%+v", infos[1])
		}
		if infos[2].ID != "loose-model" {
			t.Errorf("model 兜底条目=%+v", infos[2])
		}
		if len(efforts["gpt-6-sol"]) != 2 || efforts["gpt-6-sol"][0] != "low" {
			t.Errorf("efforts=%v", efforts)
		}
		if defaults["gpt-6-sol"] != "high" {
			t.Errorf("defaults=%v", defaults)
		}
	})

	t.Run("窄表字符串数组", func(t *testing.T) {
		names, infos, efforts, defaults, err := parseGlobalModelNames([]byte(`{"code":0,"data":{"items":["glm-5.2"," kimi-k3 ","",  "  "]}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !upstreamhttpStringsEqual(names, []string{"glm-5.2", "kimi-k3"}) {
			t.Errorf("names=%v want [glm-5.2 kimi-k3]（trim + 空串剔除）", names)
		}
		if infos != nil || efforts != nil || defaults != nil {
			t.Errorf("窄表不得编造元数据: infos=%v efforts=%v defaults=%v", infos, efforts, defaults)
		}
	})

	t.Run("data下裸数组", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"code":0,"data":[{"id":"a"},{"id":"b"}]}`))
		if err != nil || !upstreamhttpStringsEqual(names, []string{"a", "b"}) {
			t.Errorf("names=%v err=%v want [a b]/nil", names, err)
		}
	})

	t.Run("顶层裸数组被信封步骤拒绝", func(t *testing.T) {
		// 观察到的现状：resolveGlobalModelsArray 支持数组，但外层信封先用
		// map[string]json.RawMessage 解 → 顶层裸数组在信封步骤即失败。
		// 上游当前不下发该形态；若将来支持，删掉这条断言即可。
		if _, _, _, _, err := parseGlobalModelNames([]byte(`[{"id":"a"},{"id":"b"}]`)); err == nil {
			t.Error("顶层裸数组当前应被拒（want err）")
		}
	})

	t.Run("result键下钻", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"code":0,"data":{"result":[{"id":"r1"}]}}`))
		if err != nil || !upstreamhttpStringsEqual(names, []string{"r1"}) {
			t.Errorf("names=%v err=%v want [r1]/nil", names, err)
		}
	})

	t.Run("多层下钻list.items", func(t *testing.T) {
		names, _, _, _, err := parseGlobalModelNames([]byte(`{"code":0,"data":{"list":{"items":[{"id":"deep"}]}}}`))
		if err != nil || !upstreamhttpStringsEqual(names, []string{"deep"}) {
			t.Errorf("names=%v err=%v want [deep]/nil（递归下钻）", names, err)
		}
	})

	t.Run("混合数组宽松兜底", func(t *testing.T) {
		names, infos, efforts, defaults, err := parseGlobalModelNames([]byte(`{"code":0,"data":{"models":[true,"raw-id",{"model":"legacy-1","contextWindow":4096,"maxTokens":2048,"reasoning":{"effort":"low"}}]}}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !upstreamhttpStringsEqual(names, []string{"raw-id", "legacy-1"}) {
			t.Fatalf("names=%v want [raw-id legacy-1]", names)
		}
		if len(infos) != 2 || infos[0].ID != "raw-id" || infos[1].ContextWindow != 4096 || infos[1].MaxTokens != 2048 {
			t.Errorf("infos=%+v", infos)
		}
		if defaults["legacy-1"] != "low" || efforts != nil {
			t.Errorf("宽松路径 effort 桶=%v/%v want nil/{legacy-1:low}", efforts, defaults)
		}
	})

	t.Run("业务码非0拒绝", func(t *testing.T) {
		if _, _, _, _, err := parseGlobalModelNames([]byte(`{"code":11102,"data":{"models":[{"id":"x"}]}}`)); err == nil {
			t.Error("code!=0 want err")
		}
	})

	t.Run("无可解析条目报空", func(t *testing.T) {
		for _, raw := range []string{
			`{"code":0,"data":{"foo":1}}`,
			`{"code":0,"data":{"models":[{"vendor":"x"}]}}`,
			`{"code":0,"data":{"models":[]}}`,
			`{"code":0,"data":{"models":[true,{"model":"off","disabled":true}]}}`,
		} {
			if _, _, _, _, err := parseGlobalModelNames([]byte(raw)); err == nil {
				t.Errorf("raw=%s want err（空列表/无可解析条目等价失败）", raw)
			}
		}
		if _, _, _, _, err := parseGlobalModelNames([]byte(`<html>`)); err == nil {
			t.Error("非 JSON want err")
		}
	})
}

// ---------------------------------------------------------------------------
// blackcat.go
// ---------------------------------------------------------------------------

// TestUpstreamHTTPClaimGiftAndCompensation 新手礼包/补偿：路径 + credit 解析。
//
// 语义变更说明（R10 + 四态语义修复）：过去 data 非对象时用 `_ = json.Unmarshal` 吞掉错误，
// 返回 (0, nil)，把 unknown 冒充成「观测到领了 0 信用」——而调用方 streak.go 在 err==nil
// 时会打日志「🎊 新手礼包 +0c」，等于写下一个凭空捏造的事实。现在统一由 claimCredit 处理：
// 解析失败与缺 credit 字段都返回 error（unknown），只有真正观测到数值才返回 (credit, nil)。
func TestUpstreamHTTPClaimGiftAndCompensation(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(`{"credit":500}`)))
	a := upstreamhttpCNAuth()

	credit, err := c.ClaimGift(a)
	if err != nil || credit != 500 {
		t.Fatalf("ClaimGift=%d/%v want 500/nil", credit, err)
	}
	if req := rec.one(t, "/billing/meter/claim-gift"); req.Method != http.MethodPost || string(req.Body) != "{}" {
		t.Errorf("claim-gift 请求=%s body=%s want POST/{}", req.Method, req.Body)
	}

	credit, err = c.ClaimCompensation(a)
	if err != nil || credit != 500 {
		t.Fatalf("ClaimCompensation=%d/%v want 500/nil", credit, err)
	}
	if req := rec.one(t, "/billing/meter/claim-compensation"); req.Host != "billing.example" || req.Method != http.MethodPost {
		t.Errorf("claim-compensation 请求=%s %s", req.Method, req.Host)
	}

	// data 非对象：不 panic，且**不许**返回 0/nil（那会把 unknown 冒充成"观测到 0"），
	// 必须返回错误让调用方知道积分未知。
	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, `{"code":0,"msg":"ok","data":"oops-not-json"}`))
	if got, err := c2.ClaimGift(a); err == nil || got != 0 {
		t.Errorf("非对象 data: got %d/%v want 0/err（unknown 必须报错，不得冒充 0）", got, err)
	}
	if got, err := c2.ClaimCompensation(a); err == nil || got != 0 {
		t.Errorf("非对象 data(compensation): got %d/%v want 0/err", got, err)
	}
	// 缺 credit 字段（合法 JSON 对象但字段缺失）：同样是 unknown，不是 0。
	rec4 := &upstreamhttpRec{}
	c4 := testClient(upstreamhttpFixed(rec4, 200, `{"code":0,"msg":"ok","data":{"unrelated":1}}`))
	if got, err := c4.ClaimGift(a); err == nil || got != 0 {
		t.Errorf("缺 credit 字段: got %d/%v want 0/err（缺字段=unknown≠0）", got, err)
	}
	// credit 显式为 0：这是**真实观测**，必须返回 (0, nil) 而非错误——
	// 与上面两种 unknown 严格区分，正是 *int64 指针的意义。
	rec5 := &upstreamhttpRec{}
	c5 := testClient(upstreamhttpFixed(rec5, 200, `{"code":0,"msg":"ok","data":{"credit":0}}`))
	if got, err := c5.ClaimGift(a); err != nil || got != 0 {
		t.Errorf("credit=0（显式观测）: got %d/%v want 0/nil", got, err)
	}
	// 业务错误：返回错误且不误报积分。
	rec3 := &upstreamhttpRec{}
	c3 := testClient(upstreamhttpFixed(rec3, 200, `{"code":10001,"msg":"企业账号不支持该操作"}`))
	if got, err := c3.ClaimGift(a); err == nil || got != 0 {
		t.Errorf("业务错误: got %d/%v want 0/err", got, err)
	}
	if got, err := c3.ClaimCompensation(a); err == nil || got != 0 {
		t.Errorf("业务错误(compensation): got %d/%v want 0/err", got, err)
	}
}

// TestUpstreamHTTPHeatmapYesterdayMissed 昨日漏签判定：只认昨日 cell，date 带时间部分
// 也要能对上（前 10 字符比较），非 JSON 报错。
func TestUpstreamHTTPHeatmapYesterdayMissed(t *testing.T) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")

	cases := []struct {
		name string
		body string
		want bool
	}{
		{"昨日 score=0 漏签", upstreamhttpEnvelope(`{"cells":[{"date":"` + yesterday + `","score":0}]}`), true},
		{"昨日 score>0 未漏签", upstreamhttpEnvelope(`{"cells":[{"date":"` + yesterday + `","score":5},{"date":"` + today + `","score":1}]}`), false},
		{"无昨日 cell 不误报", upstreamhttpEnvelope(`{"cells":[{"date":"` + today + `","score":0}]}`), false},
		{"date 带时分秒", upstreamhttpEnvelope(`{"cells":[{"date":"` + yesterday + `T00:00:00+08:00","score":0}]}`), true},
		{"date 过短被跳过", upstreamhttpEnvelope(`{"cells":[{"date":"09-12","score":0},{"date":"` + yesterday + `","score":0}]}`), true},
		{"cells 为空", upstreamhttpEnvelope(`{"cells":[]}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &upstreamhttpRec{}
			c := testClient(upstreamhttpFixed(rec, 200, tc.body))
			got, err := c.HeatmapYesterdayMissed(upstreamhttpCNAuth())
			if err != nil {
				t.Fatalf("HeatmapYesterdayMissed: %v", err)
			}
			if got != tc.want {
				t.Errorf("missed=%v want %v", got, tc.want)
			}
			if req := rec.one(t, "/activity/growth/heatmap"); req.Method != http.MethodGet || req.Host != "chat.example" {
				t.Errorf("heatmap 请求=%s %s want GET/chat.example", req.Method, req.Host)
			}
		})
	}

	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(`"oops"`)))
	if _, err := c.HeatmapYesterdayMissed(upstreamhttpCNAuth()); err == nil {
		t.Error("非对象 data want err")
	}
}

// TestUpstreamHTTPUseMakeupCard 补签卡：路径 body 的 target_date 必须是入参日期。
func TestUpstreamHTTPUseMakeupCard(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 200, upstreamhttpEnvelope(`{}`)))
	if err := c.UseMakeupCard(upstreamhttpCNAuth(), "2026-09-10"); err != nil {
		t.Fatalf("UseMakeupCard: %v", err)
	}
	req := rec.one(t, "/activity/growth/makeup-cards/use")
	if req.Method != http.MethodPost || req.Host != "chat.example" {
		t.Errorf("请求=%s %s want POST/chat.example", req.Method, req.Host)
	}
	body := upstreamhttpObject(t, req.Body)
	if len(body) != 1 || body["target_date"] != "2026-09-10" {
		t.Errorf("body=%v want {target_date:2026-09-10}", body)
	}

	rec2 := &upstreamhttpRec{}
	c2 := testClient(upstreamhttpFixed(rec2, 200, `{"code":12001,"msg":"no makeup card"}`))
	if err := c2.UseMakeupCard(upstreamhttpCNAuth(), "2026-09-10"); err == nil {
		t.Error("无卡业务错误 want err")
	}
}

// TestUpstreamHTTPRunNightChatsSuccess 夜猫子成功路径：真的发 glm-5.2 流式对话并对齐
// 模型事件链；返回成功次数 1。
func TestUpstreamHTTPRunNightChatsSuccess(t *testing.T) {
	sse := "data: {\"id\":\"wb2api-1\",\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\ndata: [DONE]\n\n"
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
		switch q.Path {
		case "/v2/chat/completions":
			return jsonResp(200, sse), nil
		case "/v2/report":
			return jsonResp(200, `{"code":0,"msg":"OK"}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", q.Path)
		}
	}))
	a := upstreamhttpCNAuth()

	// N4：夜猫子的 4s 间隔改为可注入——本用例只验 HTTP 形状，不欠真实等待。
	// 生产默认间隔（4s）本身由 blackcat_test.go 的 TestNightChatsGapIsInjectable 钉住。
	c.SetNightChatSleep(func(time.Duration) {})

	ok, err := c.RunNightChats(a, 1)
	if err != nil {
		t.Fatalf("RunNightChats: %v", err)
	}
	if ok != 1 {
		t.Errorf("ok=%d want 1", ok)
	}
	chat := rec.one(t, "/v2/chat/completions")
	if chat.Host != "chat.example" {
		t.Errorf("chat host=%s want chat.example", chat.Host)
	}
	body := upstreamhttpObject(t, chat.Body)
	if body["model"] != "glm-5.2" || body["stream"] != true {
		t.Errorf("夜猫子对话必须用 glm-5.2 流式，got %v", body)
	}
	report := rec.one(t, "/v2/report")
	if report.Host != "billing.example" {
		t.Errorf("上报 host=%s want billing.example", report.Host)
	}
	arr := upstreamhttpArray(t, report.Body)
	if len(arr) != 1 || arr[0]["requestModelId"] != "glm-5.2" || arr[0]["requestModelName"] != "GLM-5.2" {
		t.Errorf("上报事件=%v want requestModelId=glm-5.2/requestModelName=GLM-5.2", arr)
	}
	if arr[0]["eventCode"] != "chat_request_send" || arr[0]["userId"] != "u-cn" {
		t.Errorf("上报事件缺 code/userId: %v", arr[0])
	}
}

// TestUpstreamHTTPRunNightChatsChatFailure 对话失败立即中止：ok=0、错误带次数与状态码，
// 且不得上报事件（避免拿假事件骗判据）。
func TestUpstreamHTTPRunNightChatsChatFailure(t *testing.T) {
	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpStub(rec, func(q upstreamhttpReq) (*http.Response, error) {
		if q.Path == "/v2/chat/completions" {
			return jsonResp(500, `{"code":10000,"msg":"boom"}`), nil
		}
		return jsonResp(200, `{"code":0}`), nil
	}))
	ok, err := c.RunNightChats(upstreamhttpCNAuth(), 2)
	if err == nil {
		t.Fatal("want err on chat failure")
	}
	if ok != 0 {
		t.Errorf("ok=%d want 0", ok)
	}
	if !strings.Contains(err.Error(), "第 1 次对话失败") || !strings.Contains(err.Error(), "http=500") {
		t.Errorf("err=%v want 含 第 1 次对话失败/http=500", err)
	}
	if n := rec.count(); n != 1 {
		t.Errorf("失败后不应继续后续次数，请求数=%d want 1", n)
	}
	if containsJoin(rec.pathSet(), "/v2/report") {
		t.Errorf("对话失败不得上报事件：%v", rec.paths())
	}
}

// TestUpstreamHTTPBlackcatNeed 夜猫子差额：未接受/进行中算差额，已达标或已领取为 0。
func TestUpstreamHTTPBlackcatNeed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int64
	}{
		{"进行中", upstreamhttpEnvelope(`{"tasks":[{"task_code":"black_cat","target":3,"current":1}]}`), 2},
		{"已达标", upstreamhttpEnvelope(`{"tasks":[{"task_code":"black_cat","target":3,"current":3}]}`), 0},
		{"已领取", upstreamhttpEnvelope(`{"tasks":[{"task_code":"black_cat","target":3,"current":1,"accept_status":"claimed"}]}`), 0},
		{"任务不存在", upstreamhttpEnvelope(`{"tasks":[{"task_code":"chat_5","target":1,"current":0}]}`), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &upstreamhttpRec{}
			c := testClient(upstreamhttpFixed(rec, 200, tc.body))
			got, err := c.BlackcatNeed(upstreamhttpCNAuth())
			if err != nil {
				t.Fatalf("BlackcatNeed: %v", err)
			}
			if got != tc.want {
				t.Errorf("need=%d want %d", got, tc.want)
			}
		})
	}

	rec := &upstreamhttpRec{}
	c := testClient(upstreamhttpFixed(rec, 500, `{"code":10000,"msg":"boom"}`))
	if _, err := c.BlackcatNeed(upstreamhttpCNAuth()); err == nil {
		t.Error("拉取失败 want err（不能当 0 差额静默跳过）")
	}
}

// TestUpstreamHTTPInNightWindow 夜猫子窗口边界（23:00–08:00 本地时区）。
func TestUpstreamHTTPInNightWindow(t *testing.T) {
	cases := []struct {
		hour, min int
		want      bool
	}{
		{23, 0, true}, {23, 59, true}, {0, 0, true}, {7, 59, true},
		{8, 0, false}, {12, 0, false}, {22, 59, false},
	}
	for _, tc := range cases {
		at := time.Date(2026, 9, 12, tc.hour, tc.min, 0, 0, time.Local)
		if got := InNightWindow(at); got != tc.want {
			t.Errorf("InNightWindow(%s)=%v want %v", at.Format("15:04"), got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// trial.go
// ---------------------------------------------------------------------------

// TestUpstreamHTTPClaimTrial 一次性加油包：非 global 拒绝；成功 true；幂等码 14051 两种
// 指纹（业务码 / HTTP≥400 原始 body）都算"已领过"而非错误。
func TestUpstreamHTTPClaimTrial(t *testing.T) {
	newClient := func(body string, status int) (*Client, *upstreamhttpRec) {
		rec := &upstreamhttpRec{}
		c := testClient(upstreamhttpFixed(rec, status, body))
		c.BillingBaseGlobal = "https://global-billing.example"
		// globalOn 是 config 侧第一道闸：不开则 billingBase 回落 CN base（双保险）。
		c.GlobalEnabled = true
		return c, rec
	}

	// 非 global：本地拒绝，零上游调用。
	rec := &upstreamhttpRec{}
	cn := testClient(upstreamhttpFixed(rec, 200, `{"code":0}`))
	cn.BillingBaseGlobal = "https://global-billing.example"
	cn.GlobalEnabled = true
	if _, err := cn.ClaimTrial(upstreamhttpCNAuth()); err == nil {
		t.Fatal("CN 账号 want err（CN 无 trial 端点）")
	}
	if n := rec.count(); n != 0 {
		t.Errorf("CN 账号不应打上游，请求数=%d", n)
	}
	if _, err := cn.ClaimTrial(nil); err == nil {
		t.Fatal("nil auth want err")
	}

	// 成功新领。
	c, recOK := newClient(`{"code":0,"msg":"ok","data":{"credit":1000}}`, 200)
	claimed, err := c.ClaimTrial(upstreamhttpGlobalAuth())
	if err != nil || !claimed {
		t.Fatalf("ClaimTrial=%v/%v want true/nil", claimed, err)
	}
	req := recOK.one(t, "/billing/ide/trial")
	if req.Method != http.MethodPost || req.Host != "global-billing.example" {
		t.Errorf("请求=%s %s want POST/global-billing.example", req.Method, req.Host)
	}
	if len(req.Body) != 0 {
		t.Errorf("trial 不应带 body，got %q", req.Body)
	}
	if req.Header.Get("Authorization") != "Bearer at-global" {
		t.Errorf("Authorization=%q", req.Header.Get("Authorization"))
	}

	// 幂等码 14051（HTTP 200 + 业务码）：已领过 → false/nil。
	c2, _ := newClient(`{"code":14051,"msg":"already claimed"}`, 200)
	if claimed, err := c2.ClaimTrial(upstreamhttpGlobalAuth()); err != nil || claimed {
		t.Errorf("14051(业务码) 应 false/nil，got %v/%v", claimed, err)
	}
	// 幂等码 14051（HTTP 400 原始 body 进 Msg）：同样 false/nil。
	c3, _ := newClient(`{"code":14051,"msg":"already claimed"}`, 400)
	if claimed, err := c3.ClaimTrial(upstreamhttpGlobalAuth()); err != nil || claimed {
		t.Errorf("14051(400 body) 应 false/nil，got %v/%v", claimed, err)
	}
	// 其它错误必须透出。
	c4, _ := newClient(`{"code":11101,"msg":"bad params"}`, 400)
	if claimed, err := c4.ClaimTrial(upstreamhttpGlobalAuth()); err == nil || claimed {
		t.Errorf("其他错误应 false/err，got %v/%v", claimed, err)
	}
	c5, _ := newClient(`{"code":10000,"msg":"boom"}`, 500)
	if _, err := c5.ClaimTrial(upstreamhttpGlobalAuth()); err == nil {
		t.Error("500 want err")
	}
}

// TestUpstreamHTTPTrialAlreadyErr 幂等码判定：只认 14051 的两种指纹，14052 等近似值不得命中。
func TestUpstreamHTTPTrialAlreadyErr(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"code=14051 msg=already", true},
		{`{"code":14051,"msg":"already claimed"}`, true},
		{"upstream error code=14051", true},
		{"code=14052 msg=other", false},
		{`{"code":1405,"msg":"x"}`, false},
		{"", false},
		// 观察：判定是纯子串包含，故 "code=140510" 也会命中 "code=14051"
		// （上游码恒为 5 位，实践中不构成误判；改成语义匹配时请同步改本断言）。
		{"grant code=140510", true},
	}
	for _, tc := range cases {
		if got := trialAlreadyErr(tc.msg); got != tc.want {
			t.Errorf("trialAlreadyErr(%q)=%v want %v", tc.msg, got, tc.want)
		}
	}
}
