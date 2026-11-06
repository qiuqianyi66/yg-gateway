package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// TestMain 把保活分片窗口在测试环境默认置 0（立即串行）——与 activityAccountDelay
// 「测试可置 0」同款先例。既有 8+ 处测试调用 RunKeepaliveNow()，若不置 0 会真睡到
// 生产默认 10min 窗口内各自的 fnv 偏移（曾导致整包 600s 超时）。
// 需要分片语义的测试自行 fastSpread(t, d) 设短窗口。
func TestMain(m *testing.M) {
	old := keepaliveSpread
	keepaliveSpread = 0
	code := m.Run()
	keepaliveSpread = old
	os.Exit(code)
}

// testMachineID 复刻 upstream.headers.go deriveAccountStableID 的派生公式
// （"wb2a:machine:"+uid 的 sha256 前 18 字节 hex），用于 stub 反查 uid。
// 与实现保持同公式：若实现改了派生，本测试映射失配会表现为调用 uid 空 → 断言红，
// 暴露「头语义变了但测试没跟上」。
func testMachineID(uid string) string {
	sum := sha256.Sum256([]byte("wb2a:machine:" + uid))
	return hex.EncodeToString(sum[:18])
}

// fastSpread 置保活分片窗口为短值（测试不白等 10min），Cleanup 恢复。
func fastSpread(t *testing.T, d time.Duration) {
	t.Helper()
	old := keepaliveSpread
	keepaliveSpread = d
	t.Cleanup(func() { keepaliveSpread = old })
}

// refreshStub 记录每个账号的刷新调用时刻（POST /v2/plugin/auth/token/refresh）。
type refreshStub struct {
	mu        sync.Mutex
	calls     []refreshCall
	byMachine map[string]string // X-Machine-ID(sha256 派生) → uid（headers.go deriveAccountStableID）
}

type refreshCall struct {
	uid string
	at  time.Time
}

func (s *refreshStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/plugin/auth/token/refresh" {
			http.Error(w, "not found", 404)
			return
		}
		s.mu.Lock()
		uid := s.byMachine[r.Header.Get("X-Machine-ID")]
		s.calls = append(s.calls, refreshCall{uid: uid, at: time.Now()})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"accessToken": "at-new", "refreshToken": "rt-new", "expiresIn": 3600},
		})
	})
}

func (s *refreshStub) snapshot() []refreshCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]refreshCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// newKeepaliveScheduler 构造带 refresh stub 的调度器（5 个账号）。
func newKeepaliveScheduler(t *testing.T, stub *refreshStub) (*Scheduler, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	p := pool.New("")
	stub.byMachine = map[string]string{}
	for i := 1; i <= 5; i++ {
		uid := "u-keepalive-" + string(rune('0'+i))
		p.Add(&auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
		stub.byMachine[testMachineID(uid)] = uid
	}
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	return New(Config{Pool: p, Upstream: up}), srv
}

// TestKeepaliveSpreadDeterministic 分片语义（F2）：
//   - 各号在窗口内按 fnv 偏移散布（总时长 ≤ spread）；
//   - 相邻调用间隔与期望偏移差一致（**按偏移升序 + 睡到计划时刻**——去 sleep 或
//     改池序都会让本断言红）；
//   - 同一 uid 两次运行相对偏移一致（确定性，重启可复现）。
func TestKeepaliveSpreadDeterministic(t *testing.T) {
	fastSpread(t, 3*time.Second)
	stub := &refreshStub{}
	s, _ := newKeepaliveScheduler(t, stub)

	run := func() []refreshCall {
		stub.calls = nil
		s.RunKeepaliveNow()
		return stub.snapshot()
	}
	first := run()
	if len(first) != 5 {
		t.Fatalf("应刷新 5 个账号, got %d", len(first))
	}
	second := run()
	if len(second) != 5 {
		t.Fatalf("第二次应刷新 5 个账号, got %d", len(second))
	}

	// 1) 总时长 ≤ spread（窗口内完成，不随号数线性拖长）
	if total := first[len(first)-1].at.Sub(first[0].at); total > 3*time.Second {
		t.Fatalf("总时长 %v 超出窗口 3s", total)
	}

	// 2) 调用按 offset 单调不降（排序生效；fnv 按秒取模会碰撞，相同偏移内顺序任意
	//    合法——只钉「偏移升序」这个契约，不钉碰撞组内的相对顺序）。
	offs := map[string]time.Duration{}
	for _, c := range first {
		offs[c.uid] = keepaliveOffset(c.uid)
	}
	ordered := make([]string, 0, len(offs))
	for uid := range offs {
		ordered = append(ordered, uid)
	}
	sort.Slice(ordered, func(i, j int) bool { return offs[ordered[i]] < offs[ordered[j]] })
	for i := 1; i < len(first); i++ {
		if offs[first[i].uid] < offs[first[i-1].uid] {
			t.Fatalf("调用顺序非偏移升序：第 %d 个 %s(off=%v) 早于第 %d 个 %s(off=%v)",
				i-1, first[i-1].uid, offs[first[i-1].uid], i, first[i].uid, offs[first[i].uid])
		}
	}

	// 3) 窗口实际被利用 + 无倒序（睡到计划时刻生效；去 sleep → 首末间隔≈0 → 红）
	maxOff, minOff := offs[ordered[0]], offs[ordered[0]]
	for _, o := range offs {
		if o > maxOff {
			maxOff = o
		}
		if o < minOff {
			minOff = o
		}
	}
	span := first[len(first)-1].at.Sub(first[0].at)
	if maxOff > minOff && span < (maxOff-minOff)/2 {
		t.Fatalf("首末间隔 %v 过小（窗口未被利用，睡到计划时刻失效？）", span)
	}
	for i := 1; i < len(first); i++ {
		if d := first[i].at.Sub(first[i-1].at); d < -50*time.Millisecond {
			t.Fatalf("调用顺序出现倒序：%s 在 %s 之前（间隔 %v）", first[i].uid, first[i-1].uid, d)
		}
	}

	// 4) 确定性：同一 uid 两次运行的相对偏移（相对首次）一致
	rel := func(cs []refreshCall) map[string]time.Duration {
		out := map[string]time.Duration{}
		t0 := cs[0].at
		for _, c := range cs {
			out[c.uid] = c.at.Sub(t0)
		}
		return out
	}
	r1, r2 := rel(first), rel(second)
	for uid := range r1 {
		if d := r1[uid] - r2[uid]; d < -300*time.Millisecond || d > 300*time.Millisecond {
			t.Fatalf("%s 两次运行相对偏移不一致: %v vs %v（确定性失效）", uid, r1[uid], r2[uid])
		}
	}
}

// TestKeepaliveSpreadZero 关闭态（spread=0）：立即串行全刷，无窗口散布
// （与引入分片前的行为逐字一致，回归保持绿）。
func TestKeepaliveSpreadZero(t *testing.T) {
	fastSpread(t, 0)
	stub := &refreshStub{}
	s, _ := newKeepaliveScheduler(t, stub)

	s.RunKeepaliveNow()
	calls := stub.snapshot()
	if len(calls) != 5 {
		t.Fatalf("应刷新 5 个账号, got %d", len(calls))
	}
	for i := 1; i < len(calls); i++ {
		if d := calls[i].at.Sub(calls[i-1].at); d > 200*time.Millisecond {
			t.Fatalf("spread=0 时第 %d 个调用间隔 %v 过大（应立即串行）", i, d)
		}
	}
}

// TestKeepaliveOffsetDeterministic 纯函数语义：同一 uid 偏移一致；spread=0 → 0。
func TestKeepaliveOffsetDeterministic(t *testing.T) {
	fastSpread(t, 10*time.Second)
	o1 := keepaliveOffset("u-1")
	o2 := keepaliveOffset("u-1")
	if o1 != o2 {
		t.Fatalf("同一 uid 偏移应一致: %v vs %v", o1, o2)
	}
	if o1 < 0 || o1 > 10*time.Second {
		t.Fatalf("偏移应在 [0, spread): %v", o1)
	}
	fastSpread(t, 0)
	if o := keepaliveOffset("u-1"); o != 0 {
		t.Fatalf("spread=0 时偏移应为 0, got %v", o)
	}
}
