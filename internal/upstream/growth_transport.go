// growth_transport.go upstream.Client 实现 growth.Transport（ADR-018）。
// 请求构造（base 选择 / 头族 / UA）全部留在本包：R6（出站集中）、R8（报文零改动）。
// 错误经 growthErr 转 growth.Error 透传（Status/Msg 逐字保留，幂等指纹语义不变）。
package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/growth"
)

// growthErr 把上游信封错误转换为 growth.Error（Kind 留本域：熔断决策不跨包）。
// 非信封错误（传输层/解析失败）原样透传。
func growthErr(err error) error {
	var ue *Error
	if errors.As(err, &ue) {
		return &growth.Error{Status: ue.Status, Msg: ue.Msg}
	}
	return err
}

// wrapErr 转发层把 growth.Error 还原为 *upstream.Error（对外契约：panel/scheduler/
// 同包测试继续断言 *upstream.Error）。Kind 用 Classify 重算（与原 doJSON 的分类输入
// 一致：Msg 含原始 body/业务 msg 的子串）+ ErrClient 兜底（同 doJSON 的 code!=0 口径）。
func wrapErr(err error) error {
	var ge *growth.Error
	if errors.As(err, &ge) {
		kind := Classify(ge.Status, ge.Msg)
		if kind == ErrNone {
			kind = ErrClient
		}
		return &Error{Kind: kind, Status: ge.Status, Msg: ge.Msg}
	}
	return err
}

// DoChat growth 域（chatBase + BillingHeaders）；对应原 growthJSON。
func (c *Client) DoChat(a *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.chatBase(a)+path, rdr)
	if err != nil {
		return nil, err
	}
	c.BillingHeaders(req, a)
	data, err := c.doJSON(req)
	return data, growthErr(err)
}

// DoChatMP growth 域小程序口径（chatBase + BillingHeaders + X-Client-Platform）；
// 对应原 growthJSONMP。
func (c *Client) DoChatMP(a *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.chatBase(a)+path, rdr)
	if err != nil {
		return nil, err
	}
	c.BillingHeaders(req, a)
	req.Header.Set("X-Client-Platform", mpPlatform)
	data, err := c.doJSON(req)
	return data, growthErr(err)
}

// DoBilling billing 域（billingBase + BillingHeaders）；复用 billingJSON 并转错误。
func (c *Client) DoBilling(a *auth.Auth, method, path string, body any) (json.RawMessage, error) {
	data, err := c.billingJSON(a, method, path, body)
	return data, growthErr(err)
}

// DoWeb 官网域（webBase + Web 头族，无 body；领奖类）。对应原 ClaimReward 的
// Web 请求构造（Origin/Referer/UA/企业头族，x-client-platform: web）。
func (c *Client) DoWeb(a *auth.Auth, method, path string) (json.RawMessage, error) {
	req, err := http.NewRequest(method, c.webBase(a)+path, nil)
	if err != nil {
		return nil, err
	}
	// Web 端请求头形状（对照浏览器实际请求）：Origin/Referer 指向 workbuddy.cn 成长中心，
	// 带 x-client-platform: web 标记来源端。
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://www.workbuddy.cn")
	req.Header.Set("Referer", "https://www.workbuddy.cn/profile/growth-center")
	req.Header.Set("x-client-platform", "web")
	if ua := c.userAgent(a); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if d := a.DomainValue(); d != "" {
		req.Header.Set("X-Domain", d)
	}
	data, err := c.doJSON(req)
	return data, growthErr(err)
}

// DoSchool 学院活动域（billingBase + school 头族）；复用 schoolJSON 并转错误。
func (c *Client) DoSchool(a *auth.Auth, method, path string, body map[string]any, out any) error {
	err := c.schoolJSON(a, method, path, body, out)
	return growthErr(err)
}

// DoReportMP 小程序埋点上报（BillingBaseCN 固定 + mp 头族）。对应原 ReportMPEvent
// 的请求构造（事件数组在 growth 侧合并后整体传入）。
func (c *Client) DoReportMP(a *auth.Auth, path string, body []map[string]any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.BillingBaseCN+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	req.Header.Set("X-Client-Product", "workbuddy-mp")
	req.Header.Set("X-Client-Version", "2.4.0")
	req.Header.Set("X-Client-Platform", "mp-weixin")
	req.Header.Set("X-Platform", "wechatmp")
	_, err = c.doJSON(req)
	return growthErr(err)
}

// Stream 转发核心流式（meta 填空：growth 侧调用不携带会话头元数据，与原
// RunNightChats 的 ChatMeta{} 一致）。
func (c *Client) Stream(a *auth.Auth, body []byte, clientIP string) (io.ReadCloser, int, []byte, error) {
	return c.ChatStream(a, body, clientIP, ChatMeta{})
}

// ReportActivity 转发活跃上报（chat_5 同款形状）。
func (c *Client) ReportActivity(a *auth.Auth, conversationID, requestID, modelID, modelName string) error {
	return c.ReportChatActivityModel(a, conversationID, requestID, modelID, modelName)
}

// SetNightSleep 写入夜猫子等待注入（字段持有在本 Client，blackcat_test 直读断言）。
func (c *Client) SetNightSleep(fn func(time.Duration)) { c.nightChatSleep = fn }

// NightSleep 读取夜猫子等待注入；nil = 生产默认 time.Sleep。
func (c *Client) NightSleep() func(time.Duration) { return c.nightChatSleep }
