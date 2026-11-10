package scheduler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// P2-5：面板手动触发是 `go Scheduler.RunXxxNow()`（panel.go:687-733），与排程
// runBatch 对同一族会同时进。同族并发重入 = 对同一批账号把「计次型上游接口」的
// 请求翻倍（养号最怕的风控面）。本用例在 /v2/report 上真的把两趟窗口叠起来测：
// 排程那趟卡在请求里时，手动触发必须直接跳过（不发第二轮、也不排队补跑）。
func TestTaskGateSerializesSameFamily(t *testing.T) {
	fastActivity(t)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var relOnce sync.Once
	unblock := func() { relOnce.Do(func() { close(release) }) }
	var calls, inflight, maxInflight atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/report" {
			w.Write([]byte(`{"code":0}`))
			return
		}
		calls.Add(1)
		cur := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if cur <= m || maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		inflight.Add(-1)
		w.Write([]byte(`{"code":0,"msg":"OK"}`))
	}))
	// 顺序敏感：Cleanup 是 LIFO，故先登记 Close、后登记 unblock —— 断言失败时
	// 必须先放行卡在 <-release 的 handler，否则 Close 会一直等它（第一次变异实测挂死）。
	t.Cleanup(srv.Close)
	// 断言失败时若还有 handler 卡在 <-release，srv.Close() 会等它——必须无条件放行，
	// 否则用例变成挂 10 分钟而不是失败。
	t.Cleanup(unblock)

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := New(Config{
		Pool:     p,
		Upstream: &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL},
	})

	running := make(chan struct{})
	go func() {
		s.runActivity(context.Background()) // 排程那趟（runBatch 走同一个入口）
		close(running)
	}()
	select {
	case <-entered: // 保证第一趟已经在请求里
	case <-time.After(10 * time.Second):
		t.Fatal("排程那趟 10s 内没发出 /v2/report：门控把正常路径也挡住了（否则这里会挂到 go test 超时）")
	}

	// 窗口内手动触发：必须**立刻**跳过（不排队、不再发一轮上游请求）。
	// 这里必须异步等：门控缺失时 RunActivityNow 会排进正在跑的那趟而不返回，
	// 同步调用会把「断言失败」变成「整个用例挂住」（第一次变异实测就这样）。
	manualDone := make(chan struct{})
	go func() {
		s.RunActivityNow()
		close(manualDone)
	}()
	select {
	case <-manualDone:
	case <-time.After(5 * time.Second):
		t.Fatal("手动触发 5s 未返回：同族互斥缺失，它排进了正在执行的那趟（应在占用中直接跳过）")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("并发期手动触发又打了上游：calls=%d want 1", got)
	}

	unblock()
	<-running
	if got := maxInflight.Load(); got != 1 {
		t.Fatalf("并发峰值=%d want 1（同族必须串行）", got)
	}

	// 上一趟结束后，手动触发恢复正常（锁是「占用中跳过」，不是永久拒绝）。
	s.RunActivityNow()
	if got := calls.Load(); got != 2 {
		t.Fatalf("空闲后手动触发 calls=%d want 2", got)
	}
	if got := maxInflight.Load(); got != 1 {
		t.Fatalf("串行后并发峰值=%d want 1", got)
	}
}

// P3 修复：BlackcatBusy 必须如实反映 blackcat 门控的取/放，面板靠它在不抢锁的前提下
// 判断排程是否在跑。窗口时钟钉死（不依赖运行墙钟），stub 在第一个请求处卡住。
func TestBlackcatBusyFlagReflectsGate(t *testing.T) {
	schedulerPinNightWindow(t, time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local))

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
	t.Cleanup(srv.Close) // LIFO：先登记的后执行
	t.Cleanup(unblock)

	p := pool.New("")
	p.Add(schedulerCoverAuth("u1"))
	s := New(Config{
		Pool: p,
		Upstream: &upstream.Client{
			HTTP: srv.Client(), ChatHTTP: srv.Client(),
			ChatBaseCN: srv.URL, BillingBaseCN: srv.URL, WebBaseCN: srv.URL,
		},
	})

	done := make(chan struct{})
	go func() {
		s.RunBlackcatNow()
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("blackcat 10s 内没发出请求：门控或窗口守卫把正常路径挡住了")
	}
	if !s.BlackcatBusy() {
		t.Fatal("取锁后 BlackcatBusy=false want true（面板会因此误发夜间对话）")
	}

	unblock()
	<-done
	if s.BlackcatBusy() {
		t.Fatal("释放后 BlackcatBusy=true want false")
	}
}
