// profile.go — V3-10 agent 流量画像的纯函数聚合器。
//
// 输入：reqlog 事件切片（调用方已按 Filter{Kind: KindChat} 从 archive 读回）。
// 输出：by_agent 汇总 + top_sessions 排名。
//
// 为什么是纯函数：
//   - 无 IO、无时钟、无全局状态——测试直接构造 Event 切片即可钉死 G4/G5；
//   - /status 每次请求现算，不维护进程内增量聚合（与 upstream.TaskMetrics 的
//     per-conv TTL 内存聚合不重叠——那个回答「最近一个会话」，本回答「最近 N 条
//     事件里的流量构成」）；
//   - first_seen/last_seen 从 events 里 min/max 得出，不调 time.Now——可复现。
//
// 四态纪律（与 REQ-agent-traffic-profile 逐位对齐）：
//   - agent_kind 空串 → 桶名 "unknown"（没采到 UA）；不并入 "other"。
//   - session_fp 空串 → 不计入 top_sessions，也不计入任何 session 数；
//     requests 仍进 by_agent 桶（它们是无身份散客，by_agent 的 requests 仍准）。
//   - cache_miss_unknown=true 的行：miss 不累加（miss 未知 ≠ miss=0）；
//     hit 若有值仍累加（hit 是独立观察）。
package reqlog

import (
	"sort"
	"time"
)

// Profile 是 /status `agent_profile` 块的 JSON 形状（字段名即 wire 名）。
// 调用方（server）负责在「无数据」时省略整个块——本结构体本身不做省略判断。
type Profile struct {
	Window      WindowStats            `json:"window"`
	ByAgent     map[string]AgentBucket `json:"by_agent"`
	TopSessions []SessionStat          `json:"top_sessions"`
}

// WindowStats 输入窗口的元信息：处理了多少条事件、时间跨度。
type WindowStats struct {
	Events int       `json:"events"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
}

// AgentBucket 按 agent_kind 聚合的桶。Sessions 是该 kind 下「至少出现过一次的
// session_fp 数量」，无 fp 的散客不计入。
type AgentBucket struct {
	Requests         int   `json:"requests"`
	Sessions         int   `json:"sessions"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CacheHitTokens   int64 `json:"cache_hit_tokens"`
	CacheMissTokens  int64 `json:"cache_miss_tokens"`
}

// SessionStat 单会话（session_fp）的画像。
type SessionStat struct {
	FP               string    `json:"fp"`
	AgentKind        string    `json:"agent_kind,omitempty"`
	Turns            int       `json:"turns"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	CacheHitTokens   int64     `json:"cache_hit_tokens"`
	CacheMissTokens  int64     `json:"cache_miss_tokens"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
}

// UnknownAgentBucket 是 by_agent 里「agent_kind 空（未采集）」桶的固定键名。
// 与 "other"（采到了不认识）严格区分——REQ §三态。
const UnknownAgentBucket = "unknown"

// maxTopSessions 排名榜截断长度。只取 turns 最高的前 N 个会话，避免 /status
// 响应随会话数膨胀（一次 1000 条 archive 读出可能出现几百个 fp）。
const maxTopSessions = 10

// AggregateProfile 把事件切片聚合成 Profile。
// 空切片 → 零值 Profile（调用方判断 Window.Events==0 后省略整块）。
func AggregateProfile(events []Event) Profile {
	p := Profile{
		ByAgent:     map[string]AgentBucket{},
		TopSessions: []SessionStat{},
	}
	type sessionAccum struct {
		kind               string
		turns              int
		prompt, completion int64
		hit, miss          int64
		first, last        time.Time
	}
	sessions := map[string]*sessionAccum{}

	for i := range events {
		e := events[i]
		p.Window.Events++
		if p.Window.From.IsZero() || e.Time.Before(p.Window.From) {
			p.Window.From = e.Time
		}
		if e.Time.After(p.Window.To) {
			p.Window.To = e.Time
		}

		// by_agent 桶：agent_kind 空 → unknown 桶。
		kind := e.AgentKind
		if kind == "" {
			kind = UnknownAgentBucket
		}
		b := p.ByAgent[kind]
		b.Requests++
		b.PromptTokens += e.PromptTokens
		b.CompletionTokens += e.CompletionTokens
		b.CacheHitTokens += e.CacheHitTokens
		// miss_unknown 的行 miss 不累加（miss 未知 ≠ miss=0）；hit 独立累加。
		if !e.CacheMissUnknown {
			b.CacheMissTokens += e.CacheMissTokens
		}
		p.ByAgent[kind] = b

		// top_sessions：无 fp 跳过。
		if e.SessionFP == "" {
			continue
		}
		acc, ok := sessions[e.SessionFP]
		if !ok {
			acc = &sessionAccum{kind: e.AgentKind, first: e.Time, last: e.Time}
			sessions[e.SessionFP] = acc
		}
		acc.turns++
		acc.prompt += e.PromptTokens
		acc.completion += e.CompletionTokens
		acc.hit += e.CacheHitTokens
		if !e.CacheMissUnknown {
			acc.miss += e.CacheMissTokens
		}
		if e.Time.Before(acc.first) {
			acc.first = e.Time
		}
		if e.Time.After(acc.last) {
			acc.last = e.Time
		}
	}

	// 把每个 session_fp 反查回 by_agent.sessions：每个 fp 贡献一次。
	// acc.kind 取该 fp 首次出现行的 kind（会话中途 UA 变了是边缘情况，v1 不细分）。
	for _, acc := range sessions {
		kind := acc.kind
		if kind == "" {
			kind = UnknownAgentBucket
		}
		b := p.ByAgent[kind]
		b.Sessions++
		p.ByAgent[kind] = b
	}

	// 展开 sessions → []SessionStat 并按 turns 降序截断。
	list := make([]SessionStat, 0, len(sessions))
	for fp, acc := range sessions {
		list = append(list, SessionStat{
			FP:               fp,
			AgentKind:        acc.kind,
			Turns:            acc.turns,
			PromptTokens:     acc.prompt,
			CompletionTokens: acc.completion,
			CacheHitTokens:   acc.hit,
			CacheMissTokens:  acc.miss,
			FirstSeen:        acc.first,
			LastSeen:         acc.last,
		})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Turns != list[j].Turns {
			return list[i].Turns > list[j].Turns
		}
		// 同 turns 用 last_seen 后出现的在前，再用 fp 字典序保证稳定排序。
		if !list[i].LastSeen.Equal(list[j].LastSeen) {
			return list[i].LastSeen.After(list[j].LastSeen)
		}
		return list[i].FP < list[j].FP
	})
	if len(list) > maxTopSessions {
		list = list[:maxTopSessions]
	}
	p.TopSessions = list
	return p
}
