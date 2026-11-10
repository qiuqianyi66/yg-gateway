// client_test.go growth 包本包级测试（2026-11-10）。
//
// 为什么需要本文件：growth 从 upstream 拆出后（ADR-018）没有本包测试文件，
// `go test ./... -cover` 对它恒报 0.0%，scripts/check-depth.ps1:210-213 按设计
// 豁免 0% 包 → 该包覆盖可无声退化（跌到 0）而门禁仍全绿。
// 实测其真实覆盖 89.9%（`-coverpkg=yg-gateway/internal/growth ./...`），
// 全靠 upstream 转发存根的测试间接提供；本文件让它首次拥有自己的覆盖率数字。
//
// 断言对象是「委托 + 解析 + 错误分支」三件事，不重复 upstream 侧的报文金样。
package growth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pkg/timex"
)

// fakeTransport 是 Transport 的测试替身：只替换外部边界（出站与等待），
// 不模拟业务逻辑。
type fakeTransport struct {
	chat     func(method, path string, body any) (json.RawMessage, error)
	chatMP   func(method, path string, body any) (json.RawMessage, error)
	billing  func(method, path string, body any) (json.RawMessage, error)
	web      func(method, path string) (json.RawMessage, error)
	school   func(method, path string, body map[string]any, out any) error
	reportMP func(path string, body []map[string]any) error
	stream   func(body []byte) (io.ReadCloser, int, []byte, error)
	activity func(conversationID, requestID, modelID, modelName string) error
	sleep    func(time.Duration)
}

func (f *fakeTransport) DoChat(_ *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	if f.chat == nil {
		return json.RawMessage(`{}`), nil
	}
	return f.chat(method, path, body)
}

func (f *fakeTransport) DoChatMP(_ *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	if f.chatMP == nil {
		return json.RawMessage(`{}`), nil
	}
	return f.chatMP(method, path, body)
}

func (f *fakeTransport) DoBilling(_ *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	if f.billing == nil {
		return json.RawMessage(`{}`), nil
	}
	return f.billing(method, path, body)
}

func (f *fakeTransport) DoWeb(_ *auth.Auth, method, path string) (json.RawMessage, error) {
	if f.web == nil {
		return json.RawMessage(`{}`), nil
	}
	return f.web(method, path)
}

func (f *fakeTransport) DoSchool(_ *auth.Auth, method, path string, body map[string]any, out any) error {
	if f.school == nil {
		return nil
	}
	return f.school(method, path, body, out)
}

func (f *fakeTransport) DoReportMP(_ *auth.Auth, path string, body []map[string]any) error {
	if f.reportMP == nil {
		return nil
	}
	return f.reportMP(path, body)
}

func (f *fakeTransport) Stream(_ *auth.Auth, body []byte, _ string) (io.ReadCloser, int, []byte, error) {
	if f.stream == nil {
		return io.NopCloser(strings.NewReader("")), http.StatusOK, nil, nil
	}
	return f.stream(body)
}

func (f *fakeTransport) ReportActivity(_ *auth.Auth, conversationID, requestID, modelID, modelName string) error {
	if f.activity == nil {
		return nil
	}
	return f.activity(conversationID, requestID, modelID, modelName)
}

func (f *fakeTransport) SetNightSleep(fn func(time.Duration)) { f.sleep = fn }

func (f *fakeTransport) NightSleep() func(time.Duration) { return f.sleep }

func newAuth() *auth.Auth {
	return &auth.Auth{AccessToken: "at", UID: "u1", Nickname: "nick"}
}

// globalAuth 构造 realm=global 的账号（ClaimTrial 的客户端侧前置）。
// global 路由开关是包级状态，测后恢复，避免影响同包其他用例。
func globalAuth(t *testing.T) *auth.Auth {
	t.Helper()
	prev := auth.GlobalEnabled()
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(prev) })
	a := newAuth()
	if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
		t.Fatalf("BackfillRealmFor(global): %v", err)
	}
	return a
}

func TestGrowthTravel(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody any
	tr := &fakeTransport{chat: func(method, path string, body any) (json.RawMessage, error) {
		gotMethod, gotPath, gotBody = method, path, body
		return json.RawMessage(`{"state":"arrived","daily_limit_reached":true,"record_id":42,"reward_credit":7}`), nil
	}}
	c := NewClient(tr)

	st, err := c.TravelStatus(newAuth())
	if err != nil {
		t.Fatalf("TravelStatus: %v", err)
	}
	if st.State != "arrived" || !st.DailyLimitReached || st.RecordID != 42 || st.RewardCredit != 7 {
		t.Errorf("TravelStatus 解析错: %+v", st)
	}
	if gotMethod != http.MethodGet || gotPath != travelStatusPath {
		t.Errorf("TravelStatus 请求 %s %s，want GET %s", gotMethod, gotPath, travelStatusPath)
	}

	if err := c.TravelDepart(newAuth(), 9); err != nil {
		t.Fatalf("TravelDepart: %v", err)
	}
	if gotPath != travelDepartPath {
		t.Errorf("TravelDepart 路径 %q want %q", gotPath, travelDepartPath)
	}
	if m, ok := gotBody.(map[string]any); !ok || m["location_id"] != 9 {
		t.Errorf("TravelDepart body=%v，want location_id=9", gotBody)
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"reward_credit":7}`), nil
	}
	if n, err := c.TravelClaim(newAuth(), 42); err != nil || n != 7 {
		t.Errorf("TravelClaim=(%d,%v) want (7,nil)", n, err)
	}
	// 空 body 与坏 JSON 都按 0 记（调用方只记日志），是 travel.go:71-74 的既有口径。
	tr.chat = func(string, string, any) (json.RawMessage, error) { return nil, nil }
	if n, err := c.TravelClaim(newAuth(), 42); err != nil || n != 0 {
		t.Errorf("TravelClaim(空 body)=(%d,%v) want (0,nil)", n, err)
	}
	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`not-json`), nil
	}
	if n, err := c.TravelClaim(newAuth(), 42); err != nil || n != 0 {
		t.Errorf("TravelClaim(坏 JSON)=(%d,%v) want (0,nil)", n, err)
	}

	// 传输错误必须原样上抛，不得被解析层吞成 0。
	sentinel := errors.New("upstream down")
	tr.chat = func(string, string, any) (json.RawMessage, error) { return nil, sentinel }
	if _, err := c.TravelStatus(newAuth()); !errors.Is(err, sentinel) {
		t.Errorf("TravelStatus 传输错误 err=%v want %v", err, sentinel)
	}
	if _, err := c.TravelClaim(newAuth(), 42); !errors.Is(err, sentinel) {
		t.Errorf("TravelClaim 传输错误 err=%v want %v", err, sentinel)
	}

	// 解信封后的坏 JSON 必须报错（不是静默零值）。
	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"state":`), nil
	}
	if _, err := c.TravelStatus(newAuth()); err == nil {
		t.Error("TravelStatus 坏 JSON 未报错")
	}
}

func TestGrowthBuddy(t *testing.T) {
	tr := &fakeTransport{chat: func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"buddy":{"id":7,"name":"煤球"}}`), nil
	}}
	c := NewClient(tr)
	b, err := c.BuddyInfo(newAuth())
	if err != nil || b == nil || b.ID != 7 || b.Name != "煤球" {
		t.Errorf("BuddyInfo=(%+v,%v) want id=7 name=煤球", b, err)
	}

	// null / 缺字段 = 无猫，是「缺失 ≠ 否定」的落地点：必须 (nil, nil)。
	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"buddy":null}`), nil
	}
	if b, err := c.BuddyInfo(newAuth()); b != nil || err != nil {
		t.Errorf("BuddyInfo(null)=(%+v,%v) want (nil,nil)", b, err)
	}
	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}
	if b, err := c.BuddyInfo(newAuth()); b != nil || err != nil {
		t.Errorf("BuddyInfo(缺字段)=(%+v,%v) want (nil,nil)", b, err)
	}
	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"buddy":{"id":"bad"}}`), nil
	}
	if _, err := c.BuddyInfo(newAuth()); err == nil {
		t.Error("BuddyInfo 坏 buddy 字段未报错")
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
	if err := c.BuddyFirst(newAuth()); err != nil {
		t.Errorf("BuddyFirst=%v want nil", err)
	}
	if err := c.BuddyAgreement(newAuth()); err != nil {
		t.Errorf("BuddyAgreement=%v want nil", err)
	}
}

func TestIsBuddyTaskIncomplete(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"400+关键词", &Error{Status: 400, Msg: "First_Buddy task NOT completed yet!"}, true},
		{"400 无关键词", &Error{Status: 400, Msg: "bad request"}, false},
		{"403+关键词", &Error{Status: 403, Msg: buddyTaskIncompleteMarker}, false},
		{"非本域错误", errors.New(buddyTaskIncompleteMarker), false},
	}
	for _, c := range cases {
		if got := IsBuddyTaskIncomplete(c.err); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestGrowthTasksList(t *testing.T) {
	tr := &fakeTransport{chat: func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"tasks":[
			{"task_code":"A","title":"甲","reward_credit":100,"reward_energy":5,"target":3,"current":3,"accept_status":"not_accepted"},
			{"task_code":"B","progress":{"current":2,"target":5},"accept_status":"claimed"},
			{"task_code":"C","target":0,"current":0}
		]}`), nil
	}}
	c := NewClient(tr)
	ts, err := c.ListTasks(newAuth())
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(ts) != 3 {
		t.Fatalf("len(tasks)=%d want 3", len(ts))
	}
	if ts[0].Credit != 100 || ts[0].Energy != 5 || !ts[0].Claimable || ts[0].Claimed {
		t.Errorf("任务 A=%+v，want credit=100 energy=5 claimable=true claimed=false", ts[0])
	}
	// progress 对象覆盖平铺字段；claimed 的任务不得标记可领。
	if ts[1].Current != 2 || ts[1].Target != 5 || ts[1].Claimable || !ts[1].Claimed {
		t.Errorf("任务 B=%+v，want current=2 target=5 claimable=false claimed=true", ts[1])
	}
	if ts[2].Target != 0 || ts[2].Claimable {
		t.Errorf("任务 C=%+v，want target=0 claimable=false（target=0 不算达标）", ts[2])
	}

	// 小程序口径必须走 DoChatMP（缺该头上游返回 task not found）。
	tr.chatMP = func(method, path string, _ any) (json.RawMessage, error) {
		if method != http.MethodGet || path != tasksListPath {
			return nil, errors.New("wrong request")
		}
		return json.RawMessage(`{"tasks":[{"task_code":"MP1"}]}`), nil
	}
	mp, err := c.ListTasksMP(newAuth())
	if err != nil || len(mp) != 1 || mp[0].TaskCode != "MP1" {
		t.Errorf("ListTasksMP=(%+v,%v) want 1 条 MP1", mp, err)
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) { return json.RawMessage(`{`), nil }
	if _, err := c.ListTasks(newAuth()); err == nil {
		t.Error("ListTasks 坏 JSON 未报错")
	}
	if _, err := ParseGrowthTasks(json.RawMessage(`nope`)); err == nil {
		t.Error("ParseGrowthTasks 坏 JSON 未报错")
	}
}

func TestGrowthTasksAccept(t *testing.T) {
	var gotPath string
	var gotBody any
	tr := &fakeTransport{chat: func(_, path string, body any) (json.RawMessage, error) {
		gotPath, gotBody = path, body
		return json.RawMessage(`{}`), nil
	}}
	c := NewClient(tr)
	if err := c.AcceptTasks(newAuth(), []string{"A"}); err != nil {
		t.Fatalf("AcceptTasks: %v", err)
	}
	if gotPath != tasksAcceptPath {
		t.Errorf("AcceptTasks 路径 %q want %q", gotPath, tasksAcceptPath)
	}
	if m, ok := gotBody.(map[string]any); !ok || len(m["task_codes"].([]string)) != 1 {
		t.Errorf("AcceptTasks body=%v，want task_codes=[A]", gotBody)
	}

	tr.chatMP = func(_, path string, _ any) (json.RawMessage, error) {
		if path != tasksAcceptPath {
			return nil, errors.New("wrong path")
		}
		return json.RawMessage(`{}`), nil
	}
	if err := c.AcceptTasksMP(newAuth(), []string{"A"}); err != nil {
		t.Errorf("AcceptTasksMP=%v want nil", err)
	}
}

func TestGrowthClaimReward(t *testing.T) {
	tr := &fakeTransport{
		chatMP: func(string, string, any) (json.RawMessage, error) {
			return json.RawMessage(`{"credit":50,"energy":2}`), nil
		},
	}
	c := NewClient(tr)
	credit, energy, err := c.ClaimRewardMP(newAuth(), "A")
	if err != nil || credit != 50 || energy != 2 {
		t.Errorf("ClaimRewardMP=(%d,%d,%v) want (50,2,nil)", credit, energy, err)
	}

	// chat 域 400 必须降级到 Web 域领奖（部分任务/租户形态）。

	var webPath string
	tr.chatMP = func(string, string, any) (json.RawMessage, error) {
		return nil, &Error{Status: http.StatusBadRequest, Msg: "task not completed"}
	}
	tr.web = func(_, path string) (json.RawMessage, error) {
		webPath = path
		return json.RawMessage(`{"credit":30,"energy":1}`), nil
	}
	credit, energy, err = c.ClaimRewardMP(newAuth(), "A/B")
	if err != nil || credit != 30 || energy != 1 {
		t.Errorf("ClaimRewardMP 降级=(%d,%d,%v) want (30,1,nil)", credit, energy, err)
	}
	if !strings.Contains(webPath, "A%2FB") {
		t.Errorf("降级路径 %q 未对任务码做 PathEscape", webPath)
	}

	// 非 400 错误不得降级，原样上抛。
	sentinel := errors.New("boom")
	tr.chatMP = func(string, string, any) (json.RawMessage, error) { return nil, sentinel }
	if _, _, err := c.ClaimRewardMP(newAuth(), "A"); !errors.Is(err, sentinel) {
		t.Errorf("ClaimRewardMP(500)=%v want %v", err, sentinel)
	}

	tr.web = func(string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"already_claimed":true,"credit":100}`), nil
	}
	if credit, energy, err := c.ClaimReward(newAuth(), "A"); err != nil || credit != 0 || energy != 0 {
		t.Errorf("ClaimReward(已领)=(%d,%d,%v) want (0,0,nil)", credit, energy, err)
	}
	tr.web = func(string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"credit":100,"energy":5}`), nil
	}
	if credit, energy, err := c.ClaimReward(newAuth(), "A"); err != nil || credit != 100 || energy != 5 {
		t.Errorf("ClaimReward=(%d,%d,%v) want (100,5,nil)", credit, energy, err)
	}
	tr.web = func(string, string) (json.RawMessage, error) { return nil, sentinel }
	if _, _, err := c.ClaimReward(newAuth(), "A"); !errors.Is(err, sentinel) {
		t.Errorf("ClaimReward 传输错误=%v want %v", err, sentinel)
	}
	tr.web = func(string, string) (json.RawMessage, error) {
		return json.RawMessage(`{"credit":`), nil
	}
	if _, _, err := c.ClaimReward(newAuth(), "A"); err == nil {
		t.Error("ClaimReward 坏 JSON 未报错")
	}
}

func TestGrowthStreak(t *testing.T) {
	tr := &fakeTransport{chat: func(_ string, path string, _ any) (json.RawMessage, error) {
		switch path {
		case streakPath:
			return json.RawMessage(`{"streak":{"days":6,"month_total_days":9},"makeup_cards":{"balance":1,"max":2}}`), nil
		case lotterySummaryPath:
			return json.RawMessage(`{"chances":3,"module":{"enabled":true}}`), nil
		case lotteryDrawPath:
			return json.RawMessage(`{"prize":"credit"}`), nil
		case streakRedeemPath:
			return json.RawMessage(`{}`), nil
		}
		return nil, errors.New("unexpected path " + path)
	}}
	c := NewClient(tr)

	sf, err := c.GrowthStreakFull(newAuth())
	if err != nil || sf.Streak.Days != 6 || sf.MakeupCards.Balance != 1 {
		t.Errorf("GrowthStreakFull=(%+v,%v) want days=6 balance=1", sf, err)
	}
	if n, err := c.GrowthStreak(newAuth()); err != nil || n != 6 {
		t.Errorf("GrowthStreak=(%d,%v) want (6,nil)", n, err)
	}
	if n, err := c.LotteryChances(newAuth()); err != nil || n != 3 {
		t.Errorf("LotteryChances=(%d,%v) want (3,nil)", n, err)
	}
	raw, err := c.LotteryDraw(newAuth())
	if err != nil || !strings.Contains(string(raw), "prize") {
		t.Errorf("LotteryDraw=(%s,%v) want 含 prize", raw, err)
	}
	if err := c.GrowthRedeemTier(newAuth(), "7d"); err != nil {
		t.Errorf("GrowthRedeemTier=%v want nil", err)
	}

	// 兑换必须带幂等令牌，否则重复点击会在上游重复发放。
	var redeeemBody any
	tr.chat = func(_ string, path string, body any) (json.RawMessage, error) {
		if path != streakRedeemPath {
			return nil, errors.New("unexpected path")
		}
		redeeemBody = body
		return json.RawMessage(`{}`), nil
	}
	if err := c.GrowthRedeemTier(newAuth(), "7d"); err != nil {
		t.Fatalf("GrowthRedeemTier: %v", err)
	}
	if m, ok := redeeemBody.(map[string]any); !ok || m["tier"] != "7d" || m["client_token"] == "" {
		t.Errorf("兑换 body=%v，want tier=7d + 非空 client_token", redeeemBody)
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) { return json.RawMessage(`{`), nil }
	if _, err := c.GrowthStreakFull(newAuth()); err == nil {
		t.Error("GrowthStreakFull 坏 JSON 未报错")
	}
	if _, err := c.LotteryChances(newAuth()); err == nil {
		t.Error("LotteryChances 坏 JSON 未报错")
	}
	if _, err := c.GrowthStreak(newAuth()); err == nil {
		t.Error("GrowthStreak 坏 JSON 未报错")
	}
}

func TestGrowthBlackcatNeed(t *testing.T) {
	tr := &fakeTransport{chat: func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"tasks":[
			{"task_code":"black_cat","target":3,"current":1,"accept_status":"not_accepted"},
			{"task_code":"other","target":1,"current":0}
		]}`), nil
	}}
	c := NewClient(tr)
	if n, err := c.BlackcatNeed(newAuth()); err != nil || n != 2 {
		t.Errorf("BlackcatNeed=(%d,%v) want (2,nil)", n, err)
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"tasks":[{"task_code":"black_cat","target":3,"current":3,"accept_status":"claimed"}]}`), nil
	}
	if n, err := c.BlackcatNeed(newAuth()); err != nil || n != 0 {
		t.Errorf("BlackcatNeed(已领)=(%d,%v) want (0,nil)", n, err)
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"tasks":[]}`), nil
	}
	if n, err := c.BlackcatNeed(newAuth()); err != nil || n != 0 {
		t.Errorf("BlackcatNeed(无任务)=(%d,%v) want (0,nil)", n, err)
	}
}

func TestGrowthRunNightChats(t *testing.T) {
	var sleeps []time.Duration
	tr := &fakeTransport{
		stream: func([]byte) (io.ReadCloser, int, []byte, error) {
			return io.NopCloser(strings.NewReader(`data: {"choices":[]}\n\n`)), http.StatusOK, nil, nil
		},
		sleep: func(d time.Duration) { sleeps = append(sleeps, d) },
	}
	c := NewClient(tr)

	if n, err := c.RunNightChats(newAuth(), 2); err != nil || n != 2 {
		t.Fatalf("RunNightChats=(%d,%v) want (2,nil)", n, err)
	}
	// 只拉相邻两次对话的间距：need=2 → 等 1 次，且时长必须是生产默认值。
	if len(sleeps) != 1 || sleeps[0] != nightChatGap {
		t.Errorf("等待次数=%d 时长=%v，want 1 次 %v", len(sleeps), sleeps, nightChatGap)
	}

	sentinel := errors.New("stream down")
	tr.stream = func([]byte) (io.ReadCloser, int, []byte, error) {
		return io.NopCloser(strings.NewReader("")), http.StatusBadGateway, []byte("bad gateway"), sentinel
	}
	if n, err := c.RunNightChats(newAuth(), 1); err == nil || n != 0 {
		t.Errorf("RunNightChats(上游 502)=(%d,%v)，want 0 次 + 错误", n, err)
	}

	tr.stream = func([]byte) (io.ReadCloser, int, []byte, error) {
		return io.NopCloser(strings.NewReader("")), http.StatusOK, nil, nil
	}
	actErr := errors.New("report failed")
	tr.activity = func(string, string, string, string) error { return actErr }
	if n, err := c.RunNightChats(newAuth(), 1); !errors.Is(err, actErr) || n != 0 {
		t.Errorf("RunNightChats(上报失败)=(%d,%v)，want 0 次 + %v", n, err, actErr)
	}

	// 注入的等待实现必须收到生产默认时长（防「为了跑得快把节奏删掉」）。
	var got time.Duration
	tr.sleep = func(d time.Duration) { got = d }
	tr.activity = nil
	if _, err := c.RunNightChats(newAuth(), 2); err != nil {
		t.Fatalf("RunNightChats: %v", err)
	}
	if got != nightChatGap {
		t.Errorf("注入等待收到 %v want %v", got, nightChatGap)
	}
	if fn := c.t.NightSleep(); fn == nil {
		t.Error("SetNightChatSleep 注入后 NightSleep 返回 nil")
	}
}

func TestGrowthClaimGiftAndCompensation(t *testing.T) {
	tr := &fakeTransport{billing: func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"credit":500}`), nil
	}}
	c := NewClient(tr)
	if n, err := c.ClaimGift(newAuth()); err != nil || n != 500 {
		t.Errorf("ClaimGift=(%d,%v) want (500,nil)", n, err)
	}
	if n, err := c.ClaimCompensation(newAuth()); err != nil || n != 500 {
		t.Errorf("ClaimCompensation=(%d,%v) want (500,nil)", n, err)
	}

	// 缺字段必须报错（unknown ≠ 0）：否则日志会写下「凭空捏造的 +0」。
	tr.billing = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}
	if _, err := c.ClaimGift(newAuth()); err == nil {
		t.Error("ClaimGift 缺 credit 字段未报错")
	}

	// credit 显式为 0 是可观测事实，必须 (0, nil)，不能当缺字段。
	tr.billing = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"credit":0}`), nil
	}
	if n, err := c.ClaimGift(newAuth()); err != nil || n != 0 {
		t.Errorf("ClaimGift(credit=0)=(%d,%v) want (0,nil)", n, err)
	}

	tr.billing = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`nope`), nil
	}
	if _, err := c.ClaimCompensation(newAuth()); err == nil {
		t.Error("ClaimCompensation 坏 JSON 未报错")
	}
}

func TestGrowthHeatmapAndMakeup(t *testing.T) {
	yesterday := timex.Date(time.Now().AddDate(0, 0, -1))
	tr := &fakeTransport{chat: func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"cells":[{"date":"` + yesterday + `","score":0}]}`), nil
	}}
	c := NewClient(tr)
	if missed, err := c.HeatmapYesterdayMissed(newAuth()); err != nil || !missed {
		t.Errorf("HeatmapYesterdayMissed=(%v,%v) want (true,nil)", missed, err)
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"cells":[{"date":"` + yesterday + `","score":3}]}`), nil
	}
	if missed, err := c.HeatmapYesterdayMissed(newAuth()); err != nil || missed {
		t.Errorf("HeatmapYesterdayMissed(已签)=(%v,%v) want (false,nil)", missed, err)
	}

	// 没有昨日 cell = 未知，按「未漏签」返回 false（既有口径）。
	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"cells":[]}`), nil
	}
	if missed, err := c.HeatmapYesterdayMissed(newAuth()); err != nil || missed {
		t.Errorf("HeatmapYesterdayMissed(无 cell)=(%v,%v) want (false,nil)", missed, err)
	}

	tr.chat = func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{"cells":`), nil
	}
	if _, err := c.HeatmapYesterdayMissed(newAuth()); err == nil {
		t.Error("HeatmapYesterdayMissed 坏 JSON 未报错")
	}

	var gotBody any
	tr.chat = func(_ string, path string, body any) (json.RawMessage, error) {
		if path != "/activity/growth/makeup-cards/use" {
			return nil, errors.New("unexpected path " + path)
		}
		gotBody = body
		return json.RawMessage(`{}`), nil
	}
	if err := c.UseMakeupCard(newAuth(), "2026-11-09"); err != nil {
		t.Fatalf("UseMakeupCard: %v", err)
	}
	if m, ok := gotBody.(map[string]any); !ok || m["target_date"] != "2026-11-09" {
		t.Errorf("UseMakeupCard body=%v，want target_date=2026-11-09", gotBody)
	}
}

func TestGrowthSchool(t *testing.T) {
	tr := &fakeTransport{school: func(method, path string, _ map[string]any, out any) error {
		if method != http.MethodGet || path != "/vouchers" {
			return errors.New("wrong request")
		}
		raw := json.RawMessage(`{"items":[{"grant_id":1,"code":"KFC-1","prize_name":"冰淇淋"}]}`)
		return json.Unmarshal(raw, out)
	}}
	c := NewClient(tr)
	vs, err := c.SchoolVouchers(newAuth())
	if err != nil || len(vs) != 1 || vs[0].Code != "KFC-1" {
		t.Errorf("SchoolVouchers=(%+v,%v) want 1 条 KFC-1", vs, err)
	}

	// 空事件必须拒绝：发空数组会让上游判定为无效上报。
	if err := c.ReportMPEvent(newAuth()); err == nil {
		t.Error("ReportMPEvent(无事件)未报错")
	}

	var gotPath string
	var gotArr []map[string]any
	tr.reportMP = func(path string, body []map[string]any) error {
		gotPath, gotArr = path, body
		return nil
	}
	if err := c.ReportMPEvent(newAuth(), map[string]any{"eventCode": "e1"}); err != nil {
		t.Fatalf("ReportMPEvent: %v", err)
	}
	if gotPath != mpReportPath || len(gotArr) != 1 {
		t.Errorf("ReportMPEvent path=%q len=%d want %q len=1", gotPath, len(gotArr), mpReportPath)
	}
	// 公共指纹必须合并进每条事件，且事件自带字段优先。
	if gotArr[0]["userId"] != "u1" || gotArr[0]["userNickname"] != "nick" || gotArr[0]["eventCode"] != "e1" {
		t.Errorf("事件合并错: %v", gotArr[0])
	}

	tr.reportMP = func(string, []map[string]any) error { return errors.New("report 404") }
	if err := c.ReportMPEvent(newAuth(), map[string]any{"eventCode": "e1"}); err == nil {
		t.Error("ReportMPEvent 传输错误未上抛")
	}
}

func TestGrowthEventBuilders(t *testing.T) {
	ev := SchoolChatTimesEvents("conv-1")
	if ev["eventCode"] != "chat_request_send" || ev["conversationId"] != "conv-1" {
		t.Errorf("SchoolChatTimesEvents=%v，want chat_request_send + conversationId", ev)
	}
	if rid, ok := ev["traceId"].(string); !ok || !strings.HasPrefix(rid, "wb2api-") {
		t.Errorf("SchoolChatTimesEvents traceId=%v，want wb2api- 前缀", ev["traceId"])
	}

	if ev2 := SchoolSeasonChatEvent("conv-2"); ev2["activityId"] != schoolOpenDayActivityID {
		t.Errorf("SchoolSeasonChatEvent activityId=%v want %q", ev2["activityId"], schoolOpenDayActivityID)
	}
	if ev3 := MiniChatModelEvent("conv-3", "glm-5.2", "GLM-5.2"); ev3["requestModelId"] != "glm-5.2" {
		t.Errorf("MiniChatModelEvent requestModelId=%v want glm-5.2", ev3["requestModelId"])
	}

	// 空 expertType / 空 expertName 有默认值，空 id 上游不入账（这里只锁默认值口径）。
	me := MiniExpertUseEvent("ex_1", "", "")
	if me["expertType"] != "agent" || me["expertTitle"] != "ex_1" || me["eventCode"] != "expert_actual_use" {
		t.Errorf("MiniExpertUseEvent=%v，want expertType=agent expertTitle=ex_1", me)
	}

	pb := MiniPlaybookEvents("case-1", "灵感")
	if len(pb) != 2 || pb[0]["eventCode"] != "playbook_cta_click" || pb[1]["eventCode"] != "playbook_prompt_send" {
		t.Errorf("MiniPlaybookEvents=%v，want 2 条 cta→prompt", pb)
	}
}

func TestGrowthTrial(t *testing.T) {
	tr := &fakeTransport{billing: func(string, string, any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}}
	c := NewClient(tr)

	// nil 与 CN 账号必须在客户端侧直接拒绝（CN 无该端点，打上去只是白挨一次 404）。
	if claimed, err := c.ClaimTrial(nil); err == nil || claimed {
		t.Errorf("ClaimTrial(nil)=(%v,%v) want (false,错误)", claimed, err)
	}
	if claimed, err := c.ClaimTrial(newAuth()); err == nil || claimed {
		t.Errorf("ClaimTrial(CN)=(%v,%v) want (false,错误)", claimed, err)
	}

	ga := globalAuth(t)
	if claimed, err := c.ClaimTrial(ga); err != nil || !claimed {
		t.Errorf("ClaimTrial(global)=(%v,%v) want (true,nil)", claimed, err)
	}

	// 幂等码 14051 = 已领过：必须 (false, nil)，不能当失败重试。
	tr.billing = func(string, string, any) (json.RawMessage, error) {
		return nil, &Error{Status: 400, Msg: `{"code":14051,"msg":"already claimed"}`}
	}
	if claimed, err := c.ClaimTrial(ga); err != nil || claimed {
		t.Errorf("ClaimTrial(14051)=(%v,%v) want (false,nil)", claimed, err)
	}

	tr.billing = func(string, string, any) (json.RawMessage, error) {
		return nil, &Error{Status: 500, Msg: "code=14051"}
	}
	if claimed, err := c.ClaimTrial(ga); err != nil || claimed {
		t.Errorf("ClaimTrial(200+code=14051)=(%v,%v) want (false,nil)", claimed, err)
	}

	tr.billing = func(string, string, any) (json.RawMessage, error) {
		return nil, &Error{Status: 500, Msg: "internal"}
	}
	if claimed, err := c.ClaimTrial(ga); err == nil || claimed {
		t.Errorf("ClaimTrial(500)=(%v,%v) want (false,错误)", claimed, err)
	}
}

func TestTrialAlreadyErr(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"code=14051", true},
		{`{"code":14051}`, true},
		{"code=14052", false},
		{"", false},
	}
	for _, c := range cases {
		if got := TrialAlreadyErr(c.msg); got != c.want {
			t.Errorf("TrialAlreadyErr(%q)=%v want %v", c.msg, got, c.want)
		}
	}
}

func TestClientTokenAndNightWindow(t *testing.T) {
	tok := ClientToken()
	parts := strings.Split(tok, "-")
	if len(parts) != 5 || len(tok) != 36 {
		t.Errorf("ClientToken=%q，want 8-4-4-4-12（36 字符 5 段）", tok)
	}
	if tok == ClientToken() {
		t.Error("ClientToken 连续两次相同：幂等令牌失去作用")
	}

	inside := time.Date(2026, 11, 10, 23, 30, 0, 0, time.Local)
	outside := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	if !InNightWindow(inside) {
		t.Error("23:30 应处于夜猫子窗口")
	}
	if InNightWindow(outside) {
		t.Error("12:00 不应处于夜猫子窗口")
	}
	// 08:00 是右开边界，不计入窗口。
	if InNightWindow(time.Date(2026, 11, 10, 8, 0, 0, 0, time.Local)) {
		t.Error("08:00 不应计入夜猫子窗口")
	}
	if !InNightWindow(time.Date(2026, 11, 10, 7, 59, 0, 0, time.Local)) {
		t.Error("07:59 应处于夜猫子窗口")
	}
}

// 保底：Error.Error() 对 nil 接收者不得 panic（错误链上可能传 nil）。
func TestGrowthErrorString(t *testing.T) {
	var e *Error
	if got := e.Error(); got != "<nil>" {
		t.Errorf("(*Error)(nil).Error()=%q want %q", got, "<nil>")
	}
	if got := (&Error{Msg: "boom"}).Error(); got != "boom" {
		t.Errorf("Error()=%q want boom", got)
	}
}
