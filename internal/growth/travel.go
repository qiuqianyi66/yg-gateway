// travel.go growth 域「猫猫旅行」接口：状态查询 / 派出 / 领奖 / 领养 / 协议。
// 全部走 chatBase（copilot.tencent.com，不带 /v2 前缀）+ BillingHeaders，信封同 doJSON。
// 从 upstream 拆出（ADR-018）：请求构造委托 transport.DoChat。
package growth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"yg-gateway/internal/auth"
)

// growth 域路径（实测）。
const (
	travelStatusPath   = "/activity/growth/buddy/travel/status"
	travelDepartPath   = "/activity/growth/buddy/travel/depart"
	travelClaimPath    = "/activity/growth/buddy/travel/claim"
	buddyInfoPath      = "/activity/growth/buddy/info"
	buddyFirstPath     = "/activity/growth/buddy/first"
	buddyAgreementPath = "/activity/growth/buddy/agreement"
	streakPath         = "/activity/growth/streak"
)

// buddyTaskIncompleteMarker 领养门槛未达标的业务错误关键词（HTTP 400 时出现）。
const buddyTaskIncompleteMarker = "first_buddy task not completed yet"

// Buddy 账号当前猫档案；nil（data.buddy 为 null）表示无猫。
type Buddy struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// TravelState 猫猫旅行状态。
type TravelState struct {
	State             string `json:"state"`               // idle / traveling / arrived
	DailyLimitReached bool   `json:"daily_limit_reached"` // 今日已派出过（自然日 00:00 CST 重置）
	RecordID          int64  `json:"record_id"`           // 在途/到站记录 id，claim 必带
	RewardCredit      int64  `json:"reward_credit"`       // 到站可领奖励积分
}

// TravelStatus 查询猫猫旅行状态。
func (c *Client) TravelStatus(a *auth.Auth) (*TravelState, error) {
	data, err := c.t.DoChat(a, http.MethodGet, travelStatusPath, nil)
	if err != nil {
		return nil, err
	}
	var out TravelState
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TravelDepart 派出猫猫（locationID 来自配置；daily_limit_reached 时上游返回业务错误）。
func (c *Client) TravelDepart(a *auth.Auth, locationID int) error {
	_, err := c.t.DoChat(a, http.MethodPost, travelDepartPath, map[string]any{"location_id": locationID})
	return err
}

// TravelClaim 领取到站奖励，返回 reward_credit。
func (c *Client) TravelClaim(a *auth.Auth, recordID int64) (int64, error) {
	data, err := c.t.DoChat(a, http.MethodPost, travelClaimPath, map[string]any{"record_id": recordID})
	if err != nil {
		return 0, err
	}
	var resp struct {
		RewardCredit int64 `json:"reward_credit"`
	}
	if len(data) > 0 {
		// 奖励字段缺失不视为失败：调用方按 0 记日志即可。
		_ = json.Unmarshal(data, &resp)
	}
	return resp.RewardCredit, nil
}

// BuddyInfo 查询当前猫档案；返回 (nil, nil) 表示无猫（data.buddy 为 null）。
func (c *Client) BuddyInfo(a *auth.Auth) (*Buddy, error) {
	data, err := c.t.DoChat(a, http.MethodGet, buddyInfoPath, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Buddy json.RawMessage `json:"buddy"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	// null / 缺字段 / 空对象都按无猫处理。
	trimmed := strings.TrimSpace(string(resp.Buddy))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var b Buddy
	if err := json.Unmarshal(resp.Buddy, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// BuddyFirst 领养第一只猫（未满足门槛返回 first_buddy 业务错误，见 IsBuddyTaskIncomplete）。
func (c *Client) BuddyFirst(a *auth.Auth) error {
	_, err := c.t.DoChat(a, http.MethodPost, buddyFirstPath, map[string]any{})
	return err
}

// BuddyAgreement 签署领养协议（领养前置）。
func (c *Client) BuddyAgreement(a *auth.Auth) error {
	_, err := c.t.DoChat(a, http.MethodPost, buddyAgreementPath, map[string]any{"agree": true})
	return err
}

// IsBuddyTaskIncomplete 判定「领养门槛未达标」：HTTP 400 + first_buddy 关键词。
// 该错误当日不应重试（避免对上游重试轰炸）。
func IsBuddyTaskIncomplete(err error) bool {
	if err == nil {
		return false
	}
	var ge *Error
	if !errors.As(err, &ge) || ge.Status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(ge.Msg), buddyTaskIncompleteMarker)
}
