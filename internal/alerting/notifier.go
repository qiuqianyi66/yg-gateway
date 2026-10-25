// Package alerting 异常外发 webhook 告警。
//
// 用途：账号余额跌破阈值、账号被熔断/禁用这类事件，用户不盯着面板就发现不了。
// 本包把这类事件 POST 成一个通用 JSON body 到配置的 webhook URL。
//
// body 形态（Server酱/TG bot/飞书自定义机器人/Discord webhook 都能接，真要特定平台
// 格式可在外面套一层 n8n/中转）：
//
//	{"text":"[yg-gateway] 账号 xxx 余额告急：当前 120 积分（阈值 200）",
//	 "type":"credit_low","account_uid":"...","account_nick":"...","credits":120,"at":"..."}
//
// 设计要点：
//   - fire-and-forget：POST 在 goroutine 里做，失败只打日志不阻塞业务路径；
//   - 去重：同一 (type, uid) 在 dedupWindow 内不重发（防熔断重试风暴刷屏）；
//   - nil-safe：WebhookURL 为空时 Notifier 是零值，所有方法直接返回（零回归）。
package alerting

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"yg-gateway/internal/logfmt"
)

// Config Notifier 配置。零值 = 全部关闭（WebhookURL 空 = 不发）。
type Config struct {
	WebhookURL         string        // 告警接收端点；空 = 关闭
	MinCreditThreshold int64         // 积分低于该值触发 credit_low；<=0 关闭
	DedupWindow        time.Duration // 同事件去重窗口；<=0 回落 1h
}

// Notifier 进程内告警发送器。并发安全；零值可用但不发任何东西。
type Notifier struct {
	cfg  Config
	cli  *http.Client
	mu   sync.Mutex
	last map[string]time.Time // key = "type:uid" → 上次发送时间
}

// New 构造 Notifier。cfg.WebhookURL 为空时返回一个永不发送的零值实例。
func New(cfg Config) *Notifier {
	if cfg.DedupWindow <= 0 {
		cfg.DedupWindow = time.Hour
	}
	return &Notifier{
		cfg:  cfg,
		cli:  &http.Client{Timeout: 5 * time.Second},
		last: map[string]time.Time{},
	}
}

// Event 一条告警。
type Event struct {
	Type        string `json:"type"`                   // credit_low / account_disabled / session_dead / breaker_tripped
	AccountUID  string `json:"account_uid,omitempty"`  // 账号 UID
	AccountNick string `json:"account_nick,omitempty"` // 账号昵称
	Credits     int64  `json:"credits,omitempty"`      // 当前积分（credit_low 用）
	Reason      string `json:"reason,omitempty"`       // 触发原因
	Text        string `json:"text"`                   // 人类可读的一句话摘要
}

// NotifyCreditLow 余额告急事件。credits <= 0 时仍发（区分"耗尽"与"告急"由调用方传 reason）。
// MinCreditThreshold<=0 时直接返回（该触发点关闭）。
func (n *Notifier) NotifyCreditLow(uid, nick string, credits int64) {
	if n == nil || n.cfg.WebhookURL == "" || n.cfg.MinCreditThreshold <= 0 {
		return
	}
	if credits > n.cfg.MinCreditThreshold {
		return
	}
	n.send(Event{
		Type:        "credit_low",
		AccountUID:  uid,
		AccountNick: nick,
		Credits:     credits,
		Text:        "账号 " + nick + " 余额告急：当前 " + itoa(credits) + " 积分（阈值 " + itoa(n.cfg.MinCreditThreshold) + "）",
	})
}

// NotifyAccountDown 账号级异常事件（被禁用 / 会话死 / 触发熔断）。
// NotifyKeyUsage key 级用量阈值告警（ROADMAP v3 V3-2，只告警不拦截）。
// used/threshold 由调用方从 usage.Recorder.MonthlyUsed 与配置取（月 token 口径）。
// AccountUID 承载 keyID：send 的去重键 = Type:AccountUID，天然按 key 去重。
func (n *Notifier) NotifyKeyUsage(keyID, name string, used, threshold int64) {
	if n == nil || n.cfg.WebhookURL == "" || threshold <= 0 {
		return
	}
	label := keyID
	if name != "" {
		label = name + "(" + keyID + ")"
	}
	n.send(Event{
		Type:       "key_usage",
		AccountUID: keyID,
		Reason:     "key=" + keyID + " used=" + itoa(used) + " threshold=" + itoa(threshold),
		Text:       "API Key「" + label + "」本月 token 用量 " + itoa(used) + " 已达阈值 " + itoa(threshold) + "（只告警不拦截）",
	})
}

func (n *Notifier) NotifyAccountDown(kind, uid, nick, reason string) {
	if n == nil || n.cfg.WebhookURL == "" {
		return
	}
	n.send(Event{
		Type:        kind,
		AccountUID:  uid,
		AccountNick: nick,
		Reason:      reason,
		Text:        "账号 " + nick + " " + kind + "：" + reason,
	})
}

// send 做去重检查并异步 POST。
// 去重键用完整 uid（内部，防 8 位前缀碰撞吞事件）；**外发 body 的 account_uid 截断为
// 前 8 位**（R2 日志脱敏口径：logfmt.UID8，webhook 下游可能被转存/转发，完整 uid 不外流）。
// 失败路径（网络错误 / non-2xx）删除去重登记——下一次同事件触发即可重试，避免
// 「登记了却从没发出去，整个去重窗口静默」；成功才保留登记。
func (n *Notifier) send(ev Event) {
	key := ev.Type + ":" + ev.AccountUID
	n.mu.Lock()
	if t, ok := n.last[key]; ok && time.Since(t) < n.cfg.DedupWindow {
		n.mu.Unlock()
		return
	}
	n.last[key] = time.Now()
	n.mu.Unlock()

	bodyEv := ev
	bodyEv.AccountUID = logfmt.UID8(ev.AccountUID)
	// 忽略 err：Event 仅含 string/int64 字段，json.Marshal 必然成功，不影响后续 POST（判据④）。
	body, _ := json.Marshal(bodyEv)
	go func() {
		resp, err := n.cli.Post(n.cfg.WebhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("WARN: [alerting] webhook post failed: %v", err)
			n.mu.Lock()
			delete(n.last, key)
			n.mu.Unlock()
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("WARN: [alerting] webhook non-2xx: %s", resp.Status)
			n.mu.Lock()
			delete(n.last, key)
			n.mu.Unlock()
		}
	}()
}

func itoa(n int64) string {
	// 避免引入 strconv 在热路径上的压力；告警路径不在热路径，直接用 strconv 也行。
	return int64toa(n)
}

func int64toa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
