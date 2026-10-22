package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// TestLoginPollConcurrentPlaceholder（C-6）：同一 state 并发两次 loginPoll，
// 必须只有一个走完落盘 + 签到（login/account 端点只被打一次），另一个 404。
// 修复前：known 检查在 loginMu 外，两个并发 poll 会双双通过检查，各自走完
// SaveAtomic + Pool.Add + 签到（幂等无害但双倍打上游）。
func TestLoginPollConcurrentPlaceholder(t *testing.T) {
	var accountHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "auth/token"):
			w.Write([]byte(`{"code":0,"msg":"","data":{"accessToken":"at-x","refreshToken":"rt-x","expiresIn":7200,"domain":"cn"}}`))
		case strings.Contains(r.URL.Path, "login/account"):
			accountHits.Add(1)
			w.Write([]byte(`{"code":0,"msg":"","data":{"uid":"u-conc-0001","nickname":"并发号","enterpriseId":""}}`))
		default:
			w.Write([]byte(`{"code":0,"msg":"","data":{}}`))
		}
	}))
	defer srv.Close()

	// doJSON / 上游 client 都打真实域名，重写到本地测试服务器（忽略原 Host）。
	rewrite := &http.Client{Transport: rewriteTransport(srv.URL)}
	oldHTTP := loginHTTP
	loginHTTP = rewrite
	defer func() { loginHTTP = oldHTTP }()

	up := &upstream.Client{
		HTTP:          rewrite,
		ChatBaseCN:    srv.URL,
		BillingBaseCN: srv.URL,
		WebBaseCN:     srv.URL,
	}

	p := New(Config{
		Version:  "test",
		APIKey:   "test",
		AuthDir:  t.TempDir(),
		Pool:     pool.New(""),
		Upstream: up,
	})
	p.logins["state-conc"] = loginSession{created: time.Now(), realm: "cn"}

	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/panel/api/login/poll?state=state-conc", nil)
			p.loginPoll(rec, req)
			codes <- rec.Code
		}()
	}
	wg.Wait()
	close(codes)
	ok, notFound := 0, 0
	for c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusNotFound:
			notFound++
		}
	}
	if ok != 1 || notFound != 1 {
		t.Fatalf("两个并发 poll = %d 个 200 + %d 个 404, want 1+1", ok, notFound)
	}
	if got := accountHits.Load(); got != 1 {
		t.Fatalf("login/account 被打 %d 次, want 1（双 Tab 竞态）", got)
	}
}

// rewriteTransport 把任意出站请求重写到本地测试服务器（忽略原 Host）。
type rewriteTransport string

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(string(t), "http://")
	return http.DefaultTransport.RoundTrip(req)
}
