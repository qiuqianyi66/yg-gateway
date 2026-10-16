// coverage_test.go 调度器核心路径覆盖补强（工程深度治理 Task 5，纯增量）。
//
// 覆盖重点：排程热改与 rearm 重算（Reconfigure / poke / Run）、批量派发与成长任务
// 回调（runBatch / SetGrowthHook）、迟到唤醒宽限与取消放弃（awaitWakeupGrace /
// sleepCtx）、余额后台循环（StartBalanceRefresh：周期 / 热改暂停 / 热改恢复 / ctx
// 取消退出）、连登管家（补签保连登 / 兑换已解锁档位 / 抽完 chances / 载荷裁剪）、
// 夜猫子窗口与差额门控，以及各遍历入口的账号过滤边界（无 refresh token / 无 access
// token / disabled / paused / global）。
//
// 确定性纪律：不依赖真实网络（httptest 桩；CN 与 global 的四个 base 全部指向桩，
// 门控失效也只会打到本地而非公网）、不依赖真实时间（夜猫子时钟注入、限速间隔调至
// 毫秒并在 t.Cleanup 还原）、不依赖 map 迭代顺序（断言全部用计数或集合）、
// 不靠 sleep 同步（轮询 + 期限，负数断言用「窗口内计数恒定」）。
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// ---------------------------------------------------------------------------
// 基础设施：上游桩 + 确定性辅助
// ---------------------------------------------------------------------------

// schedulerCoverStub 覆盖全部任务族端点的上游桩：
// growth 域（streak / heatmap / redeem / makeup-cards / lottery / tasks / buddy）、
// billing 域（daily-checkin / get-user-resource / claim-gift / claim-compensation /
// token refresh）、chat 域（/v2/chat/completions）。行为由字段驱动，调用逐条落账
// （路径计数 + 关键请求参数原文），断言据此取证而不是「调用后没 panic」。
type schedulerCoverStub struct {
	mu          sync.Mutex
	cnt         map[string]int
	redeemTiers []string // POST /activity/growth/redeem 的 tier 原文（按到达顺序）
	makeupDates []string // POST .../makeup-cards/use 的 target_date 原文
	draws       int      // POST .../lottery/draw 次数

	streakBody      string // GET /activity/growth/streak 的响应原文（空 → 内置骨架）
	streakStatus    int    // >0 时 streak 返回该 HTTP 状态（模拟查询失败）
	lotteryStatus   int    // >0 时 lottery summary 返回该 HTTP 状态
	drawStatus      int    // >0 时每次抽奖都返回该 HTTP 状态
	redeemFailTier  string // 该档位兑换返回 400（模拟未解锁 403 之外的业务失败）
	makeupStatus    int    // >0 时补签接口返回该 HTTP 状态
	heatmapMissed   bool   // true → heatmap 把「昨天」标成漏签（score==0）
	chances         int    // lottery summary 返回的可抽次数
	tasksFailUID    string // 该 UID 的任务表查询返回 400
	blackcatTarget  int64  // black_cat 任务目标次数
	blackcatCurrent int64  // black_cat 当前进度
	blackcatClaimed bool   // black_cat 是否已领取
	checkinBody     string // daily-checkin 响应原文（空 → code 0 成功信封）
	chatStatus      int    // >0 时 chat 返回该状态（夜猫子对话失败）
	resourceRemain  int64  // get-user-resource 的 CycleCapacityRemain
	resourceFailUID string // 该 UID 的 get-user-resource 返回 400（非瞬时错误：不触发有界重试）

	pool *pool.Pool
}

// schedulerCoverAuth 令牌齐全的 CN 个人账号（可进入全部任务族）。
func schedulerCoverAuth(uid string) *auth.Auth {
	return &auth.Auth{UID: uid, AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
}

// schedulerStreakJSON 拼 GET /activity/growth/streak 的固定响应（JSON 字面量，
// 不含被测代码算出的任何值）：days 连登天数、makeupBalance 补签卡余额、
// 三档状态、tiers 列表（每档带 credit/energy/cards/chances）。
func schedulerStreakJSON(days, makeupBalance int, status7, status14, status28 string, tiers ...string) string {
	items := make([]string, 0, len(tiers))
	for _, tier := range tiers {
		items = append(items, fmt.Sprintf(
			`{"tier":%q,"days":7,"credit":100,"energy":5,"cards":1,"chances":2}`, tier))
	}
	return fmt.Sprintf(
		`{"code":0,"msg":"ok","data":{"streak":{"days":%d},`+
			`"makeup_cards":{"balance":%d,"max":3},`+
			`"redemption_status":{"tier_7d_status":%q,"tier_14d_status":%q,"tier_28d_status":%q,`+
			`"remaining_days":0,"tiers":[%s]}}}`,
		days, makeupBalance, status7, status14, status28, strings.Join(items, ","))
}

func (s *schedulerCoverStub) count(path string) {
	s.mu.Lock()
	s.cnt[path]++
	s.mu.Unlock()
}

func (s *schedulerCoverStub) recordRedeem(tier string) {
	s.mu.Lock()
	s.redeemTiers = append(s.redeemTiers, tier)
	s.mu.Unlock()
}

func (s *schedulerCoverStub) recordMakeup(date string) {
	s.mu.Lock()
	s.makeupDates = append(s.makeupDates, date)
	s.mu.Unlock()
}

func (s *schedulerCoverStub) recordDraw() {
	s.mu.Lock()
	s.draws++
	s.mu.Unlock()
}

// hits 命中该子串的路径调用总数（一个 tick 内可并发，故加锁）。
func (s *schedulerCoverStub) hits(sub string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for p, c := range s.cnt {
		if strings.Contains(p, sub) {
			n += c
		}
	}
	return n
}

func (s *schedulerCoverStub) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.cnt {
		n += c
	}
	return n
}

// snapshot 以 "路径×次数" 形式导出全部落账（失败信息用，便于一眼看出泄漏到哪个端点）。
func (s *schedulerCoverStub) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := make([]string, 0, len(s.cnt))
	for p, c := range s.cnt {
		parts = append(parts, fmt.Sprintf("%s×%d", p, c))
	}
	return strings.Join(parts, " ")
}

func (s *schedulerCoverStub) redeemedTiers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.redeemTiers))
	copy(out, s.redeemTiers)
	return out
}

func (s *schedulerCoverStub) makeupTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.makeupDates))
	copy(out, s.makeupDates)
	return out
}

func (s *schedulerCoverStub) drawCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draws
}

func (s *schedulerCoverStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.count(r.URL.Path)
		switch {
		// ---- billing 域（BillingBaseCN）----
		case strings.HasSuffix(r.URL.Path, "/daily-checkin"):
			if s.checkinBody != "" {
				w.Write([]byte(s.checkinBody))
				return
			}
			w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		case strings.HasSuffix(r.URL.Path, "/get-user-resource"):
			if s.resourceFailUID != "" && r.Header.Get("X-User-Id") == s.resourceFailUID {
				// 400 + 无计费关键词 → 分类为 ErrClient（非瞬时），不触发有界重试。
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"code":400,"msg":"invalid param"}`))
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"Response":{"Data":{"Accounts":`+
				`[{"CycleCapacitySize":1000,"CycleCapacityRemain":%d,"CycleCapacityUsed":0}]}}}}`,
				s.resourceRemain)
		case strings.HasSuffix(r.URL.Path, "/claim-gift"), strings.HasSuffix(r.URL.Path, "/claim-compensation"):
			// 已领过 → 业务错误：streakBonusAccount 应静默跳过（不打日志、不中断）。
			w.Write([]byte(`{"code":1,"msg":"already claimed"}`))
		case strings.HasSuffix(r.URL.Path, "/token/refresh"):
			w.Write([]byte(`{"code":0,"data":{"accessToken":"new","expiresIn":3600}}`))

		// ---- growth 域（ChatBaseCN）----
		case strings.HasSuffix(r.URL.Path, "/activity/growth/streak"):
			if s.streakStatus >= 400 {
				w.WriteHeader(s.streakStatus)
				w.Write([]byte(`{"code":500,"msg":"boom"}`))
				return
			}
			if s.streakBody != "" {
				w.Write([]byte(s.streakBody))
				return
			}
			w.Write([]byte(schedulerStreakJSON(3, 0, "locked", "locked", "locked", "7d", "14d", "28d")))
		case strings.HasSuffix(r.URL.Path, "/activity/growth/heatmap"):
			yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
			score := 1
			if s.heatmapMissed {
				score = 0
			}
			fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"cells":[{"date":%q,"score":%d}]}}`,
				yesterday, score)
		case strings.HasSuffix(r.URL.Path, "/activity/growth/redeem"):
			var body struct {
				Tier string `json:"tier"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.recordRedeem(body.Tier)
			if s.redeemFailTier != "" && body.Tier == s.redeemFailTier {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"code":400,"msg":"invalid param"}`))
				return
			}
			w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		case strings.HasSuffix(r.URL.Path, "/activity/growth/makeup-cards/use"):
			var body struct {
				TargetDate string `json:"target_date"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.recordMakeup(body.TargetDate)
			if s.makeupStatus >= 400 {
				w.WriteHeader(s.makeupStatus)
				w.Write([]byte(`{"code":400,"msg":"invalid param"}`))
				return
			}
			w.Write([]byte(`{"code":0,"msg":"ok","data":{}}`))
		case strings.HasSuffix(r.URL.Path, "/activity/growth/lottery/summary"):
			if s.lotteryStatus >= 400 {
				w.WriteHeader(s.lotteryStatus)
				w.Write([]byte(`{"code":500,"msg":"boom"}`))
				return
			}
			fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"chances":%d}}`, s.chances)
		case strings.HasSuffix(r.URL.Path, "/activity/growth/lottery/draw"):
			s.recordDraw() // 记「尝试」：失败也要落账，才能断言失败后不再继续抽
			if s.drawStatus >= 400 {
				w.WriteHeader(s.drawStatus)
				w.Write([]byte(`{"code":400,"msg":"invalid param"}`))
				return
			}
			w.Write([]byte(`{"code":0,"msg":"ok","data":{"prize":"x"}}`))
		case strings.HasSuffix(r.URL.Path, "/activity/growth/tasks"):
			if s.tasksFailUID != "" && r.Header.Get("X-User-Id") == s.tasksFailUID {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"code":400,"msg":"invalid param"}`))
				return
			}
			fmt.Fprintf(w, `{"code":0,"msg":"ok","data":{"tasks":`+
				`[{"task_code":"black_cat","target":%d,"current":%d,"accept_status":%q}]}}`,
				s.blackcatTarget, s.blackcatCurrent, schedulerAcceptStatus(s.blackcatClaimed))
		case strings.HasSuffix(r.URL.Path, "/activity/growth/buddy/info"):
			// 已有猫 → 不进领养链路，直接走 travel/status 状态机。
			w.Write([]byte(`{"code":0,"msg":"ok","data":{"buddy":{"id":1,"name":"cat"}}}`))
		case strings.HasSuffix(r.URL.Path, "/activity/growth/buddy/travel/status"):
			// daily_limit_reached=true → 状态查询即止，不发 depart/claim。
			w.Write([]byte(`{"code":0,"msg":"ok","data":{"state":"idle","daily_limit_reached":true}}`))

		// ---- report / chat 域 ----
		case strings.HasSuffix(r.URL.Path, "/v2/report"):
			w.Write([]byte(`{"code":0,"msg":"OK"}`))
		case strings.HasSuffix(r.URL.Path, "/v2/chat/completions"):
			if s.chatStatus >= 400 {
				w.WriteHeader(s.chatStatus)
				w.Write([]byte(`{"code":500,"msg":"boom"}`))
				return
			}
			w.Write([]byte("data: {\"choices\":[]}\n\n"))
		default:
			http.Error(w, "not found", 404)
		}
	})
}

func schedulerAcceptStatus(claimed bool) string {
	if claimed {
		return "claimed"
	}
	return ""
}

// schedulerNewCoverStub 起桩并构造指向它的调度器。
// CN 与 global 的四个 base 全部指向桩：即便某道 realm 门控失效，用例也只会打到
// 本地桩（计数可断言），不会逃逸到真实上游。
func schedulerNewCoverStub(t *testing.T, accounts ...*auth.Auth) (*schedulerCoverStub, *Scheduler) {
	t.Helper()
	stub := &schedulerCoverStub{cnt: map[string]int{}, resourceRemain: 700}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	p := pool.New("")
	for _, a := range accounts {
		p.Add(a)
	}
	stub.pool = p

	up := &upstream.Client{
		HTTP:              srv.Client(),
		ChatHTTP:          srv.Client(),
		ChatBaseCN:        srv.URL,
		BillingBaseCN:     srv.URL,
		WebBaseCN:         srv.URL,
		ChatBaseGlobal:    srv.URL,
		BillingBaseGlobal: srv.URL,
	}
	return stub, New(Config{Pool: p, Upstream: up})
}

// schedulerFastDelays 把账号间限速与领养前置等待调成 0（测试可置 0 是生产注释的
// 既定口径），t.Cleanup 还原。
func schedulerFastDelays(t *testing.T) {
	t.Helper()
	oldTravel, oldActivity, oldAdopt := travelAccountDelay, activityAccountDelay, adoptReportGap
	travelAccountDelay, activityAccountDelay, adoptReportGap = 0, 0, 0
	t.Cleanup(func() {
		travelAccountDelay, activityAccountDelay, adoptReportGap = oldTravel, oldActivity, oldAdopt
	})
}

// schedulerPinNightWindow 把夜猫子窗口判定的时钟钉在给定时刻（窗口守卫先于账号
// 循环，不钉住时钟则用例的判别力取决于运行环境的墙钟）。
func schedulerPinNightWindow(t *testing.T, at time.Time) {
	t.Helper()
	old := nowForNightWindow
	nowForNightWindow = func() time.Time { return at }
	t.Cleanup(func() { nowForNightWindow = old })
}

// schedulerWaitUntil 轮询 cond 直到为真或超过 deadline（期限远大于预期耗时，
// 不靠 sleep 断言同步）。
func schedulerWaitUntil(deadline time.Duration, cond func() bool) bool {
	limit := time.Now().Add(deadline)
	for time.Now().Before(limit) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// schedulerStableFor 断言 get() 在 d 窗口内恒等于 want（负数断言：泄漏的循环 /
// 忙等 / 未暂停都会持续改变计数）。
func schedulerStableFor(d time.Duration, get func() int, want int) bool {
	limit := time.Now().Add(d)
	for time.Now().Before(limit) {
		if get() != want {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return get() == want
}

// schedulerDoneWithin 观察 done 是否在 d 内关闭（用于「必须仍在运行」的负数断言）。
func schedulerDoneWithin(done <-chan struct{}, d time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// ---------------------------------------------------------------------------
// 唤醒宽限：迟到补跑 / 准点零延迟 / ctx 取消放弃本批（issue #152）
// ---------------------------------------------------------------------------

// TestAwaitWakeupGraceZeroDelayWhenOnTime 准点触发（墙钟未越过时点）必须零延迟
// 放行：宽限是给 Windows Modern Standby 唤醒后的 DNS 恢复用的，不是给准点时点的。
func TestAwaitWakeupGraceZeroDelayWhenOnTime(t *testing.T) {
	old := wakeupGraceDelay
	wakeupGraceDelay = 400 * time.Millisecond // 远大于断言阈值：真等满必然失败
	t.Cleanup(func() { wakeupGraceDelay = old })

	start := time.Now()
	if !awaitWakeupGrace(context.Background(), time.Now()) {
		t.Fatal("准点触发应放行派发")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("准点触发被宽限了 %v（宽限只应作用于迟到补跑）", d)
	}
}

// TestAwaitWakeupGraceLateCatchupWaitsForNetwork 迟到补跑（墙钟已越过时点超过
// 阈值）必须先等满网络宽限再派发：唤醒瞬间 DNS 未就绪，零宽限派发等于把唯一一次
// 补跑机会打在注定失败的窗口里。
func TestAwaitWakeupGraceLateCatchupWaitsForNetwork(t *testing.T) {
	old := wakeupGraceDelay
	wakeupGraceDelay = 60 * time.Millisecond
	t.Cleanup(func() { wakeupGraceDelay = old })

	planned := time.Now().Add(-10 * time.Second) // 睡眠跨过槽位：迟到 10s
	start := time.Now()
	if !awaitWakeupGrace(context.Background(), planned) {
		t.Fatal("迟到的等价条件（ctx 未取消）应放行")
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("迟到补跑只等了 %v（< 宽限 60ms）：唤醒后的网络就绪窗口被跳过", d)
	}
}

// TestAwaitWakeupGraceCanceledCtxAbandonsBatch 停机时不再等宽限睡满：ctx 取消
// 立即返回 false（本批放弃，下轮 nextWalk 从「现在」起算）。
func TestAwaitWakeupGraceCanceledCtxAbandonsBatch(t *testing.T) {
	old := wakeupGraceDelay
	wakeupGraceDelay = 5 * time.Second // 若忽略 ctx，用例会白等 5s 并被阈值抓住
	t.Cleanup(func() { wakeupGraceDelay = old })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if awaitWakeupGrace(ctx, time.Now().Add(-10*time.Second)) {
		t.Error("ctx 已取消时应返回 false（放弃本批）")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("取消后仍等了 %v（优雅停机被宽限阻塞）", d)
	}
}

// TestSleepCtxZeroAndCanceledCtx sleepCtx 的两个早期出口：d<=0 立即放行、但
// ctx 已取消时即便 d<=0 也必须返回 false（停机不再派发新工作）。
func TestSleepCtxZeroAndCanceledCtx(t *testing.T) {
	if !sleepCtx(context.Background(), 0) {
		t.Error("d<=0 且 ctx 存活应返回 true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepCtx(ctx, 0) {
		t.Error("d<=0 但 ctx 已取消应返回 false")
	}
	if !sleepCtx(context.Background(), 20*time.Millisecond) {
		t.Error("等满时长应返回 true")
	}
}

// TestSleepCtxCancelInterruptsWait 等待中途取消必须立即返回 false，不等满时长。
func TestSleepCtxCancelInterruptsWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if sleepCtx(ctx, 5*time.Second) {
		t.Error("取消时应返回 false")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("取消响应耗时 %v（应远小于 5s 的等待时长）", d)
	}
}

// ---------------------------------------------------------------------------
// 排程热改：Reconfigure / poke / Run 的 rearm 重算
// ---------------------------------------------------------------------------

// TestReconfigureAppliesHoursAndNotifiesRearm 面板保存配置 → Reconfigure 必须
// 让新时点/新开关对 nextWake 立即生效，并同时通知 Run 与余额循环重算。
func TestReconfigureAppliesHoursAndNotifiesRearm(t *testing.T) {
	s := New(Config{
		CheckinHours:   []int{9},
		TravelHours:    []int{9},
		ActivityHours:  []int{10},
		KeepaliveHours: []int{22},
		BlackcatHours:  []int{23},
	})
	now := time.Date(2026, 9, 11, 8, 30, 0, 0, time.Local)

	// 前置：签到与旅行配到 09:00 同一槽位。
	at, kinds := s.nextWake(now)
	if want := time.Date(2026, 9, 11, 9, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Fatalf("前置 nextWake=%v want %v", at, want)
	}
	if !hasKind(kinds, taskCheckin) || !hasKind(kinds, taskTravel) {
		t.Fatalf("前置 kinds=%v want checkin+travel", kinds)
	}

	// 热改：签到 → 20:00，关闭旅行，活跃 → 11:00；nil hours = 未配置，保留原值。
	// Reconfigure(checkin, travel, activity, keepalive, blackcat, growth, 6×disabled)
	s.Reconfigure([]int{20}, nil, []int{11}, []int{22}, []int{23}, []int{1},
		false, true, false, false, false, false)

	at, kinds = s.nextWake(now)
	if want := time.Date(2026, 9, 11, 11, 0, 0, 0, time.Local); !at.Equal(want) {
		t.Errorf("热改后 nextWake=%v want %v（活跃 11:00 应为最近时点）", at, want)
	}
	if hasKind(kinds, taskTravel) {
		t.Errorf("旅行已热改成禁用却仍在候选：kinds=%v", kinds)
	}
	if !hasKind(kinds, taskActivity) {
		t.Errorf("活跃 11:00 不在候选：kinds=%v", kinds)
	}

	// 19:00 时最近时点必须是热改后的签到 20:00；若 nil hours 被当成「清空」，
	// 签到时点会消失（nextFire 对空 hours 返回零值）→ 下一个时点落到 22:00。
	at2, kinds2 := s.nextWake(time.Date(2026, 9, 11, 19, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 11, 20, 0, 0, 0, time.Local); !at2.Equal(want) {
		t.Errorf("热改后签到时点=%v want %v（nil hours 应保留 20:00，不得清空回默认）", at2, want)
	}
	if !hasKind(kinds2, taskCheckin) {
		t.Errorf("kinds=%v 缺 checkin", kinds2)
	}

	// 两条 rearm 通知各入队一次（Run 与余额循环各自消费，同 channel 会丢信号）。
	select {
	case <-s.rearmSchedule:
	default:
		t.Error("Reconfigure 未通知排程重算（rearmSchedule 无待处理信号）")
	}
	select {
	case <-s.rearmBalance:
	default:
		t.Error("Reconfigure 未通知余额循环重算（rearmBalance 无待处理信号）")
	}

	// 反向热改：重新启用旅行并把时点提前到 07:00 —— 开关与 hours 必须同时生效。
	s.Reconfigure(nil, []int{7}, []int{11}, []int{22}, []int{23}, []int{1},
		false, false, false, false, false, false)
	at3, kinds3 := s.nextWake(time.Date(2026, 9, 12, 6, 0, 0, 0, time.Local))
	if want := time.Date(2026, 9, 12, 7, 0, 0, 0, time.Local); !at3.Equal(want) {
		t.Errorf("重新启用旅行后 nextWake=%v want %v", at3, want)
	}
	if !hasKind(kinds3, taskTravel) {
		t.Errorf("kinds=%v 缺 travel（重新启用后应回到候选）", kinds3)
	}
}

// TestRunKeepsRunningAfterReconfigureAndExitsOnCancel 主循环收到 rearm 后必须
// **重算并继续运行**，而不是接到重排信号就退出；ctx 取消后必须返回（goroutine 回收）。
func TestRunKeepsRunningAfterReconfigureAndExitsOnCancel(t *testing.T) {
	// 六类任务全禁用 → nextWake 零值 → Run 只等 rearm / 退出信号（不空转）。
	s := New(Config{
		CheckinDisabled:   true,
		TravelDisabled:    true,
		ActivityDisabled:  true,
		KeepaliveDisabled: true,
		BlackcatDisabled:  true,
		GrowthDisabled:    true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	// 重新启用全部任务（hours 保留默认 → 时点均在远未来）→ rearm 唤醒重算。
	// 若 Run 把 rearm 当成退出信号，下一行断言就会看到 done 被关闭。
	s.Reconfigure(nil, nil, nil, nil, nil, nil,
		false, false, false, false, false, false)
	if schedulerDoneWithin(done, 200*time.Millisecond) {
		t.Fatal("Run 收到 rearm 后退出（应重算下一次唤醒并继续等待）")
	}

	cancel()
	if !schedulerDoneWithin(done, 2*time.Second) {
		t.Fatal("Run 未在 ctx 取消后返回：主循环 goroutine 泄漏")
	}
}

// TestRunRepokesWhenNothingScheduled 六类任务全禁用（无可等时点）时，重排信号
// 必须让 Run 重算后**继续等待**，而不是退出。信号在启动前预置，保证第一次进入
// 「无时点」分支就消费到它（与在线改配置重新启用任务同语义，不依赖调度时序）。
func TestRunRepokesWhenNothingScheduled(t *testing.T) {
	s := New(Config{
		CheckinDisabled:   true,
		TravelDisabled:    true,
		ActivityDisabled:  true,
		KeepaliveDisabled: true,
		BlackcatDisabled:  true,
		GrowthDisabled:    true,
	})
	s.rearmSchedule <- struct{}{} // 预置信号（缓冲 1，不会阻塞）

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	if schedulerDoneWithin(done, 200*time.Millisecond) {
		t.Fatal("收到重排信号后 Run 退出（应重算下一次唤醒并继续等待）")
	}
	cancel()
	if !schedulerDoneWithin(done, 2*time.Second) {
		t.Fatal("Run 未在 ctx 取消后返回")
	}
}

// ---------------------------------------------------------------------------
// 批量派发与成长任务回调
// ---------------------------------------------------------------------------

// TestRunBatchDispatchesEveryTaskKind 一个唤醒时刻的六类任务必须全部被派发，
// 且返回前等全部收尾（每类用「该任务族独有的上游路径」取证）。
func TestRunBatchDispatchesEveryTaskKind(t *testing.T) {
	schedulerFastDelays(t)
	schedulerPinNightWindow(t, time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local))
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))

	s.runBatch(context.Background(), []taskKind{
		taskCheckin, taskTravel, taskActivity, taskKeepalive, taskBlackcat, taskGrowth,
	})

	for _, c := range []struct{ kind, path string }{
		{"签到", "/daily-checkin"},
		{"旅行", "/activity/growth/buddy/info"},
		{"活跃上报", "/v2/report"},
		{"token 保活", "/token/refresh"},
		{"夜猫子", "/v2/activity/growth/tasks"},
	} {
		if n := stub.hits(c.path); n == 0 {
			t.Errorf("runBatch 未派发 %s（%s 调用数=0；实际落账=%s）", c.kind, c.path, stub.snapshot())
		}
	}
	if got := stub.hits("/v2/chat/completions"); got != 0 {
		t.Errorf("夜猫子 chat 调用=%d want 0（black_cat 已完成 → need<=0 不应发对话）", got)
	}
}

// TestSetGrowthHookReplacesAndNilHookIsSkipped 成长任务回调的挂载/热替换/卸载：
// nil 时必须静默跳过（panel 未接线），不得 panic。
func TestSetGrowthHookReplacesAndNilHookIsSkipped(t *testing.T) {
	s := New(Config{})
	var first, second atomic.Int32

	s.SetGrowthHook(func() { first.Add(1) })
	s.runBatch(context.Background(), []taskKind{taskGrowth})
	if got := first.Load(); got != 1 {
		t.Fatalf("已挂载回调调用次数=%d want 1", got)
	}

	// 热替换（SetGrowthHook 可任意时刻调用）：新回调生效、旧的立刻退场。
	s.SetGrowthHook(func() { second.Add(1) })
	s.runBatch(context.Background(), []taskKind{taskGrowth})
	if first.Load() != 1 || second.Load() != 1 {
		t.Errorf("替换后 first=%d second=%d want 1/1", first.Load(), second.Load())
	}

	// 卸载（nil）→ 跳过而非 nil 调用 panic。
	s.SetGrowthHook(nil)
	s.runBatch(context.Background(), []taskKind{taskGrowth})
	if got := second.Load(); got != 1 {
		t.Errorf("卸载后旧回调仍被调用：second=%d want 1", got)
	}
}

// ---------------------------------------------------------------------------
// 余额后台循环：周期 / 热改暂停 / 热改恢复 / ctx 取消退出（R9）
// ---------------------------------------------------------------------------

// TestStartBalanceRefreshPeriodicPauseResumeCancel 余额循环的完整生命周期：
// 按间隔周期刷新 → 热改 0 暂停（不再产生任何上游调用）→ 热改恢复（rearm 唤醒后
// 立即按新间隔刷新）→ ctx 取消后彻底停止（循环唯一的可观测副作用就是刷新请求，
// 取消后计数恒定即证 goroutine 已退出，不存在空转泄漏）。
func TestStartBalanceRefreshPeriodicPauseResumeCancel(t *testing.T) {
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	budget := func() int { return stub.hits("/get-user-resource") }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.StartBalanceRefresh(ctx, 15*time.Millisecond)

	if !schedulerWaitUntil(3*time.Second, func() bool { return budget() >= 2 }) {
		t.Fatalf("余额后台刷新未按间隔生效：刷新次数=%d（实际落账=%s）", budget(), stub.snapshot())
	}

	// 热改暂停：面板关闭 balance_refresh 时置 0，循环应停在暂停分支。
	s.SetBalanceInterval(0)
	paused := budget()
	if !schedulerStableFor(150*time.Millisecond, budget, paused) {
		t.Errorf("SetBalanceInterval(0) 后仍在刷新：%d → %d（暂停未生效）", paused, budget())
	}

	// 热改恢复：暂停分支靠 rearmBalance 唤醒，恢复后必须按新间隔继续刷新。
	s.SetBalanceInterval(10 * time.Millisecond)
	if !schedulerWaitUntil(3*time.Second, func() bool { return budget() > paused }) {
		t.Errorf("恢复间隔后未重新刷新：暂停前=%d 现在=%d", paused, budget())
	}

	// ctx 取消 → 循环退出（R9 goroutine 回收）。
	cancel()
	after := budget()
	if !schedulerStableFor(150*time.Millisecond, budget, after) {
		t.Errorf("ctx 取消后仍在刷新：%d → %d（循环未退出）", after, budget())
	}
}

// TestStartBalanceRefreshNonPositiveIntervalStartsNoLoop interval<=0 时不得启动
// 循环（main 侧关闭开关即不调用；即便调了也应直接返回）。判别方式：不启动就不会
// 有 goroutine 消费 SetBalanceInterval 的 rearm 信号 → 后续热改不会产生任何刷新。
func TestStartBalanceRefreshNonPositiveIntervalStartsNoLoop(t *testing.T) {
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))

	s.StartBalanceRefresh(context.Background(), 0)
	s.SetBalanceInterval(10 * time.Millisecond) // 若循环已启动，它会被唤醒并立刻刷新

	if !schedulerStableFor(150*time.Millisecond, func() int { return stub.hits("/get-user-resource") }, 0) {
		t.Errorf("interval<=0 仍启动了余额循环：刷新次数=%d（实际落账=%s）",
			stub.hits("/get-user-resource"), stub.snapshot())
	}
}

// TestSetBalanceIntervalNegativePausesInsteadOfSpinning 负间隔必须钳成「暂停」，
// 而不是把负值塞给 time.NewTimer（已到期的 timer 会立刻触发 → 无间隔忙等打上游）。
func TestSetBalanceIntervalNegativePausesInsteadOfSpinning(t *testing.T) {
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	budget := func() int { return stub.hits("/get-user-resource") }

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.StartBalanceRefresh(ctx, 20*time.Millisecond)
	if !schedulerWaitUntil(3*time.Second, func() bool { return budget() >= 2 }) {
		t.Fatalf("前置：余额循环未刷新，计数=%d", budget())
	}

	s.SetBalanceInterval(-5 * time.Second)
	n := budget()
	if !schedulerStableFor(150*time.Millisecond, budget, n) {
		t.Errorf("负间隔未被钳成暂停：%d → %d（疑似忙等刷上游）", n, budget())
	}
}

// ---------------------------------------------------------------------------
// 签到 / 保活 / 余额遍历的账号过滤与错误路径
// ---------------------------------------------------------------------------

// TestRunActivityDispatchStopsOnCanceledCtx 排程主循环调用的 runActivity 在 ctx
// 取消后必须放弃剩余账号（不等账号间限速睡满、不继续上报），等下一个时点再补。
func TestRunActivityDispatchStopsOnCanceledCtx(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"), schedulerCoverAuth("u2"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 停机信号先到：首个账号仍处理，之后必须立即退出

	s.runActivity(ctx)

	if got := stub.hits("/v2/report"); got != 1 {
		t.Errorf("上报次数=%d want 1（ctx 已取消 → 首个账号之后不得再上报，实际落账=%s）",
			got, stub.snapshot())
	}
}

// TestRunCheckinSkipsNoRefreshTokenAndAlreadyCheckinIsIdempotent 两条既有语义：
// 无 refresh token 的账号不签到；上游「今天已签到」是幂等成功（记为本日已签到、
// 且**继续**走余额查询，不当失败短路）。
func TestRunCheckinSkipsNoRefreshTokenAndAlreadyCheckinIsIdempotent(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t,
		schedulerCoverAuth("u1"),
		&auth.Auth{UID: "nort", AccessToken: "at", ExpiresAt: 9999999999}, // 无 refresh token
	)
	stub.checkinBody = `{"code":10001,"msg":"今天已签到"}`

	s.RunCheckinNow()

	if got := stub.hits("/daily-checkin"); got != 1 {
		t.Errorf("签到调用=%d want 1（无 refresh token 的账号必须跳过）", got)
	}
	if got := stub.hits("/get-user-resource"); got != 1 {
		t.Errorf("余额查询=%d want 1（幂等「已签到」后仍须刷新余额）", got)
	}
	st, ok := stub.pool.Status("u1")
	if !ok {
		t.Fatal("u1 status missing")
	}
	if !st.CheckinDone {
		t.Errorf("「今天已签到」应记为本日已签到（幂等成功），status=%+v", st)
	}
}

// TestRunKeepaliveSkipsDisabledAndNoRefreshToken 保活遍历的两条过滤：禁用账号
// 默认不续期（include_disabled_in_tasks 关闭时），无 refresh token 的账号不打上游。
func TestRunKeepaliveSkipsDisabledAndNoRefreshToken(t *testing.T) {
	stub, s := schedulerNewCoverStub(t,
		schedulerCoverAuth("ok"),
		&auth.Auth{UID: "nort", AccessToken: "at", ExpiresAt: 9999999999},
		schedulerCoverAuth("dis"),
	)
	stub.pool.Disable("dis", "manual disable (test)")

	s.RunKeepaliveNow()

	if got := stub.hits("/token/refresh"); got != 1 {
		t.Errorf("refresh 调用=%d want 1（仅 ok 账号；禁用号与无 refresh token 号不得续期）", got)
	}
	if st, _ := stub.pool.Status("ok"); st.Disabled {
		t.Errorf("续期成功后账号不应被禁用: %+v", st)
	}
}

// TestRunBalanceRefreshErrorKeepsSnapshotAndOtherAccounts 单账号余额查询失败
// （400，非瞬时）时：该号**已有的到期快照必须原样保留**（不得被零值覆盖），
// 且不影响同批其他账号的刷新。
func TestRunBalanceRefreshErrorKeepsSnapshotAndOtherAccounts(t *testing.T) {
	expiry := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("bad"), schedulerCoverAuth("good"))
	stub.resourceFailUID = "bad"
	stub.pool.SetCreditsDetailed("bad", 42, 100, 7, expiry, 7)

	s.RunBalanceRefreshNow()

	bad, _ := stub.pool.Status("bad")
	if bad.Credits != 42 || bad.CreditsExpiring != 7 || bad.CreditsEarliestRemaining != 7 ||
		!bad.CreditsEarliestExpiry.Equal(expiry) {
		t.Errorf("查询失败的账号快照被改写：credits=%d expiring=%d earliest=%d@%v want 42/7/7@%v",
			bad.Credits, bad.CreditsExpiring, bad.CreditsEarliestRemaining, bad.CreditsEarliestExpiry, expiry)
	}
	good, _ := stub.pool.Status("good")
	if good.Credits != 700 {
		t.Errorf("good 账号 credits=%d want 700（单号失败不应影响同批刷新）", good.Credits)
	}
}

// TestRunTravelSkipsNoRefreshTokenAndGlobal 旅行遍历的两条过滤：无 refresh token
// 的账号不进状态机；global 账号无 CN 猫猫体系，不得发任何上报/状态请求。
func TestRunTravelSkipsNoRefreshTokenAndGlobal(t *testing.T) {
	withGlobalSwitch(t)
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t,
		schedulerCoverAuth("cn"),
		&auth.Auth{UID: "nort", AccessToken: "at", ExpiresAt: 9999999999},
		globalAccount(t, "g1"),
	)

	s.RunTravelNow()

	if got := stub.hits("/activity/growth/buddy/info"); got != 1 {
		t.Errorf("buddy/info 调用=%d want 1（应只有 cn 账号；实际落账=%s）", got, stub.snapshot())
	}
	if got := stub.hits("/v2/report"); got != 0 {
		t.Errorf("报告调用=%d want 0（跳过 global 就不得有领养前置上报）", got)
	}
}

// ---------------------------------------------------------------------------
// 连登管家：补签 → 兑换 → 抽奖
// ---------------------------------------------------------------------------

// TestRunStreakBonusRedeemsUnlockedTiersAndDrawsAllChances 兑换只对「已解锁未领取」
// 档位发起（locked / claimed 一律不发），抽奖按 chances 全抽完。
func TestRunStreakBonusRedeemsUnlockedTiersAndDrawsAllChances(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.streakBody = schedulerStreakJSON(30, 0, "claimed", "locked", "redeemable", "7d", "14d", "28d")
	stub.chances = 3

	s.RunStreakBonusNow()

	if got := strings.Join(stub.redeemedTiers(), ","); got != "28d" {
		t.Errorf("兑换档位=%q want %q（claimed 的 7d 与 locked 的 14d 都不得兑换）", got, "28d")
	}
	if got := stub.drawCount(); got != 3 {
		t.Errorf("抽奖次数=%d want 3（按 chances 抽完）", got)
	}
}

// TestRunStreakBonusSkipsAccountWithoutAccessToken 无 access token 的账号连
// heatmap/streak 都不查（0 次上游调用）。
func TestRunStreakBonusSkipsAccountWithoutAccessToken(t *testing.T) {
	stub, s := schedulerNewCoverStub(t, &auth.Auth{UID: "noat", RefreshToken: "rt"})

	s.RunStreakBonusNow()

	if got := stub.total(); got != 0 {
		t.Errorf("无 access token 的账号产生了 %d 次上游调用（want 0）：%s", got, stub.snapshot())
	}
}

// TestRunStreakBonusStreakErrorSkipsRedeemAndDraw 连登状态查询失败必须整轮中止：
// 既不知道哪些档位解锁，也不该抽奖。
func TestRunStreakBonusStreakErrorSkipsRedeemAndDraw(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.streakStatus = 500
	stub.chances = 3

	s.RunStreakBonusNow()

	if got := stub.hits("/activity/growth/redeem"); got != 0 {
		t.Errorf("状态查询失败仍发起兑换 %d 次（不能让 nil 状态当「全部可兑」）", got)
	}
	if got := stub.drawCount(); got != 0 {
		t.Errorf("状态查询失败仍抽奖 %d 次 want 0", got)
	}
	if got := stub.hits("/activity/growth/streak"); got != 1 {
		t.Errorf("连登状态查询=%d want 1", got)
	}
}

// TestRunStreakBonusLotteryErrorDrawsNothing 抽奖次数查询失败时不抽奖（错误不被
// 静默当作「有次数」继续）。
func TestRunStreakBonusLotteryErrorDrawsNothing(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.streakBody = schedulerStreakJSON(30, 0, "redeemable", "locked", "locked", "7d")
	stub.lotteryStatus = 500
	stub.chances = 5

	s.RunStreakBonusNow()

	if got := stub.hits("/activity/growth/lottery/summary"); got != 1 {
		t.Errorf("抽奖次数查询=%d want 1", got)
	}
	if got := stub.drawCount(); got != 0 {
		t.Errorf("次数未知却抽奖 %d 次 want 0", got)
	}
	// 兑换在抽奖之前，不受抽奖查询失败影响（部分完成仍保留）。
	if got := strings.Join(stub.redeemedTiers(), ","); got != "7d" {
		t.Errorf("兑换档位=%q want %q", got, "7d")
	}
}

// TestMakeupYesterdayUsesYesterdayWhenCardAvailable 昨日漏签 + 有补签卡 → 必须对
// **昨天**（自然日）用卡：补到今天会占掉当天签到，连登反而断。
func TestMakeupYesterdayUsesYesterdayWhenCardAvailable(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.heatmapMissed = true
	stub.streakBody = schedulerStreakJSON(30, 2, "locked", "locked", "locked", "7d")

	s.RunStreakBonusNow()

	got := stub.makeupTargets()
	if len(got) != 1 {
		t.Fatalf("补签调用=%d 次 want 1（有漏签 + 有卡就要补）：%v", len(got), got)
	}
	want := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	if got[0] != want {
		t.Errorf("补签日期=%q want %q（补的是昨天）", got[0], want)
	}
	if got[0] == time.Now().Format("2006-01-02") {
		t.Error("补签了今天：会占掉今天的签到，连登连续性反而被破坏")
	}
}

// TestMakeupYesterdayRequiresCardBalance 无补签卡（balance=0）时不得调用补签接口
// ——上游会返业务错误，白打一次请求。
func TestMakeupYesterdayRequiresCardBalance(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.heatmapMissed = true
	stub.streakBody = schedulerStreakJSON(30, 0, "locked", "locked", "locked") // 无卡

	s.RunStreakBonusNow()

	if got := stub.makeupTargets(); len(got) != 0 {
		t.Errorf("无补签卡却发起补签：%v", got)
	}
	if got := stub.hits("/activity/growth/heatmap"); got != 1 {
		t.Errorf("heatmap 查询=%d want 1（漏签判定仍要查）", got)
	}
}

// TestRunStreakBonusContinuesAfterRedeemFailure 单档位兑换失败（非 locked 的
// 业务错误）只跳过该档，后续档位照常尝试：否则一个失败档会挡住当天所有可兑换档位。
func TestRunStreakBonusContinuesAfterRedeemFailure(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.streakBody = schedulerStreakJSON(30, 0, "redeemable", "redeemable", "redeemable",
		"7d", "14d", "28d")
	stub.redeemFailTier = "14d"

	s.RunStreakBonusNow()

	if got := strings.Join(stub.redeemedTiers(), ","); got != "7d,14d,28d" {
		t.Errorf("兑换尝试序列=%q want %q（失败档不得中断后续档位）", got, "7d,14d,28d")
	}
}

// TestRunStreakBonusDrawFailureStopsDrawing 抽奖单次失败即中止（不连打剩余次数）：
// 失败多为上游拒绝/网络问题，连着打只会放大风控面。
func TestRunStreakBonusDrawFailureStopsDrawing(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.streakBody = schedulerStreakJSON(30, 0, "locked", "locked", "locked")
	stub.chances = 3
	stub.drawStatus = 400

	s.RunStreakBonusNow()

	if got := stub.drawCount(); got != 1 {
		t.Errorf("抽奖尝试=%d want 1（首抽失败即中止，不得连打剩余 2 次）", got)
	}
}

// TestMakeupYesterdayFailureStillRunsRedeemChain 补签失败（上游拒绝）只是少保一天
// 连登，不得中断整轮：后面的兑换/抽奖必须照常跑。
func TestMakeupYesterdayFailureStillRunsRedeemChain(t *testing.T) {
	schedulerFastDelays(t)
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.heatmapMissed = true
	stub.streakBody = schedulerStreakJSON(30, 2, "redeemable", "locked", "locked", "7d")
	stub.makeupStatus = 400
	stub.chances = 1

	s.RunStreakBonusNow()

	if got := len(stub.makeupTargets()); got != 1 {
		t.Errorf("补签尝试=%d want 1（有漏签 + 有卡就要试一次）", got)
	}
	if got := strings.Join(stub.redeemedTiers(), ","); got != "7d" {
		t.Errorf("补签失败后兑换档位=%q want %q（不应中断整轮）", got, "7d")
	}
	if got := stub.drawCount(); got != 1 {
		t.Errorf("补签失败后抽奖次数=%d want 1（不应中断整轮）", got)
	}
}

// TestCompactJSONTruncatesOnlyOverLimit 日志载荷裁剪的边界：正好 220 字节不动，
// 221 字节才截成 220 + 省略号。
func TestCompactJSONTruncatesOnlyOverLimit(t *testing.T) {
	exact := strings.Repeat("a", 220)
	if got := compactJSON(json.RawMessage(exact)); got != exact {
		t.Errorf("220 字节被改写：len=%d want 220（不该加省略号）", len(got))
	}
	long := strings.Repeat("b", 221)
	if got := compactJSON(json.RawMessage(long)); got != strings.Repeat("b", 220)+"…" {
		t.Errorf("221 字节裁剪结果=%q（want 220 个 b + …）", got)
	}
	short := `{"prize":"x"}`
	if got := compactJSON(json.RawMessage(short)); got != short {
		t.Errorf("短载荷被改写：%q", got)
	}
}

// ---------------------------------------------------------------------------
// 夜猫子：窗口守卫 / 账号过滤 / 差额门控
// ---------------------------------------------------------------------------

// TestRunBlackcatOutsideWindowMakesNoCalls 窗口外（本地 12:00）整任务跳过，
// 一个上游请求都不发（只观测不报错）。
func TestRunBlackcatOutsideWindowMakesNoCalls(t *testing.T) {
	schedulerPinNightWindow(t, time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local))
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.blackcatTarget, stub.blackcatCurrent = 3, 0

	s.RunBlackcatNow()

	if got := stub.total(); got != 0 {
		t.Errorf("窗口外产生了 %d 次上游调用（want 0）：%s", got, stub.snapshot())
	}
}

// TestRunBlackcatInWindowSkipsPausedDisabledAndNoNeed 窗口内的四道过滤：paused
// （让位防风控）、disabled、无 access token、global 都在查任务表之前退场；
// 差额为 0（任务已完成）时不发对话。
func TestRunBlackcatInWindowSkipsPausedDisabledAndNoNeed(t *testing.T) {
	withGlobalSwitch(t)
	schedulerFastDelays(t)
	schedulerPinNightWindow(t, time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local))
	stub, s := schedulerNewCoverStub(t,
		schedulerCoverAuth("need0"),
		schedulerCoverAuth("paused"),
		schedulerCoverAuth("dis"),
		&auth.Auth{UID: "noat", RefreshToken: "rt"},
		globalAccount(t, "g1"),
	)
	stub.pool.Pause("paused")
	stub.pool.Disable("dis", "manual disable (test)")
	stub.blackcatTarget, stub.blackcatCurrent = 3, 3 // 进度已达标 → need = 0

	s.RunBlackcatNow()

	if got := stub.hits("/activity/growth/tasks"); got != 1 {
		t.Errorf("任务表查询=%d want 1（只有 need0 账号可进入）：%s", got, stub.snapshot())
	}
	if got := stub.hits("/v2/chat/completions"); got != 0 {
		t.Errorf("差额为 0 却发起对话 %d 次 want 0", got)
	}
}

// TestRunBlackcatTaskListErrorDoesNotStopOtherAccounts 任务表查询失败只跳过该账号，
// 后续账号照常查询（一个号的上游抖动不得吃掉整批夜猫子）。
func TestRunBlackcatTaskListErrorDoesNotStopOtherAccounts(t *testing.T) {
	schedulerFastDelays(t)
	schedulerPinNightWindow(t, time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local))
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("bad"), schedulerCoverAuth("good"))
	stub.tasksFailUID = "bad" // bad 的 /v2/activity/growth/tasks 返回 400

	s.RunBlackcatNow()

	if got := stub.hits("/activity/growth/tasks"); got != 2 {
		t.Errorf("任务表查询=%d want 2（bad 失败后仍须查 good）：%s", got, stub.snapshot())
	}
	if got := stub.hits("/v2/chat/completions"); got != 0 {
		t.Errorf("对话调用=%d want 0（good 的 black_cat 已完成）", got)
	}
}

// TestRunBlackcatChatFailureInterruptsAccount 差额 > 0 时按差额补对话；首跳失败
// 立即中断该账号（不重试、不继续下一跳），也不进入账号间限速等待。
func TestRunBlackcatChatFailureInterruptsAccount(t *testing.T) {
	schedulerFastDelays(t)
	schedulerPinNightWindow(t, time.Date(2026, 10, 7, 23, 30, 0, 0, time.Local))
	stub, s := schedulerNewCoverStub(t, schedulerCoverAuth("u1"))
	stub.blackcatTarget, stub.blackcatCurrent = 2, 0 // need = 2
	stub.chatStatus = 500

	start := time.Now()
	s.RunBlackcatNow()
	elapsed := time.Since(start)

	if got := stub.hits("/v2/chat/completions"); got != 1 {
		t.Errorf("对话调用=%d want 1（首跳失败即中断，不得重试/继续补足差额）：%s", got, stub.snapshot())
	}
	if elapsed > 2*time.Second {
		t.Errorf("失败路径耗时 %v（应在中断后立即返回，不等 4s 对话间隔）", elapsed)
	}
}

// ---------------------------------------------------------------------------
// 窗口热配置边界
// ---------------------------------------------------------------------------

// TestSetExpiringSoonWindowClampsNegative 负窗口必须钳成 0（0 = 不做路由门槛），
// 而不是把负 duration 传给到期比较（会让「快过期」恒为空集）。
func TestSetExpiringSoonWindowClampsNegative(t *testing.T) {
	s := New(Config{ExpiringSoonWindow: time.Hour})

	s.SetExpiringSoonWindow(-time.Minute)

	if got := s.ExpiringSoonWindow(); got != 0 {
		t.Errorf("负窗口钳位结果=%v want 0", got)
	}
}
