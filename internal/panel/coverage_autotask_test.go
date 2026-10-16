// coverage_autotask_test.go —— Task 5 覆盖率补强：autotask.go / taskcenter.go 的任务执行序列。
//
// 设计要点：
//   - 上游用 http.RoundTripper 桩（caFakeUpstream）按 **路径 + Host + 请求头** 分派，
//     返回结构真实的信封 JSON（{"code":0,"data":{"tasks":[...]}}）。所有请求返回同一个
//     空对象会让「解析成功」的断言形同虚设，故每条路径都返回该端点真实形状的数据。
//   - 包级节流（reportGap / claimPollGap / mpChatEventGap …）在 caFastGaps 里临时调到
//     1ms 并在 t.Cleanup 还原（生产值不变）。
//   - 无真实网络、无真实时间依赖、无 map 迭代顺序依赖、无 sleep 同步（等待一律用
//     有界轮询 + 通道信号）。
//
// 本文件所有 helper / 类型 / 常量统一带 `ca` 前缀，避免与同包并行 agent 的
// coverage_handlers_test.go 重复声明（重复声明会让整包编译失败）。
package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// ---------------------------------------------------------------------------
// 上游桩
// ---------------------------------------------------------------------------

// caRaw 原始响应覆盖（错误注入）：命中即原样返回指定状态码与原文。
type caRaw struct {
	Status int
	Body   string
}

// caCall 一次被桩接住的出站请求。
type caCall struct {
	Method string
	Host   string
	Path   string
	Body   string
	Header http.Header
}

// caFakeUpstream 上游桩：按路径分派返回结构真实的上游响应。
type caFakeUpstream struct {
	mu sync.Mutex

	// defaultTasks / mpTasks 两套口径的任务表（mp 专属任务只在 mp 表里，模拟上游真实下发）。
	defaultTasks map[string]upstream.Task
	mpTasks      map[string]upstream.Task

	calls []caCall

	// raw 覆盖表：key = METHOD + " " + path（错误注入，命中优先于正常分派）。
	raw map[string]caRaw
	// rawTaskList 非空时作为默认口径任务列表响应体原样返回（验证真实 progress 对象形态）。
	rawTaskList string
	// defaultListRaw 非 nil 时只对「默认口径」任务列表生效（mp 口径仍正常）。
	defaultListRaw *caRaw
	// mpListRaw 非 nil 时只对 mp 口径任务列表生效（默认口径不受影响）。
	mpListRaw *caRaw
	// acceptNoOp 模拟上游「accept 返回 200 但未真正登记生效」形态（accept_status 不变）。
	acceptNoOp bool

	experts        []upstream.MarketExpert
	vouchers       []upstream.SchoolVoucher
	chatSSE        string
	claimCredit    int64
	claimEnergy    int64
	alreadyClaimed bool
	// claimKeepsState 领奖后不把 accept_status 改成 claimed（模拟上游状态未即时刷新的形态）。
	claimKeepsState bool
	// voucherErrUID 该 uid 的券码请求返回 500（其余账号正常）。
	voucherErrUID string

	// onReport 每次 /v2/report 成功响应后被调用（测试借它推进任务进度，模拟上游异步计分）。
	onReport func(caCall)
	// onList 每次「默认口径任务列表」成功后按第 n 次调用（从 1 起）回调，
	// 供测试在两次回读之间推进进度（模拟上游异步计分落账）。
	onList func(n int)
	// listCalls 默认口径任务列表的累计调用次数。
	listCalls int
	// acceptFailFirst 第一次 accept 返回 5xx（后续成功）：验证「失败重试」路径。
	acceptFailFirst bool
	acceptCalls     int

	// gate 非 nil 时，第一次「默认口径任务列表」请求阻塞到 gate 关闭（测 handler 超时分支）。
	gate    chan struct{}
	gateHit bool
}

func caNewFake() *caFakeUpstream {
	return &caFakeUpstream{
		defaultTasks: map[string]upstream.Task{},
		mpTasks:      map[string]upstream.Task{},
		raw:          map[string]caRaw{},
	}
}

// caDefaultSSE 桌面/CLI chat 的 SSE 桩：带服务端 requestId（32hex，idRegex 口径），
// DesktopChatWithExpert 从这里解析出 requestId 用于 JOIN 事件。
const caDefaultSSE = "data: {\"id\":\"cmb-0123456789abcdef0123456789abcdef\",\"object\":\"chat.completion.chunk\"," +
	"\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"

func caHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type caEnv struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

func caEnvelope(data any) string {
	raw, _ := json.Marshal(caEnv{Code: 0, Msg: "ok", Data: data})
	return string(raw)
}

// caTaskWire 任务列表里单条任务的线上形状（上游平铺字段形态）。
type caTaskWire struct {
	TaskCode     string `json:"task_code"`
	Title        string `json:"title"`
	RewardCredit int64  `json:"reward_credit"`
	RewardEnergy int64  `json:"reward_energy"`
	AcceptStatus string `json:"accept_status"`
	Status       string `json:"status"`
	Locked       bool   `json:"locked"`
	Target       int64  `json:"target"`
	Current      int64  `json:"current"`
}

type caTaskListWire struct {
	Tasks []caTaskWire `json:"tasks"`
}

func (f *caFakeUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		if raw, err := io.ReadAll(req.Body); err == nil {
			body = string(raw)
		}
		_ = req.Body.Close()
	}
	call := caCall{Method: req.Method, Host: req.Host, Path: req.URL.Path, Body: body, Header: req.Header.Clone()}

	f.mu.Lock()
	f.calls = append(f.calls, call)
	if r, ok := f.raw[req.Method+" "+req.URL.Path]; ok {
		f.mu.Unlock()
		return caHTTPResponse(r.Status, r.Body), nil
	}
	blockFirst := false
	if f.gate != nil && !f.gateHit && req.Method == http.MethodGet &&
		req.URL.Path == "/v2/activity/growth/tasks" && req.Header.Get("X-Client-Platform") == "" {
		f.gateHit, blockFirst = true, true
	}
	f.mu.Unlock()

	if blockFirst {
		<-f.gate
	}

	resp := f.dispatch(call)
	if call.Path == "/v2/report" && resp.StatusCode < 400 {
		f.mu.Lock()
		hook := f.onReport
		f.mu.Unlock()
		if hook != nil {
			hook(call)
		}
	}
	return resp, nil
}

func (f *caFakeUpstream) dispatch(call caCall) *http.Response {
	mp := call.Header.Get("X-Client-Platform") == "miniprogram"

	switch {
	case call.Method == http.MethodGet && call.Path == "/v2/activity/growth/tasks":
		f.mu.Lock()
		if mp && f.mpListRaw != nil {
			raw := *f.mpListRaw
			f.mu.Unlock()
			return caHTTPResponse(raw.Status, raw.Body)
		}
		if !mp && f.defaultListRaw != nil {
			raw := *f.defaultListRaw
			f.mu.Unlock()
			return caHTTPResponse(raw.Status, raw.Body)
		}
		if !mp && f.rawTaskList != "" {
			f.mu.Unlock()
			return caHTTPResponse(http.StatusOK, f.rawTaskList)
		}
		tbl := f.defaultTasks
		if mp {
			tbl = f.mpTasks
		}
		wire := f.taskListWireLocked(tbl)
		n, hook := 0, f.onList
		if !mp {
			f.listCalls++
			n = f.listCalls
		}
		f.mu.Unlock()
		if n > 0 && hook != nil {
			hook(n)
		}
		return caHTTPResponse(http.StatusOK, caEnvelope(wire))

	case call.Method == http.MethodPost && call.Path == "/v2/activity/growth/tasks/accept":
		var body struct {
			TaskCodes []string `json:"task_codes"`
		}
		_ = json.Unmarshal([]byte(call.Body), &body)
		f.mu.Lock()
		tbl := f.defaultTasks
		if mp {
			tbl = f.mpTasks
		}
		if !f.acceptNoOp {
			for _, code := range body.TaskCodes {
				t, ok := tbl[code]
				if !ok {
					continue
				}
				// 与上游同语义：已 accepted/claimed/completed 的不再改状态（幂等）。
				if t.AcceptStatus == "accepted" || t.AcceptStatus == "claimed" || t.AcceptStatus == "completed" {
					continue
				}
				t.AcceptStatus = "accepted"
				tbl[code] = t
			}
		}
		f.acceptCalls++
		failFirst := f.acceptFailFirst && f.acceptCalls == 1
		f.mu.Unlock()
		if failFirst {
			return caHTTPResponse(http.StatusInternalServerError, `{"code":10000,"msg":"flaky"}`)
		}
		return caHTTPResponse(http.StatusOK, caEnvelope(map[string]any{}))

	case call.Method == http.MethodPost && strings.HasPrefix(call.Path, "/activity/growth/tasks/") &&
		strings.HasSuffix(call.Path, "/claim"):
		code := strings.TrimSuffix(strings.TrimPrefix(call.Path, "/activity/growth/tasks/"), "/claim")
		f.mu.Lock()
		credit, energy, already := f.claimCredit, f.claimEnergy, f.alreadyClaimed
		tbl := f.defaultTasks
		if mp {
			tbl = f.mpTasks
		}
		if t, ok := tbl[code]; ok && !f.claimKeepsState {
			t.AcceptStatus = "claimed"
			tbl[code] = t
		}
		f.mu.Unlock()
		return caHTTPResponse(http.StatusOK, caEnvelope(map[string]any{
			"already_claimed": already, "credit": credit, "energy": energy,
		}))

	case call.Path == "/v2/report":
		return caHTTPResponse(http.StatusOK, caEnvelope(map[string]any{}))

	case call.Method == http.MethodPost && call.Path == "/portal/operation-platform/market/expert/list":
		f.mu.Lock()
		ex := append([]upstream.MarketExpert(nil), f.experts...)
		f.mu.Unlock()
		return caHTTPResponse(http.StatusOK, caEnvelope(
			struct {
				Experts []upstream.MarketExpert `json:"experts"`
			}{ex}))

	case call.Path == "/portal/activity/school/vouchers":
		f.mu.Lock()
		vs := append([]upstream.SchoolVoucher(nil), f.vouchers...)
		failUID := f.voucherErrUID
		f.mu.Unlock()
		if failUID != "" && call.Header.Get("X-User-Id") == failUID {
			return caHTTPResponse(http.StatusInternalServerError, `{"code":10000,"msg":"boom"}`)
		}
		return caHTTPResponse(http.StatusOK, caEnvelope(
			struct {
				Items []upstream.SchoolVoucher `json:"items"`
			}{vs}))

	case call.Path == "/v2/user-asset/appearance/set",
		call.Path == "/activity/growth/buddy/agreement",
		call.Path == "/activity/growth/buddy/first":
		return caHTTPResponse(http.StatusOK, caEnvelope(map[string]any{}))

	case call.Method == http.MethodPost && call.Path == "/v2/chat/completions":
		f.mu.Lock()
		sse := f.chatSSE
		f.mu.Unlock()
		if sse == "" {
			sse = caDefaultSSE
		}
		return caHTTPResponse(http.StatusOK, sse)
	}

	// 未打桩的路径明确 404：任何漏桩都会让测试直接失败，而不是悄悄返回空数据。
	return caHTTPResponse(http.StatusNotFound, `{"code":404,"msg":"unstubbed path"}`)
}

// taskListWireLocked 需要已持 f.mu。
func (f *caFakeUpstream) taskListWireLocked(tbl map[string]upstream.Task) caTaskListWire {
	codes := make([]string, 0, len(tbl))
	for code := range tbl {
		codes = append(codes, code)
	}
	sort.Strings(codes) // 稳定顺序：不依赖 map 迭代顺序
	out := caTaskListWire{Tasks: make([]caTaskWire, 0, len(codes))}
	for _, code := range codes {
		t := tbl[code]
		out.Tasks = append(out.Tasks, caTaskWire{
			TaskCode: t.TaskCode, Title: t.Title, RewardCredit: t.Credit, RewardEnergy: t.Energy,
			AcceptStatus: t.AcceptStatus, Status: t.Status, Locked: t.Locked,
			Target: t.Target, Current: t.Current,
		})
	}
	return out
}

// caSetTask 写入默认口径任务表。
func (f *caFakeUpstream) caSetTask(t upstream.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultTasks[t.TaskCode] = t
}

// caSetMPTask 写入 mp 口径任务表。
func (f *caFakeUpstream) caSetMPTask(t upstream.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mpTasks[t.TaskCode] = t
}

// caTaskState 读回任务表当前状态（模拟回读上游进度）。
func (f *caFakeUpstream) caTaskState(code string, mp bool) (upstream.Task, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tbl := f.defaultTasks
	if mp {
		tbl = f.mpTasks
	}
	t, ok := tbl[code]
	return t, ok
}

// caAdvance 模拟「上游异步计分」：把任务进度推进到指定值。
func (f *caFakeUpstream) caAdvance(code string, mp bool, current int64, accept string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tbl := f.defaultTasks
	if mp {
		tbl = f.mpTasks
	}
	t, ok := tbl[code]
	if !ok {
		t = upstream.Task{TaskCode: code}
	}
	t.Current = current
	if accept != "" {
		t.AcceptStatus = accept
	}
	tbl[code] = t
}

func (f *caFakeUpstream) caCalls(method, path string) []caCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []caCall
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (f *caFakeUpstream) caCallCount(method, path string) int {
	return len(f.caCalls(method, path))
}

// caLastReport 最后一批 /v2/report 请求（专家的召唤链与使用链是两批，使用链在后）。
func (f *caFakeUpstream) caLastReport(t *testing.T) caCall {
	t.Helper()
	reps := f.caCalls("POST", "/v2/report")
	if len(reps) == 0 {
		t.Fatal("没有任何 /v2/report 请求")
	}
	return reps[len(reps)-1]
}

// caCallsByUID 统计某账号（X-User-Id 头）发出的请求数。
func (f *caFakeUpstream) caCallsByUID(uid string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Header.Get("X-User-Id") == uid {
			n++
		}
	}
	return n
}

// caBodyEvents 解析 /v2/report 请求体（事件数组）。
func caBodyEvents(t *testing.T, call caCall) []map[string]any {
	t.Helper()
	var evs []map[string]any
	if err := json.Unmarshal([]byte(call.Body), &evs); err != nil {
		t.Fatalf("上报体不是事件数组: %v body=%s", err, call.Body)
	}
	return evs
}

// caEventCodes 取事件数组的 eventCode 序列（顺序敏感）。
func caEventCodes(evs []map[string]any) []string {
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, caAsStr(ev["eventCode"]))
	}
	return out
}

func caAsStr(v any) string {
	s, _ := v.(string)
	return s
}

// caMapStr 取 map 字段的字符串值（缺失/非字符串给空串）。
func caMapStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func caEqStr(t *testing.T, got, want, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}

// ---------------------------------------------------------------------------
// Panel / 账号 / 请求构造
// ---------------------------------------------------------------------------

// caAccount 构造 CN realm 的测试账号（空 Domain/realm → cn）。
func caAccount(uid string) *auth.Auth {
	return &auth.Auth{UID: uid, Nickname: "昵称-" + uid, AccessToken: "tok-" + uid}
}

// caFastGaps 把包级节流临时调到 1ms（生产值在 t.Cleanup 还原）。
// expertSummonGap 是 const（无法临时改小），相关用例用「专家数=count」把它的睡眠降到 0。
func caFastGaps(t *testing.T) {
	t.Helper()
	rg, mg, pg, cg := reportGap, mpActionGap, claimPollGap, mpChatEventGap
	reportGap, mpActionGap, claimPollGap, mpChatEventGap =
		time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		reportGap, mpActionGap, claimPollGap, mpChatEventGap = rg, mg, pg, cg
	})
}

// caNewPanel 构建带桩上游 + 给定账号池的面板。
func caNewPanel(t *testing.T, f *caFakeUpstream, accts ...*auth.Auth) *Panel {
	t.Helper()
	pl := pool.New("")
	for _, a := range accts {
		pl.Add(a)
	}
	return New(Config{
		Version: "test",
		Upstream: &upstream.Client{
			HTTP:          &http.Client{Transport: f},
			ChatBaseCN:    "https://chat.test",
			BillingBaseCN: "https://billing.test",
			WebBaseCN:     "https://web.test",
		},
		Pool: pl,
	})
}

// caPostUID 构造带 PathValue("uid") 的 POST 请求（等价路由 {uid} 命中）。
func caPostUID(target, uid, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if uid != "" {
		req.SetPathValue("uid", uid)
	}
	return req
}

func caDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v body=%s", err, rec.Body.String())
	}
	return out
}

func caStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("字段 %q 缺失或非字符串: %+v", key, m)
	}
	return s
}

func caBool(t *testing.T, m map[string]any, key string) bool {
	t.Helper()
	b, ok := m[key].(bool)
	if !ok {
		t.Fatalf("字段 %q 缺失或非布尔: %+v", key, m)
	}
	return b
}

func caNum(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	f, ok := m[key].(float64)
	if !ok {
		t.Fatalf("字段 %q 缺失或非数字: %+v", key, m)
	}
	return f
}

// caWaitUnlocked 有界等待账号锁被释放（验证后台流水线结束、锁不泄漏）。
func caWaitUnlocked(t *testing.T, p *Panel, uid string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p.tryLockAccount(uid) {
			p.unlockAccount(uid)
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// caWaitQueueIdle 有界等待队列执行结束（runQueueItems 的 goroutine 把 running 置回 false）。
func caWaitQueueIdle(t *testing.T, p *Panel) bool {
	t.Helper()
	q := p.queue()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		running := q.running
		q.mu.Unlock()
		if !running {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

// caQueueItems 读回队列条目副本。
func caQueueItems(p *Panel) []queueItem {
	q := p.queue()
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]queueItem, len(q.items))
	copy(out, q.items)
	return out
}

// caTask 构造任务（accept_status 空 = 未接受）。
// Claimed/Claimable 与上游解析口径一致（accept_status=="claimed" ⇒ 已领；达标且未领 ⇒ 可领）。
func caTask(code string, current, target int64, accept string) upstream.Task {
	t := upstream.Task{TaskCode: code, Title: "任务-" + code, Current: current, Target: target, AcceptStatus: accept}
	t.Claimed = accept == "claimed"
	t.Claimable = !t.Claimed && target > 0 && current >= target
	return t
}

// caLockedTask 锁定的任务（上游未开放/每日零点解锁窗口）。
func caLockedTask(code string, current, target int64, accept string) upstream.Task {
	t := caTask(code, current, target, accept)
	t.Locked = true
	return t
}

// ---------------------------------------------------------------------------
// 纯映射 / 辅助函数
// ---------------------------------------------------------------------------

// 动作表查找：去空白、未知码返回 nil，且表内每一项都必须可执行。
func TestPanelAutoTaskAutoActionForLookup(t *testing.T) {
	act := autoActionFor(" chat_5 ")
	if act == nil {
		t.Fatal("autoActionFor 应去空白后命中 chat_5")
	}
	caEqStr(t, act.TaskCode, "chat_5", "autoActionFor(\" chat_5 \").TaskCode")

	if got := autoActionFor("Sequential_Tasks_7"); got == nil || got.TaskCode != "Sequential_Tasks_7" {
		t.Fatalf("autoActionFor(Sequential_Tasks_7) = %+v, want 命中该码", got)
	}
	if got := autoActionFor("no_such_task"); got != nil {
		t.Fatalf("未知任务码应返回 nil（不可自动化），得到 %+v", got)
	}
	if got := autoActionFor(""); got != nil {
		t.Fatalf("空码应返回 nil，得到 %+v", got)
	}
	// 表完整性：任何一项缺 run 或 Desc 都是运行时隐患（执行路径会 nil 调用 / 前端空白）。
	for i := range autoActions {
		if autoActions[i].run == nil {
			t.Errorf("autoActions[%d] %s 缺 run 实现（执行时会 nil 调用）", i, autoActions[i].TaskCode)
		}
		if strings.TrimSpace(autoActions[i].Desc) == "" {
			t.Errorf("autoActions[%d] %s 缺 Desc", i, autoActions[i].TaskCode)
		}
		if strings.TrimSpace(autoActions[i].TaskCode) == "" {
			t.Errorf("autoActions[%d] 缺 TaskCode", i)
		}
	}
}

// 队列按依赖序排列：first_buddy 必须在 chat_5 之后（依赖活跃上报），未知码排到最后。
func TestPanelAutoTaskAutoActionIndexOrdering(t *testing.T) {
	chat5 := autoActionIndex("chat_5")
	buddy := autoActionIndex("first_buddy")
	seq7 := autoActionIndex("Sequential_Tasks_7")

	if !(chat5 < buddy) {
		t.Fatalf("chat_5(%d) 应排在 first_buddy(%d) 之前（依赖序）", chat5, buddy)
	}
	if !(buddy < seq7) {
		t.Fatalf("first_buddy(%d) 应排在 Sequential_Tasks_7(%d) 之前", buddy, seq7)
	}
	if got := autoActionIndex("no_such_task"); got != 1<<20 {
		t.Fatalf("未知码排序值 = %d, want %d（排到最后）", got, 1<<20)
	}
}

// mp 口径任务登记：mp 专属码为真、常规码为假（决定回读/accept/claim 走哪套端点）。
func TestPanelAutoTaskIsMPTaskCode(t *testing.T) {
	for _, code := range []string{"school_season", "Sequential_Tasks_1", "Sequential_Tasks_2",
		"Sequential_Tasks_3", "Sequential_Tasks_4", "Sequential_Tasks_5", "Sequential_Tasks_6", "Sequential_Tasks_7"} {
		if !isMPTaskCode(code) {
			t.Errorf("isMPTaskCode(%q) = false, want true", code)
		}
	}
	for _, code := range []string{"chat_5", "first_buddy", "Model_chat_GLM5.2", "RichMeow_Chat", ""} {
		if isMPTaskCode(code) {
			t.Errorf("isMPTaskCode(%q) = true, want false", code)
		}
	}
}

// 进度文本：有 target 用 current/target，无 target 退化 claimed / accept_status，nil 用 "?"。
func TestPanelAutoTaskProgressText(t *testing.T) {
	cases := []struct {
		name string
		task *upstream.Task
		want string
	}{
		{"nil", nil, "?"},
		{"有 target", &upstream.Task{Current: 3, Target: 5}, "3/5"},
		{"target=0 且已领", &upstream.Task{Claimed: true}, "claimed"},
		{"target=0 未领", &upstream.Task{AcceptStatus: "accepted"}, "accepted"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			caEqStr(t, taskProgressText(c.task), c.want, "taskProgressText")
		})
	}
}

// 截断：短串原样、长串截到 n 字节并带省略号（避免上游长响应原样透给前端）。
func TestPanelAutoTaskTruncateStr(t *testing.T) {
	caEqStr(t, truncateStr("short", 10), "short", "短串应原样返回")
	caEqStr(t, truncateStr("0123456789", 10), "0123456789", "等长应原样返回")
	caEqStr(t, truncateStr("0123456789abc", 10), "0123456789…", "超长应截断并加省略号")
	caEqStr(t, truncateStr("", 4), "", "空串原样")
}

// accept_status 安全读取：nil / 空值都给 "?"（错误日志里不能打出空串）。
func TestPanelAutoTaskAcceptStatusOr(t *testing.T) {
	caEqStr(t, acceptStatusOr(nil), "?", "acceptStatusOr(nil)")
	caEqStr(t, acceptStatusOr(&upstream.Task{}), "?", "空 accept_status")
	caEqStr(t, acceptStatusOr(&upstream.Task{AcceptStatus: "completed"}), "completed", "accept_status")
}

// ---------------------------------------------------------------------------
// taskByCode / taskByCodeMP / taskByCodeWaiting
// ---------------------------------------------------------------------------

// 默认口径命中即返回，且 progress 对象形态（真实上游形状）覆盖平铺字段；
// 非 mp 码绝不回头打 mp 列表（省一次上游调用）。
func TestPanelAutoTaskTaskByCodeParsesProgressObject(t *testing.T) {
	f := caNewFake()
	f.rawTaskList = `{"code":0,"msg":"ok","data":{"tasks":[
		{"task_code":"chat_5","title":"完成 5 次对话","reward_credit":100,"reward_energy":5,
		 "accept_status":"accepted","target":0,"current":0,"progress":{"current":4,"target":5}},
		{"task_code":"first_buddy","accept_status":"claimed","target":1,"current":1}
	]}}`
	p := caNewPanel(t, f, caAccount("u1"))
	a := caAccount("u1")

	got, err := p.taskByCode(a, "chat_5")
	if err != nil {
		t.Fatalf("taskByCode 报错: %v", err)
	}
	if got == nil {
		t.Fatal("chat_5 应在默认列表里命中")
	}
	if got.Current != 4 || got.Target != 5 {
		t.Fatalf("progress 对象未生效：current=%d target=%d, want 4/5", got.Current, got.Target)
	}
	if got.Claimable {
		t.Error("4/5 不应判为可领取")
	}
	if got.Claimed {
		t.Error("accepted 不应判为已领取")
	}

	// accept_status=claimed → Claimed 真（决定「已领跳过」分支）。
	claimed, err := p.taskByCode(a, "first_buddy")
	if err != nil || claimed == nil {
		t.Fatalf("first_buddy 查询失败: %v", err)
	}
	if !claimed.Claimed {
		t.Error("accept_status=claimed 应映射为 Claimed=true")
	}

	if n := f.caCallCount("GET", "/v2/activity/growth/tasks"); n != 2 {
		t.Fatalf("默认列表调用 = %d, want 2", n)
	}
	// 非 mp 码不得回落 mp 列表（"未知码不多打一次上游"）。
	mpCalls := 0
	for _, c := range f.caCalls("GET", "/v2/activity/growth/tasks") {
		if c.Header.Get("X-Client-Platform") == "miniprogram" {
			mpCalls++
		}
	}
	if mpCalls != 0 {
		t.Fatalf("chat_5（非 mp 码）触发了 %d 次 mp 列表调用", mpCalls)
	}
}

// mp 专属码在默认列表查不到时自动回落 mp 列表（双口径）。
func TestPanelAutoTaskTaskByCodeFallsBackToMP(t *testing.T) {
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, "accepted"))
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 0, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCode(caAccount("u1"), "Sequential_Tasks_1")
	if err != nil {
		t.Fatalf("taskByCode 报错: %v", err)
	}
	if got == nil {
		t.Fatal("mp 专属码应回落 mp 列表并命中")
	}
	caEqStr(t, got.TaskCode, "Sequential_Tasks_1", "回落结果 TaskCode")

	mpCalls := 0
	for _, c := range f.caCalls("GET", "/v2/activity/growth/tasks") {
		if c.Header.Get("X-Client-Platform") == "miniprogram" {
			mpCalls++
		}
	}
	if mpCalls != 1 {
		t.Fatalf("mp 列表调用 = %d, want 1", mpCalls)
	}
}

// 未找到不是错误：返回 (nil, nil)，调用方按「该账号没此任务」处理。
func TestPanelAutoTaskTaskByCodeAbsentIsNilNotError(t *testing.T) {
	f := caNewFake()
	f.caSetTask(caTask("first_buddy", 0, 1, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCode(caAccount("u1"), "chat_5")
	if err != nil {
		t.Fatalf("未找到不应报错，得到 %v", err)
	}
	if got != nil {
		t.Fatalf("未找到应返回 nil，得到 %+v", got)
	}
}

// 列表失败必须向上传错误（handler 转 502），不能吞掉当成"没这个任务"。
func TestPanelAutoTaskTaskByCodePropagatesListError(t *testing.T) {
	f := caNewFake()
	f.raw["GET /v2/activity/growth/tasks"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCode(caAccount("u1"), "chat_5")
	if err == nil {
		t.Fatal("上游 500 应返回错误")
	}
	if got != nil {
		t.Fatalf("出错时应返回 nil 任务，得到 %+v", got)
	}
}

// mp 列表失败同样向上传错误（accept/回读据此判定）。
func TestPanelAutoTaskTaskByCodeMPPropagatesError(t *testing.T) {
	f := caNewFake()
	f.mpListRaw = &caRaw{Status: 403, Body: `{"code":10001,"msg":"growth system is only available for personal users"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCodeMP(caAccount("u1"), "Sequential_Tasks_1")
	if err == nil {
		t.Fatal("mp 列表 403 应返回错误")
	}
	if got != nil {
		t.Fatalf("出错时应返回 nil，得到 %+v", got)
	}
}

// 达标回读的轮询：第一次未达标、第二次达标 → 必须返回**最新**快照（异步计分）。
func TestPanelAutoTaskTaskByCodeWaitingPollsUntilClaimable(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("Model_chat_GLM5.2", 0, 1, "accepted"))
	// 第 1 次回读之后（响应已发出）进度才落账：轮询必须再读一次才看到 1/1。
	f.onList = func(n int) {
		if n == 1 {
			f.caAdvance("Model_chat_GLM5.2", false, 1, "completed")
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCodeWaiting(caAccount("u1"), "Model_chat_GLM5.2")
	if err != nil {
		t.Fatalf("taskByCodeWaiting 报错: %v", err)
	}
	if got == nil || !got.Claimable {
		t.Fatalf("回读结果 = %+v, want claimable=true（不能被首次 0/1 快照骗过）", got)
	}
	if n := f.caCallCount("GET", "/v2/activity/growth/tasks"); n != 2 {
		t.Fatalf("列表调用 = %d, want 2（首次 + 1 次轮询后命中即停）", n)
	}
}

// 轮询有界：预算耗尽返回最后一次结果（可能仍未达标），不会无限等待。
func TestPanelAutoTaskTaskByCodeWaitingBudgetBounded(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCodeWaiting(caAccount("u1"), "chat_5")
	if err != nil {
		t.Fatalf("taskByCodeWaiting 报错: %v", err)
	}
	if got == nil || got.Claimable {
		t.Fatalf("回读结果 = %+v, want 未达标快照", got)
	}
	if n := f.caCallCount("GET", "/v2/activity/growth/tasks"); n != claimPollAttempts {
		t.Fatalf("列表调用 = %d, want %d（claimPollAttempts 预算）", n, claimPollAttempts)
	}
}

// 轮询期间的查询失败不覆盖已拿到的结果（避免把"已达标"降级成"查询失败"）。
func TestPanelAutoTaskTaskByCodeWaitingKeepsLastOnPollError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
	f.onList = func(n int) {
		if n == 2 { // 第 2 次列表调用（首次轮询）失败
			f.mu.Lock()
			f.raw["GET /v2/activity/growth/tasks"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
			f.mu.Unlock()
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCodeWaiting(caAccount("u1"), "chat_5")
	if err != nil {
		t.Fatalf("轮询期间的失败不该冒泡成错误: %v", err)
	}
	if got == nil || got.Current != 4 || got.Target != 5 {
		t.Fatalf("应保留首次快照 4/5，得到 %+v", got)
	}
}

// 已达标/已领取的首次回读立即返回，不做无谓轮询（上游计分异步，但无需等待）。
func TestPanelAutoTaskTaskByCodeWaitingSkipsPollWhenClaimable(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	got, err := p.taskByCodeWaiting(caAccount("u1"), "chat_5")
	if err != nil || got == nil || !got.Claimable {
		t.Fatalf("回读 = %+v/%v, want claimable", got, err)
	}
	if n := f.caCallCount("GET", "/v2/activity/growth/tasks"); n != 1 {
		t.Fatalf("已达标时列表调用 = %d, want 1（不轮询）", n)
	}
}

// accept 返回 200 但未登记生效（上游真实形态）→ 必须判失败并重试一次，最终返回 false。
func TestPanelAutoTaskAcceptWithVerifyMPRejectsUnregisteredAccept(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, "not_accepted"))
	f.acceptNoOp = true // accept 200 但 accept_status 不变
	p := caNewPanel(t, f, caAccount("u1"))

	got := p.acceptWithVerifyMP(caAccount("u1"), "Sequential_Tasks_1")
	if got {
		t.Fatal("回读未生效时 acceptWithVerifyMP 必须返回 false（上报不归账，跑了白跑）")
	}
	if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 2 {
		t.Fatalf("accept 调用 = %d, want 2（未生效重试一次）", n)
	}
}

// accept 后回读 accept_status=accepted → 判定成功且不重复 accept。
func TestPanelAutoTaskAcceptWithVerifyMPTrueOnRegisteredAccept(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("school_season", 0, 1, "not_accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	if !p.acceptWithVerifyMP(caAccount("u1"), "school_season") {
		t.Fatal("回读 accepted 时应返回 true")
	}
	if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 1 {
		t.Fatalf("accept 调用 = %d, want 1（生效即停）", n)
	}
	if n := f.caCallCount("GET", "/v2/activity/growth/tasks"); n != 1 {
		t.Fatalf("回读调用 = %d, want 1", n)
	}
}

// accept 第一次失败（上游 5xx）→ 必须重试，第二次生效则整体判成功。
func TestPanelAutoTaskAcceptWithVerifyMPRetriesAfterAcceptError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "not_accepted"))
	f.acceptFailFirst = true // 第一次 5xx，第二次落账
	p := caNewPanel(t, f, caAccount("u1"))

	if !p.acceptWithVerifyMP(caAccount("u1"), "Sequential_Tasks_2") {
		t.Fatal("首次失败重试后生效应返回 true")
	}
	if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 2 {
		t.Fatalf("accept 调用 = %d, want 2", n)
	}
}

// accept 两次都失败 → 必须返回 false（不得当成功继续走上报链）。
func TestPanelAutoTaskAcceptWithVerifyMPAllAcceptsFail(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "not_accepted"))
	f.raw["POST /v2/activity/growth/tasks/accept"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	if p.acceptWithVerifyMP(caAccount("u1"), "Sequential_Tasks_2") {
		t.Fatal("accept 全失败时必须返回 false")
	}
	if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 2 {
		t.Fatalf("accept 调用 = %d, want 2（失败后重试一次）", n)
	}
}

// caFirstIndex 某「方法 + 路径」在调用序列中的首次下标（-1 = 未调用）。
func (f *caFakeUpstream) caFirstIndex(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if c.Method == method && c.Path == path {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// runMPMiniChatTask：growth 域 mp 小程序任务闭环
// ---------------------------------------------------------------------------

// 已领取的任务直接跳过（幂等，不重复消耗上游配额）。
func TestPanelAutoTaskRunSchoolSeasonAlreadyClaimed(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("school_season", 1, 1, "claimed"))
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSchoolSeason(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("已领取不应报错: %v", err)
	}
	caEqStr(t, msg, "已领取", "runSchoolSeason 已领取")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("已领取不该再上报，上报次数 %d", n)
	}
	if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 0 {
		t.Fatalf("已领取不该再 accept，accept 次数 %d", n)
	}
}

// 达标未领 → 直接领奖；mp 口径必须走 chat 域 + mp 头（缺头上游不可领）。
func TestPanelAutoTaskRunSchoolSeasonClaimUsesMPEndpoint(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("school_season", 1, 1, "accepted"))
	f.claimCredit, f.claimEnergy = 100, 5
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSchoolSeason(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("领奖失败: %v", err)
	}
	caEqStr(t, msg, "已领取奖励（+100c +5e）", "runSchoolSeason 领奖结果")

	claims := f.caCalls("POST", "/activity/growth/tasks/school_season/claim")
	if len(claims) != 1 {
		t.Fatalf("领奖调用 = %d, want 1", len(claims))
	}
	caEqStr(t, claims[0].Host, "chat.test", "mp 领奖域（chat 域）")
	caEqStr(t, claims[0].Header.Get("X-Client-Platform"), "miniprogram", "mp 领奖头")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("已达标不该再上报，上报次数 %d", n)
	}
}

// school_season 的判据事件必须带 activityId=school_open_day_2026（无 activityId 不点亮）。
func TestPanelAutoTaskRunSchoolSeasonSendsActivityID(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("school_season", 0, 1, ""))
	f.claimCredit, f.claimEnergy = 100, 5
	f.onReport = func(caCall) { f.caAdvance("school_season", true, 1, "completed") }
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSchoolSeason(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("闭环失败: %v", err)
	}
	caEqStr(t, msg, "任务点亮并领取奖励（+100c +5e）", "runSchoolSeason 点亮结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 1 {
		t.Fatalf("上报次数 = %d, want 1", len(reports))
	}
	caEqStr(t, reports[0].Host, "billing.test", "mp 上报域")
	caEqStr(t, reports[0].Header.Get("X-Client-Platform"), "mp-weixin", "mp 上报头")
	evs := caBodyEvents(t, reports[0])
	caEqStr(t, caAsStr(evs[0]["eventCode"]), "chat_request_send", "事件类型")
	caEqStr(t, caAsStr(evs[0]["activityId"]), "school_open_day_2026", "activityId")
}

// Sequential_Tasks_1 的判据事件**不带** activityId（服务端按 source=mini_program 关联）。
func TestPanelAutoTaskRunSequentialChatOmitsActivityID(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, ""))
	f.claimCredit, f.claimEnergy = 100, 5
	f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_1", true, 1, "completed") }
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("闭环失败: %v", err)
	}
	caEqStr(t, msg, "任务点亮并领取奖励（+100c +5e）", "runSequentialChat 点亮结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 1 {
		t.Fatalf("上报次数 = %d, want 1", len(reports))
	}
	evs := caBodyEvents(t, reports[0])
	if _, ok := evs[0]["activityId"]; ok {
		t.Fatalf("Sequential_Tasks_1 事件不得带 activityId（与 school_season 口径不同）: %+v", evs[0])
	}
}

// 补报条数按**服务端下发的 target - current**算，不得用硬编码 5。
func TestPanelAutoTaskRunSequentialChat5UsesServerTarget(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_3", 3, 4, "accepted")) // 服务端 target=4（非硬编码 5）
	f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_3", true, 4, "completed") }
	f.claimCredit, f.claimEnergy = 300, 5
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialChat5(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("闭环失败: %v", err)
	}
	caEqStr(t, msg, "任务点亮并领取奖励（+300c +5e）", "runSequentialChat5 点亮结果")
	if n := f.caCallCount("POST", "/v2/report"); n != 1 {
		t.Fatalf("上报次数 = %d, want 1（差额 target-current = 1，非固定 5）", n)
	}
}

// Sequential_Tasks_6 走同一 mp 闭环骨架（target 由服务端下发）。
func TestPanelAutoTaskRunSequentialChat10ReusesMPLoop(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_6", 10, 10, "accepted"))
	f.claimCredit, f.claimEnergy = 1000, 5
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialChat10(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("领奖失败: %v", err)
	}
	caEqStr(t, msg, "已领取奖励（+1000c +5e）", "runSequentialChat10 领奖结果")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("已达标不该上报，上报次数 %d", n)
	}
}

// mp 列表未下发该任务（活动结束/前置未完成）→ 不动作、不 accept。
func TestPanelAutoTaskRunMPMiniChatTaskNotFound(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSchoolSeason(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("未下发不该报错: %v", err)
	}
	caEqStr(t, msg, "mp 口径未下发该任务（活动可能已结束）", "未下发提示")
	if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 0 {
		t.Fatalf("未下发不该 accept，次数 %d", n)
	}
}

// accept 200 但未登记生效 → 明确提示待重试，且不上报事件（上报也不会归账）。
func TestPanelAutoTaskRunMPMiniChatTaskAcceptNotRegistered(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("school_season", 0, 1, "not_accepted"))
	f.acceptNoOp = true
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSchoolSeason(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "accept 未登记生效（上游 200+OK 但未落账形态），待下次重试", "accept 未生效提示")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("accept 未生效时不得上报（白报），次数 %d", n)
	}
}

// 上报中途失败 → 立刻中断并报出已完成条数（不继续盲报剩余条数）。
// 注：mp 上报前的间隔含 0~10s 随机抖动（生产反作弊节奏，非可注入变量），
// 故各 mp 上报用例把 need 压到 1，只付一次抖动。
func TestPanelAutoTaskRunMPMiniChatTaskReportErrorAborts(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, "accepted"))
	f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("上报失败应作为文本结果返回而非 error: %v", err)
	}
	if !strings.HasPrefix(msg, "完成 0/1 次上报后中断:") {
		t.Fatalf("中断提示 = %q, want 前缀 \"完成 0/1 次上报后中断:\"", msg)
	}
	if n := f.caCallCount("POST", "/v2/report"); n != 1 {
		t.Fatalf("上报次数 = %d, want 1（首条失败即中断）", n)
	}
}

// 上报成功但进度未归账（异步计分）→ 报出进度并留待下次重试，且回读用紧凑两轮预算。
func TestPanelAutoTaskRunMPMiniChatTaskProgressNotAccounted(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已上报 1 次但进度未达 0/1（异步计分未归账，下次重试）", "未归账提示")
	if n := f.caCallCount("POST", "/v2/report"); n != 1 {
		t.Fatalf("上报次数 = %d, want 1（按差额补）", n)
	}
	// mp 列表：首次 + 紧凑两轮回读 = 3 次（不得无限轮询）。
	if n := f.caCallCount("GET", "/v2/activity/growth/tasks"); n != 3 {
		t.Fatalf("mp 列表调用 = %d, want 3", n)
	}
}

// 回读期间进度直接转 claimed → 报「本轮已入账」且不再领奖（避免重复领）。
func TestPanelAutoTaskRunMPMiniChatTaskClaimedDuringReadback(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, "accepted"))
	f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_1", true, 1, "claimed") }
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "本轮已入账（claimed）", "claimed 提示")
	if n := f.caCallCount("POST", "/activity/growth/tasks/Sequential_Tasks_1/claim"); n != 0 {
		t.Fatalf("已 claimed 不该再领奖，领奖次数 %d", n)
	}
}

// 领奖失败（上游业务错误）→ 冒泡成 error 交给 handler 转 502。
func TestPanelAutoTaskRunMPMiniChatTaskClaimError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("school_season", 1, 1, "accepted"))
	f.raw["POST /activity/growth/tasks/school_season/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSchoolSeason(p, caAccount("u1"))
	if err == nil {
		t.Fatalf("领奖失败应返回 error，得到 msg=%q", msg)
	}
}

// ---------------------------------------------------------------------------
// runSequentialEventTask：Sequential 链预留任务骨架（Tasks_4/5/7）
// ---------------------------------------------------------------------------

// Tasks_4 判据走 PC 口径（automation_1 同源事件）→ 必须打到 chat 域的桌面事件通道。
func TestPanelAutoTaskRunSequentialAutomationReportsDesktopEvent(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, ""))
	f.claimCredit, f.claimEnergy = 200, 5
	f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_4", true, 1, "completed") }
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialAutomation(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("闭环失败: %v", err)
	}
	caEqStr(t, msg, "任务点亮并领取奖励（+200c +5e）", "Tasks_4 点亮结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 1 {
		t.Fatalf("上报次数 = %d, want 1", len(reports))
	}
	caEqStr(t, reports[0].Host, "chat.test", "Tasks_4 判据走 PC/桌面域")
	evs := caBodyEvents(t, reports[0])
	caEqStr(t, caAsStr(evs[0]["eventCode"]), "automated_task_create_suc", "Tasks_4 判据事件")
}

// Tasks_5 primary = 带模型字段的 mp 对话事件；未点亮时 fallback 走 PC 模型活跃上报。
func TestPanelAutoTaskRunSequentialModelChatFallsBackToPCReport(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_5", 0, 1, ""))
	f.claimCredit, f.claimEnergy = 200, 5
	// 只有「非 mp 头」的上报（= fallback 的 PC 形态）才让进度落账。
	f.onReport = func(call caCall) {
		if call.Header.Get("X-Client-Platform") != "mp-weixin" {
			f.caAdvance("Sequential_Tasks_5", true, 1, "completed")
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialModelChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("闭环失败: %v", err)
	}
	caEqStr(t, msg, "任务点亮并领取奖励（+200c +5e）", "Tasks_5 点亮结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 2 {
		t.Fatalf("上报次数 = %d, want 2（primary + fallback）", len(reports))
	}
	primary := caBodyEvents(t, reports[0])
	caEqStr(t, caAsStr(primary[0]["requestModelId"]), "glm-5.2", "primary 模型 id")
	caEqStr(t, caAsStr(primary[0]["requestModelName"]), "GLM-5.2", "primary 模型名")
	caEqStr(t, reports[0].Header.Get("X-Client-Platform"), "mp-weixin", "primary mp 指纹")
	fallback := caBodyEvents(t, reports[1])
	caEqStr(t, caAsStr(fallback[0]["requestModelId"]), "glm-5.2", "fallback 模型 id")
	if got := reports[1].Header.Get("X-Client-Platform"); got != "" {
		t.Fatalf("fallback 应为 PC 形态（无 mp 头），得到 %q", got)
	}
}

// Tasks_7 primary = PC 灵感事件组，fallback = mp 灵感事件组（双形态兜底）。
func TestPanelAutoTaskRunSequentialPlaybookFallsBackToMPEvents(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_7", 0, 1, ""))
	f.claimCredit, f.claimEnergy = 500, 5
	f.onReport = func(call caCall) {
		if call.Header.Get("X-Client-Platform") == "mp-weixin" { // fallback 落账
			f.caAdvance("Sequential_Tasks_7", true, 1, "completed")
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialPlaybook(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("闭环失败: %v", err)
	}
	caEqStr(t, msg, "任务点亮并领取奖励（+500c +5e）", "Tasks_7 点亮结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 2 {
		t.Fatalf("上报次数 = %d, want 2", len(reports))
	}
	caEqStr(t, reports[0].Host, "chat.test", "primary 走 PC 域")
	codes := caEventCodes(caBodyEvents(t, reports[0]))
	found := false
	for _, c := range codes {
		if c == "playbook_prompt_send" {
			found = true
		}
	}
	if !found {
		t.Fatalf("primary 事件组缺 playbook_prompt_send: %v", codes)
	}
	caEqStr(t, reports[1].Host, "billing.test", "fallback 走 mp 域")
	mpEvs := caBodyEvents(t, reports[1])
	caEqStr(t, caAsStr(mpEvs[0]["id"]), "pm-gtm-launch-plan", "mp 灵感案例 id")
}

// 未下发 / 已领取 / 已 completed 的三个门控分支：各自不动作或直接领奖。
func TestPanelAutoTaskRunSequentialEventTaskGates(t *testing.T) {
	caFastGaps(t)

	t.Run("未下发", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runSequentialAutomation(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "mp 口径未下发该任务（前置任务未完成或活动未开始）", "未下发提示")
	})

	t.Run("已领取", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_4", 1, 1, "claimed"))
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runSequentialAutomation(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已领取", "已领取提示")
		if n := f.caCallCount("POST", "/v2/report"); n != 0 {
			t.Fatalf("已领取不该上报，次数 %d", n)
		}
	})

	t.Run("completed 直接领奖", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, "completed")) // current<target 但 completed
		f.claimCredit, f.claimEnergy = 200, 5
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runSequentialAutomation(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("领奖失败: %v", err)
		}
		caEqStr(t, msg, "已领取奖励（+200c +5e）", "completed 直接领奖")
		if n := f.caCallCount("POST", "/v2/report"); n != 0 {
			t.Fatalf("completed 不该再上报判据，次数 %d", n)
		}
	})
}

// 每日锁定窗口：accept 不落账 → 明确提示等解锁后自动重试，且不误发判据事件。
func TestPanelAutoTaskRunSequentialEventTaskLockedWindow(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, ""))
	f.acceptNoOp = true
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialAutomation(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "accept 未登记生效（任务可能处于每日锁定窗口，等解锁后自动重试）", "锁定窗口提示")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("accept 未生效时不该上报判据，次数 %d", n)
	}
}

// 主判据上报失败 → 以文本报出（不继续 fallback），下次重试。
func TestPanelAutoTaskRunSequentialEventTaskPrimaryError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, "accepted"))
	f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialAutomation(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报 error: %v", err)
	}
	if !strings.HasPrefix(msg, "判据上报失败:") {
		t.Fatalf("提示 = %q, want 前缀 \"判据上报失败:\"", msg)
	}
	if n := f.caCallCount("POST", "/v2/report"); n != 1 {
		t.Fatalf("上报次数 = %d, want 1", n)
	}
}

// 备选判据（fallback）失败 → 独立报出，不与主判据失败混淆。
func TestPanelAutoTaskRunSequentialEventTaskFallbackError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_7", 0, 1, "accepted"))
	n := 0
	f.onReport = func(caCall) { // 主判据成功响应后，为 fallback 注入故障
		n++
		if n == 1 {
			f.mu.Lock()
			f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
			f.mu.Unlock()
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialPlaybook(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报 error: %v", err)
	}
	if !strings.HasPrefix(msg, "备选判据上报失败:") {
		t.Fatalf("提示 = %q, want 前缀 \"备选判据上报失败:\"", msg)
	}
}

// 无 fallback 且进度未点亮 → 明确「判据形态待校正」，不误领奖。
func TestPanelAutoTaskRunSequentialEventTaskProgressNotLit(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSequentialAutomation(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报 error: %v", err)
	}
	caEqStr(t, msg, "已上报但进度未点亮（判据形态待解锁后校正，下次重试）", "未点亮提示")
	if n := f.caCallCount("POST", "/activity/growth/tasks/Sequential_Tasks_4/claim"); n != 0 {
		t.Fatalf("未达标不该领奖，次数 %d", n)
	}
}

// ---------------------------------------------------------------------------
// runMiniExpert：Sequential_Tasks_2（mp 指纹 expert_actual_use）
// ---------------------------------------------------------------------------

// 专家市场不可用 → 整任务不动作（**先解析专家 id，再 accept**，避免「已登记未上报」半程态）。
func TestPanelAutoTaskRunMiniExpertAbortsBeforeAcceptWhenMarketUnavailable(t *testing.T) {
	caFastGaps(t)

	t.Run("市场报错", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, ""))
		f.raw["POST /portal/operation-platform/market/expert/list"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报 error: %v", err)
		}
		if !strings.HasPrefix(msg, "专家市场不可用") || !strings.Contains(msg, "跳过以防半程态") {
			t.Fatalf("提示 = %q, want 专家市场不可用…跳过以防半程态", msg)
		}
		if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 0 {
			t.Fatalf("市场不可用时必须**在 accept 之前**放弃，accept 次数 %d", n)
		}
	})

	t.Run("市场为空", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, ""))
		p := caNewPanel(t, f, caAccount("u1")) // experts 为空切片
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报 error: %v", err)
		}
		if !strings.HasPrefix(msg, "专家市场不可用") {
			t.Fatalf("提示 = %q", msg)
		}
		if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 0 {
			t.Fatalf("空市场必须放弃，accept 次数 %d", n)
		}
	})
}

// 成功路径：市场首个专家 id 进事件；名字空则回落 profession_zh；accept 在 report 之前。
func TestPanelAutoTaskRunMiniExpertUsesMarketExpertID(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, ""))
	f.experts = []upstream.MarketExpert{{
		ExpertID: "ex_abc123", ExpertType: "agent",
		DisplayNameZH: "", ProfessionZH: "运营增长专家", Version: "1.2.3",
	}}
	f.claimCredit, f.claimEnergy = 200, 5
	f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_2", true, 1, "completed") }
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runMiniExpert(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("闭环失败: %v", err)
	}
	caEqStr(t, msg, "任务点亮并领取奖励（+200c +5e）", "runMiniExpert 点亮结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 1 {
		t.Fatalf("上报次数 = %d, want 1", len(reports))
	}
	ev := caBodyEvents(t, reports[0])[0]
	caEqStr(t, caAsStr(ev["eventCode"]), "expert_actual_use", "判据事件")
	caEqStr(t, caAsStr(ev["id"]), "ex_abc123", "必须用市场真实 expert_id")
	caEqStr(t, caAsStr(ev["expertTitle"]), "运营增长专家", "空 display_name_zh 回落 profession_zh")
	caEqStr(t, caAsStr(ev["type"]), "send_message", "mp 口径固定 type")
	caEqStr(t, caAsStr(ev["extVersion"]), "2.2.8", "mp 口径 extVersion")
	caEqStr(t, caAsStr(ev["source"]), "mini_program", "mp 指纹 source")
	if _, ok := ev["conversationId"]; ok {
		t.Fatalf("mp 口径事件不得带 conversationId: %+v", ev)
	}
	if _, ok := ev["activityId"]; ok {
		t.Fatalf("mp 口径事件不得带 activityId: %+v", ev)
	}

	// 顺序语义：accept 必须在判据上报之前（否则事件不归账）。
	acc := f.caFirstIndex("POST", "/v2/activity/growth/tasks/accept")
	rep := f.caFirstIndex("POST", "/v2/report")
	if acc < 0 || rep < 0 || acc > rep {
		t.Fatalf("accept(%d) 必须早于 report(%d)", acc, rep)
	}
}

// 判据上报失败 / 进度未归账：两条不同的收尾提示，都不冒泡 error。
func TestPanelAutoTaskRunMiniExpertReportOutcomes(t *testing.T) {
	caFastGaps(t)

	t.Run("上报失败", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "accepted"))
		f.experts = []upstream.MarketExpert{{ExpertID: "ex_abc123", ExpertType: "agent", DisplayNameZH: "专家"}}
		f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报 error: %v", err)
		}
		if !strings.HasPrefix(msg, "上报 expert_actual_use 失败:") {
			t.Fatalf("提示 = %q", msg)
		}
	})

	t.Run("未归账", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "accepted"))
		f.experts = []upstream.MarketExpert{{ExpertID: "ex_abc123", ExpertType: "agent", DisplayNameZH: "专家"}}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报 error: %v", err)
		}
		caEqStr(t, msg, "已上报但进度未归账（异步计分，下次重试）", "未归账提示")
	})
}

// 已达标 / 已领取的两个快捷门控。
func TestPanelAutoTaskRunMiniExpertShortCircuits(t *testing.T) {
	caFastGaps(t)

	t.Run("已达标直接领奖", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_2", 1, 1, "accepted"))
		f.claimCredit, f.claimEnergy = 200, 5
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("领奖失败: %v", err)
		}
		caEqStr(t, msg, "已领取奖励（+200c +5e）", "已达标直接领奖")
		if n := f.caCallCount("POST", "/portal/operation-platform/market/expert/list"); n != 0 {
			t.Fatalf("已达标不该再拉专家市场，次数 %d", n)
		}
	})

	t.Run("已领取", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_2", 1, 1, "claimed"))
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已领取", "已领取")
	})

	t.Run("未下发", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "mp 口径未下发该任务（活动可能已结束）", "未下发")
	})
}

// ---------------------------------------------------------------------------
// 单动作实现：chat_5 / first_buddy / Model_chat / 桌面事件族
// ---------------------------------------------------------------------------

// chat_5 按差额补报，且每条上报用**独立**的 conversationId（同 id 重复会被上游判重复不计数）。
func TestPanelAutoTaskRunChat5ReportsDeficit(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 3, 5, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runChat5(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已补报 2 条对话事件", "runChat5 结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 2 {
		t.Fatalf("上报次数 = %d, want 2", len(reports))
	}
	seen := map[string]bool{}
	for i, rep := range reports {
		caEqStr(t, rep.Host, "billing.test", "活跃上报域")
		evs := caBodyEvents(t, rep)
		if len(evs) != 1 {
			t.Fatalf("第 %d 条上报应只含 1 个事件，得到 %d", i+1, len(evs))
		}
		caEqStr(t, caAsStr(evs[0]["eventCode"]), "chat_request_send", "事件类型")
		caEqStr(t, caAsStr(evs[0]["requestModelId"]), "deepseek-v4-flash", "默认模型 id")
		caEqStr(t, caAsStr(evs[0]["userId"]), "u1", "userId 必填（缺失上游静默丢弃）")
		cid := caAsStr(evs[0]["conversationId"])
		if !strings.HasPrefix(cid, "wb2api-chat5-") {
			t.Fatalf("conversationId = %q, want 前缀 wb2api-chat5-", cid)
		}
		if seen[cid] {
			t.Fatalf("第 %d 条复用了同一个 conversationId %q（上游会判重复）", i+1, cid)
		}
		seen[cid] = true
	}
}

// 已达标 → 不重复上报（幂等，省上游配额）。
func TestPanelAutoTaskRunChat5SatisfiedSkipsReports(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runChat5(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "进度已达标，无需上报", "已达标提示")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("已达标不该上报，次数 %d", n)
	}
}

// target 未下发（0）时回落 5 条（chat_5 的语义目标）。
func TestPanelAutoTaskRunChat5TargetFallback(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 0, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runChat5(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已补报 5 条对话事件", "target=0 回落 5")
	if n := f.caCallCount("POST", "/v2/report"); n != 5 {
		t.Fatalf("上报次数 = %d, want 5", n)
	}
}

// 任务不存在 / 查询失败 / 上报失败：三种错误的返回形态各不相同。
func TestPanelAutoTaskRunChat5ErrorPaths(t *testing.T) {
	caFastGaps(t)

	t.Run("任务不存在", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runChat5(p, caAccount("u1"))
		if err == nil {
			t.Fatalf("任务不存在应返回 error，msg=%q", msg)
		}
		caEqStr(t, err.Error(), "任务不存在", "错误文本")
	})

	t.Run("列表失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["GET /v2/activity/growth/tasks"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runChat5(p, caAccount("u1")); err == nil {
			t.Fatal("列表失败应返回 error")
		}
	})

	t.Run("上报失败", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
		// 第 1 条上报失败：raw 命中所有上报 → 首条即中断。
		f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runChat5(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("上报失败应作为文本结果返回: %v", err)
		}
		if !strings.HasPrefix(msg, "上报第 1/1 条失败:") {
			t.Fatalf("提示 = %q, want 前缀 \"上报第 1/1 条失败:\"", msg)
		}
	})
}

// 领养三步顺序（上报解锁 → 同意协议 → 领取），且前置上报必须带 userId。
func TestPanelAutoTaskRunFirstBuddyOrderAndBodies(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runFirstBuddy(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("领养失败: %v", err)
	}
	caEqStr(t, msg, "已领取 Buddy（+300 分 +8 能量）", "领养结果")

	repIdx := f.caFirstIndex("POST", "/v2/report")
	agrIdx := f.caFirstIndex("POST", "/activity/growth/buddy/agreement")
	fstIdx := f.caFirstIndex("POST", "/activity/growth/buddy/first")
	if repIdx < 0 || agrIdx < 0 || fstIdx < 0 {
		t.Fatalf("三步必须都发生：report=%d agreement=%d first=%d", repIdx, agrIdx, fstIdx)
	}
	if !(repIdx < agrIdx && agrIdx < fstIdx) {
		t.Fatalf("顺序错误：report=%d agreement=%d first=%d（必须先解锁再同意再领）", repIdx, agrIdx, fstIdx)
	}

	evs := caBodyEvents(t, f.caCalls("POST", "/v2/report")[0])
	caEqStr(t, caAsStr(evs[0]["userId"]), "u1", "前置上报的 userId")
	agr := f.caCalls("POST", "/activity/growth/buddy/agreement")[0]
	if !strings.Contains(agr.Body, `"agree":true`) {
		t.Fatalf("协议体 = %s, want 含 \"agree\":true", agr.Body)
	}
}

// 领养门槛未过（上游 400 first_buddy task not completed yet）→ 软提示，不冒泡 error。
func TestPanelAutoTaskRunFirstBuddyThresholdNotMet(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.raw["POST /activity/growth/buddy/first"] = caRaw{
		Status: 400, Body: `{"code":10001,"msg":"first_buddy task not completed yet"}`,
	}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runFirstBuddy(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("门槛未过是预期行为，不该是 error: %v", err)
	}
	caEqStr(t, msg, "前置已上报，但领养门槛未过（上游要求当日活跃），请稍后重试", "门槛未过提示")
}

// 前缀失败必须阻断后续步骤（否则会在未解锁状态下打 first 拿 400）。
func TestPanelAutoTaskRunFirstBuddyStopsOnPrefixFailure(t *testing.T) {
	caFastGaps(t)

	t.Run("前置上报失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runFirstBuddy(p, caAccount("u1"))
		if err == nil || !strings.HasPrefix(err.Error(), "前置上报:") {
			t.Fatalf("错误 = %v, want 前缀 \"前置上报:\"", err)
		}
		if n := f.caCallCount("POST", "/activity/growth/buddy/first"); n != 0 {
			t.Fatalf("前置失败不该继续领养，first 次数 %d", n)
		}
	})

	t.Run("同意协议失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["POST /activity/growth/buddy/agreement"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runFirstBuddy(p, caAccount("u1"))
		if err == nil || !strings.HasPrefix(err.Error(), "同意协议:") {
			t.Fatalf("错误 = %v, want 前缀 \"同意协议:\"", err)
		}
		if n := f.caCallCount("POST", "/activity/growth/buddy/first"); n != 0 {
			t.Fatalf("协议失败不该继续领养，first 次数 %d", n)
		}
	})

	t.Run("领取硬失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["POST /activity/growth/buddy/first"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runFirstBuddy(p, caAccount("u1"))
		if err == nil || !strings.HasPrefix(err.Error(), "领取 Buddy:") {
			t.Fatalf("错误 = %v, want 前缀 \"领取 Buddy:\"", err)
		}
	})
}

// Model_chat_GLM5.2：accept → 真实 glm-5.2 对话 → 对齐模型的上报（三件事都必须真发生）。
func TestPanelAutoTaskRunModelChatAcceptsChatsAndReports(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runModelChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已完成 glm-5.2 对话并上报", "runModelChat 结果")

	acc := f.caCalls("POST", "/v2/activity/growth/tasks/accept")
	if len(acc) != 1 || !strings.Contains(acc[0].Body, "Model_chat_GLM5.2") {
		t.Fatalf("accept 应带 Model_chat_GLM5.2: %+v", acc)
	}

	chats := f.caCalls("POST", "/v2/chat/completions")
	if len(chats) != 1 {
		t.Fatalf("对话次数 = %d, want 1", len(chats))
	}
	var chatBody map[string]any
	if err := json.Unmarshal([]byte(chats[0].Body), &chatBody); err != nil {
		t.Fatalf("对话体不是 JSON: %v body=%s", err, chats[0].Body)
	}
	caEqStr(t, caAsStr(chatBody["model"]), "glm-5.2", "对话模型（判据最直接证据）")
	if stream, _ := chatBody["stream"].(bool); !stream {
		t.Fatalf("对话应 stream=true，得到 %+v", chatBody["stream"])
	}
	if msgs, ok := chatBody["messages"].([]any); !ok || len(msgs) == 0 {
		t.Fatalf("对话体缺 messages: %+v", chatBody)
	}

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 1 {
		t.Fatalf("上报次数 = %d, want 1", len(reports))
	}
	evs := caBodyEvents(t, reports[0])
	caEqStr(t, caAsStr(evs[0]["requestModelId"]), "glm-5.2", "上报模型 id 必须对齐实测模型")
	caEqStr(t, caAsStr(evs[0]["requestModelName"]), "GLM-5.2", "上报模型名")
}

// accept 失败不阻塞行为链路（判据是行为事件，不是 accept）。
func TestPanelAutoTaskRunModelChatContinuesAfterAcceptError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.raw["POST /v2/activity/growth/tasks/accept"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runModelChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("accept 失败不该中断行为链路: %v", err)
	}
	caEqStr(t, msg, "已完成 glm-5.2 对话并上报", "runModelChat 结果")
	if n := f.caCallCount("POST", "/v2/chat/completions"); n != 1 {
		t.Fatalf("对话次数 = %d, want 1", n)
	}
}

// 对话 HTTP ≥400 → 带状态码的 error 冒泡（且不再上报、不领奖）。
// 注：ChatStream 对 ≥400 一律带已分类 *Error 返回，故 runModelChat 的
// "status >= 400 且 err == nil" 分支在当前上游契约下不可达（见报告遗留说明）。
func TestPanelAutoTaskRunModelChatChatHTTPError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	long := strings.Repeat("x", 400)
	f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"` + long + `"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	_, err := runModelChat(p, caAccount("u1"))
	if err == nil {
		t.Fatal("对话 500 应返回 error")
	}
	if !strings.HasPrefix(err.Error(), "对话请求: ") || !strings.Contains(err.Error(), "http 500") {
		t.Fatalf("错误 = %q, want 前缀 \"对话请求: \" 且含 http 500", err.Error())
	}
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("对话失败后不该上报，次数 %d", n)
	}
	if n := f.caCallCount("POST", "/activity/growth/tasks/Model_chat_GLM5.2/claim"); n != 0 {
		t.Fatalf("对话失败后不该领奖，次数 %d", n)
	}
}

// 对话成功但进度上报失败 → 也是软提示（对话已消耗配额，如实告知）。
func TestPanelAutoTaskRunModelChatReportError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runModelChat(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("上报失败不该冒泡 error: %v", err)
	}
	if !strings.HasPrefix(msg, "对话已完成，但进度上报失败：") {
		t.Fatalf("提示 = %q", msg)
	}
}

// RichMeow：桌面指纹完整对话链（6 事件），缺 chat_message_response 就等于没有成功回执。
func TestPanelAutoTaskRunRichMeowSendsFullDesktopChain(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runRichMeow(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已按桌面端指纹上报完整对话事件链（agent_task_created→chat_response）", "runRichMeow 结果")

	reports := f.caCalls("POST", "/v2/report")
	if len(reports) != 1 {
		t.Fatalf("上报次数 = %d, want 1（一次批量）", len(reports))
	}
	caEqStr(t, reports[0].Host, "chat.test", "桌面事件走 chat 域")
	evs := caBodyEvents(t, reports[0])
	want := []string{"agent_task_created", "chat_message_send", "chat_request_send",
		"chat_message_response", "chat_message_status", "chat_request_response"}
	got := caEventCodes(evs)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("事件链 = %v, want %v", got, want)
	}
	if ok, _ := evs[3]["isSuccessful"].(bool); !ok {
		t.Fatalf("chat_message_response 必须 isSuccessful=true: %+v", evs[3])
	}
	for i, ev := range evs {
		if ev["extName"] != "workbuddy-desktop" {
			t.Fatalf("事件 %d 缺桌面指纹 extName=workbuddy-desktop: %v", i, ev["extName"])
		}
	}
}

// Buddy_App / Buddy_App_QQ：企鹅教师助手五连事件。
func TestPanelAutoTaskRunBuddyAppSendsFiveEvents(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runBuddyApp(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已上报 buddyapp 进入五连事件（同时覆盖 Buddy_App 与 Buddy_App_QQ）", "runBuddyApp 结果")

	evs := caBodyEvents(t, f.caCalls("POST", "/v2/report")[0])
	want := []string{"buddyapp_discover_click", "buddyapp_show", "buddyapp_enter_click",
		"buddyapp_auth_confirm_click", "buddyapp_bindaccount_skip_click"}
	if got := caEventCodes(evs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("事件链 = %v, want %v", got, want)
	}
	caEqStr(t, caAsStr(evs[0]["buddyId"]), "cb_y5Dy46tPQGGWtueMxXbe", "载体应用 id")
}

// automation_1 / Library_read：判据事件 + 域（PC 走 chat 域，资料库走 web 域）。
func TestPanelAutoTaskRunAutomationAndLibraryEvents(t *testing.T) {
	caFastGaps(t)

	t.Run("automation_1", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runAutomationCreate(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已上报定时任务创建事件", "runAutomationCreate 结果")
		rep := f.caCalls("POST", "/v2/report")[0]
		caEqStr(t, rep.Host, "chat.test", "PC 事件域")
		caEqStr(t, caAsStr(caBodyEvents(t, rep)[0]["eventCode"]), "automated_task_create_suc", "判据事件")
	})

	t.Run("Library_read", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runLibraryRead(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已上报资料库介绍阅读事件", "runLibraryRead 结果")
		rep := f.caCalls("POST", "/v2/report")[0]
		caEqStr(t, rep.Host, "web.test", "资料库判据在 web 域（chat 域同路径不是判据）")
		caEqStr(t, rep.Header.Get("x-client-platform"), "web", "web 指纹头")
		ev := caBodyEvents(t, rep)[0]
		caEqStr(t, caAsStr(ev["eventCode"]), "web_element_click", "事件类型")
		caEqStr(t, caAsStr(ev["elementId"]), "library_doc_intro_click", "判据元素")
		if !strings.Contains(caAsStr(ev["pageURL"]), "workbuddy.cn/space/d/") {
			t.Fatalf("pageURL = %q, want 含资料库页面地址", caAsStr(ev["pageURL"]))
		}
	})
}

// 夜猫子：窗口内按差额补、窗口外完全不动作（窗口由真实时钟决定，测试按当前分支断言）。
func TestPanelAutoTaskRunBlackCatWindowGate(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("black_cat", 3, 3, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))

	inBefore := upstream.InNightWindow(time.Now())
	msg, err := runBlackCat(p, caAccount("u1"))
	inAfter := upstream.InNightWindow(time.Now())
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if inBefore != inAfter {
		t.Skip("本次执行跨越 23:00/08:00 边界，无法确定性断言窗口分支")
	}

	if !inBefore {
		caEqStr(t, msg, "当前不在 23:00–08:00 计数窗口，行为不计分；网关会在每日 23 点自动补足", "窗口外提示")
		if n := f.caCallCount("GET", "/v2/activity/growth/tasks"); n != 0 {
			t.Fatalf("窗口外不该发起任何上游调用，列表调用 %d", n)
		}
		return
	}
	// 窗口内：已达标 → 不再补足（不浪费对话配额）。
	caEqStr(t, msg, "进度已达标，无需补足", "窗口内已达标提示")
	if n := f.caCallCount("POST", "/v2/chat/completions"); n != 0 {
		t.Fatalf("已达标不该发夜间对话，次数 %d", n)
	}
}

// skill_1：真实对话（服务端 requestId）JOIN skill_info 技能加载事件；缺真实 id 不计数。
func TestPanelAutoTaskRunSkillFreshJoinsRealConversation(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runSkillFresh(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已上报真实对话 + skill_info 技能加载事件", "runSkillFresh 结果")

	chats := f.caCalls("POST", "/v2/chat/completions")
	if len(chats) != 1 {
		t.Fatalf("真实对话次数 = %d, want 1", len(chats))
	}
	convID := chats[0].Header.Get("X-Conversation-ID")
	if convID == "" {
		t.Fatal("对话请求应带 X-Conversation-ID（服务端会话 id）")
	}

	evs := caBodyEvents(t, f.caCalls("POST", "/v2/report")[0])
	last := evs[len(evs)-1]
	caEqStr(t, caAsStr(last["eventCode"]), "skill_info", "末事件必须是 skill_info")
	caEqStr(t, caAsStr(last["requestId"]), "cmb-0123456789abcdef0123456789abcdef", "必须用服务端返回的 requestId")
	caEqStr(t, caAsStr(last["conversationId"]), convID, "会话 id 必须与真实对话一致")
	caEqStr(t, caAsStr(last["toolStatus"]), "success", "工具状态")
	caEqStr(t, caAsStr(last["source"]), "workbuddy-desktop", "指纹来源")
	if caAsStr(last["skillId"]) == "" || caAsStr(last["skillVersion"]) == "" {
		t.Fatalf("skillId/skillVersion 不能为空: %+v", last)
	}
	if n, ok := last["fileCount"].(float64); !ok || n <= 0 {
		t.Fatalf("fileCount = %v, want > 0", last["fileCount"])
	}
	// chat_message_response 必须改写成「模型发起工具调用」语义（技能加载的判据语义）。
	for _, ev := range evs {
		if caAsStr(ev["eventCode"]) == "chat_message_response" {
			caEqStr(t, caAsStr(ev["finishReason"]), "tool_calls", "finishReason")
		}
	}
}

// 真实对话失败 → 直接失败，不发 skill_info（缺真实会话 id 的事件不计数）。
func TestPanelAutoTaskRunSkillFreshChatFailure(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	_, err := runSkillFresh(p, caAccount("u1"))
	if err == nil || !strings.HasPrefix(err.Error(), "真实对话:") {
		t.Fatalf("错误 = %v, want 前缀 \"真实对话:\"", err)
	}
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("对话失败不该上报事件，次数 %d", n)
	}
}

// Expert_lighthouse：市场命中条目以服务端 version/名称为准；chat 带 X-Expert-Id；
// actual_use 用 LOCAL 变体且 type/cost 归零（真实样本口径）。
func TestPanelAutoTaskRunExpertLighthouseUsesMarketEntry(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.experts = []upstream.MarketExpert{{
		ExpertID: "ex_2cvvUZQhDyeJ", ExpertType: "agent",
		DisplayNameZH: "轻量云专家（市场）", ProfessionZH: "轻量云", Version: "9.9.9",
	}}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runExpertLighthouse(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已上报轻量云专家召唤+使用链（真实对话 requestId）", "runExpertLighthouse 结果")

	chats := f.caCalls("POST", "/v2/chat/completions")
	if len(chats) != 1 {
		t.Fatalf("对话次数 = %d, want 1", len(chats))
	}
	caEqStr(t, chats[0].Header.Get("X-Expert-Id"), "ex_2cvvUZQhDyeJ", "对话必须带专家 id")

	// 第一批是召唤链（3 事件）；使用链与其 JOIN 在同一批（chat 链 + actual_use）。
	summon := caBodyEvents(t, f.caCalls("POST", "/v2/report")[0])
	caEqStr(t, caEventCodes(summon)[0], "web_element_click", "召唤链首事件")
	evs := caBodyEvents(t, f.caLastReport(t))
	var created map[string]any
	for _, ev := range evs {
		if caAsStr(ev["eventCode"]) == "agent_task_created" {
			created = ev
		}
	}
	if created == nil {
		t.Fatal("缺 agent_task_created 事件")
	}
	if ok, _ := created["has_expert"].(bool); !ok {
		t.Fatalf("agent_task_created 必须 has_expert=true: %+v", created)
	}
	caEqStr(t, caAsStr(created["expert_id"]), "ex_2cvvUZQhDyeJ", "expert_id")
	caEqStr(t, caAsStr(created["expert_name"]), "轻量云专家（市场）", "expert_name 应用市场命中条目")

	last := evs[len(evs)-1]
	caEqStr(t, caAsStr(last["eventCode"]), "expert_actual_use", "末事件")
	caEqStr(t, caAsStr(last["mode"]), "LOCAL", "轻量云 actual_use 必须 mode=LOCAL")
	caEqStr(t, caAsStr(last["type"]), "", "轻量云 actual_use 的 type 归空")
	if cost, _ := last["cost"].(float64); cost != 0 {
		t.Fatalf("轻量云 actual_use 的 cost 应归零，得到 %v", last["cost"])
	}
	caEqStr(t, caAsStr(last["version"]), "9.9.9", "version 以市场条目为准")
}

// 市场未命中时回落内置轻量云专家档案（离线可用，不因市场抖动整任务失败）。
func TestPanelAutoTaskRunExpertLighthouseFallsBackToBuiltinProfile(t *testing.T) {
	caFastGaps(t)
	f := caNewFake() // experts 为空 → MarketExpertList 成功但无命中
	p := caNewPanel(t, f, caAccount("u1"))

	if _, err := runExpertLighthouse(p, caAccount("u1")); err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	evs := caBodyEvents(t, f.caLastReport(t))
	last := evs[len(evs)-1]
	caEqStr(t, caAsStr(last["id"]), "ex_2cvvUZQhDyeJ", "内置轻量云专家 id")
	caEqStr(t, caAsStr(last["name"]), "腾讯轻量云专家", "内置档案名称")
	caEqStr(t, caAsStr(last["version"]), "1.0.2", "内置档案版本")
}

// Hp_Appearance：主题 set API 留痕 + 皮肤生效事件，且 set 必须先于事件。
func TestPanelAutoTaskRunAppearanceSetsThemeThenEvent(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runAppearance(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已设置主题并上报皮肤生效事件", "runAppearance 结果")

	sets := f.caCalls("POST", "/v2/user-asset/appearance/set")
	if len(sets) != 1 {
		t.Fatalf("主题 set 次数 = %d, want 1", len(sets))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(sets[0].Body), &body); err != nil {
		t.Fatalf("set 体不是 JSON: %v body=%s", err, sets[0].Body)
	}
	caEqStr(t, caAsStr(body["kind"]), "theme", "set kind")
	caEqStr(t, caAsStr(body["resource_key"]), "theme-tkmw7j", "判据主题 resource_key")

	reps := f.caCalls("POST", "/v2/report")
	if len(reps) != 1 {
		t.Fatalf("事件上报次数 = %d, want 1", len(reps))
	}
	ev := caBodyEvents(t, reps[0])[0]
	caEqStr(t, caAsStr(ev["eventCode"]), "appearance_skin_apply", "判据事件（纯 set 不计分）")
	caEqStr(t, caAsStr(ev["action"]), "apply", "action")
	caEqStr(t, caAsStr(ev["source"]), "settings_close", "source")
	caEqStr(t, caAsStr(ev["id"]), "theme-tkmw7j", "事件 id 必须等于所设主题")
	if f.caFirstIndex("POST", "/v2/user-asset/appearance/set") > f.caFirstIndex("POST", "/v2/report") {
		t.Fatal("必须先 set 主题再上报生效事件")
	}
}

// template_5：5 组「模板创建 + 模板使用」事件，模板 id 各不相同。
func TestPanelAutoTaskRunTemplateUseSendsFiveDistinctGroups(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runTemplateUse(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已上报 template_used ×5", "runTemplateUse 结果")

	reps := f.caCalls("POST", "/v2/report")
	if len(reps) != 5 {
		t.Fatalf("上报组数 = %d, want 5", len(reps))
	}
	ids := make([]string, 0, 5)
	names := map[string]bool{}
	for i, rep := range reps {
		evs := caBodyEvents(t, rep)
		var used, created map[string]any
		for _, ev := range evs {
			switch caAsStr(ev["eventCode"]) {
			case "template_used":
				used = ev
			case "agent_task_created_with_template":
				created = ev
			}
		}
		if used == nil || created == nil {
			t.Fatalf("第 %d 组缺 template_used / agent_task_created_with_template: %v", i+1, caEventCodes(evs))
		}
		id := caAsStr(used["template_id"])
		if id == "" || id != caAsStr(created["id"]) {
			t.Fatalf("第 %d 组模板 id 不一致: used=%q created=%q", i+1, id, caAsStr(created["id"]))
		}
		ids = append(ids, id)
		name := caAsStr(created["name"])
		if name == "" || names[name] {
			t.Fatalf("第 %d 组模板名缺失或重复: %q", i+1, name)
		}
		names[name] = true
	}
	caEqStr(t, strings.Join(ids, ","), "1,2,3,4,5", "5 组模板 id 序列")
}

// playbook_prompt：判据是 Dialog 发送 Prompt（playbook_prompt_send），带案例 id 与会话关联。
func TestPanelAutoTaskRunPlaybookPromptSendsDialogPrompt(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runPlaybookPrompt(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已上报 playbook_cta_click + playbook_prompt_send", "runPlaybookPrompt 结果")

	evs := caBodyEvents(t, f.caCalls("POST", "/v2/report")[0])
	var send map[string]any
	for _, ev := range evs {
		if caAsStr(ev["eventCode"]) == "playbook_prompt_send" {
			send = ev
		}
	}
	if send == nil {
		t.Fatalf("缺 playbook_prompt_send: %v", caEventCodes(evs))
	}
	caEqStr(t, caAsStr(send["id"]), "pm-gtm-launch-plan", "灵感案例 id")
	if caAsStr(send["conversationId"]) == "" || caAsStr(send["requestId"]) == "" {
		t.Fatalf("prompt 事件必须带会话关联: %+v", send)
	}
}

// create_canvas：画布创建/打开两事件 + 全链路同一会话 id（JOIN 语义）。
func TestPanelAutoTaskRunCreateCanvasKeepsConversationJoin(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runCreateCanvas(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已上报 wbx_design_canvas_task_create/open", "runCreateCanvas 结果")

	evs := caBodyEvents(t, f.caCalls("POST", "/v2/report")[0])
	var conv, req string
	for _, ev := range evs {
		if v := caAsStr(ev["conversationId"]); v != "" {
			conv = v
		}
		if v := caAsStr(ev["requestId"]); v != "" {
			req = v
		}
	}
	if !strings.HasPrefix(conv, "wb2api-canvas-") || !strings.HasPrefix(req, "wb2api-canvas-req-") {
		t.Fatalf("会话 id 形态异常: conv=%q req=%q", conv, req)
	}
	codes := caEventCodes(evs)
	for _, want := range []string{"wbx_design_canvas_task_create", "wbx_design_canvas_open"} {
		found := false
		for _, c := range codes {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺 %s: %v", want, codes)
		}
	}
	// JOIN 一致性：凡带 requestId/conversationId 的事件必须是同一组 id（否则服务端不归账）。
	for _, ev := range evs {
		if v := caAsStr(ev["requestId"]); v != "" && v != req {
			t.Fatalf("事件 %s 的 requestId 与前序不一致: %q != %q", caAsStr(ev["eventCode"]), v, req)
		}
		if v := caAsStr(ev["conversationId"]); v != "" && v != conv {
			t.Fatalf("事件 %s 的 conversationId 与前序不一致: %q != %q", caAsStr(ev["eventCode"]), v, conv)
		}
	}
}

// ---------------------------------------------------------------------------
// runExpertBatch：专家召唤 + 真实对话 + actual_use（expert_5 / Expert_team_use_3）
// ---------------------------------------------------------------------------

// 单个专家的完整链路：召唤链 → 带 X-Expert-Id 的真实对话 → actual_use（craft 变体）。
// 市场筛选参数必须是 agent（expert_5 的口径）。
func TestPanelAutoTaskRunExpertUseSingleExpertChain(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.experts = []upstream.MarketExpert{{
		ExpertID: "ex_zeta001", ExpertType: "agent", DisplayNameZH: "增长专家", ProfessionZH: "增长", Version: "1.0.0",
	}}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runExpertUse(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已对 1 位真实专家完成召唤+使用链（类型 agent）", "runExpertUse 结果")

	list := f.caCalls("POST", "/portal/operation-platform/market/expert/list")
	if len(list) != 1 {
		t.Fatalf("市场列表调用 = %d, want 1", len(list))
	}
	var listBody map[string]any
	_ = json.Unmarshal([]byte(list[0].Body), &listBody)
	caEqStr(t, caAsStr(listBody["expert_type"]), "agent", "市场筛选类型")

	chats := f.caCalls("POST", "/v2/chat/completions")
	if len(chats) != 1 {
		t.Fatalf("真实对话次数 = %d, want 1", len(chats))
	}
	caEqStr(t, chats[0].Header.Get("X-Expert-Id"), "ex_zeta001", "对话必须带专家 id")

	reps := f.caCalls("POST", "/v2/report")
	if len(reps) != 2 {
		t.Fatalf("上报批次数 = %d, want 2（召唤链 + chat链/usage）", len(reps))
	}
	summonCodes := caEventCodes(caBodyEvents(t, reps[0]))
	wantSummon := []string{"web_element_click", "expert_summon_click", "expert_summoned"}
	if strings.Join(summonCodes, ",") != strings.Join(wantSummon, ",") {
		t.Fatalf("召唤链 = %v, want %v", summonCodes, wantSummon)
	}
	second := caBodyEvents(t, reps[1])
	last := second[len(second)-1]
	caEqStr(t, caAsStr(last["eventCode"]), "expert_actual_use", "末事件")
	caEqStr(t, caAsStr(last["mode"]), "craft", "expert_5 的 actual_use 用 craft 变体")
	caEqStr(t, caAsStr(last["requestId"]), "cmb-0123456789abcdef0123456789abcdef", "必须 JOIN 服务端 requestId")
}

// Expert_team_use_3：市场筛选必须切到 team（拿错类型会做错任务）。
func TestPanelAutoTaskRunExpertTeamUseFiltersTeam(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.experts = []upstream.MarketExpert{{
		ExpertID: "tm_alpha", ExpertType: "team", DisplayNameZH: "增长团队",
		ProfessionZH: "增长", Version: "2.0.0",
	}}
	p := caNewPanel(t, f, caAccount("u1"))

	msg, err := runExpertTeamUse(p, caAccount("u1"))
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	caEqStr(t, msg, "已对 1 位真实专家完成召唤+使用链（类型 team）", "runExpertTeamUse 结果")

	list := f.caCalls("POST", "/portal/operation-platform/market/expert/list")
	var listBody map[string]any
	_ = json.Unmarshal([]byte(list[0].Body), &listBody)
	caEqStr(t, caAsStr(listBody["expert_type"]), "team", "市场筛选类型（团队）")
}

// 召唤/对话失败逐个继续（不中断整批），且成功数上限严格按 count 收口。
func TestPanelAutoTaskRunExpertBatchCountsAndContinues(t *testing.T) {
	caFastGaps(t)

	t.Run("逐个失败继续", func(t *testing.T) {
		f := caNewFake()
		f.experts = []upstream.MarketExpert{
			{ExpertID: "ex_1", ExpertType: "agent", DisplayNameZH: "A"},
			{ExpertID: "ex_2", ExpertType: "agent", DisplayNameZH: "B"},
			{ExpertID: "ex_3", ExpertType: "agent", DisplayNameZH: "C"},
		}
		f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))

		msg, err := runExpertBatch(p, caAccount("u1"), "agent", 5)
		if err != nil {
			t.Fatalf("单项失败不该中断整批: %v", err)
		}
		caEqStr(t, msg, "已对 0 位真实专家完成召唤+使用链（类型 agent）", "失败计数")
		if n := f.caCallCount("POST", "/v2/report"); n != 3 {
			t.Fatalf("召唤上报次数 = %d, want 3（每个专家都试过）", n)
		}
		if n := f.caCallCount("POST", "/v2/chat/completions"); n != 0 {
			t.Fatalf("召唤失败不该继续跑真实对话，次数 %d", n)
		}
	})

	t.Run("成功数收口在 count", func(t *testing.T) {
		f := caNewFake()
		f.experts = []upstream.MarketExpert{
			{ExpertID: "ex_1", ExpertType: "agent", DisplayNameZH: "A"},
			{ExpertID: "ex_2", ExpertType: "agent", DisplayNameZH: "B"},
			{ExpertID: "ex_3", ExpertType: "agent", DisplayNameZH: "C"},
		}
		p := caNewPanel(t, f, caAccount("u1"))

		msg, err := runExpertBatch(p, caAccount("u1"), "agent", 1)
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已对 1 位真实专家完成召唤+使用链（类型 agent）", "count 收口")
		if n := f.caCallCount("POST", "/v2/chat/completions"); n != 1 {
			t.Fatalf("真实对话次数 = %d, want 1（达到 count 即停，不再多消耗配额）", n)
		}
	})
}

// 市场不可用 → 明确失败（不静默"完成 0 位"）。
func TestPanelAutoTaskRunExpertBatchMarketErrors(t *testing.T) {
	caFastGaps(t)

	t.Run("列表为空", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runExpertBatch(p, caAccount("u1"), "agent", 5)
		if err == nil || err.Error() != "专家市场列表为空" {
			t.Fatalf("错误 = %v, want 专家市场列表为空", err)
		}
	})

	t.Run("列表失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["POST /portal/operation-platform/market/expert/list"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runExpertBatch(p, caAccount("u1"), "agent", 5)
		if err == nil || !strings.HasPrefix(err.Error(), "拉取专家列表:") {
			t.Fatalf("错误 = %v, want 前缀 拉取专家列表:", err)
		}
	})
}

// ---------------------------------------------------------------------------
// accountTaskAuto（单任务 HTTP 入口）
// ---------------------------------------------------------------------------

func caAutoReq(p *Panel, uid, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := caPostUID("/panel/api/accounts/"+uid+"/tasks/auto", uid, body)
	p.accountTaskAuto(rec, req)
	return rec
}

// task_code 校验：空体 / 非法 JSON / 空 code 一律 400（不能默认跑某个任务）。
func TestPanelAutoTaskAccountTaskAutoValidatesTaskCode(t *testing.T) {
	caFastGaps(t)
	for _, body := range []string{"", "{", `{}`, `{"task_code":""}`} {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		rec := caAutoReq(p, "u1", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%q code=%d, want 400", body, rec.Code)
		}
		if got := caStr(t, caDecode(t, rec), "error"); got != "task_code required" {
			t.Fatalf("body=%q error=%q", body, got)
		}
		if n := len(f.calls); n != 0 {
			t.Fatalf("body=%q 不该发起上游调用，次数 %d", body, n)
		}
	}
}

// 账号不存在 → 404（走 accountByUID 统一口径）。
func TestPanelAutoTaskAccountTaskAutoUnknownAccount(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))
	rec := caAutoReq(p, "nobody", `{"task_code":"chat_5"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, want 404", rec.Code)
	}
	caEqStr(t, caStr(t, caDecode(t, rec), "error"), "account not found", "错误文本")
}

// 无自动化动作的任务 → 501（明确告知需客户端内交互，而不是静默失败）。
func TestPanelAutoTaskAccountTaskAutoUnsupportedCode(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))
	rec := caAutoReq(p, "u1", `{"task_code":"needs_client_ui"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code=%d, want 501", rec.Code)
	}
	if !strings.Contains(caStr(t, caDecode(t, rec), "error"), "需要客户端内交互") {
		t.Fatalf("错误文本应说明原因: %s", rec.Body.String())
	}
}

// 该账号没有此任务 → 404；上游列表失败 → 502（两类错误不能混）。
func TestPanelAutoTaskAccountTaskAutoTaskAbsentAndListError(t *testing.T) {
	caFastGaps(t)

	t.Run("任务不存在", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		rec := caAutoReq(p, "u1", `{"task_code":"chat_5"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("code=%d, want 404", rec.Code)
		}
		caEqStr(t, caStr(t, caDecode(t, rec), "error"), "该账号没有此任务", "错误文本")
	})

	t.Run("列表失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["GET /v2/activity/growth/tasks"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		rec := caAutoReq(p, "u1", `{"task_code":"chat_5"}`)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("code=%d, want 502", rec.Code)
		}
		if !strings.HasPrefix(caStr(t, caDecode(t, rec), "error"), "list tasks:") {
			t.Fatalf("错误文本应带前缀 list tasks: %s", rec.Body.String())
		}
	})
}

// 已领取 → 200 skipped，且**不执行任何动作**（幂等，不浪费上游配额）。
func TestPanelAutoTaskAccountTaskAutoAlreadyClaimedSkips(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "claimed"))
	p := caNewPanel(t, f, caAccount("u1"))

	rec := caAutoReq(p, "u1", `{"task_code":"chat_5"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := caDecode(t, rec)
	if !caBool(t, out, "skipped") || !caBool(t, out, "ok") {
		t.Fatalf("应回 skipped=true ok=true: %+v", out)
	}
	caEqStr(t, caStr(t, out, "message"), "该任务已领取过奖励", "提示")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("已领取不该上报，次数 %d", n)
	}
	if n := f.caCallCount("POST", "/activity/growth/tasks/chat_5/claim"); n != 0 {
		t.Fatalf("已领取不该再领奖，次数 %d", n)
	}
}

// 账号锁被占用时 → 409（同账号动作串行，不并发重跑真实对话）。
func TestPanelAutoTaskAccountTaskAutoLockedConflict(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, "accepted"))
	p := caNewPanel(t, f, caAccount("u1"))
	if !p.tryLockAccount("u1") {
		t.Fatal("前置：加锁应成功")
	}
	defer p.unlockAccount("u1")

	rec := caAutoReq(p, "u1", `{"task_code":"chat_5"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d, want 409", rec.Code)
	}
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("被锁时应直接拒绝，不该跑动作，上报次数 %d", n)
	}
}

// 达标即自动领奖：整包响应契约（进度前后 / claimable / claimed / 奖励数）一次钉住。
func TestPanelAutoTaskAccountTaskAutoAutoClaimsOnWeb(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
	f.claimCredit, f.claimEnergy = 100, 5
	f.onReport = func(call caCall) {
		if call.Host == "billing.test" { // 活跃上报落到 billing 域
			f.caAdvance("chat_5", false, 5, "completed")
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	rec := caAutoReq(p, "u1", `{"task_code":"chat_5"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := caDecode(t, rec)
	caEqStr(t, caStr(t, out, "message"), "已补报 1 条对话事件；已自动领奖 +100 分 +5 能", "响应 message")
	caEqStr(t, caStr(t, out, "progress_before"), "4/5", "progress_before")
	caEqStr(t, caStr(t, out, "progress_after"), "5/5", "progress_after")
	if !caBool(t, out, "claimable") || !caBool(t, out, "claimed") {
		t.Fatalf("claimable/claimed 应为 true: %+v", out)
	}
	if got := caNum(t, out, "credit"); got != 100 {
		t.Fatalf("credit = %v, want 100", got)
	}
	if got := caNum(t, out, "energy"); got != 5 {
		t.Fatalf("energy = %v, want 5", got)
	}
	if caBool(t, out, "attempt") {
		t.Fatal("chat_5 不是尝试型任务，attempt 应为 false")
	}
	if !caBool(t, out, "verify_supported") {
		t.Fatal("verify_supported 应为 true")
	}

	claims := f.caCalls("POST", "/activity/growth/tasks/chat_5/claim")
	if len(claims) != 1 {
		t.Fatalf("领奖次数 = %d, want 1", len(claims))
	}
	caEqStr(t, claims[0].Host, "web.test", "非 mp 任务走 Web 端领奖端点")
}

// 领奖返回 0 分 0 能（此前已领）→ 文案不得说"+0 分"。
func TestPanelAutoTaskAccountTaskAutoClaimZeroReward(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
	f.alreadyClaimed = true // 上游回 already_claimed → credit/energy 都是 0
	f.onReport = func(call caCall) {
		if call.Host == "billing.test" {
			f.caAdvance("chat_5", false, 5, "completed")
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	out := caDecode(t, caAutoReq(p, "u1", `{"task_code":"chat_5"}`))
	caEqStr(t, caStr(t, out, "message"), "已补报 1 条对话事件；奖励此前已领取", "响应 message")
	if !caBool(t, out, "claimed") {
		t.Fatal("claimed 仍应为 true（领奖接口成功返回）")
	}
}

// 达标但领奖失败 → status 仍 ok，附 claim_error 供前端提示手动重试。
func TestPanelAutoTaskAccountTaskAutoClaimFailureReported(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
	f.onReport = func(call caCall) {
		if call.Host == "billing.test" {
			f.caAdvance("chat_5", false, 5, "completed")
		}
	}
	f.raw["POST /activity/growth/tasks/chat_5/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	rec := caAutoReq(p, "u1", `{"task_code":"chat_5"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200（领奖失败必须掩盖主流程）", rec.Code)
	}
	out := caDecode(t, rec)
	caEqStr(t, caStr(t, out, "message"), "已补报 1 条对话事件；达标但领奖失败，可在任务列表手动点「领取」重试", "响应 message")
	if caStr(t, out, "claim_error") == "" {
		t.Fatalf("应附 claim_error: %+v", out)
	}
	if _, ok := out["claimed"]; ok {
		t.Fatalf("领奖失败不该出现 claimed: %+v", out)
	}
}

// 动作执行 error（真实对话 500）→ 502 + "执行失败: "（不写 ok:true 的假成功）。
func TestPanelAutoTaskAccountTaskAutoRunErrorIs502(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("Model_chat_GLM5.2", 0, 1, "accepted"))
	f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"chat down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	rec := caAutoReq(p, "u1", `{"task_code":"Model_chat_GLM5.2"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	errText := caStr(t, caDecode(t, rec), "error")
	if !strings.HasPrefix(errText, "执行失败: ") || !strings.Contains(errText, "http 500") {
		t.Fatalf("错误文本 = %q，want 前缀 \"执行失败: \" 且含 http 500", errText)
	}
}

// mp 任务的回读与领奖必须走 mp 口径（默认列表查不到、Web 端不可领）。
func TestPanelAutoTaskAccountTaskAutoMPUsesMPClaim(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_1", 1, 1, "accepted")) // 达标未领（免去 mp 上报的抖动等待）
	f.claimCredit, f.claimEnergy = 100, 5
	f.claimKeepsState = true // 领奖后回读仍显示可领 → 覆盖 handler 的自动领奖 mp 分支
	p := caNewPanel(t, f, caAccount("u1"))

	rec := caAutoReq(p, "u1", `{"task_code":"Sequential_Tasks_1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := caDecode(t, rec)
	caEqStr(t, caStr(t, out, "progress_before"), "1/1", "progress_before（mp 口径读回）")
	caEqStr(t, caStr(t, out, "progress_after"), "1/1", "progress_after")
	caEqStr(t, caStr(t, out, "message"), "已领取奖励（+100c +5e）；已自动领奖 +100 分 +5 能", "响应 message")
	if !caBool(t, out, "claimed") {
		t.Fatalf("应自动领奖: %+v", out)
	}
	if got := caNum(t, out, "credit"); got != 100 {
		t.Fatalf("credit = %v, want 100", got)
	}

	claims := f.caCalls("POST", "/activity/growth/tasks/Sequential_Tasks_1/claim")
	if len(claims) != 2 { // 1 次动作内领奖 + 1 次 handler 自动领奖
		t.Fatalf("领奖次数 = %d, want 2", len(claims))
	}
	for _, c := range claims {
		caEqStr(t, c.Host, "chat.test", "mp 任务必须走 chat 域领奖")
		caEqStr(t, c.Header.Get("X-Client-Platform"), "miniprogram", "mp 领奖头")
	}
	// mp 任务误用 Web 端领奖端点会领不到（web 域同路径不存在）。
	for _, c := range f.calls {
		if c.Host == "web.test" && strings.HasSuffix(c.Path, "/claim") {
			t.Fatalf("mp 任务误用 Web 端领奖: %+v", c)
		}
	}
}

// ---------------------------------------------------------------------------
// runAutoAll（全量自动完成）
// ---------------------------------------------------------------------------

func caFindItem(t *testing.T, out []map[string]any, code string) map[string]any {
	t.Helper()
	for _, m := range out {
		if caAsStr(m["task_code"]) == code {
			return m
		}
	}
	codes := make([]string, 0, len(out))
	for _, m := range out {
		codes = append(codes, caAsStr(m["task_code"]))
	}
	t.Fatalf("结果里没有 %q 项；实际项: %v", code, codes)
	return nil
}

// 批量接受只挑「未领、未锁、未接受/完成」的任务，mp 口径单独走一批。
func TestPanelAutoTaskRunAutoAllBatchAcceptsEligibleOnly(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, ""))
	f.caSetTask(caTask("first_buddy", 0, 1, "accepted"))
	f.caSetTask(caTask("RichMeow_Chat", 1, 1, "claimed"))
	f.caSetTask(caLockedTask("black_cat", 0, 3, ""))
	f.caSetMPTask(caTask("Sequential_Tasks_1", 1, 1, ""))         // 达标未领 → 进 mp 批量接受、动作阶段 skipped
	f.caSetMPTask(caTask("Sequential_Tasks_3", 1, 1, "accepted")) // 已接受 → 不进批量
	p := caNewPanel(t, f, caAccount("u1"))

	out := p.runAutoAll(caAccount("u1"))

	item := caFindItem(t, out, "(批量接受)")
	caEqStr(t, caStr(t, item, "status"), "done", "(批量接受).status")
	caEqStr(t, caStr(t, item, "message"), "已接受 1 个任务", "(批量接受).message")

	var defAccept, mpAccept *caCall
	for _, c := range f.caCalls("POST", "/v2/activity/growth/tasks/accept") {
		c := c
		if c.Header.Get("X-Client-Platform") == "miniprogram" {
			mpAccept = &c
		} else {
			defAccept = &c
		}
	}
	if defAccept == nil {
		t.Fatal("默认口径批量 accept 未发出")
	}
	var body struct {
		TaskCodes []string `json:"task_codes"`
	}
	_ = json.Unmarshal([]byte(defAccept.Body), &body)
	caEqStr(t, strings.Join(body.TaskCodes, ","), "chat_5", "默认口径只接受未接受的那一个（锁定的不碰）")

	mpItem := caFindItem(t, out, "(批量接受-mp)")
	caEqStr(t, caStr(t, mpItem, "status"), "done", "(批量接受-mp).status")
	caEqStr(t, caStr(t, mpItem, "message"), "已接受 1 个小程序任务", "(批量接受-mp).message")
	if mpAccept == nil {
		t.Fatal("mp 口径批量 accept 未发出（默认列表不含 mp 码，必须单独一批）")
	}
	var mpBody struct {
		TaskCodes []string `json:"task_codes"`
	}
	_ = json.Unmarshal([]byte(mpAccept.Body), &mpBody)
	caEqStr(t, strings.Join(mpBody.TaskCodes, ","), "Sequential_Tasks_1", "mp 口径接受的码")
}

// 批量接受失败只记录 error 项，不阻塞后续逐项动作。
func TestPanelAutoTaskRunAutoAllBatchAcceptErrorDoesNotBlock(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "claimed")) // 终态 → 不进批量
	f.raw["POST /v2/activity/growth/tasks/accept"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"acc down"}`}
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	out := p.runAutoAll(caAccount("u1"))

	// 默认口径无待接受 → 无 "(批量接受)" 项；mp 一批失败 → error 项且提示不阻塞。
	mpItem := caFindItem(t, out, "(批量接受-mp)")
	caEqStr(t, caStr(t, mpItem, "status"), "error", "(批量接受-mp).status")
	if !strings.Contains(caStr(t, mpItem, "message"), "接受小程序任务失败（不阻塞后续）") {
		t.Fatalf("message = %q", caStr(t, mpItem, "message"))
	}
	// 后续逐项动作照跑：chat_5 已领取 → skipped。
	caEqStr(t, caStr(t, caFindItem(t, out, "chat_5"), "status"), "skipped", "已领取项")
}

// 逐项状态：已领取 / 已达标 / 无此任务 → skipped（且都写清原因）。
func TestPanelAutoTaskRunAutoAllSkipsTerminalAndAbsent(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "accepted"))       // 达标未领 → skipped（不作动作）
	f.caSetTask(caTask("RichMeow_Chat", 1, 1, "claimed")) // 已领 → skipped
	// 其余动作（first_buddy / Model_chat_* 等）账号下都没有 → skipped "该账号无此任务"
	p := caNewPanel(t, f, caAccount("u1"))

	out := p.runAutoAll(caAccount("u1"))

	caEqStr(t, caStr(t, caFindItem(t, out, "chat_5"), "message"), "已完成（5/5）", "达标项 message")
	caEqStr(t, caStr(t, caFindItem(t, out, "RichMeow_Chat"), "message"), "已完成（1/1）", "已领项 message")
	caEqStr(t, caStr(t, caFindItem(t, out, "first_buddy"), "message"), "该账号无此任务", "缺失项 message")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("skipped 项不该有任何动作上报，次数 %d", n)
	}
}

// 查询失败 → 该项 status=error（不中断整轮）。
func TestPanelAutoTaskRunAutoAllListErrorItem(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.raw["GET /v2/activity/growth/tasks"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	out := p.runAutoAll(caAccount("u1"))
	if len(out) != len(autoActions) {
		t.Fatalf("项数 = %d, want %d（每个动作一项）", len(out), len(autoActions))
	}
	for _, item := range out {
		caEqStr(t, caStr(t, item, "status"), "error", caAsStr(item["task_code"])+".status")
		if !strings.HasPrefix(caStr(t, item, "message"), "查询失败: ") {
			t.Fatalf("%s message = %q", caAsStr(item["task_code"]), caStr(t, item, "message"))
		}
	}
}

// 动作返回 error → 该项 status=error 且 message 是错误原文。
func TestPanelAutoTaskRunAutoAllActionErrorItem(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("Model_chat_GLM5.2", 0, 1, "accepted"))
	f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"chat down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	item := caFindItem(t, p.runAutoAll(caAccount("u1")), "Model_chat_GLM5.2")
	caEqStr(t, caStr(t, item, "status"), "error", "动作失败项 status")
	if !strings.Contains(caStr(t, item, "message"), "http 500") {
		t.Fatalf("message = %q, want 含上游状态码 500", caStr(t, item, "message"))
	}
}

// 达标即自动领奖（Web 口径）+ message 追加；进度回填 progress_after。
func TestPanelAutoTaskRunAutoAllAutoClaimsWeb(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
	f.claimCredit, f.claimEnergy = 100, 5
	f.onReport = func(call caCall) {
		if call.Host == "billing.test" {
			f.caAdvance("chat_5", false, 5, "completed")
		}
	}
	p := caNewPanel(t, f, caAccount("u1"))

	item := caFindItem(t, p.runAutoAll(caAccount("u1")), "chat_5")
	caEqStr(t, caStr(t, item, "status"), "done", "chat_5 status")
	caEqStr(t, caStr(t, item, "message"), "已补报 1 条对话事件；已自动领奖 +100 分 +5 能", "chat_5 message")
	caEqStr(t, caStr(t, item, "progress_after"), "5/5", "progress_after")
	if !caBool(t, item, "claimable") || !caBool(t, item, "claimed") {
		t.Fatalf("claimable/claimed 应为 true: %+v", item)
	}
	claims := f.caCalls("POST", "/activity/growth/tasks/chat_5/claim")
	if len(claims) != 1 {
		t.Fatalf("领奖次数 = %d, want 1", len(claims))
	}
	caEqStr(t, claims[0].Host, "web.test", "Web 端领奖域")
}

// mp 项的领奖走 chat 域 + mp 头（口径不对会领不到）。
func TestPanelAutoTaskRunAutoAllAutoClaimsMP(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "accepted"))
	f.experts = []upstream.MarketExpert{{ExpertID: "ex_abc123", ExpertType: "agent", DisplayNameZH: "运营专家"}}
	f.claimCredit, f.claimEnergy = 200, 5
	f.claimKeepsState = true // 领奖后状态未即时刷新 → 回读仍 claimable
	f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_2", true, 1, "completed") }
	p := caNewPanel(t, f, caAccount("u1"))

	item := caFindItem(t, p.runAutoAll(caAccount("u1")), "Sequential_Tasks_2")
	caEqStr(t, caStr(t, item, "message"), "任务点亮并领取奖励（+200c +5e）；已自动领奖 +200 分 +5 能", "mp 项 message")
	if !caBool(t, item, "claimed") {
		t.Fatalf("应自动领奖: %+v", item)
	}
	claims := f.caCalls("POST", "/activity/growth/tasks/Sequential_Tasks_2/claim")
	if len(claims) != 2 { // 1 次动作内领奖 + 1 次 runAutoAll 自动领奖
		t.Fatalf("领奖次数 = %d, want 2", len(claims))
	}
	for _, c := range claims {
		caEqStr(t, c.Host, "chat.test", "mp 领奖域")
		caEqStr(t, c.Header.Get("X-Client-Platform"), "miniprogram", "mp 领奖头")
	}
}

// 领奖返回 0/0 → "奖励此前已领取"；领奖失败 → claim_error，status 仍 done。
func TestPanelAutoTaskRunAutoAllClaimMessages(t *testing.T) {
	caFastGaps(t)

	t.Run("此前已领取", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
		f.alreadyClaimed = true
		f.onReport = func(call caCall) {
			if call.Host == "billing.test" {
				f.caAdvance("chat_5", false, 5, "completed")
			}
		}
		p := caNewPanel(t, f, caAccount("u1"))
		item := caFindItem(t, p.runAutoAll(caAccount("u1")), "chat_5")
		caEqStr(t, caStr(t, item, "message"), "已补报 1 条对话事件；奖励此前已领取", "message")
	})

	t.Run("领奖失败不掩盖主流程", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
		f.onReport = func(call caCall) {
			if call.Host == "billing.test" {
				f.caAdvance("chat_5", false, 5, "completed")
			}
		}
		f.raw["POST /activity/growth/tasks/chat_5/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		item := caFindItem(t, p.runAutoAll(caAccount("u1")), "chat_5")
		caEqStr(t, caStr(t, item, "status"), "done", "status 仍应为 done")
		caEqStr(t, caStr(t, item, "message"), "已补报 1 条对话事件；达标但领奖失败（可在列表手动重试）", "message")
		if caStr(t, item, "claim_error") == "" {
			t.Fatalf("应附 claim_error: %+v", item)
		}
	})
}

// ---------------------------------------------------------------------------
// accountTaskAutoAll（一键全量 HTTP 入口）
// ---------------------------------------------------------------------------

func TestPanelAutoTaskAccountTaskAutoAllRespondsWithResults(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "claimed")) // 终态 → 不触发真实动作
	a := caAccount("u1")
	p := caNewPanel(t, f, a)

	rec := httptest.NewRecorder()
	req := caPostUID("/panel/api/accounts/u1/tasks/auto_all", "u1", "")
	p.accountTaskAutoAll(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := caDecode(t, rec)
	if !caBool(t, out, "ok") {
		t.Fatalf("ok 应为 true: %+v", out)
	}
	results, ok := out["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("results 应是非空数组: %+v", out["results"])
	}
	// 流水线结束后账号锁必须已释放（否则后续任何入口都永久 409）。
	if !caWaitUnlocked(t, p, "u1") {
		t.Fatal("一键完成后必须释放账号锁")
	}
}

func TestPanelAutoTaskAccountTaskAutoAllConflict(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "claimed"))
	p := caNewPanel(t, f, caAccount("u1"))
	if !p.tryLockAccount("u1") {
		t.Fatal("前置：加锁应成功")
	}
	defer p.unlockAccount("u1")

	rec := httptest.NewRecorder()
	p.accountTaskAutoAll(rec, caPostUID("/panel/api/accounts/u1/tasks/auto_all", "u1", ""))
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d, want 409", rec.Code)
	}
}

func TestPanelAutoTaskAccountTaskAutoAllUnknownAccount(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))
	rec := httptest.NewRecorder()
	p.accountTaskAutoAll(rec, caPostUID("/panel/api/accounts/ghost/tasks/auto_all", "ghost", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code=%d, want 404", rec.Code)
	}
}

// HTTP 侧超时（ctx 已取消）→ 504，但后台流水线仍在跑；锁由**流水线**释放而非超时分支。
func TestPanelAutoTaskAccountTaskAutoAllTimeoutKeepsPipelineRunning(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "claimed"))
	f.gate = make(chan struct{}) // 第一次任务列表查询阻塞，保证 select 走超时分支
	a := caAccount("u1")
	p := caNewPanel(t, f, a)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 请求上下文立即超时
	req := caPostUID("/panel/api/accounts/u1/tasks/auto_all", "u1", "").WithContext(ctx)
	rec := httptest.NewRecorder()
	p.accountTaskAutoAll(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("code=%d, want 504; body=%s", rec.Code, rec.Body.String())
	}
	// 超时后流水线仍在跑：重复点击必须被 409 挡住（锁未释放）。
	if p.tryLockAccount("u1") {
		p.unlockAccount("u1")
		t.Fatal("超时返回时流水线仍在跑，锁不应已释放（否则可并发两轮）")
	}
	close(f.gate) // 放行流水线，让它自然收尾
	if !caWaitUnlocked(t, p, "u1") {
		t.Fatal("流水线结束后必须释放账号锁（锁在 goroutine 内释放）")
	}
}

// ---------------------------------------------------------------------------
// taskcenter.go：扫描
// ---------------------------------------------------------------------------

// growthPending 的四条判据：已领/锁定/无动作都不入队；达标未领仍入队（队列会去领奖）。
func TestPanelAutoTaskGrowthPending(t *testing.T) {
	cases := []struct {
		name string
		task upstream.Task
		want bool
	}{
		{"未完成的已知任务", caTask("chat_5", 1, 5, "accepted"), true},
		{"已领取", caTask("chat_5", 5, 5, "claimed"), false},
		{"锁定", caLockedTask("Sequential_Tasks_4", 0, 1, "accepted"), false},
		{"达标未领（可自动领奖）", caTask("chat_5", 5, 5, "accepted"), true},
		{"达标未领但无动作", caTask("no_action", 5, 5, "accepted"), false},
		{"未完成但无动作", caTask("no_action", 0, 5, "accepted"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := growthPending(c.task); got != c.want {
				t.Fatalf("growthPending(%s current=%d target=%d accept=%s) = %v, want %v",
					c.task.TaskCode, c.task.Current, c.task.Target, c.task.AcceptStatus, got, c.want)
			}
		})
	}
}

// 扫描：默认口径 + mp 口径合并去重；global/企业版/禁用账号被门控掉（零上游调用）。
func TestPanelAutoTaskTasksScanAllGatesAndMerges(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, "accepted"))
	f.caSetTask(caTask("first_buddy", 1, 1, "claimed"))
	f.caSetTask(caLockedTask("black_cat", 0, 3, "accepted"))
	f.caSetTask(caTask("unknown_code", 0, 1, "accepted"))
	f.caSetMPTask(caTask("chat_5", 0, 5, "accepted")) // mp 是超集：同码必须去重
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, ""))

	cn := caAccount("u1")
	global := &auth.Auth{UID: "u2", Nickname: "全球号", AccessToken: "t2", Domain: "www.workbuddy.ai"}
	enterprise := &auth.Auth{UID: "u3", Nickname: "企业号", AccessToken: "t3", EnterpriseID: "ent-1"}
	disabled := caAccount("u4")
	p := caNewPanel(t, f, cn, global, enterprise, disabled)
	p.cfg.Pool.Disable("u4", "test")

	rec := httptest.NewRecorder()
	p.tasksScanAll(rec, httptest.NewRequest(http.MethodPost, "/panel/api/tasks/scan_all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	out := caDecode(t, rec)
	rows, _ := out["accounts"].([]any)
	if len(rows) != 4 {
		t.Fatalf("行数 = %d, want 4（每个账号一行）", len(rows))
	}
	byUID := map[string]map[string]any{}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		byUID[caMapStr(m, "uid")] = m
	}

	// CN 账号：只留「未完成且可自动化」的 chat_5 + mp 专属 Sequential_Tasks_1。
	cnRow := byUID["u1"]
	if cnRow == nil {
		t.Fatalf("缺 u1 行: %+v", byUID)
	}
	caEqStr(t, caMapStr(cnRow, "nickname"), "昵称-u1", "u1 nickname")
	var codes []string
	for _, g := range cnRow["growth"].([]any) {
		gm, _ := g.(map[string]any)
		codes = append(codes, caMapStr(gm, "task_code"))
	}
	caEqStr(t, strings.Join(codes, ","), "chat_5,Sequential_Tasks_1", "u1 待办码（去重 + 过滤）")

	// global / 企业版：门控跳过，行存在但无待办、零上游调用。
	for _, uid := range []string{"u2", "u3"} {
		row := byUID[uid]
		if row == nil {
			t.Fatalf("缺 %s 行", uid)
		}
		if _, has := row["growth"]; has {
			t.Fatalf("%s 不应有待办: %+v", uid, row["growth"])
		}
		if n := f.caCallsByUID(uid); n != 0 {
			t.Fatalf("%s 被门控，不该发上游请求，次数 %d", uid, n)
		}
	}
	// 禁用账号：行末保留占位（uid 为空），不参与扫描。
	if got := caMapStr(byUID[""], "uid"); got != "" {
		t.Fatalf("禁用账号行应为空占位: %+v", byUID[""])
	}
	if n := f.caCallsByUID("u4"); n != 0 {
		t.Fatalf("禁用账号不该发上游请求，次数 %d", n)
	}
	if got := caNum(t, out, "pending_count"); got != 2 {
		t.Fatalf("pending_count = %v, want 2", got)
	}
}

// 默认列表失败 → 该行带 growth_error，仍 200（单账号失败不影响其它账号）。
func TestPanelAutoTaskTasksScanAllRecordsListError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.defaultListRaw = &caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`} // 仅默认口径失败
	f.caSetMPTask(caTask("Sequential_Tasks_1", 0, 1, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	rec := httptest.NewRecorder()
	p.tasksScanAll(rec, httptest.NewRequest(http.MethodPost, "/panel/api/tasks/scan_all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	out := caDecode(t, rec)
	row, _ := out["accounts"].([]any)[0].(map[string]any)
	if !strings.Contains(caMapStr(row, "growth_error"), "http 500") {
		t.Fatalf("growth_error = %q, want 含上游状态码 500", caMapStr(row, "growth_error"))
	}
	// mp 列表仍成功 → 待办里应有 mp 任务。
	if !strings.Contains(rec.Body.String(), "Sequential_Tasks_1") {
		t.Fatalf("mp 待办应合并进来: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// taskcenter.go：执行队列
// ---------------------------------------------------------------------------

func caQueueConc(p *Panel) int {
	q := p.queue()
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.conc
}

// 队列：账号内按 autoActions 顺序执行，先批量 accept（否则上报不归账），逐项写回状态。
func TestPanelAutoTaskStartGrowthQueueOrdersAndAcceptsFirst(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("Model_chat_GLM5.2", 0, 1, ""))
	f.caSetTask(caTask("chat_5", 0, 5, ""))
	f.caSetTask(caTask("RichMeow_Chat", 0, 1, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	started, total, seq, msg := p.startGrowthQueue(1, true)
	if !started || total != 3 || seq != 1 || msg != "" {
		t.Fatalf("startGrowthQueue = (%v,%d,%d,%q), want (true,3,1,\"\")", started, total, seq, msg)
	}
	if !caWaitQueueIdle(t, p) {
		t.Fatal("队列未在 3s 内结束（goroutine 未回收）")
	}

	items := caQueueItems(p)
	codes := make([]string, 0, len(items))
	for _, it := range items {
		codes = append(codes, it.Code)
		caEqStr(t, it.Kind, "growth", "item.Kind")
		caEqStr(t, it.Status, "done", it.Code+".Status")
		caEqStr(t, it.Nickname, "昵称-u1", it.Code+".Nickname")
		caEqStr(t, it.Title, "任务-"+it.Code, it.Code+".Title")
		if it.Message == "" {
			t.Fatalf("%s 缺 message（前端看不到结果）", it.Code)
		}
	}
	caEqStr(t, strings.Join(codes, ","), "chat_5,Model_chat_GLM5.2,RichMeow_Chat", "队列顺序（autoActions 依赖序）")
	caEqStr(t, items[0].Message, "已补报 5 条对话事件（进度 0/5）", "chat_5 结果（动作消息 + 进度后缀）")

	// 批量 accept 必须早于任何动作上报（上游对 not_accepted 的任务不计数）。
	accs := f.caCalls("POST", "/v2/activity/growth/tasks/accept")
	if len(accs) == 0 {
		t.Fatal("队列路径必须批量 accept（此前漏了这步，表现为上报 200 但进度不归账）")
	}
	var body struct {
		TaskCodes []string `json:"task_codes"`
	}
	_ = json.Unmarshal([]byte(accs[0].Body), &body)
	sort.Strings(body.TaskCodes) // 接受顺序由上游列表顺序决定，这里只钉集合
	caEqStr(t, strings.Join(body.TaskCodes, ","), "Model_chat_GLM5.2,RichMeow_Chat,chat_5", "批量 accept 的码集合")
	if f.caFirstIndex("POST", "/v2/activity/growth/tasks/accept") > f.caFirstIndex("POST", "/v2/report") {
		t.Fatal("必须先批量 accept 再跑动作（否则上报不归账）")
	}

	q := p.queue()
	q.mu.Lock()
	conc, running, startedAt, seqNow := q.conc, q.running, q.startedAt, q.seq
	q.mu.Unlock()
	if conc != 1 || running || startedAt.IsZero() || seqNow != 1 {
		t.Fatalf("队列状态 conc=%d running=%v startedAt=%v seq=%d", conc, running, startedAt, seqNow)
	}
}

// 队列已在跑 → 直接冲突返回（seq=-1），不排队不覆盖状态。
func TestPanelAutoTaskStartGrowthQueueConflictWhileRunning(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, ""))
	p := caNewPanel(t, f, caAccount("u1"))
	q := p.queue()
	q.mu.Lock()
	q.running = true
	q.mu.Unlock()
	t.Cleanup(func() {
		q.mu.Lock()
		q.running = false
		q.mu.Unlock()
	})

	started, total, seq, msg := p.startGrowthQueue(1, true)
	if started || total != 0 || seq != -1 {
		t.Fatalf("startGrowthQueue = (%v,%d,%d), want (false,0,-1)", started, total, seq)
	}
	caEqStr(t, msg, "队列正在执行中（可在任务中心查看进度）", "冲突提示")
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("冲突时不该执行任何动作，上报次数 %d", n)
	}
}

// 无待办 → 不启动，且必须把 running 占位回滚（否则后续永远冲突）。
func TestPanelAutoTaskStartGrowthQueueRollsBackWhenEmpty(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("not_automatable", 0, 1, "")) // 无 autoAction → 不入待办
	p := caNewPanel(t, f, caAccount("u1"))

	started, total, seq, msg := p.startGrowthQueue(2, true)
	if started || total != 0 || seq != 0 {
		t.Fatalf("startGrowthQueue = (%v,%d,%d), want (false,0,0)", started, total, seq)
	}
	caEqStr(t, msg, "全部账号没有待办任务", "空队列提示")
	q := p.queue()
	q.mu.Lock()
	running, startedAt := q.running, q.startedAt
	q.mu.Unlock()
	if running {
		t.Fatal("空队列必须回滚 running（否则后续启动永远 409）")
	}
	if !startedAt.IsZero() {
		t.Fatalf("空队列应清空 startedAt，得到 %v", startedAt)
	}
}

// 并发参数夹取到 [1,4]。
func TestPanelAutoTaskStartGrowthQueueClampsConcurrency(t *testing.T) {
	caFastGaps(t)

	f1 := caNewFake()
	f1.caSetTask(caTask("chat_5", 0, 5, ""))
	p1 := caNewPanel(t, f1, caAccount("u1"))
	if started, _, _, _ := p1.startGrowthQueue(0, true); !started {
		t.Fatal("concurrency=0 应被夹到 1 并启动")
	}
	if got := caQueueConc(p1); got != 1 {
		t.Fatalf("concurrency=0 → %d, want 1", got)
	}
	if !caWaitQueueIdle(t, p1) {
		t.Fatal("队列未结束")
	}

	f2 := caNewFake()
	f2.caSetTask(caTask("chat_5", 0, 5, ""))
	p2 := caNewPanel(t, f2, caAccount("u1"))
	if started, _, _, _ := p2.startGrowthQueue(9, true); !started {
		t.Fatal("concurrency=9 应被夹到 4 并启动")
	}
	if got := caQueueConc(p2); got != 4 {
		t.Fatalf("concurrency=9 → %d, want 4", got)
	}
	if !caWaitQueueIdle(t, p2) {
		t.Fatal("队列未结束")
	}
}

// 调度器 growth 回调与按钮同管线（异步启动、逐项执行）。
func TestPanelAutoTaskRunGrowthQueueOnceUsesSamePipeline(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	p.RunGrowthQueueOnce()
	if !caWaitQueueIdle(t, p) {
		t.Fatal("RunGrowthQueueOnce 未启动队列或未结束")
	}
	items := caQueueItems(p)
	if len(items) != 1 || items[0].Code != "chat_5" {
		t.Fatalf("定时队列项 = %+v, want [chat_5]", items)
	}
	caEqStr(t, items[0].Status, "done", "定时队列项状态")
}

// HTTP 入口：growth 默认开、并发夹取、非法体也照跑；运行中 → 409。
func TestPanelAutoTaskTasksRunQueueHandler(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	// 非法 JSON（decode 失败被忽略）→ 仍按默认并发 1 启动。
	rec := httptest.NewRecorder()
	p.tasksRunQueue(rec, httptest.NewRequest(http.MethodPost, "/panel/api/tasks/run_queue", strings.NewReader("{")))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := caDecode(t, rec)
	if !caBool(t, out, "started") {
		t.Fatalf("应启动队列: %+v", out)
	}
	if got := caNum(t, out, "total"); got != 1 {
		t.Fatalf("total = %v, want 1", got)
	}
	if got := caNum(t, out, "seq"); got != 1 {
		t.Fatalf("seq = %v, want 1", got)
	}
	if got := caQueueConc(p); got != 1 {
		t.Fatalf("默认并发 = %d, want 1", got)
	}
	if !caWaitQueueIdle(t, p) {
		t.Fatal("队列未结束")
	}

	// concurrency=9 → 夹到 4。
	rec2 := httptest.NewRecorder()
	p.tasksRunQueue(rec2, httptest.NewRequest(http.MethodPost, "/panel/api/tasks/run_queue",
		strings.NewReader(`{"concurrency":9,"growth":true}`)))
	if rec2.Code != http.StatusOK {
		t.Fatalf("第二轮的 code=%d; body=%s", rec2.Code, rec2.Body.String())
	}
	if got := caQueueConc(p); got != 4 {
		t.Fatalf("concurrency=9 → %d, want 4", got)
	}
	if !caWaitQueueIdle(t, p) {
		t.Fatal("第二轮队列未结束")
	}

	// 运行中 → 409。
	q := p.queue()
	q.mu.Lock()
	q.running = true
	q.mu.Unlock()
	t.Cleanup(func() {
		q.mu.Lock()
		q.running = false
		q.mu.Unlock()
	})
	rec3 := httptest.NewRecorder()
	p.tasksRunQueue(rec3, httptest.NewRequest(http.MethodPost, "/panel/api/tasks/run_queue", strings.NewReader("{}")))
	if rec3.Code != http.StatusConflict {
		t.Fatalf("运行中 code=%d, want 409", rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), "队列正在执行中") {
		t.Fatalf("冲突体 = %s", rec3.Body.String())
	}
}

// 队列状态轮询端点：空队列与跑完一轮后的字段。
func TestPanelAutoTaskTasksQueueStatusSnapshot(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, ""))
	p := caNewPanel(t, f, caAccount("u1"))

	get := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		p.tasksQueueStatus(rec, httptest.NewRequest(http.MethodGet, "/panel/api/tasks/queue", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d, want 200", rec.Code)
		}
		return caDecode(t, rec)
	}

	empty := get()
	if caBool(t, empty, "running") || caBool(t, empty, "started") {
		t.Fatalf("初始状态不该 running/started: %+v", empty)
	}
	if got := caNum(t, empty, "total"); got != 0 {
		t.Fatalf("初始 total = %v, want 0", got)
	}
	if items, ok := empty["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("初始 items 应为空数组: %+v", empty["items"])
	}

	if started, _, _, _ := p.startGrowthQueue(1, true); !started {
		t.Fatal("应能启动")
	}
	if !caWaitQueueIdle(t, p) {
		t.Fatal("队列未结束")
	}
	after := get()
	if caBool(t, after, "running") {
		t.Fatalf("结束后不该 running: %+v", after)
	}
	if !caBool(t, after, "started") {
		t.Fatalf("跑过一轮后 started 应为 true: %+v", after)
	}
	if got := caNum(t, after, "seq"); got != 1 {
		t.Fatalf("seq = %v, want 1", got)
	}
	items, _ := after["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1 项", after["items"])
	}
	first, _ := items[0].(map[string]any)
	caEqStr(t, caMapStr(first, "code"), "chat_5", "队列项 code")
	caEqStr(t, caMapStr(first, "status"), "done", "队列项 status")
}

// queueMarkAt / queueSet / snapshotAt：索引与 uid 的定位语义。
func TestPanelAutoTaskQueueItemMutations(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	p := caNewPanel(t, f, caAccount("u1"))
	q := p.queue()
	q.mu.Lock()
	q.items = []queueItem{
		{UID: "u1", Kind: "growth", Code: "a", Status: "pending"},
		{UID: "u2", Kind: "growth", Code: "b", Status: "pending"},
		{UID: "u1", Kind: "growth", Code: "c", Status: "pending"},
	}
	q.mu.Unlock()

	p.queueMarkAt(1, "running", "进行中")
	items := caQueueItems(p)
	caEqStr(t, items[1].Status, "running", "queueMarkAt status")
	caEqStr(t, items[1].Message, "进行中", "queueMarkAt message")
	caEqStr(t, items[0].Status, "pending", "未指定索引不应被改")

	p.queueSet(q, "u1", func(it *queueItem) {
		it.Status, it.Message = "skipped", "跳过"
	})
	items = caQueueItems(p)
	caEqStr(t, items[0].Status, "skipped", "queueSet 命中 u1 #1")
	caEqStr(t, items[2].Status, "skipped", "queueSet 命中 u1 #2")
	caEqStr(t, items[1].Status, "running", "queueSet 不该动 u2")

	uid, kind, code := q.snapshotAt(2)
	caEqStr(t, uid, "u1", "snapshotAt uid")
	caEqStr(t, kind, "growth", "snapshotAt kind")
	caEqStr(t, code, "c", "snapshotAt code")
}

// runQueueItems：账号锁被占用 → 该项 skipped；未知 kind → done 但不执行动作。
func TestPanelAutoTaskRunQueueItemsLockAndUnknownKind(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, "accepted"))
	a := caAccount("u1")
	p := caNewPanel(t, f, a)
	q := p.queue()
	q.mu.Lock()
	q.items = []queueItem{
		{UID: "u1", Kind: "growth", Code: "chat_5", Status: "pending"},
		{UID: "u1", Kind: "mystery", Code: "chat_5", Status: "pending"},
	}
	q.running = true
	q.mu.Unlock()

	// 场景 1：账号锁已被其它入口占用 → 整账号跳过。
	if !p.tryLockAccount("u1") {
		t.Fatal("前置：加锁应成功")
	}
	p.runQueueItems([]queueAccount{{a: a}}, nil, 1)
	items := caQueueItems(p)
	for _, it := range items {
		caEqStr(t, it.Status, "skipped", "被锁账号项状态")
		caEqStr(t, it.Message, "该账号有其它任务动作在执行，跳过", "被锁账号项提示")
	}
	if n := f.caCallCount("POST", "/v2/report"); n != 0 {
		t.Fatalf("被锁账号不该跑动作，上报次数 %d", n)
	}
	if q := p.queue(); func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.running
	}() {
		t.Fatal("runQueueItems 结束必须把 running 置回 false")
	}
	p.unlockAccount("u1")

	// 场景 2：锁可用 → growth 项真跑；未知 kind 记 done 但不执行（不 panic）。
	p.queueMarkAt(0, "pending", "")
	p.queueMarkAt(1, "pending", "")
	q.mu.Lock()
	q.running = true
	q.mu.Unlock()
	p.runQueueItems([]queueAccount{{a: a}}, nil, 1)
	items = caQueueItems(p)
	caEqStr(t, items[0].Status, "done", "growth 项状态")
	if !strings.Contains(items[0].Message, "进度") {
		t.Fatalf("growth 项 message = %q, want 含进度", items[0].Message)
	}
	caEqStr(t, items[1].Status, "done", "未知 kind 项状态")
	caEqStr(t, items[1].Message, "", "未知 kind 不产生消息")
	if n := f.caCallCount("POST", "/v2/report"); n == 0 {
		t.Fatal("scene2 应至少跑一次动作上报")
	}
}

// acceptPendingTasks：只接受「未领、未锁、未接受/完成」的，返回接受个数。
func TestPanelAutoTaskAcceptPendingTasks(t *testing.T) {
	caFastGaps(t)

	t.Run("混合状态只接受待接受项", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 0, 5, ""))
		f.caSetTask(caTask("first_buddy", 0, 1, "accepted"))
		f.caSetTask(caTask("RichMeow_Chat", 1, 1, "claimed"))
		f.caSetTask(caLockedTask("black_cat", 0, 3, ""))
		p := caNewPanel(t, f, caAccount("u1"))

		if got := p.acceptPendingTasks(caAccount("u1")); got != 1 {
			t.Fatalf("接受个数 = %d, want 1", got)
		}
		accs := f.caCalls("POST", "/v2/activity/growth/tasks/accept")
		if len(accs) != 1 {
			t.Fatalf("accept 次数 = %d, want 1", len(accs))
		}
		var body struct {
			TaskCodes []string `json:"task_codes"`
		}
		_ = json.Unmarshal([]byte(accs[0].Body), &body)
		caEqStr(t, strings.Join(body.TaskCodes, ","), "chat_5", "只提交待接受项（不碰锁定/已领）")
	})

	t.Run("全部已接受则不发请求", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 0, 5, "accepted"))
		p := caNewPanel(t, f, caAccount("u1"))
		if got := p.acceptPendingTasks(caAccount("u1")); got != 0 {
			t.Fatalf("接受个数 = %d, want 0", got)
		}
		if n := f.caCallCount("POST", "/v2/activity/growth/tasks/accept"); n != 0 {
			t.Fatalf("不该发 accept 请求，次数 %d", n)
		}
	})

	t.Run("列表失败返回 0 不阻塞", func(t *testing.T) {
		f := caNewFake()
		f.raw["GET /v2/activity/growth/tasks"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if got := p.acceptPendingTasks(caAccount("u1")); got != 0 {
			t.Fatalf("接受个数 = %d, want 0", got)
		}
	})

	t.Run("accept 失败返回 0 不阻塞", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 0, 5, ""))
		f.raw["POST /v2/activity/growth/tasks/accept"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if got := p.acceptPendingTasks(caAccount("u1")); got != 0 {
			t.Fatalf("accept 失败应返回 0，得到 %d", got)
		}
	})
}

// runGrowthQueued 的五个分支：无动作 / 无任务 / 已领 / 正常领奖 / 无新增奖励。
func TestPanelAutoTaskRunGrowthQueuedBranches(t *testing.T) {
	caFastGaps(t)

	t.Run("无自动动作", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := p.runGrowthQueued(caAccount("u1"), "not_automatable")
		if err == nil || err.Error() != "任务 not_automatable 无自动动作" {
			t.Fatalf("错误 = %v", err)
		}
	})

	t.Run("该账号无此任务", func(t *testing.T) {
		f := caNewFake()
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := p.runGrowthQueued(caAccount("u1"), "chat_5")
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "该账号无此任务", "无任务提示")
	})

	t.Run("已领取", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 5, 5, "claimed"))
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := p.runGrowthQueued(caAccount("u1"), "chat_5")
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已完成（已领取）", "已领提示")
	})

	t.Run("正常领奖含进度后缀", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
		f.claimCredit, f.claimEnergy = 100, 5
		f.onReport = func(call caCall) {
			if call.Host == "billing.test" {
				f.caAdvance("chat_5", false, 5, "completed")
			}
		}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := p.runGrowthQueued(caAccount("u1"), "chat_5")
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已补报 1 条对话事件；自动领奖 +100 分 +5 能（进度 5/5）", "结果文本")
		claims := f.caCalls("POST", "/activity/growth/tasks/chat_5/claim")
		if len(claims) != 1 || claims[0].Host != "web.test" {
			t.Fatalf("非 mp 领奖应走 Web 域: %+v", claims)
		}
	})

	t.Run("领奖无新增不追加奖励文案", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
		f.alreadyClaimed = true // 上游回 0 分 0 能
		f.onReport = func(call caCall) {
			if call.Host == "billing.test" {
				f.caAdvance("chat_5", false, 5, "completed")
			}
		}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := p.runGrowthQueued(caAccount("u1"), "chat_5")
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已补报 1 条对话事件（进度 5/5）", "结果文本（不得出现 +0 分）")
	})

	t.Run("mp 任务领奖走 chat 域", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_1", 1, 1, "accepted")) // 达标未领
		f.claimCredit, f.claimEnergy = 100, 5
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := p.runGrowthQueued(caAccount("u1"), "Sequential_Tasks_1")
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已领取奖励（+100c +5e）（进度 1/1）", "结果文本")
		claims := f.caCalls("POST", "/activity/growth/tasks/Sequential_Tasks_1/claim")
		if len(claims) != 1 {
			t.Fatalf("领奖次数 = %d, want 1", len(claims))
		}
		caEqStr(t, claims[0].Host, "chat.test", "mp 领奖域")
		caEqStr(t, claims[0].Header.Get("X-Client-Platform"), "miniprogram", "mp 领奖头")
	})

	t.Run("动作失败冒泡 error", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("Model_chat_GLM5.2", 0, 1, "accepted"))
		f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"chat down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := p.runGrowthQueued(caAccount("u1"), "Model_chat_GLM5.2"); err == nil {
			t.Fatal("动作失败应返回 error（队列项标 error）")
		}
	})
}

// 我的券码：逐 CN 账号查，global 直接标注不查（零上游调用），禁用账号不出行。
func TestPanelAutoTaskSchoolVouchers(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.vouchers = []upstream.SchoolVoucher{{GrantID: 7, SKUCode: "kfc_ice_cream", Code: "KFC-ABC-123", PrizeName: "肯德基冰淇淋"}}
	f.voucherErrUID = "u3"
	cn := caAccount("u1")
	global := &auth.Auth{UID: "u2", Nickname: "全球号", AccessToken: "t2", Domain: "www.workbuddy.ai"}
	broken := caAccount("u3")
	disabled := caAccount("u4")
	p := caNewPanel(t, f, cn, global, broken, disabled)
	p.cfg.Pool.Disable("u4", "test")

	rec := httptest.NewRecorder()
	p.schoolVouchers(rec, httptest.NewRequest(http.MethodGet, "/panel/api/school/vouchers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	out := caDecode(t, rec)
	rows, _ := out["accounts"].([]any)
	if len(rows) != 3 {
		t.Fatalf("行数 = %d, want 3（禁用账号被压掉）: %+v", len(rows), rows)
	}
	byUID := map[string]map[string]any{}
	for _, r := range rows {
		m, _ := r.(map[string]any)
		byUID[caMapStr(m, "uid")] = m
	}

	u1 := byUID["u1"]
	if u1 == nil {
		t.Fatalf("缺 u1 行: %+v", byUID)
	}
	vs, _ := u1["vouchers"].([]any)
	if len(vs) != 1 {
		t.Fatalf("u1 vouchers = %+v, want 1 条", u1["vouchers"])
	}
	v0, _ := vs[0].(map[string]any)
	caEqStr(t, caMapStr(v0, "code"), "KFC-ABC-123", "券码本体")
	caEqStr(t, caMapStr(v0, "sku_code"), "kfc_ice_cream", "券类型")

	if got := caMapStr(byUID["u2"], "error"); !strings.Contains(got, "global realm") {
		t.Fatalf("global 行 error = %q, want 含 global realm 说明", got)
	}
	if n := f.caCallsByUID("u2"); n != 0 {
		t.Fatalf("global 账号不该发上游请求，次数 %d", n)
	}
	if got := caMapStr(byUID["u3"], "error"); !strings.Contains(got, "http 500") {
		t.Fatalf("失败账号 error = %q, want 含上游状态码", got)
	}
	if _, ok := byUID["u4"]; ok {
		t.Fatal("禁用账号不应出现在结果里")
	}
}

// ---------------------------------------------------------------------------
// 错误传播：上报失败必须冒泡（不得"上报失败也报成功"）
// ---------------------------------------------------------------------------

// 各「纯上报型」动作在上游 5xx 时必须把错误交回调用方（handler 才能转 502 / 队列才能标 error）。
func TestPanelAutoTaskReportOnlyActionsPropagateErrors(t *testing.T) {
	caFastGaps(t)
	cases := []struct {
		name     string
		run      func(p *Panel, a *auth.Auth) (string, error)
		wantPre  string
		wantSoft bool // true = 以文本结果报错（err == nil）
	}{
		{"rich_meow", runRichMeow, "", false},
		{"buddy_app", runBuddyApp, "", false},
		{"automation_1", runAutomationCreate, "", false},
		{"library_read", runLibraryRead, "", false},
		{"playbook_prompt", runPlaybookPrompt, "", false},
		{"create_canvas", runCreateCanvas, "", false},
		{"appearance", runAppearance, "", false},
		{"skill_1", runSkillFresh, "skill_info 事件:", false},
		{"template_5", runTemplateUse, "第 1 组模板事件上报失败:", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := caNewFake()
			f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
			p := caNewPanel(t, f, caAccount("u1"))

			msg, err := c.run(p, caAccount("u1"))
			if c.wantSoft {
				if err != nil {
					t.Fatalf("应为软失败（文本结果），得到 err=%v", err)
				}
				if !strings.HasPrefix(msg, c.wantPre) {
					t.Fatalf("msg = %q, want 前缀 %q", msg, c.wantPre)
				}
				return
			}
			if err == nil {
				t.Fatalf("上报 500 必须返回 error，得到 msg=%q", msg)
			}
			if !strings.Contains(err.Error(), "http 500") {
				t.Fatalf("err = %q, want 含上游状态码", err.Error())
			}
			if c.wantPre != "" && !strings.HasPrefix(err.Error(), c.wantPre) {
				t.Fatalf("err = %q, want 前缀 %q", err.Error(), c.wantPre)
			}
		})
	}
}

// 主题 set 失败与「召唤链/使用链」分段失败的错误前缀各不相同（ops 据此定位阶段）。
func TestPanelAutoTaskMultiStageActionErrorPrefixes(t *testing.T) {
	caFastGaps(t)

	t.Run("主题 set 失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["POST /v2/user-asset/appearance/set"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runAppearance(p, caAccount("u1"))
		if err == nil || !strings.HasPrefix(err.Error(), "设置主题:") {
			t.Fatalf("err = %v, want 前缀 设置主题:", err)
		}
		if n := f.caCallCount("POST", "/v2/report"); n != 0 {
			t.Fatalf("set 失败不该继续上报事件，次数 %d", n)
		}
	})

	t.Run("轻量云召唤链失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runExpertLighthouse(p, caAccount("u1"))
		if err == nil || !strings.HasPrefix(err.Error(), "召唤链:") {
			t.Fatalf("err = %v, want 前缀 召唤链:", err)
		}
	})

	t.Run("轻量云使用链失败", func(t *testing.T) {
		f := caNewFake()
		n := 0
		f.onReport = func(caCall) { // 召唤链成功后注入故障
			n++
			if n == 1 {
				f.mu.Lock()
				f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
				f.mu.Unlock()
			}
		}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runExpertLighthouse(p, caAccount("u1"))
		if err == nil || !strings.HasPrefix(err.Error(), "使用事件:") {
			t.Fatalf("err = %v, want 前缀 使用事件:", err)
		}
	})

	t.Run("轻量云真实对话失败", func(t *testing.T) {
		f := caNewFake()
		f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"chat down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		_, err := runExpertLighthouse(p, caAccount("u1"))
		if err == nil || !strings.HasPrefix(err.Error(), "真实对话:") {
			t.Fatalf("err = %v, want 前缀 真实对话:", err)
		}
	})
}

// ---------------------------------------------------------------------------
// 补齐：错误分支与门控（扫描/队列的第二套口径）
// ---------------------------------------------------------------------------

// taskByCodeWaiting 的首次回读失败直接冒泡（不轮询/不返回假结果）。
func TestPanelAutoTaskTaskByCodeWaitingFirstReadError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.defaultListRaw = &caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	if _, err := p.taskByCodeWaiting(caAccount("u1"), "chat_5"); err == nil {
		t.Fatal("首次回读失败应返回 error")
	}
}

// mp 闭环：mp 列表失败 / target 兜底 / 最终领奖失败三条错误路径。
func TestPanelAutoTaskRunMPMiniChatTaskErrorPaths(t *testing.T) {
	caFastGaps(t)

	t.Run("mp 列表失败", func(t *testing.T) {
		f := caNewFake()
		f.mpListRaw = &caRaw{Status: 403, Body: `{"code":10001,"msg":"growth system is only available for personal users"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runSchoolSeason(p, caAccount("u1")); err == nil {
			t.Fatal("mp 列表失败应返回 error")
		}
	})

	t.Run("target 兜底 1 且最终领奖失败", func(t *testing.T) {
		f := caNewFake()
		// 未 accept 的 mp 任务 progress 为 null（target=0）→ 兜底 target=1。
		f.caSetMPTask(caTask("school_season", 0, 0, ""))
		f.onReport = func(caCall) { f.caAdvance("school_season", true, 1, "completed") }
		f.raw["POST /activity/growth/tasks/school_season/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
		p := caNewPanel(t, f, caAccount("u1"))

		msg, err := runSchoolSeason(p, caAccount("u1"))
		if err == nil {
			t.Fatalf("最终领奖失败应返回 error，msg=%q", msg)
		}
		if n := f.caCallCount("POST", "/v2/report"); n != 1 {
			t.Fatalf("上报次数 = %d, want 1（target 兜底为 1，只补一条）", n)
		}
	})
}

// Sequential 链：mp 列表失败 / target 兜底 / 回读期间 claimed / 回读出错 / 最终领奖失败。
func TestPanelAutoTaskSequentialEventTaskErrorPaths(t *testing.T) {
	caFastGaps(t)

	t.Run("mp 列表失败", func(t *testing.T) {
		f := caNewFake()
		f.mpListRaw = &caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runSequentialAutomation(p, caAccount("u1")); err == nil {
			t.Fatal("mp 列表失败应返回 error")
		}
	})

	t.Run("target 兜底并直接领奖", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 0, "completed")) // target 未下发 + completed
		f.claimCredit, f.claimEnergy = 200, 5
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runSequentialAutomation(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已领取奖励（+200c +5e）", "completed 直接领奖")
	})

	t.Run("回读期间 claimed", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, "accepted"))
		f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_4", true, 1, "claimed") }
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runSequentialAutomation(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "本轮已入账（claimed）", "claimed 提示")
	})

	t.Run("回读出错继续下一轮", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, "accepted"))
		f.onReport = func(caCall) {
			f.mu.Lock()
			f.mpListRaw = &caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
			f.mu.Unlock()
		}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runSequentialAutomation(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("回读失败不该冒泡 error: %v", err)
		}
		caEqStr(t, msg, "已上报但进度未点亮（判据形态待解锁后校正，下次重试）", "回读失败后的提示")
	})

	t.Run("最终领奖失败", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_4", 1, 1, "accepted"))
		f.raw["POST /activity/growth/tasks/Sequential_Tasks_4/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runSequentialAutomation(p, caAccount("u1")); err == nil {
			t.Fatal("领奖失败应返回 error")
		}
	})

	t.Run("回读达标后领奖失败", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_4", 0, 1, "accepted"))
		f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_4", true, 1, "completed") }
		f.raw["POST /activity/growth/tasks/Sequential_Tasks_4/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runSequentialAutomation(p, caAccount("u1")); err == nil {
			t.Fatal("回读达标后领奖失败应返回 error")
		}
	})
}

// runMiniExpert：mp 列表失败 / target 兜底 / 回读期间 claimed / 回读出错 / 最终领奖失败。
func TestPanelAutoTaskRunMiniExpertErrorPaths(t *testing.T) {
	caFastGaps(t)
	experts := []upstream.MarketExpert{{ExpertID: "ex_abc123", ExpertType: "agent", DisplayNameZH: "运营专家"}}

	t.Run("mp 列表失败", func(t *testing.T) {
		f := caNewFake()
		f.mpListRaw = &caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runMiniExpert(p, caAccount("u1")); err == nil {
			t.Fatal("mp 列表失败应返回 error")
		}
	})

	t.Run("target 兜底并直接领奖", func(t *testing.T) {
		f := caNewFake()
		f.experts = experts
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 0, "completed"))
		f.claimCredit, f.claimEnergy = 200, 5
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已领取奖励（+200c +5e）", "completed 直接领奖")
		if n := f.caCallCount("POST", "/portal/operation-platform/market/expert/list"); n != 0 {
			t.Fatalf("completed 时不该拉市场，次数 %d", n)
		}
	})

	t.Run("回读期间 claimed", func(t *testing.T) {
		f := caNewFake()
		f.experts = experts
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "accepted"))
		f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_2", true, 1, "claimed") }
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "本轮已入账（claimed）", "claimed 提示")
	})

	t.Run("回读出错继续下一轮", func(t *testing.T) {
		f := caNewFake()
		f.experts = experts
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "accepted"))
		f.onReport = func(caCall) {
			f.mu.Lock()
			f.mpListRaw = &caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
			f.mu.Unlock()
		}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("回读失败不该冒泡 error: %v", err)
		}
		caEqStr(t, msg, "已上报但进度未归账（异步计分，下次重试）", "回读失败后的提示")
	})

	t.Run("最终领奖失败", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_2", 1, 1, "accepted"))
		f.raw["POST /activity/growth/tasks/Sequential_Tasks_2/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runMiniExpert(p, caAccount("u1")); err == nil {
			t.Fatal("领奖失败应返回 error")
		}
	})

	t.Run("accept 未登记生效", func(t *testing.T) {
		f := caNewFake()
		f.experts = experts
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "not_accepted"))
		f.acceptNoOp = true
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runMiniExpert(p, caAccount("u1"))
		if err != nil {
			t.Fatalf("不该报 error: %v", err)
		}
		caEqStr(t, msg, "accept 未登记生效（上游 200+OK 但未落账形态），待下次重试", "accept 未生效提示")
		if n := f.caCallCount("POST", "/v2/report"); n != 0 {
			t.Fatalf("accept 未生效时不该上报判据，次数 %d", n)
		}
	})

	t.Run("回读达标后领奖失败", func(t *testing.T) {
		f := caNewFake()
		f.experts = experts
		f.caSetMPTask(caTask("Sequential_Tasks_2", 0, 1, "accepted"))
		f.onReport = func(caCall) { f.caAdvance("Sequential_Tasks_2", true, 1, "completed") }
		f.raw["POST /activity/growth/tasks/Sequential_Tasks_2/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := runMiniExpert(p, caAccount("u1")); err == nil {
			t.Fatal("回读达标后领奖失败应返回 error")
		}
	})
}

// 专家批处理：真实对话失败 / 使用事件上报失败都只记失败数并继续，不中断整批。
func TestPanelAutoTaskRunExpertBatchStageFailures(t *testing.T) {
	caFastGaps(t)
	experts := []upstream.MarketExpert{{ExpertID: "ex_1", ExpertType: "agent", DisplayNameZH: "A"}}

	t.Run("真实对话失败", func(t *testing.T) {
		f := caNewFake()
		f.experts = experts
		f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"chat down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runExpertBatch(p, caAccount("u1"), "agent", 5)
		if err != nil {
			t.Fatalf("单项失败不该中断整批: %v", err)
		}
		caEqStr(t, msg, "已对 0 位真实专家完成召唤+使用链（类型 agent）", "失败计数")
		if n := f.caCallCount("POST", "/v2/report"); n != 1 {
			t.Fatalf("召唤链应已上报（1 批），得到 %d", n)
		}
	})

	t.Run("使用事件上报失败", func(t *testing.T) {
		f := caNewFake()
		f.experts = experts
		n := 0
		f.onReport = func(caCall) { // 召唤链成功后注入故障
			n++
			if n == 1 {
				f.mu.Lock()
				f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
				f.mu.Unlock()
			}
		}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := runExpertBatch(p, caAccount("u1"), "agent", 5)
		if err != nil {
			t.Fatalf("单项失败不该中断整批: %v", err)
		}
		caEqStr(t, msg, "已对 0 位真实专家完成召唤+使用链（类型 agent）", "失败计数")
		if n := f.caCallCount("POST", "/v2/chat/completions"); n != 1 {
			t.Fatalf("真实对话已发生（1 次），得到 %d", n)
		}
	})
}

// runAutoAll 的默认口径批量接受失败 → 该项目 status=error（且不阻塞后续逐项）。
func TestPanelAutoTaskRunAutoAllDefaultBatchAcceptError(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "claimed")) // 终态，不进批量；下面单独造一个待接受项
	f.caSetTask(caTask("Model_chat_GLM5.2", 0, 1, ""))
	f.raw["POST /v2/activity/growth/tasks/accept"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"acc down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	item := caFindItem(t, p.runAutoAll(caAccount("u1")), "(批量接受)")
	caEqStr(t, caStr(t, item, "status"), "error", "(批量接受).status")
	if !strings.Contains(caStr(t, item, "message"), "接受任务失败（不阻塞后续）") {
		t.Fatalf("message = %q", caStr(t, item, "message"))
	}
}

// runGrowthQueued：查询失败冒泡 error；领奖失败不追加奖励文案。
func TestPanelAutoTaskRunGrowthQueuedClaimAndQueryErrors(t *testing.T) {
	caFastGaps(t)

	t.Run("查询失败", func(t *testing.T) {
		f := caNewFake()
		f.defaultListRaw = &caRaw{Status: 500, Body: `{"code":10000,"msg":"boom"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		if _, err := p.runGrowthQueued(caAccount("u1"), "chat_5"); err == nil {
			t.Fatal("查询失败应返回 error")
		}
	})

	t.Run("mp 领奖成功追加奖励文案", func(t *testing.T) {
		f := caNewFake()
		f.caSetMPTask(caTask("Sequential_Tasks_1", 1, 1, "accepted"))
		f.claimCredit, f.claimEnergy = 100, 5
		f.claimKeepsState = true // 领奖后回读仍 claimable → 走 runGrowthQueued 的 mp 领奖分支
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := p.runGrowthQueued(caAccount("u1"), "Sequential_Tasks_1")
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		caEqStr(t, msg, "已领取奖励（+100c +5e）；自动领奖 +100 分 +5 能（进度 1/1）", "结果文本")
	})

	t.Run("领奖失败不追加奖励文案", func(t *testing.T) {
		f := caNewFake()
		f.caSetTask(caTask("chat_5", 4, 5, "accepted"))
		f.onReport = func(call caCall) {
			if call.Host == "billing.test" {
				f.caAdvance("chat_5", false, 5, "completed")
			}
		}
		f.raw["POST /activity/growth/tasks/chat_5/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
		p := caNewPanel(t, f, caAccount("u1"))
		msg, err := p.runGrowthQueued(caAccount("u1"), "chat_5")
		if err != nil {
			t.Fatalf("领奖失败不该中断队列项: %v", err)
		}
		caEqStr(t, msg, "已补报 1 条对话事件（进度 5/5）", "结果文本（领奖失败时不提奖励）")
	})
}

// 队列扫描的门控与去重：global / 企业版 / 禁用账号不发上游调用，mp 与默认口径按码去重。
func TestPanelAutoTaskStartGrowthQueueGatesAndDedupes(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 0, 5, ""))
	f.caSetMPTask(caTask("chat_5", 0, 5, ""))             // 与默认口径重复 → 去重
	f.caSetMPTask(caTask("Sequential_Tasks_1", 1, 1, "")) // mp 专属未领 → 入队
	f.claimCredit, f.claimEnergy = 100, 5

	cn := caAccount("u1")
	global := &auth.Auth{UID: "u2", Nickname: "全球", AccessToken: "t2", Domain: "www.workbuddy.ai"}
	ent := &auth.Auth{UID: "u3", Nickname: "企业", AccessToken: "t3", EnterpriseID: "ent-1"}
	dis := caAccount("u4")
	p := caNewPanel(t, f, cn, global, ent, dis)
	p.cfg.Pool.Disable("u4", "test")

	started, total, seq, msg := p.startGrowthQueue(2, true)
	if !started || total != 2 || seq != 1 || msg != "" {
		t.Fatalf("startGrowthQueue = (%v,%d,%d,%q), want (true,2,1,\"\")", started, total, seq, msg)
	}
	if !caWaitQueueIdle(t, p) {
		t.Fatal("队列未结束")
	}
	items := caQueueItems(p)
	codes := make([]string, 0, len(items))
	for _, it := range items {
		codes = append(codes, it.Code)
	}
	caEqStr(t, strings.Join(codes, ","), "chat_5,Sequential_Tasks_1", "去重后的待办码")
	for _, uid := range []string{"u2", "u3", "u4"} {
		if n := f.caCallsByUID(uid); n != 0 {
			t.Fatalf("%s 被门控，不该发上游请求，次数 %d", uid, n)
		}
	}
}

// 队列执行：多账号并发时各账号只动自己的条目，失败项标 error。
func TestPanelAutoTaskRunQueueItemsTwoAccounts(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "accepted")) // 达标 → 动作阶段 skipped（不影响计数）
	f.caSetTask(caTask("Model_chat_GLM5.2", 0, 1, "accepted"))
	f.raw["POST /v2/chat/completions"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"chat down"}`}
	a1, a2 := caAccount("u1"), caAccount("u2")
	p := caNewPanel(t, f, a1, a2)
	q := p.queue()
	q.mu.Lock()
	q.items = []queueItem{
		{UID: "u1", Kind: "growth", Code: "chat_5", Status: "pending"},
		{UID: "u2", Kind: "growth", Code: "Model_chat_GLM5.2", Status: "pending"},
	}
	q.running = true
	q.mu.Unlock()

	p.runQueueItems([]queueAccount{{a: a1}, {a: a2}}, nil, 2)

	items := caQueueItems(p)
	caEqStr(t, items[0].Status, "done", "u1 项状态")
	// 达标未领的项仍会跑动作，动作自身报「无需上报」；奖励为 0 时不追加奖励文案。
	caEqStr(t, items[0].Message, "进度已达标，无需上报（进度 5/5）", "u1 项消息")
	caEqStr(t, items[1].Status, "error", "u2 项状态")
	if !strings.Contains(items[1].Message, "http 500") {
		t.Fatalf("u2 项消息 = %q, want 含上游状态码", items[1].Message)
	}
	if !caWaitQueueIdle(t, p) {
		t.Fatal("队列应已结束")
	}
}

// HTTP 入口在无待办时回 started:false（不是错误码），且不占位 running。
func TestPanelAutoTaskTasksRunQueueNoPending(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("not_automatable", 0, 1, "")) // 无动作 → 不入待办
	p := caNewPanel(t, f, caAccount("u1"))

	rec := httptest.NewRecorder()
	p.tasksRunQueue(rec, httptest.NewRequest(http.MethodPost, "/panel/api/tasks/run_queue", strings.NewReader(`{"growth":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := caDecode(t, rec)
	if caBool(t, out, "started") {
		t.Fatalf("无待办不该 started: %+v", out)
	}
	caEqStr(t, caStr(t, out, "message"), "全部账号没有待办任务", "提示")
	if !caBool(t, out, "ok") {
		t.Fatalf("仍应 ok:true: %+v", out)
	}
	if q := p.queue(); func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.running
	}() {
		t.Fatal("无待办必须回滚 running")
	}
}
