// keyalert_wiring_test.go V3-2：单轮检查的闩锁/阈值/月度键语义 + notifier 门面。
package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/alerting"
	"yg-gateway/internal/usage"
)

// TestStartKeyUsageAlertsEndToEnd 装配层牙齿：真 Recorder + 真 webhook（httptest
// 捕获）——k1 达标告警恰一次（闩锁），k2 未达标静默；goroutine 随 ctx 退出（R9）。
// 覆盖 startKeyUsageAlerts 的 goroutine 体（单轮函数另测，两者覆盖面不同）。
func TestStartKeyUsageAlertsEndToEnd(t *testing.T) {
	var mu sync.Mutex
	var events []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		events = append(events, string(b))
		mu.Unlock()
	}))
	defer ts.Close()

	rec := usage.New(t.TempDir() + "/usage.json")
	rec.Add(time.Now(), "cn", "u", "m", usage.Delta{KeyID: "k1", HasTotal: true, TotalTokens: 500}, true)
	keys := []APIKeyConfig{
		{ID: "k1", Name: "Heavy", UsageAlertTokens: 400},
		{ID: "k2", UsageAlertTokens: 400}, // 零用量：静默
	}
	old := keyAlertInterval
	keyAlertInterval = 2 * time.Millisecond
	defer func() { keyAlertInterval = old }()

	ctx, cancel := context.WithCancel(context.Background())
	startKeyUsageAlerts(keys, rec, alerting.New(alerting.Config{WebhookURL: ts.URL, DedupWindow: time.Minute}), ctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("2s 内未告警 k1")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("应恰告警 1 次（闩锁抑制 + k2 静默）：%v", events)
	}
	if !strings.Contains(events[0], `"key_usage"`) || !strings.Contains(events[0], `"k1"`) {
		t.Fatalf("事件形态错：%s", events[0])
	}
}

func TestCheckKeyUsageAlertsOnce(t *testing.T) {
	watched := []APIKeyConfig{
		{ID: "k1", UsageAlertTokens: 400},
		{ID: "k2", UsageAlertTokens: 400},
		{ID: "k3", UsageAlertTokens: 0}, // 未配置：永不告警
	}
	used := map[string]int64{"k1": 500, "k2": 100, "k3": 999999}
	var fired []string
	notify := func(keyID string, u, th int64) { fired = append(fired, keyID) }
	l := &keyAlertLatch{fired: map[string]bool{}}

	checkKeyUsageAlertsOnce(watched, func(id string) int64 { return used[id] }, notify, l, "2026-10")
	if len(fired) != 1 || fired[0] != "k1" {
		t.Fatalf("首轮应只告警 k1（超阈值）：fired=%v", fired)
	}

	// 同月重复检查：k1 已闩锁不重发；k2 未达标仍静默；k3 未配置跳过。
	checkKeyUsageAlertsOnce(watched, func(id string) int64 { return used[id] }, notify, l, "2026-10")
	if len(fired) != 1 {
		t.Fatalf("闩锁应抑制重复告警：fired=%v", fired)
	}

	// 月滚动：键含月份 → 新月重新武装（k1 再次达标即再告警）。
	checkKeyUsageAlertsOnce(watched, func(id string) int64 { return used[id] }, notify, l, "2026-11")
	if len(fired) != 2 || fired[1] != "k1" {
		t.Fatalf("新月应重新武装：fired=%v", fired)
	}

	// 未配置项（k3）即使爆表也永不告警；已配置的 k1（新月重新武装）与 k2（新月
	// 后爆表达标）各告警一次。
	checkKeyUsageAlertsOnce(watched, func(id string) int64 { return 1 << 40 }, notify, l, "2026-12")
	if len(fired) != 4 || fired[3] != "k2" {
		t.Fatalf("fired=%v, want 4 次且末位 k2", fired)
	}
	for _, id := range fired {
		if id == "k3" {
			t.Fatal("未配置阈值的 k3 不得告警")
		}
	}
}

// TestNotifyKeyUsageGuard 阈值<=0 与空 webhook 的门面守卫（不 panic 即过；webhook
// 空时 send 内部静默）。
func TestNotifyKeyUsageGuard(t *testing.T) {
	n := alerting.New(alerting.Config{})
	n.NotifyKeyUsage("k1", "展示名", 500, 400) // 空 webhook：send 静默，不 panic
}
