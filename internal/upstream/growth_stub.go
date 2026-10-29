// growth_stub.go upstream.Client → growth 委托层（ADR-018）。
//
// 结构：upstream.Client 保留全部公开业务方法为**转发存根**（scheduler/panel/测试
// 调用点零改动）；growth.Transport 由本包实现（growth_transport.go）；类型与
// 纯函数以别名形式留在 upstream 命名空间（panel/scheduler/同包测试引用不变）。
// 报文内容零改动（R8）：转发路径不改任何请求/响应字节。
package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/growth"
)

// mpPlatform 小程序口径头值（growth_transport.go 的 DoChatMP 使用；原 tasks.go 常量）。
const mpPlatform = "miniprogram"

// schoolBase 学院活动端点前缀（schoolJSON 使用；原 school.go 常量）。
const schoolBase = "/portal/activity/school"

// tasksListPath / mpReportPath 常量（coverage/r8 金样测试直引用，原 tasks.go/school.go）。
const (
	tasksListPath = "/v2/activity/growth/tasks"
	mpReportPath  = "/v2/report"
)

// ---- 类型别名（panel/scheduler/测试的 upstream.Task 等引用零改动）----

type (
	Task          = growth.Task
	SchoolVoucher = growth.SchoolVoucher
	Buddy         = growth.Buddy
	TravelState   = growth.TravelState
	StreakFull    = growth.StreakFull
)

// ---- 私有别名（同包测试与 r8_golden 直接调用，保持包内命名空间）----

func clientToken() string { return growth.ClientToken() }
func parseClaimReward(data json.RawMessage) (int64, int64, error) {
	return growth.ParseClaimReward(data)
}
func parseGrowthTasks(data json.RawMessage) ([]Task, error) { return growth.ParseGrowthTasks(data) }
func trialAlreadyErr(msg string) bool                       { return growth.TrialAlreadyErr(msg) }
func mpEventBase(a *auth.Auth) map[string]any               { return growth.MPEventBase(a) }

// ---- 公开纯函数别名（panel/测试引用 upstream.InNightWindow / 事件构造）----

func InNightWindow(now time.Time) bool { return growth.InNightWindow(now) }

// buddyTaskIncompleteMarker 领养门槛未达标的业务错误关键词（HTTP 400 时出现）。
// 与 growth 侧同名常量**必须保持同值**：两侧按各自错误类型断言同一指纹。
const buddyTaskIncompleteMarker = "first_buddy task not completed yet"

// IsBuddyTaskIncomplete 领养门槛未达标判定（转发层错误已是 *upstream.Error，
// 不能复用 growth 侧的 *growth.Error 断言——独立实现，判定语义一致）。
func IsBuddyTaskIncomplete(err error) bool {
	var ue *Error
	if !errors.As(err, &ue) || ue.Status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(ue.Msg), buddyTaskIncompleteMarker)
}

func SchoolChatTimesEvents(conversationID string) map[string]any {
	return growth.SchoolChatTimesEvents(conversationID)
}
func SchoolSeasonChatEvent(conversationID string) map[string]any {
	return growth.SchoolSeasonChatEvent(conversationID)
}
func MiniExpertUseEvent(expertID, expertName, expertType string) map[string]any {
	return growth.MiniExpertUseEvent(expertID, expertName, expertType)
}
func MiniChatModelEvent(conversationID, modelID, modelName string) map[string]any {
	return growth.MiniChatModelEvent(conversationID, modelID, modelName)
}
func MiniPlaybookEvents(caseID, caseName string) []map[string]any {
	return growth.MiniPlaybookEvents(caseID, caseName)
}

// schoolJSON 学院活动 API 请求（剥信封，业务 code≠0 返回带 msg 的 error）。
// 保留在本包：school_json_test.go 直接调用它（R10 marshal 失败钉子）；DoSchool 转发。
func (c *Client) schoolJSON(a *auth.Auth, method, path string, body map[string]any, out any) error {
	var raw []byte
	if body != nil {
		// R10：marshal 失败必须上报，不能静默发空 body——那会把「参数不可序列化」
		// 伪装成上游拒绝，排障时全查错方向。原 ReportMPEvent 同口径。
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("序列化 school-season 请求体失败（请求未发出）: %w", err)
		}
		raw = b
	}
	req, err := http.NewRequest(method, c.billingBase(a)+schoolBase+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	data, err := c.doJSON(req)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// growthClient 惰性构造 growth 委托（sync.Once：面板/调度并发首调安全）。
func (c *Client) growthClient() *growth.Client {
	c.growthOnce.Do(func() { c.gc = growth.NewClient(c) })
	return c.gc
}

// ---- 转发存根（公开业务方法；实现与语义全在 growth 域，错误经 wrapErr 还原）----

func (c *Client) ListTasks(a *auth.Auth) ([]Task, error) {
	ts, err := c.growthClient().ListTasks(a)
	return ts, wrapErr(err)
}
func (c *Client) ListTasksMP(a *auth.Auth) ([]Task, error) {
	ts, err := c.growthClient().ListTasksMP(a)
	return ts, wrapErr(err)
}
func (c *Client) AcceptTasks(a *auth.Auth, codes []string) error {
	return wrapErr(c.growthClient().AcceptTasks(a, codes))
}
func (c *Client) AcceptTasksMP(a *auth.Auth, codes []string) error {
	return wrapErr(c.growthClient().AcceptTasksMP(a, codes))
}
func (c *Client) ClaimReward(a *auth.Auth, code string) (int64, int64, error) {
	p, e, err := c.growthClient().ClaimReward(a, code)
	return p, e, wrapErr(err)
}
func (c *Client) ClaimRewardMP(a *auth.Auth, code string) (int64, int64, error) {
	p, e, err := c.growthClient().ClaimRewardMP(a, code)
	return p, e, wrapErr(err)
}
func (c *Client) TravelStatus(a *auth.Auth) (*TravelState, error) {
	st, err := c.growthClient().TravelStatus(a)
	return st, wrapErr(err)
}
func (c *Client) TravelDepart(a *auth.Auth, locationID int) error {
	return wrapErr(c.growthClient().TravelDepart(a, locationID))
}
func (c *Client) TravelClaim(a *auth.Auth, recordID int64) (int64, error) {
	n, err := c.growthClient().TravelClaim(a, recordID)
	return n, wrapErr(err)
}
func (c *Client) BuddyInfo(a *auth.Auth) (*Buddy, error) {
	b, err := c.growthClient().BuddyInfo(a)
	return b, wrapErr(err)
}
func (c *Client) BuddyFirst(a *auth.Auth) error { return wrapErr(c.growthClient().BuddyFirst(a)) }
func (c *Client) BuddyAgreement(a *auth.Auth) error {
	return wrapErr(c.growthClient().BuddyAgreement(a))
}
func (c *Client) GrowthStreakFull(a *auth.Auth) (*StreakFull, error) {
	s, err := c.growthClient().GrowthStreakFull(a)
	return s, wrapErr(err)
}
func (c *Client) GrowthRedeemTier(a *auth.Auth, tier string) error {
	return wrapErr(c.growthClient().GrowthRedeemTier(a, tier))
}
func (c *Client) LotteryChances(a *auth.Auth) (int, error) {
	n, err := c.growthClient().LotteryChances(a)
	return n, wrapErr(err)
}
func (c *Client) LotteryDraw(a *auth.Auth) (json.RawMessage, error) {
	raw, err := c.growthClient().LotteryDraw(a)
	return raw, wrapErr(err)
}
func (c *Client) GrowthStreak(a *auth.Auth) (int, error) {
	n, err := c.growthClient().GrowthStreak(a)
	return n, wrapErr(err)
}
func (c *Client) ClaimTrial(a *auth.Auth) (bool, error) {
	ok, err := c.growthClient().ClaimTrial(a)
	return ok, wrapErr(err)
}
func (c *Client) BlackcatNeed(a *auth.Auth) (int64, error) {
	n, err := c.growthClient().BlackcatNeed(a)
	return n, wrapErr(err)
}
func (c *Client) RunNightChats(a *auth.Auth, need int) (int64, error) {
	n, err := c.growthClient().RunNightChats(a, need)
	return n, wrapErr(err)
}
func (c *Client) ClaimGift(a *auth.Auth) (int64, error) {
	n, err := c.growthClient().ClaimGift(a)
	return n, wrapErr(err)
}
func (c *Client) ClaimCompensation(a *auth.Auth) (int64, error) {
	n, err := c.growthClient().ClaimCompensation(a)
	return n, wrapErr(err)
}
func (c *Client) HeatmapYesterdayMissed(a *auth.Auth) (bool, error) {
	ok, err := c.growthClient().HeatmapYesterdayMissed(a)
	return ok, wrapErr(err)
}
func (c *Client) UseMakeupCard(a *auth.Auth, date string) error {
	return wrapErr(c.growthClient().UseMakeupCard(a, date))
}
func (c *Client) SetNightChatSleep(fn func(time.Duration)) { c.growthClient().SetNightChatSleep(fn) }
func (c *Client) SchoolVouchers(a *auth.Auth) ([]SchoolVoucher, error) {
	vs, err := c.growthClient().SchoolVouchers(a)
	return vs, wrapErr(err)
}
func (c *Client) ReportMPEvent(a *auth.Auth, events ...map[string]any) error {
	return wrapErr(c.growthClient().ReportMPEvent(a, events...))
}
