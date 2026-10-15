// chatflow.go chatCompletions 阶段函数的落点。
//
// 背景：chatCompletions 曾是一个 847 行的单函数，同时承担准入、解析、会话派生、
// 出站改写、轮转重试、末端错误构造六件事。拆分按**原函数注释里已有的边界**进行
// （不是新设计的划分），每个阶段函数提取一段，主函数退化为编排。
//
// 拆分纪律：
//   - 每次只抽一个阶段，抽完立即跑 TestChatCompletionsBehaviorSnapshot（7 场景
//     行为快照），全绿才提交；
//   - 跨阶段共享状态用显式参数或结构体承载，**禁止**提升为包级变量（那是把局部
//     复杂度转成全局复杂度，属于假重构）；
//   - 纯计算与副作用分离：能做成纯函数的优先做成纯函数（可单独测试、无隐式耦合）。
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/session"
	"yg-gateway/internal/upstream"
)

// chatPrep 阶段①（准入之后：读取 + 单次解析 + 模型/Key 校验）的产物。
//
// 为什么用结构体而不是多返回值：阶段① 产出 11 个值，全部被后续阶段消费；
// 多返回值会让调用点变成一长串解构赋值，且新增字段时到处改签名。
//
// 纪律：这是**请求级**状态，只在本请求的生命周期内存在，绝不提升为包级变量。
type chatPrep struct {
	clientIP string
	body     []byte
	reqView  session.RequestView
	// viewOK 报告 body 是否被成功解析成 JSON 对象视图。下游的「是否带图」判定
	// 依赖它选路径：视图可用时走视图（与其余消费方共用同一份 messages 子树），
	// 不可用时回落字节路径（与旧行为一致）。
	viewOK        bool
	modelStr      string
	bareModel     string
	upstreamModel string
	realm         string
	modelRate     string
	authKey       *httpauth.Key
	peekStream    bool
	declaredTools map[string]bool
	st            *chatStat
}

// prepareChatRequest 阶段①：客户端 IP 提取 → 读 body（含上限截断）→ 单次解析
// → 模型名/realm/别名解析 → 多 Key 约束校验 → 建请求级统计对象。
//
// 契约：**失败时已写好响应**（413/400），返回 ok=false，调用方直接 return。
//
// 刻意不搬走的两件事（留在主函数，因为它们的作用域必须是整个请求生命周期）：
//   - `defer h.cfg.Pool.ReleaseGlobal()`：搬进来会在本函数返回时提前释放全局
//     在途名额，让准入控制形同虚设（超限不再被拦）；
//   - `defer st.done()`：搬进来会让统计在阶段① 结束时就落日志，后面所有阶段
//     的耗时统计全部丢失。
func (h *Handler) prepareChatRequest(w http.ResponseWriter, r *http.Request) (*chatPrep, bool) {
	p := &chatPrep{}

	// 客户端 IP 提取（F-002：按受信任代理判定；未配置受信任代理时不采信伪造的
	// X-Forwarded-For，回落 TCP 对端）。透传与否由 upstream.PassthroughIP 决定，
	// 但这里先保证「采信」这件事是安全的。
	p.clientIP = extractClientIP(h.cfg.TrustedProxy, r)

	// 入站请求体上限（F-001）：MaxBodyBytes > 0 时用 MaxBytesReader 截断，超大
	// body 不会整体读进内存（否则 io.ReadAll 会把几 GB 的 body 一次性吞下——DoS）。
	// 超限时 ReadAll 返回 *http.MaxBytesError，就地 413（语义精确，客户端可据此调整）。
	// 缺省 0 = 不设上限（保留旧行为）。#41 的截断防御语义保留在读错误路径：
	// 移除预拦截后，截断只可能来自客户端自己断流或 MaxBytesReader，读 body 出错
	// 就地 4xx，不把半截 JSON 喂上游 unmarshal 报 unexpected EOF 冤枉罚号。
	var body []byte
	var err error
	if h.cfg.MaxBodyBytes > 0 {
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	} else {
		body, err = io.ReadAll(r.Body)
	}
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large",
				fmt.Sprintf("request body exceeds %d bytes", mbe.Limit))
			return nil, false
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return nil, false
	}
	p.body = body

	// 入站请求体**只解析一次**（F-701）：顶层惰性视图 + 子树按需解析。
	//
	// 此前每个消费方各自全文档 json.Unmarshal（peek / 日志取 model / 工具名单 /
	// 会话键 / 轮级键 / conversationId / 是否带图），大上下文请求下是纯 CPU+GC
	// 支出。改成一次顶层解析后，messages / tools 子树只在真有消费方需要时才解析，
	// 且同一请求内只解析一次（视图内缓存）。
	//
	// 解析失败（body 不是 JSON 对象）→ viewOK=false，各消费方回落既有空值语义，
	// 与旧实现「各自解析失败」逐位一致（不引入新的失败模式）。
	reqView, viewOK := session.ParseRequestView(body)
	if !viewOK {
		// 兜底：视图不可用时退化为零值视图（各取值方法返回空值，与旧语义一致）。
		reqView = session.RequestView{}
	}
	p.reqView = reqView
	p.viewOK = viewOK

	// peek 只取 model 的原始字节与 stream 布尔：Model 声明为 json.RawMessage
	// 是为了判定原始 JSON 类型（missing/null/string/other 四态），不需要对整份
	// body 再解一遍。
	peekModel := reqView.Model()
	p.peekStream = reqView.Stream()
	modelKind := modelKindOf(peekModel) // "missing" / "null" / "string" / "other"
	// 字符串形态才解出裸值；missing/null/other 一律空串（与旧 peek.Model 同语义：
	// json.Unmarshal 到 string 时非字符串会被静默置零，result 同样是空串）。
	modelStr := ""
	if modelKind == "string" {
		_ = json.Unmarshal(peekModel, &modelStr)
	}
	p.modelStr = modelStr

	// 工具名名单：上游在没有 tools 的请求里会把工具调用吐成原生标记文本
	// （见 upstream/dsml.go），修复层用它做严格判定。名单来自「本请求声明的 tools」
	// 与「会话历史里出现过的工具名」两个来源——实测 tools 声明会在中转环节丢失，
	// 只认前者会让修复层在最需要它的场景下失效。
	// 在提示词/模型名改写之前取（那些改写不动 tools / messages[].tool_calls 子树）。
	// 走视图路径：与会话键/轮级键共用同一份 messages 子树，不重复解析。
	p.declaredTools = upstream.ToolNameAllowlistView(&reqView)

	// realm 前缀解析（D6）：model 名可能带 "[realm:]" 前缀。剥出 realm + bareModel，
	// bareModel 用于选号/粘性/出站 body 重写（前缀是网关侧路由协议，上游只认裸名）。
	// 裸名 → ("cn", 原串)，CN 现状零回归。
	realm, bareModel := resolveModel(modelStr)
	p.realm = realm
	p.bareModel = bareModel

	// 模型别名映射：bareModel 是客户端原始裸名（选号/白名单/用量/回包全用它）；
	// upstreamModel 是出站 body 真正发给上游的名。无映射时二者相等（零回归）。
	p.upstreamModel = applyModelAliases(bareModel, h.loadLive().ModelAliases)
	if h.cfg.Upstream != nil {
		p.modelRate = h.cfg.Upstream.ModelRate(realm, bareModel)
	}

	// 多 Key 约束校验（T6）：鉴权命中的 Key 的 realm / models 白名单，在选号/出站
	// 之前执行（ADR-001 §五）。不符合即 400，零上游消耗。
	// authKey 放进 chatPrep：下方用量记录要用它的 ID 做归因（ADR-001 F-005 二期），
	// 复用同一次匹配结果，不重新鉴权（身份一致性不变量）。
	p.authKey = authKeyFrom(r)
	if p.authKey != nil {
		if code, msg := h.checkKeyConstraints(p.authKey, modelStr, realm, bareModel, modelKind); code != "" {
			writeOpenAIError(w, http.StatusBadRequest, code, msg)
			return nil, false
		}
	}

	// 请求级统计：出口即打一行表格日志（任何路径都会走到）。
	// model 直接复用已解析的 modelStr（省掉一次全文档解析）；不可解析时
	// modelStr 为空 → 沿用 parseModelFromBody 的 "-" 兜底口径。
	p.st = newChatStat(time.Now(), modelStr, p.peekStream)
	if tr := requestTraceFrom(r); tr != nil {
		tr.stat = p.st
		// 来源在 ServeHTTP 入口采集（此时才知道开关与请求头），此处转交给统计对象，
		// 让 stdout 流水行与归档事件共用同一份来源值，两处不会漂移。
		p.st.clientIP, p.st.userAgent = tr.clientIP, tr.userAgent
	}
	return p, true
}

// finalErrorOf 把末端错误映射成 (status, code, msg, gateway_hint)。
//
// 这是 chatCompletions 阶段 ⑤「末端错误构造」的**纯计算部分**：不写响应、不改
// 统计，只做「错误 → 展示三元组」的映射。写回由调用方负责，因此本函数可以脱离
// HTTP 上下文单独测试。
//
// 语义（与原内联实现逐位一致）：
//   - 默认：503 + no_healthy_account（本地调度错误，无上游原文可透传，不编造）；
//   - 上游超时：区分文案 upstream_timeout（轮转已在传输层止损，排查方向与"没有
//     可用账号"完全不同）；
//   - 上游 *Error：按 Kind 映射状态码/业务码，且**上游原文优先**（透传
//     code/msg/requestId，不拼本地前缀）；
//   - 模型级阻塞：优先级最高，覆盖为 400 + model_unavailable（"你选的模型当前
//     不可用"不是"服务端过载"，客户端不该按可重试错误处理）；
//   - key 绑定账号组却选不到号：追加运维提示。
//
// 参数说明：hasImage 是 reqHasImage() 的**求值结果**（原闭包是惰性的，调用方在
// 需要时才求值；这里显式传入，避免本函数隐式持有请求态）。
func (h *Handler) finalErrorOf(
	lastErr error,
	modelBlock pool.ModelBlockStatus,
	groupFilteredOut bool,
	authKey *httpauth.Key,
	bareModel string,
	hasImage bool,
) (status int, code, msg, hint string) {
	status = http.StatusServiceUnavailable
	code = "no_healthy_account"
	msg = "all accounts are temporarily unavailable, please retry later"

	// 上游超时：轮转已在传输层分支止损（见 isUpstreamTimeout），这里给一条**能区分**
	// 的文案，别混进"没有可用账号"——两者的排查方向完全不同。
	if errors.Is(lastErr, errUpstreamTimeout) {
		code = "upstream_timeout"
		msg = "upstream timed out: rotation stopped (another account would hit the same slow upstream), please retry later"
	}

	// gateway_hint（末端透传）：上游错误按 Kind + 原文 + 请求形态判定；本地调度类
	// 错误（无上游原文）固定 no_healthy_account hint。
	hint = upstream.NoHealthyAccountHint()

	// upstreamMsgPassed 记录 msg 是否已被上游原文占据：模型级阻塞分支据此决定要不要
	// 覆盖 msg（上游原文优先，含 requestId）。
	upstreamMsgPassed := false
	var ue *upstream.Error
	if errors.As(lastErr, &ue) {
		hint = h.hintOf(ue.Kind, ue.Msg, bareModel, hasImage, ue)
		switch ue.Kind {
		case upstream.ErrSoftRate:
			status = http.StatusTooManyRequests
			code = "rate_limit_exceeded"
			msg = "rate limited: all accounts are cooling down, please wait a moment and try again"
		case upstream.ErrWafBlock:
			if h.wafIP.active() {
				// IP 级拦截措辞（fail-fast 终止路径）：网关出口 IP 被 WAF 拦截、
				// 轮转已止损、窗口过后自动解除。客户端提前重试无意义（换号不换 IP）；
				// 有上游原文时原文优先（下方统一）。
				code = "waf_ip_blocked"
				msg = "waf ip-level block: upstream firewall is blocking the gateway IP, rotation stopped; retry after the block window expires"
			}
		case upstream.ErrModelBlocked:
			// 11102「该后端无此模型」：上游原文（下方统一透传）已经写清了原因，但
			// code 此前停在默认的 no_healthy_account —— 那是"服务端过载"的语义，
			// 客户端据此会不断重试（#81 里就是 503 → 客户端自动重试 5 次），而真正
			// 该做的是换个模型。用 400 + 明确的 code 把不可重试的性质讲清楚。
			code = "model_unavailable"
			status = http.StatusBadRequest
		}
		if s := strings.TrimSpace(ue.Msg); s != "" {
			// 上游原文优先：透传 code/msg/requestId，不拼接本地前缀。
			msg = s
			upstreamMsgPassed = true
		}
	}

	// 模型级阻塞覆盖上面的通用文案（放在最后 = 优先级最高）。
	//
	// 为什么能覆盖 code 而不丢信息：modelBlock.Reason 就是 BlockModelBackoff 存下的
	// 上游原因，已在 hint 里原样带出，所以覆盖 message 不会丢掉上游信息，反而补上
	// 了上游不会告诉客户端的两件事——「有几个号被挡」和「最早什么时候解封」。
	//
	// 用 400 而非 503：这是"你选的这个模型当前不可用"，不是"服务端暂时过载"。
	// 换号重试必然同样失败，客户端不该按可重试错误处理。
	if modelBlock.Blocked {
		code = "model_unavailable"
		// 有上游原文时保留它（含 requestId，用户要拿去向上游反馈），只把"有几个号
		// 被挡、最早何时解封"这类本地调度信息放进 gateway_hint。
		if !upstreamMsgPassed {
			msg = "model is unavailable on every account (per-model cooldown), try another model"
		}
		hint = fmt.Sprintf("model_blocked: %d account(s) cooling down this model", modelBlock.Count)
		if !modelBlock.Until.IsZero() {
			hint += "; earliest unblock at " + modelBlock.Until.Format(time.RFC3339)
		}
		if s := strings.TrimSpace(modelBlock.Reason); s != "" {
			hint += "; upstream: " + s
		}
		status = http.StatusBadRequest
	}

	// P1-B：key 绑定了账号组但组里没选到号 → 在 hint 里给运维明确提示。
	// 只在「不是模型级阻塞」时加（modelBlock 已覆盖那一类，避免信息重复）。
	if groupFilteredOut && !modelBlock.Blocked && authKey != nil {
		hint += fmt.Sprintf("; note: API key is bound to account group %q, no healthy account in this group — check auth files group tag or the group name typo", authKey.Group)
	}

	return status, code, msg, hint
}
