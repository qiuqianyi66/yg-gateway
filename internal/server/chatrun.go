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
	"io"
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
	st := run.st
	bareModel, realm := run.bareModel, run.realm
	clientIP := run.clientIP
	peekStream := run.peekStream
	authKey := run.authKey
	degradedApplied := run.degradedApplied
	tried, modelBlock, groupFilteredOut := run.tried, run.modelBlock, run.groupFilteredOut
	lastErr := run.lastErr
	body := run.body
	for i := 0; i < h.cfg.MaxRotate; i++ {
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
			acct = h.cfg.Pool.PickExcludingForRealmGroup(tried, bareModel, realm, group)
		}
		if acct == nil {
			st.status = http.StatusServiceUnavailable
			// 记下"是不是模型级阻塞"。选号返回 nil 有两种完全不同的成因：
			//   (a) 池子真的没有可用号（账号级冷却/在途占满/积分保底）；
			//   (b) 号都在，但每个号都对这个模型处于模型级冷却（11102/6004）。
			// 两者此前都报 no_healthy_account，导致「模型不可用」被读成「号全挂了」。
			// 在这里取一次快照，供下方错误构造区分（issue #102 附带发现 1）。
			modelBlock = h.cfg.Pool.ModelBlocked(bareModel)
			// P1-B：key 绑定了账号组但组里没号可选 → 标记，错误 hint 里给运维提示。
			if authKey != nil && authKey.Group != "" {
				groupFilteredOut = true
			}
			break
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
				break
			}
			continue // 最后一个名额被并发抢走 → 换号
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
				lastErr = err
				var ue *upstream.Error
				if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
					h.cfg.Pool.Disable(acct.UID, "refresh session dead")
				} else {
					h.cfg.Pool.NoteError(acct.UID)
				}
				run.fail(acct.UID)
				if !rotateBackoff(i, r.Context()) {
					break // ctx 取消：终止轮转（refresh 失败换号退避）
				}
				continue
			}
			if err := acct.SaveAtomic(); err != nil {
				// 刷新成功但落盘失败：下次启动会用旧 token，必须暴露
				log.Printf("ERR: [server] chat refresh acct=%s: save auth failed: %v", logfmt.Label(acct.UID, acct.Nickname), err)
			}
		}

		// 客户端 IP 按请求传递（PassthroughIP 开启时注入；消除共享字段竞态）。
		attemptStarted := time.Now()
		rc, status, respBody, terr := h.cfg.Upstream.ChatStreamContext(r.Context(), acct, body, clientIP, chatMeta)
		// 分类信封一次成型：upstream 已在错误路径返回 *upstream.Error（Kind +
		// Retry-After 头解析）。传输层错误（非 *Error）走抖动换号分支；防御分支
		// （terr 为 nil 但 status>=400，如 ErrNone 兜底）回落本地 Classify，双保险。
		var uerr *upstream.Error
		if errors.As(terr, &uerr) {
			status = uerr.Status
		}
		if uerr == nil && terr != nil {
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
				lastErr = fmt.Errorf("%w: %v", errUpstreamTimeout, terr)
				log.Printf("WARN: [server] upstream timeout acct=%s: %v (rotation stopped, account not penalized)",
					logfmt.Label(acct.UID, acct.Nickname), terr)
				break
			}
			// 网络层抖动：只换号，不喂熔断计数（传输层错误对连续失败连坐熔断过于严苛）。
			// 连败兜底（issue #114）：喂连败计数——连不上上游是「不知道原因的失败」，
			// 连败 N 次临时出池，单次/偶发不罚（NoteFailures 内部达阈才动作）。
			// 上游 client 已打 transport error 日志。
			run.recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
			st.status = http.StatusServiceUnavailable
			lastErr = terr
			h.cfg.Pool.NoteFailures(acct.UID)
			run.fail(acct.UID)
			if !rotateBackoff(i, r.Context()) {
				break // ctx 取消：终止轮转（传输层错误换号退避）
			}
			continue
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
			if kind == upstream.ErrContentBlocked && (h.cfg.PromptMode == "passthrough" || h.cfg.PromptMode == "append") && !degradedApplied {
				h.degrade.Trigger()
				body = prompt.Rewrite(body, prompt.Degraded)
				degradedApplied = true
				delete(tried, acct.UID) // 单账号池也能拿到重试机会（降级重试占一次名额）
				run.releaseHeld()
				log.Printf("content-blocked (likely fingerprint false positive) -> degraded prompt retry")
				continue
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
				return true
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
				return true
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
				return true
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
				return true
			}
			// lastErr 携带完整 body（uerr.Msg 在 upstream 侧截断 200 字符，透传语义
			// 要求原文全量）+ Kind/RetryAfter（末端映射与冷却时长共用）。
			lastErr = &upstream.Error{Kind: kind, Status: status, Msg: string(respBody), RetryAfter: uerr.RetryAfter}
			h.applyErrorPolicy(acct.UID, kind, string(respBody), bareModel, uerr)
			run.fail(acct.UID)
			// WAF IP 级 fail-fast（优先于 rotateBackoff 退避——IP 级拦截时退避无意义）：
			// 该次 WAF 403 喂入 IP 级状态机，若激活（短窗多号命中，IP 被拦而非账号）
			// 则立即终止轮转——继续换号只会把请求放大 MaxRotate 倍打同一出口 IP，
			// 加重风控。账号级软冷却已在上方 applyErrorPolicy 照常记账。
			if kind == upstream.ErrWafBlock && h.wafIP.noteWaf(acct.UID) {
				break
			}
			if !rotateBackoff(i, r.Context()) {
				break // ctx 取消：终止轮转（分类错误换号退避）
			}
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
	// 回写运行期状态：body 可能被降级改写替换（循环后的 fallbackForward 要用），
	// 其余三项是阶段⑤ finalErrorOf 的输入。
	run.body = body
	run.degradedApplied = degradedApplied
	run.lastErr, run.modelBlock, run.groupFilteredOut = lastErr, modelBlock, groupFilteredOut
	return false
}

// serveStream 流式响应分支：透传上游 SSE 到客户端，并处理流尾的成功/失败判定。
//
// 返回值 handled 恒为 true——本分支的所有路径都会把响应写完（成功透传、上游空流、
// 快照错误、中断）。之所以仍返回 bool 而不是直接 void：与 serveNonStream 保持同一
// 形状，调用点一眼看得出「这里会终结请求」。
func (run *chatRun) serveStream(w http.ResponseWriter, acct *auth.Auth, rc io.ReadCloser, attemptStarted time.Time) bool {
	h := run.h
	st := run.st
	bareModel, sessKey := run.bareModel, run.sessKey
	declaredTools := run.declaredTools
	// 流式：透传结束后立即关闭上游 body，避免 defer 在轮转场景下堆积 fd。
	st.status = http.StatusOK
	stats := newChatStatsReaderSince(rc, st.start)
	// gateway_hint（SSE）：成功状态 200 已开流，中途 error 帧透传时附加
	// hint 字段（hintFn 惰性求值——正常流零开销，只有真撞到 error 帧才
	// 组装请求上下文做判定）。
	// errFrame：上游 error 帧原文（观察者旁路采集），用于流尾的账号处置。
	var errFrame string
	// 标记修复：上游在没有 tools 的请求里会把工具调用吐成原生标记文本，
	// 这里在透传前还原成 delta.tool_calls（见 upstream/dsml.go）。
	// 第二个参数为 true = 名单为空时启用弱判定——tools 声明在链路上丢失是
	// 实测最常见的泄漏成因，只做严格判定等于对主场景不设防。
	repair := upstream.NewMarkupRepair(declaredTools, true)
	// SSE 心跳（F-706）：配置开启时才传 WithKeepAlive，让长静默流持续给客户端
	// 写 `: ping` 注释帧（防中间层按空闲掐连）。关闭时（默认）不传该选项——
	// 出站字节流与旧版逐位一致，心跳 goroutine 也不会起。
	streamOpts := []upstream.StreamOption{
		upstream.WithErrorFrameObserver(func(payload string) { errFrame = payload }),
		upstream.WithMarkupRepair(repair),
	}
	if ka := upstream.SSEKeepAlive(); ka > 0 {
		streamOpts = append(streamOpts, upstream.WithKeepAlive(ka))
	}
	sErr := upstream.StreamHint(w, stats, upstream.FrameHintFunc(func() upstream.HintContext {
		return h.hintContext(bareModel, run.hasImage())
	}), streamOpts...)
	switch {
	case upstream.IsEmptyStreamError(sErr):
		// 上游 200 但空流（0 有效帧）：StreamHint 已写 error 帧 + [DONE]
		// 兜底（HTTP 头已发出只能 200），但这是上游缺陷不是成功——日志/
		// 状态收敛到 502 观测，与非流式 Aggregate 空流→502 upstream_parse
		// 同语义（此前 `_ =` 吞错把失败流记成 200，运维看到假成功）。
		// 只认 IsEmptyStreamError：客户端断连的写失败不误标（人已走，
		// 502 观测没有意义）。
		st.status = http.StatusBadGateway
		st.outcome = reqlog.OutcomeStreamError
		log.Printf("WARN: [server] stream acct=%s model=%s: empty upstream stream (200+0 frames)", logfmt.Label(acct.UID, acct.Nickname), bareModel)
	case upstream.IsFrameTooLarge(sErr):
		// 上游吐了超过 maxSSEFrameBytes 的单帧（不换行的畸形流，F-705）：
		// 这是**上游缺陷**，不是客户端断连（人已走那种）也不是账号限流。
		// HTTP 头已在首帧前提交（200 发不出去别的码），只能以 502 口径记观测；
		// 不罚号不轮转（换任何账号都撞同一份畸形响应）。
		st.status = http.StatusBadGateway
		st.outcome = reqlog.OutcomeStreamError
		log.Printf("WARN: [server] stream acct=%s model=%s: upstream frame too large (>%d bytes)",
			logfmt.Label(acct.UID, acct.Nickname), bareModel, 1<<20)
	case errFrame != "":
		// 上游以 error 帧报错（6004 限流 / 内容拦截 / 审核）：按帧内容分类并
		// 处置账号——**不记成功、不清 11102 负缓存、不绑粘性**。此前这些动作
		// 在流开始前就做了，于是一个正在限流的号被当成健康号，粘性还会把
		// 整个会话钉在它身上，后续每轮都失败。
		kind := upstream.FrameKind(errFrame)
		h.applyErrorPolicy(acct.UID, kind, errFrame, bareModel, nil)
		st.status = http.StatusServiceUnavailable
		st.outcome = reqlog.OutcomeStreamError
		log.Printf("WARN: [server] stream acct=%s model=%s: upstream error frame kind=%s payload=%s",
			logfmt.Label(acct.UID, acct.Nickname), bareModel, kind, logfmt.Truncate(errFrame, 200))
	case sErr != nil:
		// 客户端写失败（断连）：上游帧无恙，账号健康——账号侧照常记成功
		// （与 default 同语义），请求日志归为 Interrupted（人已走，未完成）。
		st.outcome = reqlog.OutcomeInterrupted
		h.cfg.Pool.NoteSuccess(acct.UID)
		h.cfg.Pool.BlockModelClear(acct.UID, bareModel)
		if sessKey != "" && h.cfg.Session != nil {
			h.cfg.Session.Bind(sessKey, acct.UID)
		}
	default:
		// 真成功：这一跳读完且上游没有报错，才记成功并让粘性跟上。
		st.outcome = reqlog.OutcomeSuccess
		h.cfg.Pool.NoteSuccess(acct.UID)
		// 11102 负缓存清命：该账号该模型实测成功，立即解除避让（不必等 TTL 到期）。
		// BlockModelClear 按 "11102" reason 前缀识别，只清 11102 条目、不碰 6004 独立冷却。
		h.cfg.Pool.BlockModelClear(acct.UID, bareModel)
		// 粘性跟随最终成功号：本轮成功的账号成为该会话的粘性绑定（覆盖旧绑定）。
		// 若 sticky 号失败、轮换到别的号成功，这里把会话重绑到新号，多轮对话下一跳不再随机抽。
		if sessKey != "" && h.cfg.Session != nil {
			h.cfg.Session.Bind(sessKey, acct.UID)
		}
	}
	credit, hasCredit := stats.Credit()
	if hit, miss, ok := stats.CacheTokens(); ok {
		st.cacheHit, st.cacheMiss, st.hasCache = hit, miss, true
	}
	run.recordAttempt(acct.UID, stats.Usage(), credit, hasCredit, attemptStarted, stats.TTFB())
	// WARN 信号放 recordAttempt 之后：st.promptTokens 此时才是本次的观测值。
	if st.hasCache {
		cacheMissWarn.noteCacheTokens(bareModel, st.promptTokens, st.cacheHit, st.cacheMiss)
	}
	if n := repair.Converted(); n > 0 {
		// 修复命中：上游本来会把工具调用吐成正文标记，这里已还原成 tool_calls。
		// 打一行便于运维确认「这次不再空转」以及统计发生率。
		log.Printf("INFO: [server] stream acct=%s model=%s: repaired %d native tool call(s) from assistant text (tool names known=%d)",
			logfmt.Label(acct.UID, acct.Nickname), bareModel, n, len(declaredTools))
	} else if m := repair.Seen(); m > 0 {
		// 识别到标记却没还原：说明判定没过（工具名不合规 / 块内夹带正文 /
		// 参数畸形）。单独记一行，排障时能一眼区分「没识别到」与「识别到但拒绝」。
		log.Printf("WARN: [server] stream acct=%s model=%s: saw %d native tool call block(s) but repaired none (tool names known=%d)",
			logfmt.Label(acct.UID, acct.Nickname), bareModel, m, len(declaredTools))
	}
	st.ttfb = stats.TTFB()
	// TTFB 分位数观测（可观测补齐）：只在真拿到首帧时记（0 = 无观测，
	// 空流/断连不该把 0 当成「首帧 0 毫秒就到了」）。
	if h.cfg.TTFB != nil && st.ttfb > 0 {
		h.cfg.TTFB.Observe(bareModel, st.ttfb)
	}
	// usage 缺失时保留 chatStat.toks 的 -1 哨兵（观测缺失 → 显示 "-"），
	// 不写入零值——否则「没观测到 usage」被伪造成「测得 0 token」，
	// 与非流式走 completionTokens 返回 -1 的口径不一致。
	if toks, hasUsage := stats.Tokens(); hasUsage {
		st.toks = toks
	}
	// 成本账本：末帧 usage 带 credit 与 token 总数时记录实测单价，
	// 供下次选号把免费/便宜的号排在前面。
	if hasCredit {
		st.credit = credit
		st.hasCredit = true
		if total, tok := stats.TotalTokens(); tok && total > 0 {
			h.cfg.Pool.NoteModelCost(acct.UID, bareModel, credit, total)
		}
	}
	rc.Close()
	return true
}

// serveNonStream 非流式响应分支：把上游 SSE 聚合为单个 JSON 响应写回。
//
// 与 serveStream 同样恒返回 true（解析失败 502 / 聚合成功 200 都是终态响应）。
func (run *chatRun) serveNonStream(w http.ResponseWriter, acct *auth.Auth, rc io.ReadCloser, attemptStarted time.Time) bool {
	h := run.h
	st := run.st
	bareModel, sessKey := run.bareModel, run.sessKey
	declaredTools := run.declaredTools
	resp, err := upstream.Aggregate(rc)
	rc.Close()
	if err != nil {
		run.recordAttempt(acct.UID, pool.TokenUsageDelta{}, 0, false, attemptStarted, 0)
		// 上游流解析失败：客户端还没看到任何输出，回 502 并告知原因。
		writeOpenAIError(w, http.StatusBadGateway, "upstream_parse", err.Error())
		st.status = http.StatusBadGateway
		st.outcome = reqlog.OutcomeHTTPError
		return true
	}
	// 标记修复（非流式）：把正文里的原生工具调用标记还原成 message.tool_calls。
	// 只在「没有结构化 tool_calls」且「finish_reason 为 stop」时生效（见 dsml.go）；
	// 名单为空时走弱判定（tools 声明在中转环节丢失是常态）。
	mrepair := upstream.RepairAggregatedResponse(resp, declaredTools, true)
	if n := mrepair.Converted(); n > 0 {
		log.Printf("INFO: [server] non-stream model=%s: repaired %d native tool call(s) from assistant text (tool names known=%d)",
			bareModel, n, len(declaredTools))
	} else if m := mrepair.Seen(); m > 0 {
		log.Printf("WARN: [server] non-stream model=%s: saw %d native tool call block(s) but repaired none (tool names known=%d)",
			bareModel, m, len(declaredTools))
	}
	credit, total, hasCredit := usageCreditTotal(resp)
	if usage, ok := resp["usage"].(map[string]any); ok {
		if hit, okH := upstream.UsageCacheHitTokens(usage); okH {
			miss, _ := upstream.UsageCacheMissTokens(usage)
			st.cacheHit, st.cacheMiss, st.hasCache = int64(hit), int64(miss), true
		}
	}
	run.recordAttempt(acct.UID, usageDeltaFromResponse(resp), credit, hasCredit, attemptStarted, 0)
	if st.hasCache {
		cacheMissWarn.noteCacheTokens(bareModel, st.promptTokens, st.cacheHit, st.cacheMiss)
	}
	writeJSON(w, http.StatusOK, resp)
	st.status = http.StatusOK
	st.outcome = reqlog.OutcomeSuccess
	st.toks = completionTokens(resp)
	// 非流式同理：聚合成功（无 error 帧、非空流）才算这一跳成功，事后才记成功/绑粘性。
	h.cfg.Pool.NoteSuccess(acct.UID)
	h.cfg.Pool.BlockModelClear(acct.UID, bareModel)
	if sessKey != "" && h.cfg.Session != nil {
		h.cfg.Session.Bind(sessKey, acct.UID)
	}
	// 成本账本（非流式）：从聚合响应的 usage 取 credit 与 token 总数。
	if hasCredit {
		st.credit = credit
		st.hasCredit = true
		h.cfg.Pool.NoteModelCost(acct.UID, bareModel, credit, total)
	}
	return true
}
