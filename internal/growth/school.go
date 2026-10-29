// school.go 「开学季/校园日」学院活动：school 域券码（SchoolVouchers）、
// 小程序埋点上报（ReportMPEvent）与判据事件构造（SchoolChatTimesEvents 等）。
// 从 upstream 拆出（ADR-018）：请求构造委托 transport.DoSchool / DoReportMP；
// 事件构造为纯函数（报文内容零改动，R8 金样持续断言）。
package growth

import (
	"fmt"
	"net/http"
	"time"

	"yg-gateway/internal/auth"
)

// schoolBase 学院活动端点前缀（school 域挂 billingBase 下）。
const schoolBase = "/portal/activity/school"

// mpReportPath 小程序埋点上报端点（BillingBaseCN 固定）。
const mpReportPath = "/v2/report"

// schoolOpenDayActivityID 开学季/校园日活动 id（事件 activityId 字段值，两域共用）。
const schoolOpenDayActivityID = "school_open_day_2026"

// SchoolVoucher 开学季抽奖抽中的第三方券（KFC/瑞幸/酷狗等）。
// 字段结构按真实响应样本：GET /vouchers 单次拉全（无分页），data.items[]。
type SchoolVoucher struct {
	GrantID   int64  `json:"grant_id"`
	DrawUUID  string `json:"draw_uuid,omitempty"`
	SKUCode   string `json:"sku_code,omitempty"`   // kfc_ice_cream / voucher_luckin / voucher_kugou …
	PrizeName string `json:"prize_name,omitempty"` // 肯德基冰淇淋
	Code      string `json:"code"`                 // 券码本体（复制给店员核销）
	ValidFrom string `json:"valid_from,omitempty"` // 上游常为空
	ValidTo   string `json:"valid_to,omitempty"`   // "2026-10-24"
	GrantedAt string `json:"granted_at,omitempty"` // RFC3339
}

// SchoolVouchers 查询账号的开学季券码列表（只读）。
// 抽到积分的记录不在此端点（那是 /rewards 的 type=credit 条目）。
func (c *Client) SchoolVouchers(a *auth.Auth) ([]SchoolVoucher, error) {
	var out struct {
		Items []SchoolVoucher `json:"items"`
	}
	if err := c.t.DoSchool(a, http.MethodGet, "/vouchers", nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// MPEventBase 小程序埋点公共指纹（appservice wQ()+Ao() 对齐）。
func MPEventBase(a *auth.Auth) map[string]any {
	return map[string]any{
		"timestamp":    time.Now().UnixMilli(),
		"ideType":      "WorkBuddy_MP",
		"ideVersion":   "2.4.0",
		"extName":      "workbuddy-mp",
		"extVersion":   "2.4.0",
		"product":      "SaaS",
		"ideName":      "wx_app_cloud",
		"platform":     "mini_program",
		"os":           "windows",
		"osVersion":    "11",
		"arch":         "x64",
		"machineId":    "0655736a-607f-4d9d-b430-58176ee9a090",
		"timezone":     "Asia/Shanghai",
		"userId":       a.UID,
		"userNickname": a.Nickname,
	}
}

// ReportMPEvent 以小程序指纹向 www.codebuddy.cn/v2/report 批量上报事件。
func (c *Client) ReportMPEvent(a *auth.Auth, events ...map[string]any) error {
	if len(events) == 0 {
		return fmt.Errorf("mp report: no events")
	}
	base := MPEventBase(a)
	arr := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range ev {
			m[k] = v
		}
		arr = append(arr, m)
	}
	return c.t.DoReportMP(a, mpReportPath, arr)
}

// SchoolChatTimesEvents 构造一条 chat_request_send 事件（chat_3_times 计数）。
func SchoolChatTimesEvents(conversationID string) map[string]any {
	rid := "wb2api-" + clientToken()
	return map[string]any{
		"eventCode":   "chat_request_send",
		"inputLength": 14, "isPlan": false, "isAutoExecuteTerminal": false,
		"isAutoModify": false, "codebaseEnable": false, "maxToken": 0,
		"maxSteps": 500, "temperature": 0, "maxRetries": 0,
		"mentionContexts": []any{}, "knowledgeId": []any{}, "knowledgeName": []any{},
		"codebaseId": "", "mentionContextCount": 0, "command": "",
		"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
		"traceId": rid, "rootRequestId": rid,
		"parentConversationId": conversationID, "conversationId": conversationID,
		"messageId": "msg-" + rid[len(rid)-8:],
		"agentName": "mp", "agentType": "main",
		"codebuddy.session_id":              conversationID,
		"codebuddy.conversation_request_id": rid,
	}
}

// SchoolSeasonChatEvent 构造 growth 域「校园日」（school_season）判据事件：
// mini 指纹 chat_request_send + activityId=school_open_day_2026（与 school 域
// 开学季同 activityId 关联；实测无 activityId 的事件不点亮）。事件形状与
// SchoolChatTimesEvents 同构（school 域 chat_3_times 同款），仅追加 activityId。
func SchoolSeasonChatEvent(conversationID string) map[string]any {
	ev := SchoolChatTimesEvents(conversationID)
	ev["activityId"] = schoolOpenDayActivityID
	return ev
}

// MiniExpertUseEvent 构造 growth 域 Sequential_Tasks_2「在小程序内选中专家并完成
// 有效对话」的判据事件：mp 指纹 expert_actual_use。形状对齐小程序源码
// app-service.js 的真实发射点（上游 task_runner 实测 2026-09-23：上报即 completed，
// claim +200c+5e）。与 school 域的 SchoolExpertUseEvents 是**两套口径**，勿照抄：
//   - 不带 conversationId/activityId——真实事件就是这两个字段都不带；
//   - extVersion 用小程序自身版本 2.2.8（覆盖 mpEventBase 的 2.4.0）；
//   - source=mini_program + type 固定 "send_message"（小程序恒发此值）。
//
// expertID 必须是专家市场真实 ex_ id（ListMarketExperts），空 id 服务端不入账。
func MiniExpertUseEvent(expertID, expertName, expertType string) map[string]any {
	if expertType == "" {
		expertType = "agent"
	}
	if expertName == "" {
		expertName = expertID
	}
	return map[string]any{
		"eventCode": "expert_actual_use", "reportDelay": 0,
		"extVersion": "2.2.8", "source": "mini_program",
		"id": expertID, "name": expertID,
		"expertTitle": expertName, "type": "send_message",
		"characterCount": 12, "expertType": expertType,
	}
}

// MiniChatModelEvent mp 对话事件 + 模型字段（Sequential_Tasks_5「使用 GLM5.2」判据
// 载体）：小程序 chat_request_send 真实发射点（mpsrc main 32904 模块）带
// requestModelId / requestModelName——Tasks_1/3 的裸对话事件不带模型，模型任务
// 须用本形态（判据待解锁实测验证）。
func MiniChatModelEvent(conversationID, modelID, modelName string) map[string]any {
	ev := SchoolChatTimesEvents(conversationID)
	ev["requestModelId"] = modelID
	ev["requestModelName"] = modelName
	return ev
}

// MiniPlaybookEvents mp 指纹灵感事件组（Sequential_Tasks_7「体验灵感功能」判据
// 载体，形状对齐 mpsrc main 73640/73665 发射点：playbook_cta_click →
// playbook_prompt_send）。issue #42 称该任务为 PC 口径（+500c+5e）——PC 序列
// （DesktopPlaybookPromptSequence）已实测点亮 playbook_prompt，本组作为 mp 形态
// 补充（任务在 mp 链上，判据究竟认哪侧待解锁实测）。
func MiniPlaybookEvents(caseID, caseName string) []map[string]any {
	base := map[string]any{
		"id": caseID, "name": caseName, "type": "document",
		"categoryId": "", "categoryName": "",
		"skills": "", "skillNames": "",
	}
	cta := map[string]any{
		"eventCode": "playbook_cta_click", "source": "discover", "position": 1,
		"extVersion": "2.2.8",
	}
	for k, v := range base {
		cta[k] = v
	}
	send := map[string]any{
		"eventCode": "playbook_prompt_send", "source": "discover",
		"promptLength": 96, "isOfficial": 1,
		"conversationId": "wb2api-mp-pb-" + clientToken(),
		"extVersion":     "2.2.8",
	}
	for k, v := range base {
		send[k] = v
	}
	return []map[string]any{cta, send}
}
