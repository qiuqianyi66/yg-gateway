package panel

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/scheduler"
	"yg-gateway/internal/upstream"
)

// P3 修复的面板侧闭环：排程 blackcat 正在跑时，runBlackCat 必须在 BlackcatNeed 之前
// 跳过，不产生任何面板侧上游调用。面板侧窗口时钟可钉在 23:30；但排程那趟的窗口时钟
// 在 scheduler 包内、面板测试无法替换，故墙钟在窗口外时 Skip（不伪造排程状态）。
func TestRunBlackCatSkipsWhileSchedulerBusy(t *testing.T) {
	if !upstream.InNightWindow(time.Now()) {
		t.Skip("当前墙钟不在 23:00–08:00：排程 blackcat 不会进请求，无法叠窗口，夜间再跑")
	}
	oldClock := nowForBlackcat
	nowForBlackcat = func() time.Time { return time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local) }
	t.Cleanup(func() { nowForBlackcat = oldClock })

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var relOnce sync.Once
	unblock := func() { relOnce.Do(func() { close(release) }) }
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.Write([]byte(`{"code":0}`))
	}))
	t.Cleanup(srv.Close) // LIFO：Close 后登记 unblock → 放行先于关闭
	t.Cleanup(unblock)

	pl := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	pl.Add(a)
	up := &upstream.Client{
		HTTP: srv.Client(), ChatHTTP: srv.Client(),
		ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
	}
	sched := scheduler.New(scheduler.Config{Pool: pl, Upstream: up})
	p := New(Config{Version: "test", Pool: pl, Upstream: up, Scheduler: sched})

	go sched.RunBlackcatNow()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("排程 blackcat 10s 内没进入请求")
	}
	if !sched.BlackcatBusy() {
		t.Fatal("前置：排程已在请求里，BlackcatBusy 应为 true")
	}

	msg, err := runBlackCat(p, a)
	if err != nil {
		t.Fatalf("跳过不该报错: %v", err)
	}
	if msg != "排程正在自动补足夜猫子，跳过本次手动触发" {
		t.Fatalf("提示=%q want 排程跳过提示", msg)
	}
	// 排程那趟占 1 个请求；面板若没拦住，BlackcatNeed 会再发请求把计数抬到 2。
	if got := calls.Load(); got != 1 {
		t.Fatalf("面板侧多发了上游调用：calls=%d want 1", got)
	}
}
