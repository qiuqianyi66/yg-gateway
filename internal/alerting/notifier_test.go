package alerting

import (
	"sync"
	"testing"
	"time"
)

func TestNotifierDisabledWhenURLEmpty(t *testing.T) {
	n := New(Config{}) // WebhookURL 空
	// 不应 panic；不应发送。
	n.NotifyCreditLow("uid1", "nick", 100)
	n.NotifyAccountDown("session_dead", "uid2", "nick", "reason")
}

func TestNotifierDedup(t *testing.T) {
	n := New(Config{
		WebhookURL:         "http://127.0.0.1:1/none", // 永远连不上，但 send 是异步的，失败只打日志
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
	n := New(Config{WebhookURL: "http://127.0.0.1:1/none", MinCreditThreshold: 200})
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
