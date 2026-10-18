// blackcat.go 夜猫子任务（black_cat）+ 新手礼包/补偿 API。
//
// 判据（WorkBuddy-Daily 项目实测口径 + 本网关验证）：black_cat 要求在
// **23:00–08:00（本地时区）窗口内**完成 3 次 glm-5.2 对话并上报 chat 事件链；
// 窗口外行为不计分。真实对话走网关既有 ChatStream（glm-5.2），事件链用
// ReportChatActivityModel（chat_5 同款上报形状）。
package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"yg-gateway/internal/auth"
)

// InNightWindow 当前是否处于夜猫子计数窗口（23:00–08:00 本地时区）。
func InNightWindow(now time.Time) bool {
	h := now.Hour()
	return h >= 23 || h < 8
}

// BlackcatNeed 查 black_cat 任务剩余差额（需要再完成几次对话）。
// 任务不存在返回 0（无可做）；拉取失败返回错误。
func (c *Client) BlackcatNeed(a *auth.Auth) (int64, error) {
	tasks, err := c.ListTasks(a)
	if err != nil {
		return 0, err
	}
	for _, t := range tasks {
		if t.TaskCode == "black_cat" {
			if t.Claimed || t.Current >= t.Target {
				return 0, nil
			}
			return t.Target - t.Current, nil
		}
	}
	return 0, nil
}

// nightChatGap 夜猫子相邻两次真实对话之间的间隔。原为函数内字面量 `4 * time.Second`，
// N4 提为常量：一是让「默认值没被改小」成为可断言的事实（blackcat_test.go 钉住它），
// 二是把「等多久」从实现细节变成显式契约。
//
// 为什么不能直接删掉这个等待：连续发对话会被上游当成脚本行为，4s 是实测可用的节奏。
// 删它是拿风控观感换测试速度——测试要的是「不用真等」，所以修法是**可注入**而不是删除。
const nightChatGap = 4 * time.Second

// SetNightChatSleep 仅供测试注入「夜猫子两次对话之间的等待」实现；生产代码不应调用。
// 注入的函数**会收到生产默认时长**（nightChatGap），因此测试既能跳过真实等待，
// 又能断言默认值本身没有被改小。传 nil = 恢复生产默认（time.Sleep）。
func (c *Client) SetNightChatSleep(fn func(time.Duration)) {
	c.nightChatSleep = fn
}

// sleepNightChat 执行一次夜间对话间隔等待：生产默认 time.Sleep(nightChatGap)，
// 测试注入时走注入实现。
func (c *Client) sleepNightChat() {
	if c.nightChatSleep != nil {
		c.nightChatSleep(nightChatGap)
		return
	}
	time.Sleep(nightChatGap)
}

// RunNightChats 夜猫子：发 need 次 glm-5.2 真实对话（读干流）并上报事件链。
// 返回成功次数。对话内容极短（1+1），消耗可忽略。
//
// 等待只发生在**相邻两次对话之间**，共 need-1 次（可注入，见 SetNightChatSleep）：
// 末次成功后没有第 need+1 个请求要拉开间距，那次等待只是空耗（B4a，每任务 4s）。
// 间隔时长本身与风控观感相关（见 nightChatGap），所以只去掉末次，不动时长。
func (c *Client) RunNightChats(a *auth.Auth, need int) (int64, error) {
	var ok int64
	for i := 0; i < need; i++ {
		body, _ := json.Marshal(map[string]any{
			"model":    "glm-5.2",
			"messages": []map[string]any{{"role": "user", "content": "1+1等于几？直接回答。"}},
			"stream":   true,
		})
		rc, status, respBody, err := c.ChatStream(a, body, "", ChatMeta{})
		if err != nil || status >= 400 {
			if rc != nil {
				rc.Close()
			}
			return ok, fmt.Errorf("第 %d 次对话失败: http=%d err=%v body=%.120s", i+1, status, err, respBody)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
		rc.Close()
		if err := c.ReportChatActivityModel(a, fmt.Sprintf("wb2api-night-%d-%d", time.Now().UnixMilli(), i), "", "glm-5.2", "GLM-5.2"); err != nil {
			return ok, fmt.Errorf("第 %d 次上报失败: %w", i+1, err)
		}
		ok++
		if i < need-1 { // 只拉相邻两次对话的间距；末次之后没有下一个请求
			c.sleepNightChat()
		}
	}
	return ok, nil
}

// ClaimGift 领取新手礼包（每号一次，已领返回业务错误）。
func (c *Client) ClaimGift(a *auth.Auth) (int64, error) {
	data, err := c.billingJSON(a, http.MethodPost, "/billing/meter/claim-gift", map[string]any{})
	if err != nil {
		return 0, err
	}
	var resp struct {
		Credit int64 `json:"credit"`
	}
	_ = json.Unmarshal(data, &resp)
	return resp.Credit, nil
}

// ClaimCompensation 领取活动补偿（有则领，无则业务错误）。
func (c *Client) ClaimCompensation(a *auth.Auth) (int64, error) {
	data, err := c.billingJSON(a, http.MethodPost, "/billing/meter/claim-compensation", map[string]any{})
	if err != nil {
		return 0, err
	}
	var resp struct {
		Credit int64 `json:"credit"`
	}
	_ = json.Unmarshal(data, &resp)
	return resp.Credit, nil
}

// HeatmapYesterdayMissed 检查昨日是否漏签（heatmap cell score==0）。
func (c *Client) HeatmapYesterdayMissed(a *auth.Auth) (bool, error) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	data, err := c.growthJSON(a, http.MethodGet, "/activity/growth/heatmap", nil)
	if err != nil {
		return false, err
	}
	var resp struct {
		Cells []struct {
			Date  string `json:"date"`
			Score int    `json:"score"`
		} `json:"cells"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return false, err
	}
	for _, cell := range resp.Cells {
		if len(cell.Date) >= 10 && cell.Date[:10] == yesterday {
			return cell.Score == 0, nil
		}
	}
	return false, nil
}

// UseMakeupCard 对指定日期使用补签卡（保住连登连续天数；无卡返回业务错误）。
func (c *Client) UseMakeupCard(a *auth.Auth, date string) error {
	_, err := c.growthJSON(a, http.MethodPost, "/activity/growth/makeup-cards/use",
		map[string]any{"target_date": date})
	return err
}
