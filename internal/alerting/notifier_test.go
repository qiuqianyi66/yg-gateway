package alerting

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// okWebhook 起一个永远 200 的 webhook 端点。
//
// 为什么不能继续用 "http://127.0.0.1:1/none"：send 的失败路径会 delete(n.last, key)
// （设计如此——登记了却发不出去要允许下一次重试）。而 127.0.0.1:1 的连接拒绝在 Linux
// 上快到异步 goroutine 能抢在断言前抹掉登记，于是「第一次发送应记录时间戳」变成竞态：
// Windows 上失败慢，侥幸一直绿；2026-11-17 CI 第一次在 ubuntu 上跑到 -cover 口径时
// 才暴露（同一次 run 的 test/race job 反而绿，它们只是运气更好）。
// 走成功路径之后 goroutine 永不删登记，断言与异步之间没有竞争。
func okWebhook(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNotifierDisabledWhenURLEmpty(t *testing.T) {
	n := New(Config{}) // WebhookURL 空
	// 不应 panic；不应发送。
	n.NotifyCreditLow("uid1", "nick", 100)
	n.NotifyAccountDown("session_dead", "uid2", "nick", "reason")
}

func TestNotifierDedup(t *testing.T) {
	n := New(Config{
		WebhookURL:         okWebhook(t).URL,
		MinCreditThreshold: 200,
		DedupWindow:        time.Minute,
	})

	// 直接走 send 的去重路径：连续两次同 (type, uid)，第二次应被去重。
	n.send(Event{Type: "credit_low", AccountUID: "u1", Text: "x"})
	n.mu.Lock()
	first := n.last["credit_low:u1"]
	n.mu.Unlock()
	if first.IsZero() {
		t.Fatal("第一次发送应记录时间戳")
	}
	// 第二次在窗口内，不应更新时间戳。
	n.send(Event{Type: "credit_low", AccountUID: "u1", Text: "x"})
	n.mu.Lock()
	second := n.last["credit_low:u1"]
	n.mu.Unlock()
	if !second.Equal(first) {
		t.Fatal("窗口内重复事件应被去重，时间戳不应更新")
	}

	// 不同 uid 不互相影响。
	n.send(Event{Type: "credit_low", AccountUID: "u2", Text: "x"})
	n.mu.Lock()
	_, ok := n.last["credit_low:u2"]
	n.mu.Unlock()
	if !ok {
		t.Fatal("不同 uid 应独立计数")
	}
}

func TestNotifyCreditLowThreshold(t *testing.T) {
	n := New(Config{WebhookURL: okWebhook(t).URL, MinCreditThreshold: 200})
	// 余额高于阈值：不发。
	n.NotifyCreditLow("u", "n", 500)
	n.mu.Lock()
	_, ok := n.last["credit_low:u"]
	n.mu.Unlock()
	if ok {
		t.Fatal("余额高于阈值不应触发")
	}
	// 余额等于/低于阈值：触发。
	n.NotifyCreditLow("u", "n", 100)
	n.mu.Lock()
	_, ok = n.last["credit_low:u"]
	n.mu.Unlock()
	if !ok {
		t.Fatal("余额低于阈值应触发")
	}
}

// 确保并发 send 不 panic / 不死锁。
// 这里仍用连不上的地址：本用例不断言 n.last，只验并发安全，失败路径反而更快结束。
func TestNotifierConcurrent(t *testing.T) {
	n := New(Config{WebhookURL: "http://127.0.0.1:1/none", DedupWindow: time.Minute})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.NotifyAccountDown("x", "u", "n", "r")
		}()
	}
	wg.Wait()
}
