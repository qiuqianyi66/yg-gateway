// errors.go 上游错误分类：ErrKind/Error、标记词表、Classify 与 Retry-After/RateReset 解析。
// 从 client.go 机械拆出（2026-10-22 R-201），导出符号签名与语义零变化。
package upstream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ErrKind 错误分类，pool 据此决定冷却时长。
type ErrKind int

// EvidenceLevel 错误判定的证据强度（四态纪律的错误分类版）。
//
// 为什么需要：现在的 13 种 ErrKind 告诉我们"是什么错误"，但没告诉我们
// "这个判定的确定性有多高"。402 状态码是权威信号（余额就是没了），但
// body 里出现"积分不足"四个字只是关键词匹配——上游可能在不同语境下
// 用这个词。把猜测当事实会导致基于噪声做严厉操作（禁用账号、长冷却）。
//
// 三级标签：
//   - High：确定性证据。HTTP 状态码本身就是权威信号（402/429/401/5xx），
//     或精确业务码（11102/14018/11115/11135/11101）、精确词（12153）。
//     可以直接执行严厉操作（禁用账号、长冷却、不累计错误计数）。
//   - Medium：中等证据。body 关键词匹配，但关键词有歧义空间
//     （"积分不足"可能指额度也可能指余额，"rate limit"可能指账号也可能指模型）。
//     执行操作要保守一些（短冷却、累计错误计数但不直接禁用）。
//   - Low：低证据/推断。纯文案猜测，没有结构化证据
//     （"blocked by security policy"是我们猜测上游是误报）。
//     最保守处理（降级重试、不罚账号、观察）。
type EvidenceLevel int

const (
	EvidenceNone   EvidenceLevel = iota // 成功
	EvidenceHigh                        // 确定性证据：状态码/精确业务码/精确词
	EvidenceMedium                      // 中等证据：body 关键词匹配，有歧义空间
	EvidenceLow                         // 低证据/推断：纯文案猜测，无结构化证据
)

func (e EvidenceLevel) String() string {
	switch e {
	case EvidenceHigh:
		return "high"
	case EvidenceMedium:
		return "medium"
	case EvidenceLow:
		return "low"
	default:
		return "none"
	}
}

const (
	ErrNone           ErrKind = iota // 成功
	ErrHardCredit                    // 余额不足（402 或 body 关键词）→ 长冷却
	ErrSoftRate                      // 429 软限流 → 短冷却
	ErrSessionDead                   // 401 + 12153 offline session 失效 → 禁用
	ErrNotFound                      // 404 上游偶发 → 短冷却，不累计错误计数（防雪崩）
	ErrServer                        // 5xx 上游故障
	ErrContentBlocked                // 内容策略拦截（400 + 审核文案）→ 不罚账号，走降级重试
	ErrBadParams                     // 请求体解析失败（400 + Unmarshal chat params failed / 11101）→ 请求级错误：不罚号、不轮转，末端 400 透传原文
	ErrAccountFault                  // 账号级授权/配额故障（11140 request illegal / 14017 trial not activated）→ 冷却轮换，不无限重试
	ErrModelBlocked                  // 11102「该后端无此模型」→ (账号,模型) 负缓存避让，切模型/切账号
	ErrWafBlock                      // 403 + 非业务信封体（APISIX WAF 拦截页/空体）→ 账号软冷却 + 抖动退避
	ErrPromptTooLong                 // 11115「prompt is too long」→ 请求级错误（上下文超限是请求的问题非账号的问题）：不罚号、不轮转，末端透传原文
	ErrImageInvalid                  // 图片请求格式/数据无效 → 请求级错误：不罚号、不轮转，末端透传原文
	ErrClient                        // 其他 4xx / 业务错误
)

func (k ErrKind) String() string {
	switch k {
	case ErrHardCredit:
		return "hard_credit"
	case ErrSoftRate:
		return "soft_rate"
	case ErrSessionDead:
		return "session_dead"
	case ErrNotFound:
		return "not_found"
	case ErrServer:
		return "server"
	case ErrContentBlocked:
		return "content_blocked"
	case ErrBadParams:
		return "bad_params"
	case ErrModelBlocked:
		return "model_blocked"
	case ErrWafBlock:
		return "waf_block"
	case ErrPromptTooLong:
		return "prompt_too_long"
	case ErrImageInvalid:
		return "image_invalid"
	case ErrAccountFault:
		return "account_fault"
	case ErrClient:
		return "client"
	default:
		return "none"
	}
}

// Error 带分类的上游错误。
type Error struct {
	Kind   ErrKind
	Status int
	Msg    string
	// Evidence 错误判定的证据强度（见 EvidenceLevel 注释）。
	// 为什么放在 Error 而不是 ErrKind：同一个 ErrKind 可能有不同的判定路径
	// （比如 ErrHardCredit 可能来自 402 状态码=High，也可能来自 body 关键词=Medium）。
	// 证据强度是这次判定的属性，不是错误类型的属性。
	Evidence EvidenceLevel
	// UpstreamRequestID 上游响应里的 requestId 字段（从错误 body 解析）。
	// 用途：拿着这个 ID 去查上游日志，关联网关请求和上游侧的 trace。
	// 零值 = 上游响应里没有 requestId（或者成功响应我们不解析）。
	UpstreamRequestID string
	// RetryAfter 上游明示的等待时长（Retry-After 秒 / retry-after-ms /
	// x-ratelimit-reset 头解析，见 ParseRetryAfter）。零值 = 上游未明示，
	// 冷却时长回落调用方计算值。挂载点选在 Error 信封：Kind 决定「罚不罚」，
	// RetryAfter 决定「罚多久」，同为上游响应的一等公民。
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	return fmt.Sprintf("upstream %s (http %d): %s", e.Kind, e.Status, e.Msg)
}

// hardMarkers 余额不足关键词（小写比较 + 中文原文比较双通道）。
var hardMarkers = []string{
	"insufficient credit", "no credit", "credit exhausted", "credits exhausted", "out of credit",
	"quota exceeded", "quota exhaust", "payment required", "credit not enough",
	"not enough credit",
	"积分不足", "额度不足", "余额不足", "积分用完", "额度用尽", "没有积分",
}

// softRateMarkers 限流/节流关键词（小写比较 + 中文原文比较双通道）。
// 上游在状态码非 429 时也会返回限流语义（如 200 + code 11140
// "The model provider is rate-limiting requests."、400 + "rate limit"），
// 此类响应若不识别，账号既不被冷却也不喂熔断，下次请求仍会被选中（issue #28）。
//
// 词表按子串匹配，宁缺毋滥：只收录明确指向「请求速率/模型用量被节流」的措辞。
// 连字符形式（rate-limiting / rate-limited）需单列——Contains 不跨 '-'。
// "too many" 会命中 "too many tokens" 这类客户端参数错误，代价是该号被软冷却
// 一个 SoftCooldown（默认 60s）后自愈，远小于漏判限流导致反复选中同一号的代价。
var softRateMarkers = []string{
	"rate limit", // rate limit / rate limits / rate limiting
	"rate-limiting",
	"rate-limited",
	"too many requests",
	"too many",
	"usage limit", // usage limit reached / model usage limit exceeded（用量节流，非计费余额）
	"请求过于频繁", "限流",
}

var sessionDeadMarkers = []string{"Offline user session not found", "12153"}

// accountFaultMarkers 账号级授权/配额故障关键词（大小写不敏感子串匹配）。
//
// 定位：这类错误是**账号本身状态**决定的本机故障，不是请求格式、不是临时限流、
// 也不是内容误报——继续重试只会反复刷上游风控/配额检查，必须把该账号冷却轮换。
//   - "request illegal"（code 11140）→ 上游 auth/auth_forbidden，账号级授权风控，
//     需重新 OAuth 登录才能恢复，短冷却只能阻止继续送死。
//   - code 14017（"trial not activated" / "The trial version is not yet activated"）→
//     上游 quota/quota_not_activated，register 未完成的试用未激活账号，同样账号级。
//
// 注意 11140 **不能**按 code 判定：该 code 也承载模型级限流文案（"The model provider
// is rate-limiting requests."），那种场景必须保持 ErrSoftRate（下方 softRateMarkers
// 后判定）。故此处只收 msg 关键词 "request illegal"（auth_forbidden 的真实文案）。
// 14017 文案唯一（无软限流歧义），可安全收录。
var accountFaultMarkers = []string{
	"request illegal",
	"trial not activated",
	"trial version is not yet activated",
}

// contentBlockedMarkers 内容策略拦截关键词（大小写不敏感子串匹配）。
//
// 定位：上游按逐字精确指纹审核，system 来源的模板句（如 Claude Code/Codex
// 注入指令）触发 HTTP 400 + 以下文案。这是「误报」（合法流量被审核误杀），
// 非账号问题——该账号余额健康、未限流、session 未死，故 ErrContentBlocked
// 在 applyErrorPolicy 中不罚账号（无冷却/熔断/NoteError），改由网关降级重试。
var contentBlockedMarkers = []string{
	"blocked by security policy",
	"unapproved channel",
	"illegal api invocation",
}

// badParamsMarkers 请求体解析失败关键词（issue #41 连带）：HTTP 400 + 上游
// "Unmarshal chat params failed..."（code 11101）。这是"发给上游的 body 有问题"，
// 与账号健康无关——不罚号，但仍轮转（commit B）。
var badParamsMarkerMsg = "Unmarshal chat params failed"

// invalidImageMarkers 图片请求格式/数据无效（HTTP 400）的**文案**形态。这类错误由
// 请求内容决定，不是账号问题：换账号不会改变同一 body 的解析结果。上游常见形态包括
// `Parse message failed: invalid image_url content`、invalid_image_data、
// `replace the image`。
//
// 业务码 11135 不放在这里：code 判定必须容忍 JSON 空白（`"code": 11135`），
// 字面量 marker 只能覆盖紧凑形态，故统一走 codeMarker（见 Classify 的 400 分支，
// 与 hint.go 的 isInvalidImageData 同口径；上游 5d5223d 的 Copilot review 修复）。
var invalidImageMarkers = []string{
	"invalid image_url content",
	"invalid_image_data",
	"replace the image",
}

// 定位：上下文超限是**请求的问题不是账号的问题**——同一个 body 换任何账号发都会
// 超限，与 WAF fail-fast 同哲学（确定与账号无关的错误不罚号不轮转，白白浪费健康号
// 的请求配额）。marker 双通道：
//   - `"code":11115`：业务信封 code 字段（JSON 空格容差；`"code":"11115"` 字符串
//     形态也命中）；
//   - "prompt is too long"：msg 文案（大小写不敏感）。
//
// 只在 400/404/413 请求级状态码上判（429+11115 概率极低且属限流语义优先，
// 5xx 属服务端故障优先）。误判代价（好 body 被归 prompt_too_long）：不罚号 +
// 不轮转 + 透传原文，客户端看到上游原文可自行排查，代价可控。
var promptTooLongMarkers = []string{
	`"code":11115`,
	`"code": 11115`,
	`"code":"11115"`,
	"prompt is too long",
}

// isPromptTooLongStatus 11115 只在请求级 4xx 上判（见 promptTooLongMarkers 注释）。
func isPromptTooLongStatus(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusNotFound ||
		status == http.StatusRequestEntityTooLarge
}

// alreadyCheckinMarkers "今天已签到"关键词（上游对重复签到返回 code!=0，
// 实测 code=10001/14001 "今天已签到"/"今日已签到"）。只对 *Error.Msg 做包含匹配，
// 网络层/解析层错误不在此识别（见 IsAlreadyCheckin）。
var alreadyCheckinMarkers = []string{"已签到", "already"}
var badParamsMarkerCode = `"code":11101`

// softRateResetLoc 上游 429 6004 文案中的重置时间固定按 UTC+8 解释（上游文案如此，
// 与容器时区无关）。
var softRateResetLoc = time.FixedZone("UTC+8", 8*60*60)

// SoftRateResetLoc 暴露重置时间的固定时区（供测试构造/断言同一时区口径）。
func SoftRateResetLoc() *time.Location { return softRateResetLoc }

// modelRateLimitCode 明确指向「模型级 429 限流」的业务 code。
// 上游用它表达"该模型的使用量超限"（code 6004，msg 带「将在 … 重置」），
// 而不是账号整体被限流——账号健康，只是这个模型此刻被限（issue #31）。
const modelRateLimitCode = "6004"

// softRateResetPatternCN/EN 匹配重置文案（CN「将在 … 重置」/ global 域英文
// "reset at <固定格式时间>"），捕获中间的时间串。
const softRateResetPatternCN = `将在 (.+?) 重置`
const softRateResetPatternEN = `(?i)reset at (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})`

// 限流判定正则预编译为包级 var：IsModelRateLimit / ParseRateReset 在每次错误
// 分类、每个限流 body 上调用，函数体内 MustCompile 是纯浪费；错误风暴（429
// 轰炸）时尤甚。模式串均为纯常量。regexp 并发安全（匹配只读），无需额外锁。
var (
	reModelRateLimit  = regexp.MustCompile(`"code"\s*:\s*"?` + modelRateLimitCode + `"?`)
	reSoftRateResetCN = regexp.MustCompile(softRateResetPatternCN)
	reSoftRateResetEN = regexp.MustCompile(softRateResetPatternEN)
)

// softRateTimeLayout 上游重置时间的格式（无时区后缀；时区固定 UTC+8）。
const softRateTimeLayout = "2006-01-02 15:04:05"

// IsModelRateLimit 报告 429 body 是否明确指向模型级限流（业务 code 6004）。
// 用于区分"账号级软限流"（按账号冷却）与"模型级用量限流"（切模型即可用）。
func IsModelRateLimit(body string) bool {
	// `"code":6004` / `"code": 6004` / `"code":"6004"` 均可命中（JSON 空格容差）。
	return reModelRateLimit.MatchString(body)
}

// modelBlockCode 明确指向「该后端无此模型」的业务 code。
const modelBlockCode = "11102"

// modelBlockMsgMarker 11102 答复的确定性文案（官方 error message 固定短语）。
// 只收这个窄短语，不收 "model ... not found" 宽正则——后者会误伤其他业务的
// not found 措辞。
const modelBlockMsgMarker = "service info not found"

// ModelBlockReason 11102 负缓存条目在 pool.modelCooldowns 里的 reason 前缀。
// handler 写 BlockModelBackoff；pool.BlockModelClear 按 "11102" 前缀识别条目
// （与 6004 条目的 "6004 model rate limit" reason 互不干扰）。
const ModelBlockReason = "11102 model not available"

// IsModelBlocked 报告 body 是否是「该后端无此模型」(11102) 的确定性答复。
//
// 只比对 code/msg 等独立字段，绝不做整段文本子串匹配：错误体还带 requestId 等字段，
// 拿整段文本匹配会把 "11102" 撞在 ID 上、误避让一个本来能用的模型。判定 =
// code 字段精确等于 "11102"，或 msg/message 字段命中窄短语 "service info not
// found"（两者任一命中即真）。只看 400/404：429 带 11102 属限流语义。
// 字段遍历覆盖顶层与 error 子对象两层。
func IsModelBlocked(status int, body string) bool {
	if (status != http.StatusBadRequest && status != http.StatusNotFound) || body == "" {
		return false
	}
	// 轻量预检：body 既无 "11102" 又无 marker 时直接短路（大多数 4xx 零分配返回）。
	if !strings.Contains(body, modelBlockCode) && !strings.Contains(strings.ToLower(body), modelBlockMsgMarker) {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return false
	}
	nodes := []map[string]any{root}
	if inner, ok := root["error"].(map[string]any); ok {
		nodes = append(nodes, inner)
	}
	code, msg := "", ""
	for _, node := range nodes {
		for _, key := range []string{"code", "errCode", "error_code"} {
			if v, ok := node[key]; ok && v != nil && code == "" {
				code = strings.TrimSpace(fmt.Sprint(v))
			}
		}
		for _, key := range []string{"msg", "message"} {
			if v, ok := node[key].(string); ok && v != "" && msg == "" {
				msg = strings.TrimSpace(v)
			}
		}
	}
	if code == modelBlockCode {
		return true
	}
	return strings.Contains(strings.ToLower(msg), modelBlockMsgMarker)
}

// hasBusinessCode reports whether a JSON error envelope contains an exact
// business code in a field named "code". Upstream envelopes vary between
// top-level and nested error/data objects, so walk the decoded structure.
func hasBusinessCode(body, want string) bool {
	var root any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return false
	}
	var walk func(any) bool
	walk = func(value any) bool {
		switch node := value.(type) {
		case map[string]any:
			if code, ok := node["code"]; ok && strings.TrimSpace(fmt.Sprint(code)) == want {
				return true
			}
			for _, child := range node {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range node {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(root)
}

// hasBusinessEnvelope 报告错误 body 是否携带上游业务信封形态（JSON 且含
// `"code":` 或 `"msg":` 字段）。WAF 403 判定（IsWafBlocked）用「无业务信封」
// 区分 APISIX WAF 拦截页（HTML/空体/纯文本）与上游业务层 403（带 code/msg
// 信封，正常走既有分类）。JSON 解析不做：信封存在性只需字段名命中——
// 畸形 JSON 但含 `"msg":` 字样仍按业务响应保守处理（宁漏判 WAF 也不误罚
// 业务 403，后者有各自的权威分类）。
func hasBusinessEnvelope(body string) bool {
	return strings.Contains(body, `"code":`) || strings.Contains(body, `"msg":`)
}

// IsWafBlocked 报告 403 响应是否为 WAF 拦截形态：HTTP 403 且 body 无业务信封
// （无 `"code":`/`"msg":` JSON 字段——HTML 拦截页、空体、纯文本均命中）。
// 带业务信封的 403（11140 request illegal / 11128 等）仍走既有分类链。
// 403 含 accountFault 文案的维持现状（ErrAccountFault），由 Classify 的规则序保证。
func IsWafBlocked(status int, body string) bool {
	return status == http.StatusForbidden && !hasBusinessEnvelope(body)
}

// retryAfterHeaderCandidates 冷却时长优先解析的响应头候选序列：
// retry-after（秒，RFC 7231）/ retry-after-ms（毫秒）/ x-ratelimit-reset
// （epoch 秒或毫秒，取 now+ 剩余量）。大小写不敏感（http.Header.Get 已归一）。
var retryAfterHeaderCandidates = []string{"Retry-After", "Retry-After-Ms", "X-Ratelimit-Reset"}

// retryAfterSanity 解析结果的上限（超过视为上游异常值丢弃，回落本地计算），
// 与 pool 的 softRateMax 默认 2h 同量级。
const retryAfterSanity = 2 * time.Hour

// ParseRetryAfter 从限流/拦截响应头解析上游明示的等待时长：
// 依次尝试 Retry-After（整数秒）→ retry-after-ms（整数毫秒）→
// x-ratelimit-reset（纯数字按 epoch 秒/毫秒推断；HTTP-Date 形态不支持——
// 上游族实践发的是数字）。任一头缺失/非法/非正/超上限则尝试下一头；
// 全部不可用返回 false（调用方回落既有计算值，绝不臆造等待时长）。
func ParseRetryAfter(h http.Header) (time.Duration, bool) {
	for _, name := range retryAfterHeaderCandidates {
		v := strings.TrimSpace(h.Get(name))
		if v == "" {
			continue
		}
		if !isAllDigits(v) {
			continue // 非纯数字（如 HTTP-Date）不解析，宁缺毋滥
		}
		n, ok := parseRetryNumber(v, name)
		if !ok {
			continue
		}
		if n <= 0 || n > retryAfterSanity {
			continue // 非正/异常大：丢弃（回落本地计算）
		}
		return n, true
	}
	return 0, false
}

// isAllDigits 报告 s 是否为纯数字（前置快筛，免 strconv 之后再判语义）。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseRetryNumber 按头名口径把纯数字串折算成时长。x-ratelimit-reset 是
// epoch 时刻而非时长：秒口径（10 位）与毫秒口径（13 位）都按「now+ 该时刻
// 的剩余量」折算，已在过去则不可用。位数不足（8 位以下）无法判定 epoch
// 语义的丢弃（宁缺毋滥：短串多半是序号之类的误用头）。
func parseRetryNumber(v, headerName string) (time.Duration, bool) {
	// 上限 16 位防 int64 溢出（超过 epoch 毫秒的现实量级必非法）。
	if len(v) > 16 {
		return 0, false
	}
	var n int64
	for _, r := range v {
		n = n*10 + int64(r-'0')
	}
	switch headerName {
	case "Retry-After":
		// 先做上限校验再乘 time.Second：16 位数字乘 1e9 会溢出 int64 回绕成
		// 小正数，进而通过调用方的 retryAfterSanity 校验被当作合法等待时长。
		if n > int64(retryAfterSanity/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	case "Retry-After-Ms":
		if n > int64(retryAfterSanity/time.Millisecond) {
			return 0, false
		}
		return time.Duration(n) * time.Millisecond, true
	default: // X-Ratelimit-Reset：epoch → 剩余量
		sec := n
		if len(v) >= 12 { // 毫秒口径（13 位）；11 位边界按秒（误判代价是多算 1000 倍）
			sec = n / 1000
		}
		remain := time.Until(time.Unix(sec, 0))
		return remain, true
	}
}

// ParseRateReset 从任何限流响应 body 里统一解析「将在 … 重置」时间（上游 UTC+8 文案）。
// 成功返回解析出的**墙钟时刻**（按 UTC+8 解释），失败返回零值 + false。
//
// 是否走模型级豁免、时日对齐到 until 还是 modelCooldowns，由冷却决策侧（pool）按
// IsModelRateLimit 判定，本函数只负责「把上游明说的恢复时刻抽出来」。没有时间文案
// 的限流也照常由调用方退回有界退避（绝不臆造时间）。
func ParseRateReset(body string) (time.Time, bool) {
	// CN 文案优先；global 域 429 body 是英文形态（"will reset at YYYY-MM-DD HH:MM:SS
	// UTC+8"），此前只认中文 → global 限流解析不到恢复时刻，退回有界退避基数反复
	// 翻倍（修「global 域冷却指数翻倍」）。英文正则锚定固定格式时间，自然语言
	// （"reset at the end of the day"）不匹配。
	m := reSoftRateResetCN.FindStringSubmatch(body)
	if len(m) < 2 {
		m = reSoftRateResetEN.FindStringSubmatch(body)
	}
	if len(m) < 2 {
		return time.Time{}, false
	}
	ts := strings.TrimSpace(m[1])
	ts = strings.TrimSuffix(ts, " UTC+8") // 去掉后缀，固定按 softRateResetLoc 解释
	t, err := time.ParseInLocation(softRateTimeLayout, ts, softRateResetLoc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// Classify 按 HTTP 状态码 + body 判定错误类别。
//
// 判定顺序自「严」到「宽」，每层的先后都有语义依据：
//  0. 11102（IsModelBlocked）——「该后端无此模型」确定性答复，语义最具体，最先判
//     （只认 400/404，429+11102 属限流语义走第 4 层）。
//  1. 402 —— 真正的计费余额耗尽状态码，最严、最不可自愈，最先判。
//  2. sessionDeadMarkers —— 需要人工重登的终态。若 401 body 同时含 "12153" 与
//     "rate limit"（如网关错误页混排），归 session_dead：短冷却救不活失效 session，
//     误判为限流会让该死号留在池中反复被选中；且此层 marker 是精确词（12153 等），
//     比限流层的大范围子串更具体，具体优先于宽泛。
//  3. accountFaultMarkers —— 账号级授权/配额故障（11140 request illegal auth 风控、
//     14017 trial not activated register 未完成）。必须先于 status==429 判定：
//     14017 常带 429 状态码，若落到 status==429 会误归 soft_rate（"限流"语义不符：
//     限流可指数退避等自愈，账号级故障等不来）。11140 的 model 级限流变体
//     （rate-limiting 文案）因 marker 不含该文案而天然落到 softRateMarkers 层，
//     不受影响。
//  4. 429 + code 14018 —— 明确的账号积分耗尽，归 ErrHardCredit（issue #175）。
//     只认结构化业务码，不靠可能跨计费/限流两界的文案猜测。
//  5. status==429 —— 限流状态码兜底（先于 hardMarkers）：429 body 高频携带
//     "quota exceeded"/"额度不足" 等跨计费/限流两界的措辞，hardMarkers 先判会把
//     限流误归 ErrHardCredit 硬冷却到次日 04:00，白扔号约 12h。状态码是比关键词
//     更权威的信号；真正的余额耗尽由 402（第 1 层）或 14018（第 4 层）捕获，
//     非 429 状态码的 quota 措辞仍走下方 hardMarkers（第 6 层）。
//  6. hardMarkers —— 非 429 响应携带计费关键词（200 业务信封 / 403 信封等）。
//  7. softRateMarkers —— 非 429 状态码携带限流文案（issue #28 修复点）。
//     位于此处可覆盖 200/400/403/5xx 各状态码。
//  8. 11115 —— 「prompt is too long」请求级语义：判在 404/5xx 与通用 4xx 兜底
//     之前（404 上打 11115 若落 ErrNotFound 会误冷却账号——上下文超限与账号无关）。
//  9. 404 / 5xx —— 与限流无关的常规分类。
//  10. IsWafBlocked —— 403 且无业务信封（HTML 拦截页/空体/纯文本）：APISIX WAF
//     拦截形态。判在通用 4xx 兜底**之前**：此前该形态落 ErrClient → 只换号不罚 →
//     连环 403。带业务信封的 403 已被上方各层捕获，走不到本层。
//  11. 内容策略/参数错误/其他 4xx —— 通用兜底。
func Classify(status int, body string) ErrKind {
	// 11102「该后端无此模型」须最先判：它是「模型在后端不存在」的确定性答复，语义比
	// 计费/限流都更具体——若不先判，msg 里的 "service info not found" 会被更宽的
	// 4xx 兜底归为 ErrClient（只换号不避让），该坏号会留在池内反复被选中。
	// 只认 400/404（见 IsModelBlocked），429+11102 落下方 status==429 层走限流语义。
	if IsModelBlocked(status, body) {
		return ErrModelBlocked
	}
	// 402：真正的计费余额耗尽状态码，最严、最不可自愈，最先判。
	if status == http.StatusPaymentRequired {
		return ErrHardCredit
	}
	lower := strings.ToLower(body)
	// sessionDead / accountFault 先于 status==429：账号级终态等不来自愈，限流状态码
	// 不得掩盖它们（429+14017 必须 accountFault，401+12153 混排 "rate limit" 必须
	// sessionDead——此层 marker 是精确词，比限流层的大范围子串更具体，具体优先于宽泛）。
	for _, m := range sessionDeadMarkers {
		if strings.Contains(body, m) {
			return ErrSessionDead
		}
	}
	// 11128 "request illegal" 在 global 域是**请求形态问题**（首条非 system / 出站路径错 /
	// 渠道校验未过），不是账号故障——同一 body 换任何号都 11128（2026-10-29 实测：网关错打
	// /v2 路径，两个健康 global 号全部 11128，换 /console 后同 body 直接 200）。必须先于
	// accountFaultMarkers 判——否则 "request illegal" 文案子串匹配会把这类路径/报文事故
	// 误归 ErrAccountFault，健康号被反复冷却熔断。11140 的 "request illegal" 才是真账号风控，
	// 由下方 accountFaultMarkers 子串匹配兜底（它 msg 同文但 code 不同，本处已按 code 分流）。
	// 例外：body 同时带内容审核文案（blocked by security policy / unapproved channel）时，
	// 仍是 content_blocked——那是上游按内容误杀，不是请求形态错。
	if status == http.StatusBadRequest && hasBusinessCode(body, "11128") {
		for _, m := range contentBlockedMarkers {
			if strings.Contains(lower, m) {
				return ErrContentBlocked
			}
		}
		return ErrBadParams
	}
	for _, m := range accountFaultMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return ErrAccountFault
		}
	}
	// 14018 是明确的账号积分耗尽业务码。它必须先于通用 429 兜底，否则会被误判为
	// 可自愈的软限流并在全池冷却时反复兜底选中（issue #175）。仅按结构化 code
	// 判定；无该 code 的 "credits exhausted" 文案仍保持普通 429 的软限流语义。
	if status == http.StatusTooManyRequests && hasBusinessCode(body, "14018") {
		return ErrHardCredit
	}
	// status==429 先于 hardMarkers：限流响应 body 高频携带 "quota exceeded"/
	// "额度不足" 等跨计费/限流两界的措辞，hardMarkers 先判会把限流误归
	// ErrHardCredit 硬冷却到次日 04:00，白扔号约 12h。状态码是比关键词更权威的
	// 信号：上游既然给了 429，就按限流语义处理（宁可短冷却自愈，不可长冷却弃号）；
	// 真正的余额耗尽由 402（上层）或 14018（上层）捕获，非 429 状态码的 quota
	// 措辞仍走下方 hardMarkers（历史语义不变）。
	if status == http.StatusTooManyRequests {
		return ErrSoftRate
	}
	for _, m := range hardMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return ErrHardCredit
		}
	}
	for _, m := range softRateMarkers {
		if strings.Contains(lower, strings.ToLower(m)) || strings.Contains(body, m) {
			return ErrSoftRate
		}
	}
	// 11115「prompt is too long」：判在 404/5xx/WAF/内容策略/参数错误/通用 4xx
	// 之前——请求级语义最具体（上下文超限），须先于宽泛的状态码兜底（404 兜底会
	// 误归 ErrNotFound 只冷却不透传；ErrClient 只换号，浪费健康号配额）。
	if isPromptTooLongStatus(status) {
		for _, m := range promptTooLongMarkers {
			if strings.Contains(body, m) || (m != strings.ToLower(m) && strings.Contains(lower, strings.ToLower(m))) {
				return ErrPromptTooLong
			}
		}
	}
	if status == http.StatusNotFound {
		return ErrNotFound
	}
	if status >= 500 {
		return ErrServer
	}
	// WAF 403（无业务信封的拦截形态）：判在内容策略/参数错误/通用 4xx 之前——
	// 这些层只认带文案的 body，WAF 空体/HTML 永远不会命中它们的 marker，
	// 但落 ErrClient 兜底的代价是「只换号不罚」（连环 403 根因），必须在兜底前分流。
	// 带信封的 403 在上方各层已有权威分类，不受影响。
	if IsWafBlocked(status, body) {
		return ErrWafBlock
	}
	// 图片格式/数据错误是确定性的请求级错误：同 body 换账号结果不变，直接
	// fail-fast，避免把健康账号轮转一遍后仍把最终 503 返回给客户端。
	// 11135 业务码走 codeMarker（JSON 空白容差），文案走 invalidImageMarkers。
	if status == http.StatusBadRequest && codeMarker(lower, "11135") {
		return ErrImageInvalid
	}
	if status == http.StatusBadRequest {
		for _, m := range invalidImageMarkers {
			if strings.Contains(lower, m) {
				return ErrImageInvalid
			}
		}
	}
	// 内容策略拦截（HTTP 400 + 审核文案）：判在通用 ErrClient 之前。
	// 这是误报信号，不罚账号，由网关降级重试处理（见 handler.applyErrorPolicy）。
	if status >= 400 {
		for _, m := range contentBlockedMarkers {
			if strings.Contains(lower, m) {
				return ErrContentBlocked
			}
		}
		// 请求级参数问题一律归 ErrBadParams：不冷却/不熔断/不计错，且**不轮转**。
		//
		//   - 11101 Unmarshal chat params failed：上游解析请求体失败。网关侧截断已由
		//     413 消灭（issue #41 commit A），剩余来源是客户端 JSON 本身畸形；
		//   - 11133 model_param_invalid：模型供应商拒绝请求参数（图片 URL 拉不到、
		//     模型版本不接受该请求等）。
		//
		// 两者都是「同一个 body 换任何账号结果不变」，所以轮转只会白烧健康号配额，
		// 并把客户端看到的最终错误变成误导性的 no_healthy_account——而 11133 的原文
		// 明确写着「这次对话的内容这个模型不接受，与网络无关」。
		//
		// 「不同账号可能有不同模型权限」不是这里的理由，那是 11102（ErrModelBlocked），
		// 已有 (账号,模型) 负缓存避让；11133 的实测证据是**同号同 body** 也会因模型
		// 版本不同而 15 拒 / 16 过（见 payload.go），与账号无关。
		//
		// 判定复用 hint.go 的 isModelParamInvalid：hint 与分类必须同一口径，否则会
		// 出现「提示说请求问题、行为却轮转所有账号」的分裂（2026-10-15 修复的即此）。
		if strings.Contains(body, badParamsMarkerMsg) || strings.Contains(body, badParamsMarkerCode) || isModelParamInvalid(body) {
			return ErrBadParams
		}
		return ErrClient
	}
	// HTTP 200 但业务 code 非 0 且含余额关键词的情况已被上面 hardMarkers 捕获。
	return ErrNone
}

// ClassifyError 按 HTTP 状态码 + body 判定错误类别，返回完整 Error 对象
// （含 EvidenceLevel 证据强度标签）。新代码优先用这个。
//
// EvidenceLevel 推断口径（与 Classify 判定路径对应）：
//   - High：状态码本身就是权威信号（402/429/401/404/5xx），或精确业务码
//     （11102/14018/11115/11135/11101）、精确词（12153）。
//   - Medium：body 关键词匹配，但关键词有歧义空间（"积分不足"/"rate limit" 等）。
//   - Low：纯文案猜测（contentBlockedMarkers）、兜底的 ErrClient。
//
// 注意：这个函数的 evidence level 是「事后推断」——基于 status + kind 反推判定路径。
// 大部分情况下准确，但边界情况（比如 ErrPromptTooLong 是 11115 业务码命中还是文案命中）
// 可能有偏差。后续如果需要精确到每条判定路径的 evidence level，可以拆成 classifyWithEvidence。
func ClassifyError(status int, body string) *Error {
	kind := Classify(status, body)
	ev := inferEvidenceLevel(kind, status, body)
	return &Error{
		Kind:              kind,
		Status:            status,
		Msg:               truncate(body, 200),
		Evidence:          ev,
		UpstreamRequestID: extractRequestID(body),
	}
}

// extractRequestID 从错误 body 里提取上游的 requestId 字段。
// 上游错误信封形态：{"code":11133,"msg":"...","requestId":"b0b28043..."}
// 或者嵌套在 error 子对象里。解析失败返回空串（宁缺毋滥，不猜）。
func extractRequestID(body string) string {
	if body == "" {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return ""
	}
	nodes := []map[string]any{root}
	if inner, ok := root["error"].(map[string]any); ok {
		nodes = append(nodes, inner)
	}
	for _, node := range nodes {
		for _, key := range []string{"requestId", "request_id", "RequestId"} {
			if v, ok := node[key]; ok && v != nil {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// inferEvidenceLevel 根据 ErrKind + status + body 反推证据强度。
// 这是 ClassifyError 的辅助函数，不要单独调用。
func inferEvidenceLevel(kind ErrKind, status int, body string) EvidenceLevel {
	switch kind {
	case ErrHardCredit:
		// 402 状态码 = 确定性；body 关键词 = 中等
		if status == 402 {
			return EvidenceHigh
		}
		return EvidenceMedium
	case ErrSoftRate:
		// 429 状态码 = 确定性；body 关键词 = 中等
		if status == 429 {
			return EvidenceHigh
		}
		return EvidenceMedium
	case ErrSessionDead:
		// 12153 精确词 = 确定性
		return EvidenceHigh
	case ErrNotFound:
		// 404 状态码 = 确定性
		return EvidenceHigh
	case ErrServer:
		// 5xx 状态码 = 确定性
		return EvidenceHigh
	case ErrContentBlocked:
		// 纯文案猜测 = 低证据
		return EvidenceLow
	case ErrBadParams:
		// 11101 业务码 = High；文案 = Medium
		if strings.Contains(body, `"code":11101`) || strings.Contains(body, `"code": 11101`) {
			return EvidenceHigh
		}
		return EvidenceMedium
	case ErrAccountFault:
		// 关键词匹配 = 中等
		return EvidenceMedium
	case ErrModelBlocked:
		// 11102 精确业务码 = 确定性
		return EvidenceHigh
	case ErrWafBlock:
		// 403 + 无业务信封 = 推断（可能是 WAF 也可能是其他）
		return EvidenceMedium
	case ErrPromptTooLong:
		// 11115 业务码 = High；文案 = Medium
		if strings.Contains(body, `"code":11115`) || strings.Contains(body, `"code": 11115`) {
			return EvidenceHigh
		}
		return EvidenceMedium
	case ErrImageInvalid:
		// 11135 业务码 = High；文案 = Medium
		if codeMarker(strings.ToLower(body), "11135") {
			return EvidenceHigh
		}
		return EvidenceMedium
	case ErrClient:
		// 兜底 = 低证据/推断
		return EvidenceLow
	default:
		return EvidenceNone
	}
}
