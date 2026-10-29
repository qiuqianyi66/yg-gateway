// trends.go — 时间序列证据的按天聚合纯函数。
//
// 输入：reqlog 事件切片（调用方已从 archive 全量读回，如 ReadAll）。
// 输出：按天（本地时区）升序的 DailyPoint 列表，回答「账号池健康度/选号/路由
// 的周级趋势」——时间序列证据（账号存活率、到期曲线、路由准确率的支撑数据）。
//
// 为什么是纯函数（与 profile.go 同纪律）：
//   - 无 IO、无时钟、无全局状态——测试直接构造 Event 切片即可钉死聚合逻辑；
//   - /panel/api/trends 每次请求现算，不维护进程内增量聚合；
//   - 天界取 e.Time 的本地时区日期（ArchiveStats 落盘也是本地日期），可复现。
//
// 四态纪律（与 AGENTS.md / REQ 对齐）：
//   - requests==0 的天：success_rate 恒 0，**语义是 unknown**（没样本），消费方
//     必须按「0 请求 = 无观测」展示（如 "—"），禁止读成「成功率 0%」；
//   - succeeded/failed 以 e.OK 为准（HTTP 成功即 ok，与 /status 的 succeeded 同口径；
//     outcome 字段是分类详情，不影响成败计数）；
//   - active_accounts = 当天至少出现过一次的账号数（去重，observation）；
//   - 账号存活率 / 到期曲线不在本文件——那是账号侧聚合，由 panel 端点
//     叠加 Pool.List() 现态完成（当前快照，observation）。
package reqlog

// DailyPoint 单日聚合点（JSON 字段名即 wire 名）。
type DailyPoint struct {
	Date             string  `json:"date"`              // "2006-01-02"（本地时区）
	Requests         int     `json:"requests"`          // 当天总请求数
	Succeeded        int     `json:"succeeded"`         // 当天成功数（e.OK）
	Failed           int     `json:"failed"`            // 当天失败数
	SuccessRate      float64 `json:"success_rate"`      // derivation：成功/总；requests==0 时无意义（unknown）
	ActiveAccounts   int     `json:"active_accounts"`   // 当天活跃账号数（去重）
	PromptTokens     int64   `json:"prompt_tokens"`     // 当天 prompt token 总量
	CompletionTokens int64   `json:"completion_tokens"` // 当天 completion token 总量
}

// AggregateDaily 把事件切片按本地日期聚合成升序日序列。
// 空切片 → 空列表（不是 nil：前端把 null 与"无归档"混在一起会走错分支，
// 与 requestLogs 的空结果回 [] 同口径）。
func AggregateDaily(events []Event) []DailyPoint {
	if len(events) == 0 {
		return []DailyPoint{}
	}
	type accum struct {
		req, ok, fail      int
		active             map[string]struct{}
		prompt, completion int64
	}
	days := make(map[string]*accum)
	for i := range events {
		e := events[i]
		d := e.Time.Local().Format("2006-01-02")
		a, ok := days[d]
		if !ok {
			a = &accum{active: map[string]struct{}{}}
			days[d] = a
		}
		a.req++
		if e.OK {
			a.ok++
		} else {
			a.fail++
		}
		if e.Account != "" {
			a.active[e.Account] = struct{}{}
		}
		a.prompt += e.PromptTokens
		a.completion += e.CompletionTokens
	}
	out := make([]DailyPoint, 0, len(days))
	for d, a := range days {
		rate := 0.0
		if a.req > 0 {
			rate = float64(a.ok) / float64(a.req)
		}
		out = append(out, DailyPoint{
			Date:             d,
			Requests:         a.req,
			Succeeded:        a.ok,
			Failed:           a.fail,
			SuccessRate:      rate,
			ActiveAccounts:   len(a.active),
			PromptTokens:     a.prompt,
			CompletionTokens: a.completion,
		})
	}
	sortDaily(out)
	return out
}

func sortDaily(pts []DailyPoint) {
	for i := 1; i < len(pts); i++ {
		for j := i; j > 0 && pts[j].Date < pts[j-1].Date; j-- {
			pts[j], pts[j-1] = pts[j-1], pts[j]
		}
	}
}
