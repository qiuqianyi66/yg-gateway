// growth 养号活动域（从 internal/upstream 拆出，ADR-018）。
//
// 边界契约（AGENTS.md）：
//   - growth → auth（Auth 凭证类型）；不反向依赖 upstream（transport 由上游注入）。
//   - 出站请求仍集中 upstream：本包不构造 http.Request、不直连上游（R6）。
//   - 报文内容零改动（R8）：请求构造逻辑（base/头族/UA）全在 transport 实现侧，
//     本包只传 path/body 参数；错误经 growth.Error 透传上游 Msg（指纹判断语义不变）。
package growth

import (
	"encoding/json"
	"io"
	"time"

	"yg-gateway/internal/auth"
)

// Transport 由 upstream.Client 注入的窄能力接口：封装「构造请求 + 发信解信封」，
// 各 base 的 host/头族/UA 选择留在实现侧（R6/R8）。
type Transport interface {
	// DoChat growth 域（chatBase + BillingHeaders）。
	DoChat(a *auth.Auth, method, path string, body any) (json.RawMessage, error)
	// DoChatMP growth 域小程序口径（chatBase + BillingHeaders + X-Client-Platform: miniprogram）。
	DoChatMP(a *auth.Auth, method, path string, body any) (json.RawMessage, error)
	// DoBilling billing 域（billingBase + BillingHeaders）。
	DoBilling(a *auth.Auth, method, path string, body any) (json.RawMessage, error)
	// DoWeb 官网域（webBase + Web 头族，无 body；领奖类）。
	DoWeb(a *auth.Auth, method, path string) (json.RawMessage, error)
	// DoSchool 学院活动域（billingBase + school 头族）。
	DoSchool(a *auth.Auth, method, path string, body map[string]any, out any) error
	// DoReportMP 小程序埋点上报（BillingBaseCN 固定 + mp 头族，body 为事件数组）。
	DoReportMP(a *auth.Auth, path string, body []map[string]any) error
	// Stream 真实对话流（black_cat 夜猫子用；meta 由实现侧填空，与旧调用一致）。
	Stream(a *auth.Auth, body []byte, clientIP string) (io.ReadCloser, int, []byte, error)
	// ReportActivity 活跃上报（chat_5 同款形状）。
	ReportActivity(a *auth.Auth, conversationID, requestID, modelID, modelName string) error
	// SetNightSleep / NightSleep 夜猫子等待注入（字段持有在实现侧，生产默认 time.Sleep）。
	SetNightSleep(fn func(time.Duration))
	NightSleep() func(time.Duration)
}

// Error growth 域错误（transport 从上游错误透传而来）。只携带本域消费的字段：
// Status（ClaimRewardMP 400 降级 / IsBuddyTaskIncomplete）与 Msg（幂等指纹
// trialAlreadyErr / buddyTaskIncomplete）。Kind 留实现侧（熔断决策不在本域）。
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Msg
}

// Client growth 活动客户端。方法语义与拆包前（upstream.Client 对应方法）逐字一致。
type Client struct {
	t Transport
}

// NewClient 以 transport 构造活动客户端。
func NewClient(t Transport) *Client { return &Client{t: t} }

// clientToken 幂等令牌（前端 randomUUID 同款语义；上游 r8_golden 经私有别名调用）。
func clientToken() string { return ClientToken() }

// ClientToken 幂等令牌（8-4-4-4-12 hex，前端 randomUUID 同款）。
func ClientToken() string { return newClientToken() }

// InNightWindow 当前是否处于夜猫子计数窗口（23:00–08:00 本地时区）。
func InNightWindow(now time.Time) bool {
	h := now.Hour()
	return h >= 23 || h < 8
}
