// chatrun_serve.go 两个响应终结分支：流式透传与非流式聚合。
//
// 从 chatrun.go 拆出（2026-11-17）：这两个函数是本包唯一「直接往 ResponseWriter 写、
// 且决定这次请求算不算成功」的地方（含账号处置、粘性绑定、成本台账、缓存观测）。
// 与选号/轮转（chatrun.go 剩下的部分）责任不同，独立成文件后改动面更清楚。
package server

import (
	"io"
	"log"
	"net/http"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/logfmt"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/upstream"
)

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
		//
		// 口径澄清（2026-10-18 实测二改，勿再误读）：下面的 st.status 进的是**聊天表格日志**
		// （logging.go 的 logChatRowEx → chatLogOut），而 chatLogOut 在生产里由 main 经
		// SetChatLogOutput 注入 MultiWriter，**镜像进面板的 /panel/api/logs 环形缓冲**。
		// 所以：面板「日志」视图能看到这个 502；但面板**统计**与 reqlog 归档看不到——
		// 它们的 Status 取 handler 层的 HTTP 码，流式响应头早已发出 → 恒 200。
		// 要不要把 st.status 也带进 Event（改归档与统计口径），另议。
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
	if hit, miss, missKnown, ok := stats.CacheTokens(); ok {
		if missKnown {
			st.cacheHit, st.cacheMiss, st.hasCache = hit, miss, true
		} else {
			// 与非流式同口径：miss 未知不写 0，只标记未知（见 CacheTokens 注释）。
			st.cacheHit, st.cacheMissUnknown = hit, true
		}
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
	credit, total, hasCredit, creditUnknown := usageCreditTotal(resp)
	if creditUnknown {
		// 未知不得静默：不记样本（hasCredit=false 已挡住 NoteModelCost），但要留下可查的痕迹。
		// 该 (uid,model) 的「成本未知」由 TrackCostUnknown 登记的条目保持未消解（不调用
		// NoteModelCost 即不消解），因此 /metrics 的 unknowns_open 会持续非零 —— 这是信号，不是故障。
		log.Printf("WARN: [server] non-stream acct=%s model=%s: usage.credit 非法/缺失（类型错或非数值），"+
			"本次不写成本台账（unknown，非 0）；该组合的成本未知保持未消解",
			logfmt.Label(acct.UID, acct.Nickname), bareModel)
	}
	if usage, ok := resp["usage"].(map[string]any); ok {
		if hit, okH := upstream.UsageCacheHitTokens(usage); okH {
			miss, okM := upstream.UsageCacheMissTokens(usage)
			if okM {
				st.cacheHit, st.cacheMiss, st.hasCache = int64(hit), int64(miss), true
			} else {
				// miss 未知（没给 prompt_cache_miss_tokens 且 prompt 不可用）：**不得**当 0 ——
				// 0 会被当成「全部命中」，把命中率抬到 100% 并压掉低命中率告警（issue #92）。
				st.cacheHit, st.cacheMissUnknown = int64(hit), true
			}
		}
	}
	run.recordAttempt(acct.UID, usageDeltaFromResponse(resp), credit, hasCredit, attemptStarted, 0)
	if st.hasCache {
		cacheMissWarn.noteCacheTokens(bareModel, st.promptTokens, st.cacheHit, st.cacheMiss)
	}
	if run.outProto == "responses" {
		// F8：/v1/responses 非流式出口——chat 响应转 response 对象（转换 B）。
		writeResponsesObject(w, resp, run.bareModel)
	} else {
		writeJSON(w, http.StatusOK, resp)
	}
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
