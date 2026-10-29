// blackcat.go 夜猫子任务（black_cat）+ 新手礼包/补偿 API。
//
// 判据（WorkBuddy-Daily 项目实测口径 + 本网关验证）：black_cat 要求在
// **23:00–08:00（本地时区）窗口内**完成 3 次 glm-5.2 对话并上报 chat 事件链；
// 窗口外行为不计分。真实对话走网关既有 ChatStream（glm-5.2），事件链用
// ReportActivity（chat_5 同款上报形状）。
// 从 upstream 拆出（ADR-018）：ChatStream/上报/等待注入经 transport；等待字段
// 持有在实现侧（upstream.Client.nightChatSleep），生产默认 time.Sleep。
package growth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"yg-gateway/internal/auth"
)

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
	c.t.SetNightSleep(fn)
}

// sleepNightChat 执行一次夜间对话间隔等待：生产默认 time.Sleep(nightChatGap)，
// 测试注入时走注入实现。
func (c *Client) sleepNightChat() {
	if fn := c.t.NightSleep(); fn != nil {
		fn(nightChatGap)
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
		rc, status, respBody, err := c.t.Stream(a, body, "")
		if err != nil || status >= 400 {
			if rc != nil {
				rc.Close()
			}
			return ok, fmt.Errorf("第 %d 次对话失败: http=%d err=%v body=%.120s", i+1, status, err, respBody)
		}
		// 忽略：排干 body 是回收连接前的清理路径，失败随即随 rc.Close 释放，不影响主流程（判据②）。
		_, _ = io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
		rc.Close()
		if err := c.t.ReportActivity(a, fmt.Sprintf("wb2api-night-%d-%d", time.Now().UnixMilli(), i), "", "glm-5.2", "GLM-5.2"); err != nil {
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
	data, err := c.t.DoBilling(a, http.MethodPost, "/billing/meter/claim-gift", map[string]any{})
	if err != nil {
		return 0, err
	}
	return claimCredit(data, "新手礼包")
}

// ClaimCompensation 领取活动补偿（有则领，无则业务错误）。
func (c *Client) ClaimCompensation(a *auth.Auth) (int64, error) {
	data, err := c.t.DoBilling(a, http.MethodPost, "/billing/meter/claim-compensation", map[string]any{})
	if err != nil {
		return 0, err
	}
	return claimCredit(data, "补偿领取")
}

// claimCredit 解析「领取类」响应的 credits 数。
//
// 关键口径（R10 + 四态语义）：**解析失败与字段缺失都必须返回错误**，绝不返回 (0, nil)。
// 因为调用方（scheduler/streak.go streak-bonus）在 err==nil 时会打日志
// 「🎊 新手礼包 +%dc」——若把 unknown 冒充成 0，日志会写下一条**凭空捏造的事实**
// （"观测到领了 0 信用"），而真实状态是 unknown。这比功能缺失更危险：它不报错。
//
// 三种状态分别如何用 (int64, error) 表达：
//   - credit=500      → (500, nil)      observation: 观测到 +500
//   - credit=0        → (0, nil)        observation: 确实观测到 0（credits 为指针，与 nil 可区分）
//   - 解析失败/缺字段 → (0, err)        unknown: 不知道，交给调用方决定是否静默跳过
//
// Credit 用 *int64 而非 int64，正是为了把「字段缺失」与「字段存在且为 0」分开——
// 这正是本项目「缺失 ≠ 否定」的落地点。
func claimCredit(data []byte, op string) (int64, error) {
	var resp struct {
		Credit *int64 `json:"credit"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, fmt.Errorf("解析%s响应失败（积分为 unknown，非 0）: %w body=%.120s", op, err, data)
	}
	if resp.Credit == nil {
		return 0, fmt.Errorf("%s响应缺 credit 字段（积分为 unknown，非 0）: body=%.120s", op, data)
	}
	return *resp.Credit, nil
}

// HeatmapYesterdayMissed 检查昨日是否漏签（heatmap cell score==0）。
func (c *Client) HeatmapYesterdayMissed(a *auth.Auth) (bool, error) {
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	data, err := c.t.DoChat(a, http.MethodGet, "/activity/growth/heatmap", nil)
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
	_, err := c.t.DoChat(a, http.MethodPost, "/activity/growth/makeup-cards/use",
		map[string]any{"target_date": date})
	return err
}
