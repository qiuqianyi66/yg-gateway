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
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/logfmt"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/prompt"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/session"
	"yg-gateway/internal/upstream"
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

	// ---- 阶段④（轮转循环）所需的只读上下文 ----
	// 这些值在循环内只读；放字段里是为了让循环体整体搬出主函数后仍能访问
	// （Go 没有局部函数，方法只能看到接收者的字段、看不到别人的局部变量）。
	realm         string
	turnKey       string
	declaredTools map[string]bool
	clientIP      string
	peekStream    bool
	// outProto 出站协议（F8）："" = chat（原样）；"responses" = /v1/responses
	// （serveNonStream 出口把 chat 响应转成 response 对象）。
	outProto string
	// degradedApplied 本轮是否套用了降级提示词（阶段③ 置位，循环内可能再置位）。
	degradedApplied bool

	// ---- 阶段④ 的读写状态 ----
	// body 会被循环内的降级改写替换（passthrough/append 降级期换中性提示词），
	// 改后的值要交给循环后的 fallbackForward，故需回写。
	body             []byte
	tried            map[string]bool
	modelBlock       pool.ModelBlockStatus
	groupFilteredOut bool
	lastErr          error

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

	// requestClassDone/Val 流量类惰性判定（bandMult 分口径）：选号入口按原始 body
	// 的估算 tokens 判聊天/评测类。body 可能被降级改写，故首次调用即缓存（只算一次）。
	requestClassDone bool
	requestClassVal  pool.RequestClass
	// guardrailEst 护栏估算的 prompt token（REQ-context-guardrail）。warn/blocked 命中
	// 时由 chatCompletions 挂点置位；上游返回实际 usage 后经 RecordCalibration 补对照
	// 样本（估算 vs 上游，供权重真机校准；unknown 不冒充 0）。
	guardrailEst int64
}

// requestClass 按请求体估算规模判流量类（>BenchTokenThreshold = 评测/大请求）。
// estimateTokensJSON 是启发式（CJK÷1 + 其余÷4 向上取整），与上游真实计数存在
// 偏差——但双峰间隙（5k vs 200k）远大于启发式误差，判类稳健（derivation）。
func (r *chatRun) requestClass() pool.RequestClass {
	if !r.requestClassDone {
		r.requestClassDone = true
		r.requestClassVal = pool.ClassChat
		if r.body != nil && estimateTokensJSON(r.body) > pool.BenchTokenThreshold {
			r.requestClassVal = pool.ClassBench
		}
	}
	return r.requestClassVal
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

// rotateAndServe 阶段④：轮转主循环（原 chatCompletions 内联的 415 行）。
//
// 接收者命名为 run（而非本项目其它 chatRun 方法惯用的 r）：方法体内 `r` 必须保持
// 指 *http.Request ——循环体里 12 处用到它，改名会让搬迁 diff 失去可读性。
//
// 为让循环体**零改动**搬迁（diff 越小，快照失败时越好定位），本方法先把 chatRun
// 的字段解构为沿用原名的局部变量，跑完再回写变化项。这是阶段① 用过的同一手法：
// 搬迁与改名分两步，任何一步出问题都能精确定位。
func (run *chatRun) rotateAndServe(w http.ResponseWriter, r *http.Request, chatMeta upstream.ChatMeta) (handled bool) {
	h := run.h
	clientIP := run.clientIP
	peekStream := run.peekStream
	for i := 0; i < h.cfg.MaxRotate; i++ {
		// 选号 + 占租约 + token 就绪（含 continue/break 的三态控制流）已抽到
		// chatrun.go 的 selectAccount。
		acct, outcome := run.selectAccount(i, r)
		// 用 if 而非 switch 承接：**switch 里的 break 只跳出 switch，不跳出 for**
		// （Go 的隐式 break 语义）。写成 switch 时 selectAbort 会退化成"什么都不做"
		// 继续下一轮——症状是轮转次数翻倍并泄漏在途名额，而不是立刻失败
		// （TestChatGlobalAdmissionRelease 抓到的就是这个）。
		if outcome == selectAbort {
			break
		}
		if outcome == selectRetry {
			continue
		}

		// 出站分派（ADR-026）：非 codebuddy 账号走 OpenAI 兼容端点转发（独立路径，
		// 不复用 serveStream/serveNonStream——其 SSE 解析/积分统计/工具标记修复/
		// 错误帧分类是 WorkBuddy 协议专属）。失败 NoteError + 释放租约 + 换号。
		attemptStarted := time.Now()
		if ep, ok := h.cfg.Providers[acct.ProviderID()]; ok {
			if run.serveProviderCompat(w, r, acct, ep, attemptStarted) {
				return true
			}
			h.cfg.Pool.NoteError(acct.UID)
			run.fail(acct.UID)
			if !rotateBackoff(i, r.Context()) {
				break
			}
			continue
		}
		// 客户端 IP 按请求传递（PassthroughIP 开启时注入；消除共享字段竞态）。
		rc, status, respBody, terr := h.cfg.Upstream.ChatStreamContext(r.Context(), acct, run.body, clientIP, chatMeta)
		// 分类信封一次成型：upstream 已在错误路径返回 *upstream.Error（Kind +
		// Retry-After 头解析）。传输层错误（非 *Error）走抖动换号分支；防御分支
		// （terr 为 nil 但 status>=400，如 ErrNone 兜底）回落本地 Classify，双保险。
		// 错误分类与处置（传输层超时/抖动、上游 4xx/5xx）已抽到 chatrun.go
		// 的 handleErrorPath。四态返回值：proceed/retry/abort/handled。
		// **必须用 if 承接，不能用 switch**——switch 里的 break 只跳出 switch，
		// 会让 errAbort 退化成"继续下一轮"（同名陷阱已在 selectAccount 踩过一次）。
		eout := run.handleErrorPath(w, i, r, acct, status, respBody, terr, attemptStarted)
		if eout == errHandled {
			return true
		}
		if eout == errAbort {
			break
		}
		if eout == errRetry {
			continue
		}
		// 成功判定与粘性绑定一律**延后到这一跳真正成功之后**（见下方流式/非流式分支）：
		// 上游「200 已开流 + 一帧 error」是真实形态（6004 限流、内容拦截、审核），
		// 此前在读第一帧之前就 NoteSuccess + 清 11102 负缓存 + 绑粘性 → 被限流的号
		// 记成健康、粘性把会话钉死在它身上，后续每一轮都打同一个限流号。
		if peekStream {
			return run.serveStream(w, acct, rc, attemptStarted)
		}
		return run.serveNonStream(w, acct, rc, attemptStarted)
	}
	// 回写阶段⑤ 的输入：body/degradedApplied 已就地在 run 上更新；错误/阻塞/组过滤
	// 状态由 handleErrorPath/selectAccount 写在 run 上，这里**不能**用循环前的快照
	// 覆盖——否则最后一条上游错误会被抹成 nil，末端退化成 no_healthy_account 503，
	// 上游原文（含 requestId）全部丢失。
	return false
}

// serveStream 与 serveNonStream 已拆到 chatrun_serve.go（2026-11-17）：
// 这两个是「直接写 ResponseWriter 并决定本次请求成败」的终结分支，
// 与这里的选号/轮转责任不同。

// selectOutcome 选号阶段对轮转循环控制流的影响。
//
// 为什么需要它：选号块内含 continue（租约被并发抢走、token 刷新失败换号）与
// break（无可服务账号、ctx 取消），抽成函数后必须用返回值表达控制流——Go 没有
// 跨函数的 break/continue。三态刻意分开而不用 bool：break 与 continue 语义不同
// （前者终止整轮轮转走末端错误构造，后者只放弃本轮），合并会掩盖差异。
type selectOutcome int

const (
	selectOK    selectOutcome = iota // 账号可用：租约已占、token 已就绪
	selectRetry                      // 本轮无果，continue 下一轮
	selectAbort                      // 终止轮转，break 出去走末端错误构造
)

// selectAccount 选号 + 占在途租约 + 确保 token 就绪。
//
// 副作用都写在 run 上（heldUID / stickyUID / lastErr / modelBlock /
// groupFilteredOut），调用方无需再接一长串返回值。
func (run *chatRun) selectAccount(i int, r *http.Request) (*auth.Auth, selectOutcome) {
	h := run.h
	st := run.st
	bareModel, realm := run.bareModel, run.realm
	authKey := run.authKey
	tried := run.tried
	// 选号：粘性号优先（PickByUIDForModel 已校验该模型可用性 + 在途未满），否则普通轮换。
	var acct *auth.Auth
	if run.stickyUID != "" {
		acct = h.cfg.Pool.PickByUIDForModel(run.stickyUID, bareModel)
		if acct == nil || (realm != "" && acct.Realm() != realm) {
			// 粘性号在当前模型不可用（冷却/占满/该模型被 6004 限额）或 realm 不符 → 解绑，
			// 本次回落普通轮换。
			run.unbindSticky()
			acct = nil
		}
	}
	if acct == nil {
		// 模型感知 + realm 感知选号：模型非空时启用 6004 模型级冷却豁免
		// （healthyForModel），realm 谓词过滤跨域账号。
		// P1-B：再叠加 authKey.Group 过滤——该 key 只能用自己组的账号。
		group := ""
		if authKey != nil {
			group = authKey.Group
		}
		acct = h.cfg.Pool.PickExcludingForRealmGroupClass(tried, bareModel, realm, group, run.requestClass())
	}
	if acct == nil {
		st.status = http.StatusServiceUnavailable
		// 记下"是不是模型级阻塞"。选号返回 nil 有两种完全不同的成因：
		//   (a) 池子真的没有可用号（账号级冷却/在途占满/积分保底）；
		//   (b) 号都在，但每个号都对这个模型处于模型级冷却（11102/6004）。
		// 两者此前都报 no_healthy_account，导致「模型不可用」被读成「号全挂了」。
		// 在这里取一次快照，供下方错误构造区分（issue #102 附带发现 1）。
		run.modelBlock = h.cfg.Pool.ModelBlocked(bareModel)
		// P1-B：key 绑定了账号组但组里没号可选 → 标记，错误 hint 里给运维提示。
		if authKey != nil && authKey.Group != "" {
			run.groupFilteredOut = true
		}
		return nil, selectAbort
	}
	st.uid = acct.UID
	// 同步昵称：请求流水行只写 uid8 时无法直观看是哪一号，昵称随本次选号带入日志行。
	st.nick = acct.Nickname
	tried[acct.UID] = true

	// 占用在途名额：Pick 已跳过满额账号，此处 CAS 兜底并发抢名额的竞态。
	if !h.cfg.Pool.Acquire(acct.UID) {
		// 若被抢的正是粘性号，立即解绑并回落普通轮换，避免下一轮仍撞同一个
		// 满载粘性号再浪费一次 PickByUID 往返（语义与 fail()/PickByUID-nil 的解绑一致）。
		if run.stickyUID != "" && acct.UID == run.stickyUID {
			run.unbindSticky()
		}
		if !rotateBackoff(i, r.Context()) {
			// 客户端已断连：换号重试无意义，终止轮转走末端错误透传。
			return nil, selectAbort
		}
		return nil, selectRetry
	}
	run.heldUID = acct.UID

	// 成本未知跟踪（AGENTS.md「Unknown 消解纪律」）：选中即意味着这个
	// (账号,模型) 组合即将被使用，而它的成本可能还没摸清。登记为可消解的
	// 未知（幂等去重，命中路径只是一次原子读），由后续 NoteModelCost 自动消解。
	//
	// 为什么在这里而不是 pick 内部：pick 在 pool 写锁/读锁的临界区里，
	// 不该再碰别的锁（ADR-005 特意让 pick 走读锁以避免串行化）。
	h.cfg.Pool.TrackCostUnknown(acct.UID, bareModel)

	// token 临近过期 → 先 refresh（失败冷却换号）
	// 继承请求 ctx（F-708）：客户端断开/停机时刷新随之取消，不再占满 30s。
	if acct.NeedsRefresh(h.cfg.RefreshSkew) {
		if err := h.cfg.Upstream.RefreshTokenContext(r.Context(), acct); err != nil {
			run.lastErr = err
			var ue *upstream.Error
			if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
				h.cfg.Pool.Disable(acct.UID, "refresh session dead")
			} else {
				h.cfg.Pool.NoteError(acct.UID)
			}
			run.fail(acct.UID)
			if !rotateBackoff(i, r.Context()) {
				return nil, selectAbort
			}
			return nil, selectRetry
		}
		if err := acct.SaveAtomic(); err != nil {
			// 刷新成功但落盘失败：下次启动会用旧 token，必须暴露
			log.Printf("ERR: [server] chat refresh acct=%s: save auth failed: %v", logfmt.Label(acct.UID, acct.Nickname), err)
		}
	}
	return acct, selectOK
}

// errOutcome 错误分类处置后对轮转循环控制流的影响。
//
// 四态而非 bool：块内有三类不同的"不继续本轮的正常流程"——
//
//	errRetry  只放弃本轮，continue 下一轮；
//	errAbort  终止整轮轮转，break 出去走末端错误构造；
//	errHandled 响应已经写完（请求级错误就地 400），或客户端已断连无需写响应；调用方必须立即 return true。
//
// 合并任意两态都会让行为静默改变（errHandled 被当成 errAbort 会导致重复写响应）。
type errOutcome int

const (
	errProceed errOutcome = iota // status < 400：继续处理响应（流式/非流式分派）
	errRetry                     // continue 下一轮
	errAbort                     // break 退出轮转
	errHandled                   // 响应已写完，或客户端断连无需写响应；调用方 return true
)

// statusClientClosedRequest 客户端主动取消/断连的内部审计状态（nginx 惯例 499）。
// 它不是上游返回码，也不写回 wire（客户端已经走了）；只用于把请求归档从
// “503 上游故障”中分离出来，避免把客户端取消计成账号失败。
const statusClientClosedRequest = 499

// handleTransportError 传输层错误（非 *upstream.Error）的两分支处置：上游超时/停滞
// 直接止损（不换号、不罚号），网络层抖动换号但不喂熔断。
//
// 从 handleErrorPath 机械拆出（2026-11-02：该函数代码行 121 越过深度棘轮阈值 120）。
// 判定顺序、注释与副作用（recordAttempt / lastErr / status / NoteFailures / fail）
// 原样保留，调用方只看返回值决定控制流。
func (run *chatRun) handleTransportError(r *http.Request, acct *auth.Auth, terr error, i int, attemptStarted time.Time) errOutcome {
	h := run.h
	st := run.st
	// 客户端主动取消/断连：上游收到的是同一个 ctx 取消，不是上游故障。
	// 绝不能走下面的 NoteFailures/fail 惩罚路径——否则 DSH 每次取消/重试都会
	// 给健康号累积连败，最终把可用池打成 no_healthy_account 503（本日 5 条
	// “503”里 4 条就是这种伪故障）。也不写响应：客户端已经不在。
	if r.Context().Err() != nil {
		run.recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
		st.status = statusClientClosedRequest
		st.outcome = reqlog.OutcomeInterrupted
		run.lastErr = terr
		log.Printf("INFO: [server] client canceled acct=%s: %v (not an upstream failure; account not penalized)",
			logfmt.Label(acct.UID, acct.Nickname), terr)
		run.fail(acct.UID)
		return errHandled
	}
	// 上游超时 / 停滞：**不换号、不罚号**。
	//
	// 超时不是账号的问题：同一份请求换到别的号，撞上的是同一个慢上游，
	// 只会把客户端拖到 MaxRotate × header_timeout（部署值 600s 时最坏
	// 约半小时），期间还给一串健康号喂连败计数。此前全仓没有任何超时
	// 识别，超时和"网络抖动"共用同一条换号路径。
	//
	// 判定三态：net.Error.Timeout()（ResponseHeaderTimeout / Client.Timeout）、
	// 显式 deadline（DeadlineExceeded / os.ErrDeadlineExceeded）、以及
	// **客户端仍在但 ctx 被取消**——那只能是我们自己的空闲看门狗掐的流，
	// 也就是上游停滞。客户端主动断连时 r.Context() 已取消，走下面的抖动分支。
	if isUpstreamTimeout(terr, r.Context().Err() != nil) {
		run.recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
		st.status = http.StatusServiceUnavailable
		run.lastErr = fmt.Errorf("%w: %v", errUpstreamTimeout, terr)
		log.Printf("WARN: [server] upstream timeout acct=%s: %v (rotation stopped, account not penalized)",
			logfmt.Label(acct.UID, acct.Nickname), terr)
		return errAbort
	}
	// 网络层抖动：只换号，不喂熔断计数（传输层错误对连续失败连坐熔断过于严苛）。
	// 连败兜底（issue #114）：喂连败计数——连不上上游是「不知道原因的失败」，
	// 连败 N 次临时出池，单次/偶发不罚（NoteFailures 内部达阈才动作）。
	// 上游 client 已打 transport error 日志。
	run.recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
	st.status = http.StatusServiceUnavailable
	run.lastErr = terr
	h.cfg.Pool.NoteFailures(acct.UID)
	run.fail(acct.UID)
	if !rotateBackoff(i, r.Context()) {
		return errAbort
	}
	return errRetry
}

// handleErrorPath 传输层错误与上游 4xx/5xx 的分类与处置。
//
// 副作用写 run/st（lastErr / status / outcome），调用方只需看返回值决定控制流。
func (run *chatRun) handleErrorPath(w http.ResponseWriter, i int, r *http.Request, acct *auth.Auth, status int, respBody []byte, terr error, attemptStarted time.Time) errOutcome {
	h := run.h
	st := run.st
	bareModel := run.bareModel
	tried := run.tried
	var uerr *upstream.Error
	if errors.As(terr, &uerr) {
		status = uerr.Status
	}
	if uerr == nil && terr != nil {
		return run.handleTransportError(r, acct, terr, i, attemptStarted)
	}
	if status >= 400 {
		run.recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
		st.status = status
		var kind upstream.ErrKind
		if uerr != nil {
			kind = uerr.Kind
		} else {
			kind = upstream.Classify(status, string(respBody))
			uerr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody)}
		}
		// 内容拦截误报（passthrough/append 模式首遇）：判定为 system 指纹误报，
		// 触发降级到次日 00:00 CST，换 Degraded 中性提示词同请求内重试（append
		// 降级重试同样退化为 replace——原文在场只会确定性再撞 400）。
		// 第二次仍被拦（用户内容本身触发审核）→ 回内容防火墙错误（见下分支）。
		// 内容问题非账号问题：applyErrorPolicy 不罚账号（见 ErrContentBlocked 分支）。
		if kind == upstream.ErrContentBlocked && (h.cfg.PromptMode == "passthrough" || h.cfg.PromptMode == "append") && !run.degradedApplied {
			h.degrade.Trigger()
			// 直接写 run.body：降级改写必须让**下一轮出站**与**循环后的
			// fallbackForward**都看到新 body。写成局部变量会只在本次调用内生效，
			// 症状是第二个请求仍带原始 system 指纹（TestContentBlockedTriggersDegradedRetry 抓到）。
			run.body = prompt.Rewrite(run.body, prompt.Degraded)
			run.degradedApplied = true
			delete(tried, acct.UID) // 单账号池也能拿到重试机会（降级重试占一次名额）
			run.releaseHeld()
			log.Printf("content-blocked (likely fingerprint false positive) -> degraded prompt retry")
			return errRetry
		}
		if kind == upstream.ErrContentBlocked {
			// 内容命中网关内容防火墙：立即回客户端，**不轮转**——换任何账号都会撞同一
			// 审核，轮转纯属浪费时间。不罚账号（ErrContentBlocked 分支无冷却/熔断/NoteError）。
			// error-passthrough：message 装上游 body 原文（code/msg/requestId 原样），
			// 不再改写成网关固定文案——客户端必须看到真实错误才能排查。
			h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
			run.fail(acct.UID)
			msg := string(respBody)
			if strings.TrimSpace(msg) == "" {
				// 空 body 兜底：无上游原文可透传，保留可读分类文案（不编造原文）。
				msg = "content blocked by upstream content firewall"
			}
			writeOpenAIErrorHint(w, http.StatusBadRequest, "content_blocked", msg,
				h.hintOf(upstream.ErrContentBlocked, string(respBody), bareModel, run.hasImage(), uerr))
			st.status = http.StatusBadRequest
			st.outcome = reqlog.OutcomeHTTPError
			return errHandled
		}
		// 11115「prompt is too long」：立即透传上游原文回客户端，**不罚号不轮转**
		// ——上下文超限是请求的问题（同一 body 换任何号都超限，白扔健康号配额；
		// 与 WAF IP fail-fast 同哲学：确定与账号无关的错误直接终止轮转）。
		// applyErrorPolicy ErrPromptTooLong 分支零动作，fail 只释放租约。
		// message 装上游 body 原文（含真实 token 数与上限值——上游原文是最有价值
		// 的错误信息，客户端必须看到，禁止固定词覆盖）。
		if kind == upstream.ErrPromptTooLong {
			h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
			run.fail(acct.UID)
			writeOpenAIErrorHint(w, http.StatusBadRequest, "prompt_too_long", promptTooLongMessage(string(respBody)),
				h.hintOf(upstream.ErrPromptTooLong, string(respBody), bareModel, run.hasImage(), uerr))
			st.status = http.StatusBadRequest
			st.outcome = reqlog.OutcomeHTTPError
			return errHandled
		}
		// 图片格式/数据无效：立即透传上游原文回客户端，不罚号不轮转。
		// 同一 body 换账号仍是同样的解析结果，轮转只会放大无效请求。
		if kind == upstream.ErrImageInvalid {
			h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
			run.fail(acct.UID)
			msg := string(respBody)
			if strings.TrimSpace(msg) == "" {
				msg = "image request was rejected by upstream"
			}
			writeOpenAIErrorHint(w, http.StatusBadRequest, "image_invalid", msg,
				h.hintOf(upstream.ErrImageInvalid, string(respBody), bareModel, run.hasImage(), uerr))
			st.status = http.StatusBadRequest
			st.outcome = reqlog.OutcomeHTTPError
			return errHandled
		}
		// 请求体解析失败（11101）：与 11115 / 图片无效同一哲学——同一 body 换任何
		// 账号都是同样的解析结果，轮转只会放大无效请求（每号一次上游调用 +
		// rotateBackoff 占用在途名额）。更要紧的是：继续轮转后末端会落到
		// 「其余保持 503」，把确定失败的请求伪装成"账号不可用、稍后再试"，
		// 客户端于是对必然失败的请求无限重试。立即透传上游原文回 400。
		if kind == upstream.ErrBadParams {
			h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
			run.fail(acct.UID)
			msg := string(respBody)
			if strings.TrimSpace(msg) == "" {
				msg = "chat request body was rejected by upstream"
			}
			writeOpenAIErrorHint(w, http.StatusBadRequest, "bad_params", msg,
				h.hintOf(upstream.ErrBadParams, string(respBody), bareModel, run.hasImage(), uerr))
			st.status = http.StatusBadRequest
			st.outcome = reqlog.OutcomeHTTPError
			return errHandled
		}
		// 模型不存在（11102）：只有**确定**模型名不在已知目录里（客户端拼错）才 fail-fast
		// ——换任何号都会错，和 11101 同一哲学：直接透传 4xx，不换号重试，也不写负缓存
		// （拼错的模型名不该占缓存槽位、污染面板）。
		//
		// 三值纪律（2026-11-02 独立复审 🟡-1）：目录为空 = **unknown**（刚启动还没拉到 /
		// 拉取失败），不得当成"模型不存在"。旧写法 `!isKnownModel(...)` 会在冷启动时把一个
		// 真实模型（换号本可成功）直接 400 掉，与 isKnownModel 自己的注释意图相反。
		if kind == upstream.ErrModelBlocked {
			// 只有"目录非空且确实没有"才 fail-fast；unknown（目录为空）继续换号尝试。
			if h.modelCatalogVerdict(bareModel) == modelCatalogAbsent {
				h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
				run.fail(acct.UID)
				msg := string(respBody)
				if strings.TrimSpace(msg) == "" {
					msg = "model not found"
				}
				writeOpenAIErrorHint(w, http.StatusBadRequest, "model_not_found", msg,
					h.hintOf(upstream.ErrModelBlocked, string(respBody), bareModel, run.hasImage(), uerr))
				st.status = http.StatusBadRequest
				st.outcome = reqlog.OutcomeHTTPError
				return errHandled
			}
		}
		// lastErr 携带完整 body（uerr.Msg 在 upstream 侧截断 200 字符，透传语义
		// 要求原文全量）+ Kind/RetryAfter（末端映射与冷却时长共用）。
		run.lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody), RetryAfter: uerr.RetryAfter}
		h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
		run.fail(acct.UID)
		// WAF IP 级 fail-fast（优先于 rotateBackoff 退避——IP 级拦截时退避无意义）：
		// 该次 WAF 403 喂入 IP 级状态机，若激活（短窗多号命中，IP 被拦而非账号）
		// 则立即终止轮转——继续换号只会把请求放大 MaxRotate 倍打同一出口 IP，
		// 加重风控。账号级软冷却已在上方 applyErrorPolicy 照常记账。
		if kind == upstream.ErrWafBlock && h.wafIP.noteWaf(acct.UID) {
			return errAbort
		}
		if !rotateBackoff(i, r.Context()) {
			return errAbort
		}
		return errRetry
	}
	return errProceed
}
