// pickreason.go 选号决策溯源（NEW-FRONTIERS 2026-10-25 #1）。
//
// 为什么需要：选号器的**决策依据**（成本层、是否探索、是否兜底、粘性命中）在 pick()
// 内全部算过就丢——事后无法回答「这个请求为什么落在 3 号而不是 5 号」。metrics/reqlog
// 覆盖结果（延迟/错误/用量），不覆盖**决策**；对标 magpie/CRS/one-api 均无此层。
// 四态语义的下一步自然延伸：让「为什么选它」和「成本是多少」一样可审计，opsagent
// 的假设（如「402 矛盾号仍被兜底捞回」）也从这里取观察证据。
//
// 实现口径：不可变结构 + atomic.Pointer——pick 走读锁（ADR-005 D-500），写普通字段
// 违规；指针整体换，读侧永远看到完整一份。每账号只留**最近一次**（历史靠 reqlog 的
// 请求级归档承接，账号级快照只答「现在为什么是它」）。
package pool

import (
	"strconv"
	"time"
)

// pickReason 一次选中的结构化理由（不可变，构造后不改）。
type pickReason struct {
	Mode     string    // weighted（正常加权）/ sticky（粘性命中）/ lru（top5 全刚用过）/ earliest-expiry（全冷却兜底）
	Tier     int       // weighted：命中成本层（0 免费实测 / 1 未知 / 2 收费）
	Explored bool      // weighted：本次是否切层探索（issue #136 a′）
	Probe    bool      // weighted：half-open 试探放行（ADR-003 D-400）
	Kind     string    // earliest-expiry：冷却类别（hard/soft/breaker/degrade 粗口径）
	At       time.Time // 选中时刻（注入时钟口径，与 pick 同源）
}

// setPickReason 原子整体替换（读锁路径安全）。
func (e *entry) setPickReason(pr *pickReason) { e.lastPick.Store(pr) }

// pickReasonString 格式化为运维可读单行（Status.PickReason / 日志用）。
func (pr *pickReason) string() string {
	if pr == nil {
		return ""
	}
	s := pr.Mode
	switch pr.Mode {
	case "weighted":
		s += " tier=" + strconv.Itoa(pr.Tier)
		if pr.Explored {
			s += " explore"
		}
		if pr.Probe {
			s += " halfopen-probe"
		}
	case "earliest-expiry":
		if pr.Kind != "" {
			s += " kind=" + pr.Kind
		}
	}
	return s
}

// lastPickReason 原子读最近一次选号理由（nil = 从未记录）。
func (e *entry) lastPickReason() *pickReason { return e.lastPick.Load() }

// coolKindOfCoarse 最早到期兜底用的粗口径类别（与 cmd/server 投影同词表归一思想：
// pool 原生 fallbackKind 返回 hard_credit/soft_rate/breaker/degrade，这里收成
// hard/soft 前缀，供 pickReason.Kind 稳定输出）。
func coolKindOfCoarse(kind string) string {
	switch {
	case len(kind) >= 4 && kind[:4] == "hard":
		return "hard"
	case len(kind) >= 4 && kind[:4] == "soft":
		return "soft"
	}
	return kind
}
