// handler_errpolicy.go 上游错误分类 → 账号处置策略（冷却/禁用/负缓存/熔断）。
//
// 从 handler.go 拆出（2026-11-17）：这是本包唯一决定「一次上游失败该怎么罚账号」的
// 地方，策略密度高、注释比代码长；与 Handler 的装配/鉴权/路由放一起时，改策略要
// 在 900 行里找。单独成文件后，调冷却参数的改动面一眼可见。
//
// 四态纪律相关：这里的每个分支都在把「观察到的上游信号」翻译成「对账号的动作」，
// 未知形态一律走 default 不罚（不把推断当事实去禁用账号）。
package server

import (
	"strings"
	"time"

	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// applyErrorPolicy 按错误分类对账号施加冷却/禁用/熔断策略（最终版状态机）。
// kind 是唯一权威分类（来自 upstream.Classify / ChatStreamContext 的 *Error 信封），
// 此处不再按原始 status 二次判断。仅在 chatCompletions 轮转循环内调用：内容拦截
// 会立即 400 返回，其余种类 continue 换号（continue 前由 rotateBackoff 退避）。
//
// 十条路径，各司其职：
//   - ErrHardCredit → CooldownUntilTomorrow4AM：即时硬冷却到次日 04:00（等签到恢复）。
//   - ErrSoftRate → 优先对齐上游重置墙钟（带「将在 … 重置」时 6004 走模型级豁免、
//     非 6004 走账号级，均不指数堆加）；无重置时间才走有界退避。冷却时长优先采信
//     Retry-After 头（uerr.RetryAfter，body 文案墙钟之外的头形态来源）。
//   - ErrWafBlock → 账号级软冷却：**不 Disable**——WAF 403 是 IP/指纹维频控信号，
//     罚过即走、到期自愈。时长优先 Retry-After 头；缺失按 wafCooldownBase(60s)
//     起 · softStreak 指数、封顶 soft_rate_max 的既有 CooldownSoftRate 有界退避。
//     基数经 jitterDur 抖动（防多账号同相位冷却到期再聚团）。
//   - ErrNotFound → Cooldown(CoolSoft, notFoundCooldown 固定 60s)：短冷却防雪崩。
//   - ErrSessionDead → Disable：session 死亡，永久禁用（需人工重登）。
//   - ErrContentBlocked → 不罚账号；passthrough 首遇触发降级重试，最终仍拦则回 400。
//   - ErrBadParams → 不罚账号，且与 ErrPromptTooLong/ErrImageInvalid 同待遇：
//     调用方在轮转循环内即刻 400 透传原文终止（换号必然同样失败）。
//   - ErrPromptTooLong → 11115：请求的问题不是账号的问题。零动作（不冷却/不熔断/
//     不 NoteError、不喂连败），chatCompletions 已直接透传原文返回不轮转。
//   - ErrImageInvalid → 图片格式/数据无效：请求的问题不是账号的问题（同一 body
//     换任何号都会得到相同的解析错误）。零动作（不冷却/不熔断/不 NoteError、
//     不喂连败），chatCompletions 已直接透传原文返回不轮转。
//   - ErrModelBlocked → BlockModelBackoff：(账号, 模型) 11102 负缓存避让。
//   - ErrServer → NoteError：喂单一连续失败计数器 fails + 累计错误 errTotal，
//     达到 breakerThreshold 触发熔断（指数退避）。
//   - 其他（default：ErrClient/ErrNone）→ 只换号不罚（防雪崩），不喂熔断；ErrClient
//     额外喂连败计数（NoteFailures，issue #114）：未知 4xx 连败 N 次临时出池。
//
// body 仅在 ErrSoftRate/ErrAccountFault 分支用于解析重置时间/分野；model 为请求
// 携带的模型名。uerr 是 ChatStreamContext 返回的分类信封（可携带 RetryAfter）；
// 零值/防御路径下为 nil，冷却时长回落既有计算。
//
// 本函数同时是**上游错误分类计数的唯一汇聚点**：所有轮转失败分支都经过这里，
// 故在开头按 kind 计数一次即覆盖全部分类（不用在每个调用点各写一遍，避免漏记
// 与口径漂移）。
func (h *Handler) applyErrorPolicy(uid string, kind upstream.ErrKind, body, model string, uerr *upstream.Error) {
	if h.cfg.ErrKinds != nil {
		h.cfg.ErrKinds.Inc(kind.String())
	}
	switch kind {
	case upstream.ErrHardCredit:
		// 402 + 余额关键词即积分耗尽：同步冷却到次日 04:00（签到任务 09/21 点恢复），
		// 不需要异步核查（冗余）。立即换号。
		h.cfg.Pool.CooldownUntilTomorrow4AM(uid, "余额不足")
		if h.cfg.Alerting != nil {
			h.cfg.Alerting.NotifyCreditLow(uid, "", 0)
		}
	case upstream.ErrSoftRate:
		// 统一对齐上游重置时间：只要 body 带「将在 … 重置」，无论业务 code 是
		// 6004 还是 11140 rate-limiting 等形态，都精确冷却到该墙钟、绝不指数堆加。
		//   - 模型级（6004）→ CooldownSoftForModel：写 modelCooldowns[model]，切模型豁免。
		//   - 账号级（非 6004）→ CooldownSoftRate：写账号级 until，不产生模型豁免。
		modelRateLimited := upstream.IsModelRateLimit(body)
		if resetAt, ok := upstream.ParseRateReset(body); ok {
			if modelRateLimited {
				h.cfg.Pool.CooldownSoftForModel(uid, h.softCooldown(), resetAt, model, "6004 model rate limit")
				return
			}
			h.cfg.Pool.CooldownSoftRate(uid, h.softCooldown(), resetAt, "429 rate limit")
			return
		}
		// body 无重置文案但带 Retry-After 头 → 冷却到该时刻（不做指数堆加）。
		// 头优先于「有界退避」，但低于 body 重置文案（文案是上游更权威的口径）。
		if uerr != nil && uerr.RetryAfter > 0 {
			h.cfg.Pool.CooldownSoftRate(uid, h.softCooldown(), time.Now().Add(uerr.RetryAfter), "429 rate limit (retry-after)")
			if modelRateLimited {
				h.cfg.Pool.RecordModelRateLimitAudit(uid, model, "6004 model rate limit (reset unknown)")
			}
			return
		}
		// 无重置时间 → 账号级有界退避（soft_rate 基数起、softStreak 翻倍、封顶
		// soft_rate_max；已在冷却中的兜底探测不翻倍）。基数取 h.softCooldown()
		// （热改优先），管理面板改 soft_rate 后立即生效。
		h.cfg.Pool.CooldownSoftRate(uid, h.softCooldown(), time.Time{}, "429 rate limit")
		if modelRateLimited {
			h.cfg.Pool.RecordModelRateLimitAudit(uid, model, "6004 model rate limit (reset unknown)")
		}
	case upstream.ErrWafBlock:
		// WAF 403（无业务信封拦截形态）。软冷却复用 CooldownSoftRate 家族：基数
		// wafCooldownBase（60s，抖动后落 [45s,75s]）、softStreak 指数升级、封顶
		// soft_rate_max、冷却中兜底探测不翻倍——全部继承既有语义。
		// Retry-After 头优先（WAF 拦截页可能带该头）。不 Disable。
		if uerr != nil && uerr.RetryAfter > 0 {
			h.cfg.Pool.CooldownSoftRate(uid, jitterDur(wafCooldownBase), time.Now().Add(uerr.RetryAfter), "waf 403 block (retry-after)")
			return
		}
		h.cfg.Pool.CooldownSoftRate(uid, jitterDur(wafCooldownBase), time.Time{}, "waf 403 block")
	case upstream.ErrSessionDead:
		h.cfg.Pool.Disable(uid, "12153 session dead")
		if h.cfg.Alerting != nil {
			h.cfg.Alerting.NotifyAccountDown("session_dead", uid, "", "12153 session dead")
		}
	case upstream.ErrNotFound:
		// 404 短冷却（软冷却），防雪崩。固定 notFoundCooldown，不随 soft_rate 退避：
		// 偶发路径缺失不是限流信号，不该按限流惩罚升级。
		h.cfg.Pool.Cooldown(uid, pool.CoolSoft, notFoundCooldown, "upstream 404")
	case upstream.ErrAccountFault:
		// 账号级授权/配额故障按 msg 分野（口径与 Classify 的 accountFaultMarkers 一致）：
		//   - "request illegal"（code 11140）→ 账号级**授权封禁**：硬禁用（Disable）。
		//   - 14017（trial not activated）→ register 未完成，补完 register 后可能自愈，
		//     **保持软冷却**（禁用会让用户补完 register 后仍无法用）。
		// 大小写不敏感（与 Classify 的 marker 匹配同口径）。
		if strings.Contains(strings.ToLower(body), "request illegal") {
			h.cfg.Pool.Disable(uid, "account banned by upstream (11140 request illegal), re-login required")
			if h.cfg.Alerting != nil {
				h.cfg.Alerting.NotifyAccountDown("account_disabled", uid, "", "11140 request illegal, re-login required")
			}
			return
		}
		h.cfg.Pool.Cooldown(uid, pool.CoolSoft, h.softCooldown(), "account fault (14017)")
	case upstream.ErrServer:
		// 5xx 上游故障：Classify 已把 ≥500 判为 ErrServer，在此喂熔断计数（不再手写 status>=500）。
		h.cfg.Pool.NoteError(uid)
	case upstream.ErrContentBlocked:
		// 内容策略拦截（误报）：内容问题非账号问题，不罚账号（无冷却/熔断/NoteError）。
		// passthrough 模式由 chatCompletions 内降级重试处理；custom 模式本不会到此分支。
	case upstream.ErrPromptTooLong:
		// 11115「prompt is too long」：请求的问题不是账号的问题（同一 body 换任何
		// 号都超限）。零动作（不冷却/不熔断/不 NoteError，同 ErrContentBlocked 待遇），
		// chatCompletions 已直接透传原文返回不轮转——该分支只为文档完备。
	case upstream.ErrImageInvalid:
		// 图片格式/数据无效：请求的问题不是账号的问题（同一 body 换任何号都会
		// 得到相同解析错误）。零动作，chatCompletions 已 fail-fast 透传。
	case upstream.ErrBadParams:
		// 请求体解析失败（400 + Unmarshal chat params failed / 11101）：发给上游的 body
		// 有问题（网关侧不再截断，均为客户端畸形 JSON）。换了账号照样 400，
		// 不罚账号（无冷却/熔断/NoteError，同 ErrContentBlocked 待遇）；chatCompletions
		// 已 fail-fast 400 透传原文、终止轮转——「换号可能有不同模型权限」属 11102
		// （ErrModelBlocked）的分类域，与本类无关。
	case upstream.ErrModelBlocked:
		// 11102「该后端无此模型」：(账号, 模型) 负缓存避让。复用 modelCooldowns 机制
		// （与 6004 同域），选号侧 healthyForModel 对该账号自动避开该模型。
		// 立即换号（本轮 continue），该账号该模型冷却，下次选号避开。
		//
		// 白名单：模型名不在已知目录里（客户端拼错了），不写负缓存——
		// 这种情况换任何号都会错，缓存只是浪费槽位（还会污染面板，
		// 比如测试发了一个 nonexistent-model-xyz 就锁 6h）。
		if h.isKnownModel(model) {
			h.cfg.Pool.BlockModelBackoff(uid, model, upstream.ModelBlockReason)
		}
	default:
		// 其余（ErrClient/ErrNone）：只换号不罚（防雪崩），不喂熔断。
		// ErrClient（未知 4xx）喂连败计数（issue #114）：连续 N 次该形态失败 →
		// 账号临时出池（NoteFailures 达阈降权），单次/偶发不罚（不误伤）。ErrNone
		// 到这里属防御路径（status>=400 但分类成功），语义不明不喂。
		if kind == upstream.ErrClient {
			h.cfg.Pool.NoteFailures(uid)
		}
	}
}
