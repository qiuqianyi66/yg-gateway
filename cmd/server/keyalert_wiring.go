// keyalert_wiring.go key 级用量阈值告警装配（ROADMAP v3 V3-2，只告警不拦截）。
//
// 数据源：usage.Recorder.MonthlyUsed（P1-A 月 token 账，持久化跨重启）——不新造
// 聚合。周期 5 分钟检查一次；每 (月, key) 只告警一次（latch），月滚动后键自然过期
// = 下月重新武装。无配置项（全部 threshold=0）时不起 goroutine（零回归）。
package main

import (
	"context"
	"log"
	"sync"
	"time"

	"yg-gateway/internal/alerting"
	"yg-gateway/internal/usage"
)

var keyAlertInterval = 5 * time.Minute // var 非 const：测试可缩短（minPickGap 同款模式）

// keyAlertLatch 每 (月, keyID) 一次的告警闩锁。
type keyAlertLatch struct {
	mu    sync.Mutex
	fired map[string]bool
}

func (l *keyAlertLatch) done(k string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fired[k]
}

func (l *keyAlertLatch) fire(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fired[k] = true
}

// checkKeyUsageAlertsOnce 单轮检查（独立成函数：月度键/阈值判定/闩锁可直测）。
func checkKeyUsageAlertsOnce(watched []APIKeyConfig, monthlyUsed func(string) int64,
	notify func(keyID string, used, threshold int64), l *keyAlertLatch, month string) {
	for _, k := range watched {
		if k.UsageAlertTokens <= 0 {
			continue
		}
		lk := month + "|" + k.ID
		if l.done(lk) {
			continue
		}
		used := monthlyUsed(k.ID)
		if used >= k.UsageAlertTokens {
			l.fire(lk)
			notify(k.ID, used, k.UsageAlertTokens)
		}
	}
}

// startKeyUsageAlerts 装配并启动 key 用量告警循环。ctx 取消即退出（R9）。
func startKeyUsageAlerts(keys []APIKeyConfig, rec *usage.Recorder, n *alerting.Notifier, ctx context.Context) {
	var watched []APIKeyConfig
	for _, k := range keys {
		if k.UsageAlertTokens > 0 {
			watched = append(watched, k)
		}
	}
	if len(watched) == 0 {
		return
	}
	l := &keyAlertLatch{fired: map[string]bool{}}
	t := time.NewTicker(keyAlertInterval)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				checkKeyUsageAlertsOnce(watched, rec.MonthlyUsed,
					func(keyID string, used, threshold int64) {
						n.NotifyKeyUsage(keyID, keyNameOf(keys, keyID), used, threshold)
					}, l, time.Now().Format("2006-01"))
			}
		}
	}()
	log.Printf("[keyalert] key 级用量告警已启用：watched=%d interval=%s（只告警不拦截）", len(watched), keyAlertInterval)
}

// keyNameOf 取 Key 展示名（告警文案用）。
func keyNameOf(keys []APIKeyConfig, id string) string {
	for _, k := range keys {
		if k.ID == id {
			return k.Name
		}
	}
	return ""
}
