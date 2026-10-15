// chatrun.go chatCompletions 的**运行期状态**载体（阶段②③ 之后到请求结束）。
//
// 为什么需要它：原实现用 5 个闭包（releaseHeld / unbindSticky / fail /
// recordAttempt / reqHasImage）共享 heldUID、stickyUID、sessKey、st 等局部变量。
// 拆阶段函数时这些闭包必须跨函数边界可用，而 Go 没有局部函数——唯一不违反纪律
// 的选择是让它们成为**结构体方法**：状态显式挂在结构体上，而不是提升为包级变量
// （那会把局部复杂度转成全局复杂度，是假重构）。
//
// 方法名刻意用小写（包内私有）且与原闭包名同名：调用点从 `fail(uid)` 变成
// `run.fail(uid)`，只多一个接收者——diff 越小，快照测试失败时越好定位。
//
// 并发：本结构只被 chatCompletions 所在的那一个 goroutine 访问（含其 defer），
// 不需要同步。**不要**把它传给轮转内可能并发的东西。
package server

import (
	"time"

	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/session"
	"yg-gateway/internal/usage"
)

// chatRun 单次 chat 请求的运行期状态。
type chatRun struct {
	h         *Handler
	st        *chatStat
	sessKey   string
	bareModel string
	modelStr  string
	modelRate string
	authKey   *httpauth.Key

	// heldUID 当前持有的在途租约（空串 = 未持有）。
	// 由 releaseHeld/fail 维护；defer 兜底也用同一字段。
	heldUID string
	// stickyUID 当前会话的粘性绑定号（空串 = 无绑定）。
	// 由 unbindSticky/fail 维护；换号成功时会重新赋值为新号。
	stickyUID string

	// 带图探测的惰性状态（原 imageProbe + reqHasImage 闭包）。
	// imageRawBody 必须在 body 被改写**之前**捕获——改写会动 messages 内容。
	imageDone    bool
	imageVal     bool
	imageViewOK  bool
	imageReqView *session.RequestView
	imageRawBody []byte
}

// hasImage 惰性判定请求是否携带图片（gateway_hint 用）。
//
// 只在错误路径（hintOf / hintContext）需要；成功请求完全用不到，而 hasImagePart
// 是一次全文档 json.Unmarshal——大上下文请求下对成功路径是纯浪费。故首次调用才
// 解析，结果缓存（同请求内 hint 可能被调用多次）。
func (r *chatRun) hasImage() bool {
	if !r.imageDone {
		// 视图可用时走视图（与请求其余消费方共用同一份 messages 子树，不重复
		// 解析）；视图不可用时才回落字节路径（与旧行为一致）。
		if r.imageViewOK {
			r.imageVal = hasImageView(r.imageReqView)
		} else {
			r.imageVal = hasImagePart(r.imageRawBody)
		}
		r.imageDone = true
	}
	return r.imageVal
}

// releaseHeld 释放在途租约（幂等）。成功选中即占名额，函数出口统一释放。
func (r *chatRun) releaseHeld() {
	if r.heldUID != "" {
		r.h.cfg.Pool.Release(r.heldUID)
		r.heldUID = ""
	}
}

// unbindSticky 解绑当前会话粘性号（stickyUID 非空时）。
// 供「粘性号不可用/被抢」与 fail 共用。幂等：stickyUID 已空则空操作；不会误解绑
// 其他轮的绑定。仅当 Session != nil 时 stickyUID 才会非空。
func (r *chatRun) unbindSticky() {
	if r.stickyUID != "" {
		r.h.cfg.Session.Unbind(r.sessKey)
		r.stickyUID = ""
	}
}

// fail 轮转失败分支的统一收尾：释放租约 + 若失败号正是粘性号则解绑
// （下次请求重新分配，避免钉在坏号上）。
func (r *chatRun) fail(uid string) {
	r.releaseHeld()
	if r.stickyUID != "" && uid == r.stickyUID {
		r.unbindSticky()
	}
}

// recordAttempt 记一次上游尝试的观测（token / 延迟 / 用量）。
//
// ttfb 首 token 等待**显式入参**而不是读 st.ttfb：后者在流式分支里是调用之后才
// 赋值的，靠顺序传递会让将来重排代码时静默把速率算回旧的错口径。
func (r *chatRun) recordAttempt(uid string, delta pool.TokenUsageDelta, credit float64, hasCredit bool, started time.Time, ttfb time.Duration) {
	st := r.st
	h := r.h
	st.attempts++
	if delta.HasPromptTokens {
		st.promptTokens = delta.PromptTokens
	}
	if delta.HasCompletionTokens {
		st.completionTokens = delta.CompletionTokens
	}
	if delta.HasTotalTokens {
		st.totalTokens = delta.TotalTokens
	} else if delta.HasPromptTokens || delta.HasCompletionTokens {
		st.totalTokens = st.promptTokens + st.completionTokens
	}
	delta.Model = r.bareModel
	if delta.Model == "" {
		delta.Model = r.modelStr
	}
	latency := time.Since(started)
	latencyMs := latency.Milliseconds()
	if latencyMs < 1 {
		latencyMs = 1
	}
	// 延迟分位数观测（ADR-003 D-403）：与 usage 同汇聚点，全局 + 按模型。
	// 观测完整延迟（含 TTFB），而非仅生成阶段——网关要回答的是"用户等了多久"。
	if h.cfg.Latency != nil {
		h.cfg.Latency.Observe(delta.Model, latency)
	}
	delta.HasLatencyMs = true
	delta.LatencyMs = latencyMs

	// HasCompletionTokens 是「本次有没有 token 观测」的独立判断，与速率怎么算
	// 无关，所以留在调用点；速率本身的边界（TTFB 缺失/超界）收在 tokensPerSecond。
	if delta.HasCompletionTokens {
		if tps, ok := tokensPerSecond(delta.CompletionTokens,
			time.Duration(latencyMs)*time.Millisecond, ttfb); ok {
			delta.HasTokensPerSecond = true
			delta.TokensPerSecond = tps
		}
	}
	h.cfg.Pool.RecordTokenUsage(uid, delta)

	// 用量时序记录。ok 以「上游是否给了 usage」判定：空 delta 意味着这次尝试
	// 没拿到任何 token 统计（传输错误 / >=400 / 解析失败），计为失败尝试。
	// 失败也计入请求数——否则重试放大在「用量」视图里看不见。
	if h.cfg.Usage != nil {
		// realm 只取一个字段：此前用 Pool.Status(uid) 会构造整个 Status
		//（rate_limited_models / model_costs 切片 + 两次排序），每次尝试都白跑。
		realm := h.cfg.Pool.RealmOf(uid)
		if realm == "" {
			realm = "cn"
		}
		keyID := ""
		if r.authKey != nil {
			keyID = r.authKey.ID
		}
		h.cfg.Usage.Add(time.Now(), realm, uid, delta.Model, usage.Delta{
			PromptTokens:     delta.PromptTokens,
			HasPromptTokens:  delta.HasPromptTokens,
			CompletionTokens: delta.CompletionTokens,
			HasCompletion:    delta.HasCompletionTokens,
			TotalTokens:      delta.TotalTokens,
			HasTotal:         delta.HasTotalTokens,
			Credit:           credit,
			HasCredit:        hasCredit,
			HasCacheTokens:   st.hasCache,
			CacheHitTokens:   st.cacheHit,
			CacheMissTokens:  st.cacheMiss,
			ModelRate:        r.modelRate,
			KeyID:            keyID,
			LatencyMs:        delta.LatencyMs,
			HasLatency:       delta.HasLatencyMs,
			TokensPerSecond:  delta.TokensPerSecond,
			HasTPS:           delta.HasTokensPerSecond,
		}, delta.HasTotalTokens || delta.HasCompletionTokens || delta.HasPromptTokens)
	}
}
