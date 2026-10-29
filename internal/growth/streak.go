// streak.go 成长中心连登兑换 + 抽奖 API（2026-09-12 成长中心 bundle 逆向 + 页面实测）。
//
// 机制：连登档位（7d/14d/28d）按**连续登录天数**解锁；兑换（POST /activity/growth/redeem）
// 发 credit/energy/补签卡/**抽奖次数**；抽奖（POST /activity/growth/lottery/draw）每次消耗
// 1 次 chances。未解锁兑换返回 HTTP 403「连续登录天数不足」。
// client_token 为前端生成的幂等令牌（randomUUID）。
//
// 从 upstream 拆出（ADR-018）：逻辑逐字保留，请求构造委托 transport.DoChat。
package growth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"yg-gateway/internal/auth"
)

// streakRedeemPath / lottery 路径（growth 域）。
const (
	streakRedeemPath   = "/activity/growth/redeem"
	lotterySummaryPath = "/activity/growth/lottery/summary"
	lotteryDrawPath    = "/activity/growth/lottery/draw"
)

// newClientToken 幂等令牌（前端 randomUUID 同款语义）。
func newClientToken() string {
	b := make([]byte, 16)
	// 忽略：crypto/rand.Read 自 Go 1.24 起契约保证填满且永不返回错误，失败无路径，不影响幂等令牌（判据④）。
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

// StreakFull 连登完整状态（GET /activity/growth/streak）。
type StreakFull struct {
	Streak struct {
		Days              int    `json:"days"`
		MonthTotalDays    int    `json:"month_total_days"`
		NextTier          string `json:"next_tier"`
		NextTierRemaining int    `json:"next_tier_remaining"`
	} `json:"streak"`
	MakeupCards struct {
		Balance int `json:"balance"`
		Max     int `json:"max"`
	} `json:"makeup_cards"`
	RedemptionStatus struct {
		Tier7dStatus  string `json:"tier_7d_status"`
		Tier14dStatus string `json:"tier_14d_status"`
		Tier28dStatus string `json:"tier_28d_status"`
		RemainingDays int    `json:"remaining_days"`
		Tiers         []struct {
			Tier    string `json:"tier"`
			Days    int    `json:"days"`
			Credit  int    `json:"credit"`
			Energy  int    `json:"energy"`
			Cards   int    `json:"cards"`
			Chances int    `json:"chances"`
		} `json:"tiers"`
	} `json:"redemption_status"`
}

// GrowthStreakFull 拉取连登完整状态。
func (c *Client) GrowthStreakFull(a *auth.Auth) (*StreakFull, error) {
	data, err := c.t.DoChat(a, http.MethodGet, streakPath, nil)
	if err != nil {
		return nil, err
	}
	out := &StreakFull{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GrowthRedeemTier 兑换连登档位（tier: "7d"|"14d"|"28d"）。
// 未解锁返回 *Error（HTTP 403「连续登录天数不足」），调用方按 locked 状态跳过即可。
func (c *Client) GrowthRedeemTier(a *auth.Auth, tier string) error {
	_, err := c.t.DoChat(a, http.MethodPost, streakRedeemPath,
		map[string]any{"tier": tier, "client_token": clientToken()})
	return err
}

// LotteryChances 当前抽奖次数（GET /activity/growth/lottery/summary）。
func (c *Client) LotteryChances(a *auth.Auth) (int, error) {
	data, err := c.t.DoChat(a, http.MethodGet, lotterySummaryPath, nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Chances int `json:"chances"`
		Module  struct {
			Enabled bool `json:"enabled"`
		} `json:"module"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, err
	}
	return resp.Chances, nil
}

// LotteryDraw 抽奖一次，返回原始奖品载荷（prize 字段形状由活动期决定，透传给调用方）。
func (c *Client) LotteryDraw(a *auth.Auth) (json.RawMessage, error) {
	return c.t.DoChat(a, http.MethodPost, lotteryDrawPath,
		map[string]any{"client_token": clientToken()})
}

// GrowthStreak 查询连登天数（travel.go 迁移前的轻量查询；缺字段零值）。
func (c *Client) GrowthStreak(a *auth.Auth) (int, error) {
	data, err := c.t.DoChat(a, http.MethodGet, streakPath, nil)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Streak struct {
			Days int `json:"days"`
		} `json:"streak"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, err
	}
	return resp.Streak.Days, nil
}
