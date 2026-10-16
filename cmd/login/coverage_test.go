package main

// coverage_test.go — Task 5 覆盖率补强（纯增量，不改生产代码）。
//
// 覆盖重点（关键路径，非边角分支）：
//   - realmConfig / commonHeaders：realm → 上游 base 与 Origin/Referer 的映射（出站头构造）
//   - doJSON：{code,msg,data} 信封解析 + 错误分类（业务 code / 4xx / 3xx / 非法 JSON / 传输失败）
//   - runURL：POST state 端点 → 落盘 state（带 realm）→ stdout 打 authUrl；各 fatal 分支
//   - runPoll：读 state + 防混域校验 → GET token（Bearer 转 account）→ 组装输出 → 删 state
//   - validateRealmMatch / fatal / main 的参数校验分支
//
// 变异补牙（漏网行为逐条钉住，全部为负断言或此前未插桩的观察点）：
//   - runPoll token 端点 4xx 判档：401/403 归「未完成」档（现有产品意图，待确认），不得混入上游错误档
//   - account 请求的 method/body（此前只断言了 token 的 method）
//   - parseRealmArgs 分离式 --realm <v> 的大小写/空白归一（此前只走等号分支）
//   - main 的 case "realm" 接线（promptRealm 被覆盖了，接线没有）
//
// 全部使用 httptest（本机回环，无真实外网）+ 注入 exitFunc 捕获 fatal，确定性、可 -count=3 重复。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// covLoginExitPanic 由替换后的 exitFunc 抛出，让测试在进程内捕获 fatal（默认 os.Exit 会杀进程）。
type covLoginExitPanic struct{ code int }

// covLoginErrTransport 恒定失败的 RoundTripper：确定性触发 client.Do 失败分支，不碰网络。
type covLoginErrTransport struct{ err error }

func (rt covLoginErrTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, rt.err }

// covLoginFatalCapture 在替换 exitFunc/os.Stderr 的前提下执行 fn，返回捕获到的 stderr 文本与退出码。
// 若 fn 正常返回（未 fatal），标记测试失败。调用方必须保证串行（本包测试未用 t.Parallel）。
func covLoginFatalCapture(t *testing.T, fn func()) (string, int) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "covlogin-stderr-*.log")
	if err != nil {
		t.Fatalf("create stderr capture file: %v", err)
	}
	name := f.Name()
	oldExit, oldStderr := exitFunc, os.Stderr
	exitFunc = func(c int) { panic(covLoginExitPanic{code: c}) }
	os.Stderr = f

	code := -1
	func() {
		defer func() { os.Stderr, exitFunc = oldStderr, oldExit }()
		defer func() {
			r := recover()
			if r == nil {
				t.Errorf("fn() returned normally; want fatal exit (exitFunc)")
				return
			}
			p, ok := r.(covLoginExitPanic)
			if !ok {
				panic(r) // 非 fatal 的 panic 原样抛出
			}
			code = p.code
		}()
		fn()
	}()

	if err := f.Close(); err != nil {
		t.Fatalf("close stderr capture: %v", err)
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read stderr capture: %v", err)
	}
	return string(raw), code
}

// covLoginReq 是请求快照（handler 在别的 goroutine 执行，必须在 handler 内拷成字符串）。
type covLoginReq struct {
	method, path, query    string
	origin, referer        string
	authorization          string
	userAgent, contentType string
	body                   string
}

// covLoginReqLog 并发安全的请求记录器。
type covLoginReqLog struct {
	mu   sync.Mutex
	reqs []covLoginReq
}

func (l *covLoginReqLog) add(r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	rec := covLoginReq{
		method:        r.Method,
		path:          r.URL.Path,
		query:         r.URL.RawQuery,
		origin:        r.Header.Get("Origin"),
		referer:       r.Header.Get("Referer"),
		authorization: r.Header.Get("Authorization"),
		userAgent:     r.Header.Get("User-Agent"),
		contentType:   r.Header.Get("Content-Type"),
		body:          string(body),
	}
	l.mu.Lock()
	l.reqs = append(l.reqs, rec)
	l.mu.Unlock()
}

func (l *covLoginReqLog) find(t *testing.T, path string) covLoginReq {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, rec := range l.reqs {
		if rec.path == path {
			return rec
		}
	}
	t.Fatalf("no request recorded for path %q (recorded=%d)", path, len(l.reqs))
	return covLoginReq{}
}

func covLoginWriteFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// TestCovLoginRealmConfig 钉住 realm → (上游 base, Origin origin) 映射：
// global 走 workbuddy 同域；cn/非法/缺省回落 copilot.tencent.com + codebuddy.cn。
// 还原（例如把 global 判断去掉或把 base/origin 写混）会让本测试 FAIL。
func TestCovLoginRealmConfig(t *testing.T) {
	cases := []struct {
		name       string
		realm      string
		wantBase   string
		wantOrigin string
	}{
		{"cn", "cn", "https://copilot.tencent.com", "https://www.codebuddy.cn"},
		{"global", "global", "https://www.workbuddy.ai", "https://www.workbuddy.ai"},
		{"空串回落 cn", "", "https://copilot.tencent.com", "https://www.codebuddy.cn"},
		{"非法值回落 cn", "eu", "https://copilot.tencent.com", "https://www.codebuddy.cn"},
		{"未归一大写回落 cn（归一化由调用方 parseRealmArgs 负责）", "GLOBAL", "https://copilot.tencent.com", "https://www.codebuddy.cn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, origin := realmConfig(c.realm)
			if base != c.wantBase {
				t.Errorf("realmConfig(%q) base=%q want %q", c.realm, base, c.wantBase)
			}
			if origin != c.wantOrigin {
				t.Errorf("realmConfig(%q) origin=%q want %q", c.realm, origin, c.wantOrigin)
			}
		})
	}
}

// TestCovLoginCommonHeaders 钉住出站头集合与 Referer=origin+"/" 的拼接：
// 任何一项被删/改值（尤其 Referer 少尾斜杠、Origin 不随 realm 变）都会 FAIL。
func TestCovLoginCommonHeaders(t *testing.T) {
	for _, c := range []struct{ name, origin string }{
		{"cn", "https://www.codebuddy.cn"},
		{"global", "https://www.workbuddy.ai"},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "https://copilot.tencent.com/v2/plugin/auth/state", nil)
			commonHeaders(c.origin)(req)
			want := map[string]string{
				"Content-Type":     "application/json",
				"Accept":           "application/json, text/plain, */*",
				"X-Requested-With": "XMLHttpRequest",
				"Origin":           c.origin,
				"Referer":          c.origin + "/",
				"User-Agent":       "CLI/2.63.2 CodeBuddy/2.63.2",
			}
			for k, v := range want {
				if got := req.Header.Get(k); got != v {
					t.Errorf("header %s=%q want %q", k, got, v)
				}
			}
		})
	}
}

// TestCovLoginDoJSON 钉住 {code,msg,data} 信封的成功路径与错误分类：
// 业务 code!=0 → "code=%d msg=%s"；>=400 → "http_error: upstream %d"；
// >=300 → "http_error: upstream redirect %d"；非法 JSON → "parse failed: ..."；
// headers 为 nil 时回落 CN 默认头；method/body 原样透传；传输失败原样返回。
func TestCovLoginDoJSON(t *testing.T) {
	const (
		okData   = `{"state":"s1","authUrl":"https://copilot.tencent.com/authorize?state=s1"}`
		cnOrigin = "https://www.codebuddy.cn"
		glOrigin = "https://www.workbuddy.ai"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			io.WriteString(w, `{"code":0,"msg":"","data":`+okData+`}`)
		case "/echo":
			body, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, `{"code":0,"msg":"","data":%q}`,
				r.Method+"|"+string(body)+"|"+r.Header.Get("Origin")+"|"+r.Header.Get("Referer"))
		case "/bizerr":
			io.WriteString(w, `{"code":40201,"msg":"login ing","data":null}`)
		case "/empty-data":
			io.WriteString(w, `{"code":0,"msg":""}`)
		case "/badjson":
			io.WriteString(w, `{"code":0,"msg":"`)
		case "/http400":
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"code":0,"msg":"","data":null}`)
		case "/redirect":
			w.WriteHeader(http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	failing := &http.Client{Transport: covLoginErrTransport{err: errors.New("covlogin dial boom")}}

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		headers    func(*http.Request) // nil → 触发缺省 CN 头分支
		client     *http.Client
		wantData   string
		wantStatus int
		wantErr    string // 精确匹配
		wantErrHas string // 子串匹配（wantErr 为空时使用）
	}{
		{name: "成功返回 data", method: http.MethodPost, path: "/ok", wantData: okData, wantStatus: 200},
		// /echo 用 %q 把结果输出成 JSON 字符串，故期望值自带一对引号
		{name: "缺省头回落 CN origin 且 method/body 透传", method: http.MethodPost, path: "/echo", body: "{}",
			wantData: `"POST|{}|` + cnOrigin + `|` + cnOrigin + `/"`, wantStatus: 200},
		{name: "自定义头生效", method: http.MethodPost, path: "/echo", body: "{}", headers: commonHeaders(glOrigin),
			wantData: `"POST|{}|` + glOrigin + `|` + glOrigin + `/"`, wantStatus: 200},
		{name: "业务 code 非 0", method: http.MethodGet, path: "/bizerr",
			wantStatus: 200, wantErr: "code=40201 msg=login ing"},
		{name: "data 缺失返回空", method: http.MethodGet, path: "/empty-data", wantData: "", wantStatus: 200},
		{name: "非法 JSON", method: http.MethodGet, path: "/badjson", wantStatus: 200, wantErrHas: "parse failed:"},
		{name: "上游 400", method: http.MethodGet, path: "/http400", wantStatus: 400, wantErr: "http_error: upstream 400"},
		{name: "上游 302 视为 redirect 错误", method: http.MethodGet, path: "/redirect", wantStatus: 302,
			wantErr: "http_error: upstream redirect 302"},
		{name: "上游 404", method: http.MethodGet, path: "/nope", wantStatus: 404, wantErr: "http_error: upstream 404"},
		{name: "传输失败 status 归零", method: http.MethodGet, path: "/ok", client: failing,
			wantStatus: 0, wantErrHas: "covlogin dial boom"},
		{name: "非法 method 在发请求前报错", method: "BAD METHOD", path: "/ok", client: failing,
			wantStatus: 0, wantErrHas: "invalid method"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := c.client
			if client == nil {
				client = srv.Client()
			}
			var body io.Reader
			if c.body != "" {
				body = strings.NewReader(c.body)
			}
			data, status, err := doJSON(client, c.method, srv.URL+c.path, c.headers, body)

			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("err=%v want %q", err, c.wantErr)
				}
			} else if c.wantErrHas != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrHas) {
					t.Fatalf("err=%v want containing %q", err, c.wantErrHas)
				}
			} else if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			if status != c.wantStatus {
				t.Errorf("status=%d want %d", status, c.wantStatus)
			}
			if got := string(data); got != c.wantData {
				t.Errorf("data=%q want %q", got, c.wantData)
			}
		})
	}
}

// TestCovLoginRunURLPostsAndPersistsState 钉住 url 子命令的出站构造与落盘契约：
// POST /v2/plugin/auth/state?platform=CLI、body "{}"、Origin 随 realm，
// state 文件内容恰为 {state,realm}（realm 键必须落盘，poll 靠它防混域），stdout 只打 authUrl。
func TestCovLoginRunURLPostsAndPersistsState(t *testing.T) {
	cases := []struct {
		realm       string
		wantOrigin  string
		wantAuthURL string
	}{
		{"cn", "https://www.codebuddy.cn", "https://www.codebuddy.cn/authorize?state=st-abc"},
		{"global", "https://www.workbuddy.ai", "https://www.workbuddy.ai/authorize?state=st-abc"},
	}
	for _, c := range cases {
		t.Run(c.realm, func(t *testing.T) {
			var log covLoginReqLog
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				io.WriteString(w, `{"code":0,"msg":"","data":{"state":"st-abc","authUrl":"`+c.wantAuthURL+`"}}`)
			}))
			defer srv.Close()

			statePath := filepath.Join(t.TempDir(), "state.json")
			var out bytes.Buffer
			runURL(srv.URL, c.wantOrigin, c.realm, statePath, srv.Client(), &out)

			if got, want := out.String(), c.wantAuthURL+"\n"; got != want {
				t.Errorf("stdout=%q want %q", got, want)
			}

			req := log.find(t, "/v2/plugin/auth/state")
			if req.method != http.MethodPost {
				t.Errorf("method=%q want POST", req.method)
			}
			if req.query != "platform=CLI" {
				t.Errorf("query=%q want %q", req.query, "platform=CLI")
			}
			if req.body != "{}" {
				t.Errorf("body=%q want %q", req.body, "{}")
			}
			if req.origin != c.wantOrigin {
				t.Errorf("Origin=%q want %q", req.origin, c.wantOrigin)
			}
			if req.referer != c.wantOrigin+"/" {
				t.Errorf("Referer=%q want %q", req.referer, c.wantOrigin+"/")
			}

			raw, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatalf("state file not written: %v", err)
			}
			var got map[string]string
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("state file is not JSON: %v (%s)", err, raw)
			}
			wantState := map[string]string{"state": "st-abc", "realm": c.realm}
			if !reflect.DeepEqual(got, wantState) {
				t.Errorf("state file=%v want %v", got, wantState)
			}
		})
	}
}

// TestCovLoginRunURLFatalBranches 钉住 url 子命令的失败路径文案与退出码 1：
// 拿不到 state/authUrl 时禁止落盘（否则 poll 会拿空 state 干跑）。
func TestCovLoginRunURLFatalBranches(t *testing.T) {
	cases := []struct {
		name       string
		resp       string
		status     int
		wantErrHas []string
	}{
		{"业务 code 非 0", `{"code":9,"msg":"denied","data":null}`, 200, []string{"auth state failed", "code=9 msg=denied"}},
		{"缺 authUrl", `{"code":0,"msg":"","data":{"state":"s1"}}`, 200, []string{"auth state: missing state or authUrl"}},
		{"state 为空", `{"code":0,"msg":"","data":{"state":"","authUrl":"https://x/y"}}`, 200, []string{"auth state: missing state or authUrl"}},
		{"data 形态不是对象", `{"code":0,"msg":"","data":"oops"}`, 200, []string{"auth state: missing state or authUrl"}},
		{"上游 400", `{"code":0,"msg":"","data":null}`, 400, []string{"auth state failed", "http_error: upstream 400"}},
		{"上游 500", `{"code":0,"msg":"","data":null}`, 500, []string{"auth state failed", "http_error: upstream 500"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.status != 200 {
					w.WriteHeader(c.status)
				}
				io.WriteString(w, c.resp)
			}))
			defer srv.Close()

			statePath := filepath.Join(t.TempDir(), "state.json")
			stderr, code := covLoginFatalCapture(t, func() {
				runURL(srv.URL, "https://www.codebuddy.cn", "cn", statePath, srv.Client(), io.Discard)
			})
			if code != 1 {
				t.Errorf("exit code=%d want 1", code)
			}
			if !strings.HasPrefix(stderr, "login: ") {
				t.Errorf("stderr=%q want prefix %q", stderr, "login: ")
			}
			for _, want := range c.wantErrHas {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr=%q want containing %q", stderr, want)
				}
			}
			if _, err := os.Stat(statePath); !os.IsNotExist(err) {
				t.Errorf("state file must not be written on failure, stat err=%v", err)
			}
		})
	}

	t.Run("state 落盘失败", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"code":0,"msg":"","data":{"state":"s1","authUrl":"https://x/y"}}`)
		}))
		defer srv.Close()

		// 目标目录不存在 → os.WriteFile 必失败（Windows/Linux 一致）
		statePath := filepath.Join(t.TempDir(), "no-such-dir", "state.json")
		stderr, code := covLoginFatalCapture(t, func() {
			runURL(srv.URL, "https://www.codebuddy.cn", "cn", statePath, srv.Client(), io.Discard)
		})
		if code != 1 {
			t.Errorf("exit code=%d want 1", code)
		}
		if !strings.Contains(stderr, "write state:") {
			t.Errorf("stderr=%q want containing %q", stderr, "write state:")
		}
	})

	t.Run("传输失败", func(t *testing.T) {
		client := &http.Client{Transport: covLoginErrTransport{err: errors.New("covlogin dial boom")}}
		stderr, code := covLoginFatalCapture(t, func() {
			runURL("https://copilot.tencent.com", "https://www.codebuddy.cn", "cn",
				filepath.Join(t.TempDir(), "state.json"), client, io.Discard)
		})
		if code != 1 {
			t.Errorf("exit code=%d want 1", code)
		}
		if !strings.Contains(stderr, "auth state failed") || !strings.Contains(stderr, "covlogin dial boom") {
			t.Errorf("stderr=%q want auth state failure with cause", stderr)
		}
	})
}

// TestCovLoginValidateRealmMatch 钉住防混域规则：state 无 realm（旧文件）放行；
// 非空且与命令行不一致 → 报错，且报错文案含两个 realm（便于用户定位 url/poll 不同域）。
func TestCovLoginValidateRealmMatch(t *testing.T) {
	if err := validateRealmMatch("", "cn"); err != nil {
		t.Errorf("legacy state (empty realm) must pass, got %v", err)
	}
	for _, same := range [][2]string{{"cn", "cn"}, {"global", "global"}} {
		if err := validateRealmMatch(same[0], same[1]); err != nil {
			t.Errorf("validateRealmMatch(%q,%q)=%v want nil", same[0], same[1], err)
		}
	}
	for _, diff := range [][2]string{{"global", "cn"}, {"cn", "global"}} {
		err := validateRealmMatch(diff[0], diff[1])
		if err == nil {
			t.Fatalf("validateRealmMatch(%q,%q)=nil want error", diff[0], diff[1])
		}
		for _, want := range []string{"realm mismatch", fmt.Sprintf("state file realm=%q", diff[0]), fmt.Sprintf("command --realm=%q", diff[1])} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err=%q want containing %q", err.Error(), want)
			}
		}
	}
}

// TestCovLoginRunPollHappyPath 钉住 poll 全链路：
//   - token 端点 GET ?state=（不带 Bearer）；account 端点 GET ?state= 且带 Bearer <accessToken>
//   - 输出恰为 login.sh 需要的 8 个字段（字面量期望，单行 JSON），realm 走显式值
//   - 成功后删除 state 文件（否则重复 poll 会复用旧 state）
func TestCovLoginRunPollHappyPath(t *testing.T) {
	const wantOrigin = "https://www.workbuddy.ai"
	var log covLoginReqLog
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch r.URL.Path {
		case "/v2/plugin/auth/token":
			io.WriteString(w, `{"code":0,"msg":"","data":{"accessToken":"at-1","refreshToken":"rt-1","expiresIn":7200,"domain":"www.workbuddy.ai"}}`)
		case "/v2/plugin/login/account":
			io.WriteString(w, `{"code":0,"msg":"","data":{"uid":"u-9","enterpriseId":"ent-9","nickname":"nick-9"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	statePath := covLoginWriteFile(t, "state.json", `{"state":"st-1","realm":"global"}`)
	var out bytes.Buffer
	runPoll(srv.URL, wantOrigin, "global", statePath, srv.Client(), &out)

	raw := strings.TrimSpace(out.String())
	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Errorf("stdout newline count=%d want 1 (single line for login.sh), got %q", n, out.String())
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, raw)
	}
	want := map[string]any{
		"access_token":  "at-1",
		"refresh_token": "rt-1",
		"expires_in":    float64(7200),
		"domain":        "www.workbuddy.ai",
		"realm":         "global",
		"uid":           "u-9",
		"enterprise_id": "ent-9",
		"nickname":      "nick-9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("poll output=%v want %v", got, want)
	}

	tokReq := log.find(t, "/v2/plugin/auth/token")
	if tokReq.method != http.MethodGet {
		t.Errorf("token method=%q want GET", tokReq.method)
	}
	if tokReq.query != "state=st-1" {
		t.Errorf("token query=%q want %q", tokReq.query, "state=st-1")
	}
	if tokReq.authorization != "" {
		t.Errorf("token request must not carry Authorization, got %q", tokReq.authorization)
	}
	if tokReq.origin != wantOrigin {
		t.Errorf("token Origin=%q want %q", tokReq.origin, wantOrigin)
	}

	acctReq := log.find(t, "/v2/plugin/login/account")
	// 变异实测：把 account 请求的 http.MethodGet 改成 POST（或给它塞 body）后原测试仍全绿
	// ——account 的 method/body 从未被断言（token 的 method 有断言，account 没有）。期望值为手写字面量。
	if acctReq.method != "GET" {
		t.Errorf("account method=%q want %q", acctReq.method, "GET")
	}
	if acctReq.body != "" {
		t.Errorf("account body=%q want %q (GET 请求不得带 body)", acctReq.body, "")
	}
	if acctReq.query != "state=st-1" {
		t.Errorf("account query=%q want %q", acctReq.query, "state=st-1")
	}
	if acctReq.authorization != "Bearer at-1" {
		t.Errorf("account Authorization=%q want %q", acctReq.authorization, "Bearer at-1")
	}
	if acctReq.origin != wantOrigin {
		t.Errorf("account Origin=%q want %q", acctReq.origin, wantOrigin)
	}

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("state file must be removed after successful poll, stat err=%v", err)
	}
}

// TestCovLoginRunPollAccountFailureIsBestEffort 钉住 account 端点失败不影响登录产物：
// token 已拿到就照常输出（uid/enterprise_id/nickname 留空），并删 state 文件。
func TestCovLoginRunPollAccountFailureIsBestEffort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/token":
			io.WriteString(w, `{"code":0,"msg":"","data":{"accessToken":"at-2","refreshToken":"rt-2","expiresIn":60,"domain":"www.codebuddy.cn"}}`)
		case "/v2/plugin/login/account":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"code":0,"msg":"","data":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	statePath := covLoginWriteFile(t, "state.json", `{"state":"st-2","realm":"cn"}`)
	var out bytes.Buffer
	runPoll(srv.URL, "https://www.codebuddy.cn", "cn", statePath, srv.Client(), &out)

	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, out.String())
	}
	want := map[string]any{
		"access_token":  "at-2",
		"refresh_token": "rt-2",
		"expires_in":    float64(60),
		"domain":        "www.codebuddy.cn",
		"realm":         "cn",
		"uid":           "",
		"enterprise_id": "",
		"nickname":      "",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("poll output=%v want %v", got, want)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Errorf("state file must be removed, stat err=%v", err)
	}
}

// TestCovLoginRunPollFatalBranches 钉住 poll 的失败分类：
// state 缺失/损坏、realm 混域、未完成登录（业务 code 或空 accessToken）、token 端点 5xx、
// 连接失败（status=0）—— 逐条给出不同文案与退出码 1，禁止「未完成」与「上游错」混淆。
func TestCovLoginRunPollFatalBranches(t *testing.T) {
	cases := []struct {
		name          string
		stateJSON     string // "" → 不写 state 文件
		cliRealm      string
		tokenResp     string
		tokenStatus   int
		transport     bool
		wantErrHas    []string
		wantErrNotHas []string // 负断言：出现任一子串即 FAIL（用于钉住「档位阈值」而非仅仅钉住文案）
	}{
		{
			name: "state 文件缺失", stateJSON: "", cliRealm: "cn",
			wantErrHas: []string{"read state:", "先跑 login url"},
		},
		{
			name: "state 非法 JSON", stateJSON: "{not json", cliRealm: "cn",
			wantErrHas: []string{"parse state:"},
		},
		{
			name: "realm 混域", stateJSON: `{"state":"st-1","realm":"global"}`, cliRealm: "cn",
			wantErrHas: []string{"realm mismatch", `state file realm="global", command --realm="cn"`},
		},
		{
			name: "登录未完成（业务 code 非 0）", stateJSON: `{"state":"st-1","realm":"cn"}`, cliRealm: "cn",
			tokenResp: `{"code":40201,"msg":"login ing","data":null}`, tokenStatus: 200,
			wantErrHas: []string{"登录未完成（waiting for login）"},
		},
		{
			name: "token 端点 500", stateJSON: `{"state":"st-1","realm":"cn"}`, cliRealm: "cn",
			tokenResp: `{"code":0,"msg":"","data":null}`, tokenStatus: 500,
			wantErrHas: []string{"token endpoint error", "http_error: upstream 500"},
		},
		{
			// **2026-10-16 口径变更（P4）**：原用例注释写的是「401 归未完成是现有产品意图，
			// 待确认；若产品想让 401 走上游错误档，应改生产代码并把本用例改成正断言」。
			// 本次就是那次变更（ERROR-HANDLING 表 P4：真 401/403 时用户按到天亮也不会成功，
			// 且看不到上游状态码）→ 断言翻正。负断言保留：仍不得回显上游错误原文
			// （状态码是用户能行动的信息，原文只会让人误判成上游故障）。
			name: "token 端点 401：归「被拒」档（不再说「还未登录完成」）", stateJSON: `{"state":"st-1","realm":"cn"}`, cliRealm: "cn",
			tokenResp: `{"code":0,"msg":"","data":null}`, tokenStatus: 401,
			wantErrHas:    []string{"登录被上游拒绝", "HTTP 401", "继续等待不会成功"},
			wantErrNotHas: []string{"token endpoint error", "http_error: upstream 401", "请确认已在浏览器完成登录再按 y"},
		},
		{
			// 403 与 401 同档（凭证/授权被拒）。保留整档断言：只喂 401 时把阈值改动仍会全绿。
			name: "token 端点 403：同归「被拒」档", stateJSON: `{"state":"st-1","realm":"cn"}`, cliRealm: "cn",
			tokenResp: `{"code":0,"msg":"","data":null}`, tokenStatus: 403,
			wantErrHas:    []string{"登录被上游拒绝", "HTTP 403"},
			wantErrNotHas: []string{"token endpoint error", "http_error: upstream 403", "请确认已在浏览器完成登录再按 y"},
		},
		{
			// 404 单列：它是「realm/域配错」，与 401/403 的「被拒」不是同一类失效原因。
			name: "token 端点 404：提示 realm/域不匹配", stateJSON: `{"state":"st-1","realm":"cn"}`, cliRealm: "cn",
			tokenResp: `{"code":0,"msg":"","data":null}`, tokenStatus: 404,
			wantErrHas:    []string{"端点不存在", "HTTP 404"},
			wantErrNotHas: []string{"http_error: upstream 404", "请确认已在浏览器完成登录再按 y"},
		},
		{
			name: "accessToken 为空", stateJSON: `{"state":"st-1","realm":"cn"}`, cliRealm: "cn",
			tokenResp: `{"code":0,"msg":"","data":{"accessToken":"","refreshToken":"rt","expiresIn":1}}`, tokenStatus: 200,
			wantErrHas: []string{"登录未完成（waiting for login）"},
		},
		{
			name: "连接失败 status 归零", stateJSON: `{"state":"st-1","realm":"cn"}`, cliRealm: "cn",
			transport:  true,
			wantErrHas: []string{"token endpoint error", "covlogin dial boom"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var statePath string
			if c.stateJSON == "" {
				statePath = filepath.Join(t.TempDir(), "absent-state.json")
			} else {
				statePath = covLoginWriteFile(t, "state.json", c.stateJSON)
			}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.tokenStatus != 0 && c.tokenStatus != 200 {
					w.WriteHeader(c.tokenStatus)
				}
				io.WriteString(w, c.tokenResp)
			}))
			defer srv.Close()

			client := srv.Client()
			if c.transport {
				client = &http.Client{Transport: covLoginErrTransport{err: errors.New("covlogin dial boom")}}
			}

			stderr, code := covLoginFatalCapture(t, func() {
				runPoll(srv.URL, "https://www.codebuddy.cn", c.cliRealm, statePath, client, io.Discard)
			})
			if code != 1 {
				t.Errorf("exit code=%d want 1", code)
			}
			if !strings.HasPrefix(stderr, "login: ") {
				t.Errorf("stderr=%q want prefix %q", stderr, "login: ")
			}
			for _, want := range c.wantErrHas {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr=%q want containing %q", stderr, want)
				}
			}
			for _, bad := range c.wantErrNotHas {
				if strings.Contains(stderr, bad) {
					t.Errorf("stderr=%q must NOT contain %q", stderr, bad)
				}
			}
		})
	}
}

// TestCovLoginFatalFormatAndExitCode 钉住 fatal 的两件事：stderr 前缀 "login: " + 格式化消息，退出码恒 1。
func TestCovLoginFatalFormatAndExitCode(t *testing.T) {
	stderr, code := covLoginFatalCapture(t, func() { fatal("boom %d %s", 7, "x") })
	if code != 1 {
		t.Errorf("exit code=%d want 1", code)
	}
	if stderr != "login: boom 7 x\n" {
		t.Errorf("stderr=%q want %q", stderr, "login: boom 7 x\n")
	}
}

// TestCovLoginMainArgValidation 钉住 main 的参数校验（不触碰网络）：
// 缺子命令 → usage；非法 --realm → 含 usage 提示；未知子命令 → 原文回显。
// main 的 poll 分支在 state 文件缺失时必须在读文件阶段就失败（不得发请求）。
func TestCovLoginMainArgValidation(t *testing.T) {
	for _, c := range []struct {
		name       string
		args       []string
		wantErrHas []string
	}{
		{"缺子命令", nil, []string{"usage: login [--realm=cn|global] <url|poll>"}},
		{"非法 realm", []string{"--realm=bogus", "url"}, []string{"invalid --realm", "usage: login"}},
		{"分离式 realm 缺值", []string{"--realm"}, []string{"--realm requires a value"}},
		{"未知子命令", []string{"frobnicate"}, []string{`unknown subcommand "frobnicate" (want url|poll|realm)`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			oldArgs := os.Args
			os.Args = append([]string{"login"}, c.args...)
			defer func() { os.Args = oldArgs }()

			stderr, code := covLoginFatalCapture(t, main)
			if code != 1 {
				t.Errorf("exit code=%d want 1", code)
			}
			for _, want := range c.wantErrHas {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr=%q want containing %q", stderr, want)
				}
			}
		})
	}
}

// TestCovLoginMainPollMissingStateFile 覆盖 main 的 poll 分发分支：state 文件缺失时
// 在读文件阶段 fatal（不发任何请求，故无需网络）。
func TestCovLoginMainPollMissingStateFile(t *testing.T) {
	oldArgs, oldStateFile := os.Args, stateFile
	os.Args = []string{"login", "poll"}
	stateFile = filepath.Join(t.TempDir(), "absent-state.json")
	defer func() { os.Args, stateFile = oldArgs, oldStateFile }()

	stderr, code := covLoginFatalCapture(t, main)
	if code != 1 {
		t.Errorf("exit code=%d want 1", code)
	}
	if !strings.Contains(stderr, "read state:") {
		t.Errorf("stderr=%q want containing %q", stderr, "read state:")
	}
}

// TestCovLoginParseRealmArgsSeparatedForm 钉住分离式 `--realm <v>`（main.go:145）的归一化：
// 该分支的 ToLower+TrimSpace 此前没有任何断言钉住——原有用例只走等号分支 `--realm=GLOBAL`
// （152 行），把 145 行的归一化删掉后测试仍全绿。期望值一律手写字面量。
func TestCovLoginParseRealmArgsSeparatedForm(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantRealm  string
		wantRest   []string
		wantErrHas string
	}{
		{
			name: "分离式大写归一", args: []string{"--realm", "GLOBAL", "url"},
			wantRealm: "global", wantRest: []string{"url"},
		},
		{
			name: "分离式首尾空白归一", args: []string{"--realm", "  Global  ", "poll"},
			wantRealm: "global", wantRest: []string{"poll"},
		},
		{
			name: "分离式大写 CN 归一", args: []string{"--realm", "CN", "realm"},
			wantRealm: "cn", wantRest: []string{"realm"},
		},
		{
			name: "分离式非法值报错且原文回显", args: []string{"--realm", "eu", "url"},
			wantErrHas: `invalid --realm "eu" (want cn|global)`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			realm, rest, err := parseRealmArgs(c.args)
			if c.wantErrHas != "" {
				if err == nil {
					t.Fatalf("parseRealmArgs(%v) err=nil want containing %q", c.args, c.wantErrHas)
				}
				if !strings.Contains(err.Error(), c.wantErrHas) {
					t.Errorf("err=%q want containing %q", err.Error(), c.wantErrHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRealmArgs(%v) err=%v", c.args, err)
			}
			if realm != c.wantRealm {
				t.Errorf("realm=%q want %q (分离式 --realm 的取值必须大小写/空白归一)", realm, c.wantRealm)
			}
			if !reflect.DeepEqual(rest, c.wantRest) {
				t.Errorf("rest=%v want %v", rest, c.wantRest)
			}
		})
	}
}

// TestCovLoginMainRealmSubcommandWiring 钉住 main 的 `case "realm"` 接线（main.go:327-331）：
// 纯函数 promptRealm 此前被单测覆盖了，但「接线」没被覆盖——把第 330 行换成常量后测试仍全绿。
// 这里以注入式跑真 main：os.Stdin 指向含选项的临时文件、os.Stdout/os.Stderr 指向临时文件，
// 断言 stdout 恰好是归一化 realm（stdout 归 $( ) 捕获）、提示走 stderr、且不 panic。
func TestCovLoginMainRealmSubcommandWiring(t *testing.T) {
	cases := []struct {
		name       string
		stdin      string
		wantStdout string
	}{
		{name: "输入 2 → stdout 仅 global", stdin: "2\n", wantStdout: "global\n"},
		{name: "输入 1 → stdout 仅 cn", stdin: "1\n", wantStdout: "cn\n"},
		{name: "输入 global → stdout 仅 global", stdin: "global\n", wantStdout: "global\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			oldArgs, oldStdin, oldStdout, oldStderr := os.Args, os.Stdin, os.Stdout, os.Stderr
			defer func() { os.Args, os.Stdin, os.Stdout, os.Stderr = oldArgs, oldStdin, oldStdout, oldStderr }()

			inPath := covLoginWriteFile(t, "stdin.txt", c.stdin)
			in, err := os.Open(inPath)
			if err != nil {
				t.Fatalf("open stdin temp: %v", err)
			}
			defer func() { _ = in.Close() }()

			dir := t.TempDir()
			outPath := filepath.Join(dir, "stdout.txt")
			out, err := os.Create(outPath)
			if err != nil {
				t.Fatalf("create stdout temp: %v", err)
			}
			defer func() { _ = out.Close() }()
			errPath := filepath.Join(dir, "stderr.txt")
			errF, err := os.Create(errPath)
			if err != nil {
				t.Fatalf("create stderr temp: %v", err)
			}
			defer func() { _ = errF.Close() }()

			os.Args = []string{"login", "realm"}
			os.Stdin, os.Stdout, os.Stderr = in, out, errF

			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("main() panicked on `login realm`: %v", r)
					}
				}()
				main()
			}()

			if err := out.Close(); err != nil {
				t.Fatalf("close stdout temp: %v", err)
			}
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read stdout temp: %v", err)
			}
			if string(got) != c.wantStdout {
				t.Errorf("stdout=%q want %q (realm 子命令的 stdout 必须恰好是归一化 realm)", got, c.wantStdout)
			}

			if err := errF.Close(); err != nil {
				t.Fatalf("close stderr temp: %v", err)
			}
			herr, err := os.ReadFile(errPath)
			if err != nil {
				t.Fatalf("read stderr temp: %v", err)
			}
			if !strings.Contains(string(herr), "选择登录版本") {
				t.Errorf("stderr=%q want containing prompt %q (交互提示必须走 stderr)", herr, "选择登录版本")
			}
		})
	}
}
