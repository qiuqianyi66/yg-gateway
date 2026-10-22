package alerting

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestNotifierFailureClearsDedup 钉住 C-1：发送失败（连接拒绝）后去重登记必须被清除，
// 下一次同事件触发才能重试——不允许「登记了却从没发出去，整个去重窗口静默」。
func TestNotifierFailureClearsDedup(t *testing.T) {
	n := New(Config{
		WebhookURL:  "http://127.0.0.1:1/none", // 永远连不上，send 异步失败
		DedupWindow: time.Minute,
	})

	n.send(Event{Type: "credit_low", AccountUID: "u1", Text: "x"})

	// 等异步 goroutine 跑完失败路径：登记必须被删除。
	deadline := time.Now().Add(3 * time.Second)
	for {
		n.mu.Lock()
		_, ok := n.last["credit_low:u1"]
		n.mu.Unlock()
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("发送失败后去重登记应被清除（允许下次重试）")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNotifierHTTPErrorRetriesNextTrigger 钉住 C-1 的 HTTP 侧：webhook 返回 non-2xx
// 时登记被清除，随后同一事件再次触发会重新发送（hits 递增）。
func TestNotifierHTTPErrorRetriesNextTrigger(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := New(Config{WebhookURL: srv.URL, DedupWindow: time.Minute})
	n.send(Event{Type: "credit_low", AccountUID: "u1", Text: "x"})

	// 第一次失败：登记必须被清除。
	deadline := time.Now().Add(3 * time.Second)
	for {
		n.mu.Lock()
		_, ok := n.last["credit_low:u1"]
		n.mu.Unlock()
		if !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("non-2xx 后去重登记应被清除")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("第一次触发应恰好发 1 次，实际 %d", atomic.LoadInt32(&hits))
	}

	// 再次触发同一事件：应重新发送（hits 递增）。
	n.send(Event{Type: "credit_low", AccountUID: "u1", Text: "x"})
	deadline = time.Now().Add(3 * time.Second)
	for {
		if atomic.LoadInt32(&hits) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("失败事件在去重清除后应可重发，hits=%d", atomic.LoadInt32(&hits))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestNotifierSuccessKeepsDedup 钉住 C-1 反向：发送成功后登记保留，
// 窗口内同事件不得重发（防熔断重试风暴刷屏的原始语义不受破坏）。
func TestNotifierSuccessKeepsDedup(t *testing.T) {
	received := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := New(Config{WebhookURL: srv.URL, DedupWindow: time.Minute})
	n.send(Event{Type: "credit_low", AccountUID: "u1", Text: "x"})

	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook 未收到首次请求")
	}

	// 成功后登记保留 → 窗口内同事件直接去重，不发第二次。
	n.send(Event{Type: "credit_low", AccountUID: "u1", Text: "x"})
	select {
	case <-received:
		t.Fatal("成功去重后窗口内不应重发")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestNotifierBodyUIDTruncated 钉住 C-2：外发 body 的 account_uid 只含前 8 位
// （R2 口径，logfmt.UID8），完整 uid 不得外流到 webhook。
func TestNotifierBodyUIDTruncated(t *testing.T) {
	received := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case received <- b:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n := New(Config{WebhookURL: srv.URL, DedupWindow: time.Minute})
	const full = "0123456789abcdef"
	n.send(Event{Type: "credit_low", AccountUID: full, Text: "x"})

	var body []byte
	select {
	case body = <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook 未收到请求")
	}

	if !bytes.Contains(body, []byte("01234567")) {
		t.Fatalf("body 应含截断 uid 前 8 位：%s", body)
	}
	if bytes.Contains(body, []byte(full)) {
		t.Fatalf("body 不得含完整 uid：%s", body)
	}
}
