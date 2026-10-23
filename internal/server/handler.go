// Package server 暴露 OpenAI 兼容 HTTP 接口，内部驱动 pool 挑号 + upstream 转发。
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"time"

	"yg-gateway/internal/alerting"
	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/livecfg"
	"yg-gateway/internal/metrics"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/session"
	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// Config handler 依赖。
type Config struct {
	Pool      *pool.Pool
	Upstream  *upstream.Client
	APIKey    string // 空 = 不鉴权（静态值；与 Live 同时给出时 Live 优先）
	MaxRotate int    // 单请求最多换号次数，默认 3
	// Session 会话粘性路由器（可选；nil = 关闭粘性，纯 Pick 轮换）。
	Session *session.Router
	// DisableSessionDerive 关闭「内容派生会话键」回退（默认 false = 开启派生，
	// 保持既有行为）。开启派生时，无显式会话标识的请求按
	// SHA-256(system + 首条 user) 归组——固定 system + 短开场会把无关对话
	// 误判为同一会话、全量钉到同一个账号（详见 session.ExtractKeyOpt）。
	// 反向命名（Disable… 而非 Enable…）见使用处注释：保证零值不改变既有语义。
	DisableSessionDerive bool
	// StickyCount 返回当前粘性会话绑定数（供 /status）；nil 时报告 0。
	StickyCount func() int
	// RedisMode 观测字段（"upstash" / "noop"），供 /status 透出。
	RedisMode    string
	SoftCooldown time.Duration // 429/限流文案软冷却基数，默认 600s（连续触发指数退避，封顶 soft_rate_max）
	RefreshSkew  time.Duration // token 提前刷新窗口，默认 10m

	// Panel 管理面板 handler（可选；nil = 不挂载）。挂载在 /panel/ 前缀下，
	// 面板自带 Bearer 鉴权（同一 api_key）与内嵌静态资源，主路由只做转发。
	Panel http.Handler

	// Live 运行期可变配置（面板在线改 api_key / soft_rate / 脱敏开关时立即生效）。
	// nil 时回退静态字段（测试与裸用场景）。
	Live *livecfg.Holder

	// PromptMode "custom"（网关用自有提示词替换 system）/ "passthrough"（透传）。
	PromptMode string
	// PromptText custom 模式下注入的系统提示词文本（来自 config.PromptText）。
	PromptText string

	// GlobalEnabled global realm 路由开关（config global.enabled，缺省 true）。
	// handler 侧第三道闸（与 main 注入 auth 开关、upstream.GlobalEnabled 呼应）：
	// false（显式逃生门）时即便 auth realm=global 也不提供 global: 模型名
	// （modelList 不列 global 名单）。
	GlobalEnabled bool

	// Usage 逐请求用量记录器（可选；nil = 不记录）。
	// 在 recordAttempt 这一唯一汇聚点调用，因此流式/非流式、成功/失败都会计入，
	// 且与 pool 的每账号累计器同源，两条口径不会漂移。
	Usage *usage.Recorder

	// Latency 延迟分位数观测（ADR-003 D-403，可选；nil = 不观测）。
	// 在 recordAttempt 汇聚点记录每次尝试的耗时，暴露到 /status 的 latency 段。
	// 自实现固定分桶（internal/metrics），零新依赖。
	Latency *metrics.Registry

	// TTFB 首 token 延迟分位数观测（可选；nil = 不观测）。
	// 与 Latency 的区别：Latency 记端到端（含 TTFB + 生成），这里只记「请求进来到
	// 上游首帧到达」——网关要回答的两个问题不同（"总共等多久" vs "上游多快开始吐"）。
	// 只在流式路径有观测（非流式无首帧概念）。
	TTFB *metrics.Registry

	// ErrKinds 上游错误分类计数（可选；nil = 不计数）。
	// 分类逻辑本就齐全（upstream.Classify 13 种 ErrKind），此前只落单行日志；
	// 挂上计数器后可在 /metrics 直接回答「最近是限流多了还是 WAF 多了」。
	ErrKinds *metrics.KindCounters

	// Attempts 重试放大率观测（F-305；可选；nil = 不观测）。
	// 每个用户请求收口（chatCompletions defer）时记录其上游尝试次数分布：
	// 1 = 无换号重试，>1 = 发生换号重试/失败转移。成功/失败/轮转耗尽全结局恰好记一次。
	Attempts *metrics.AttemptsStats

	// SlowRequests 慢请求计数器（F-305；可选；nil = 不计数）。
	// 与 SlowRequestThreshold 配合：端到端耗时超过阈值的完成请求 +1。
	SlowRequests *metrics.Counter

	// SlowRequestThreshold 慢请求阈值（端到端耗时）；<=0 = 关闭（旧配置零回归）。
	// 纯观测计数，不改写请求路径；不依赖后台 goroutine（R9，只在收口 defer 比较一次）。
	SlowRequestThreshold time.Duration

	// RequestLog 请求指标与脱敏 JSONL 归档（可选；nil = 不记录）。
	RequestLog *reqlog.Recorder

	// RecordClientInfo 是否在请求日志里记录调用来源（客户端 IP / User-Agent）。
	// 来自 logging.request_client_info（缺省 true）；关闭时 reqlog 事件的来源字段
	// 保持为空，归档与面板都不出现来源信息。
	RecordClientInfo bool

	// MaxBodyBytes 入站 /v1/chat/completions 请求体上限（字节）。超限返回 413，
	// 且读取阶段即被 MaxBytesReader 截断，超大 body 不会整体进内存（DoS 防护）。
	// 缺省 0 = 不设上限（保留旧行为）；生产建议显式配置（如 64<<20）。
	MaxBodyBytes int64

	// TrustedProxy 受信任反向代理来源（可选；nil = 不信任任何代理头）。
	// 非 nil 时，仅当请求来源落在其中（CIDR 或精确 IP）才采信 X-Forwarded-For /
	// X-Real-IP 作为客户端真实 IP；否则忽略代理头、回落 TCP 对端（防伪造 XFF
	// 污染上游风控/计费——ProjectDiscovery「Abusing Reverse Proxies」点名的向量）。
	// 直连部署（前面无反代）保持 nil 即安全。
	TrustedProxy *TrustedProxyMatcher

	// ReadOnlyRateLimit 只读端点（/v1/models、/status）限流：window 内最多 limit 次。
	// 缺省 nil = 不限流（保留旧行为）。按来源 IP 计数（未配置受信任代理时回落 TCP
	// 对端，等价按直连来源计数）。
	ReadOnlyRateLimit *ReadOnlyRateLimit

	// Alerting 异常外发告警（可选；nil = 不告警）。账号余额告急/被禁用/会话死/触发
	// 熔断时 POST 到配置的 webhook。fire-and-forget，不阻塞业务路径。
	Alerting *alerting.Notifier

	// Fallback 备用上游级联（P2，可选；nil = 关闭）。
	// CodeBuddy 选号全失败时，把请求按 endpoints 顺序转发到独立 OpenAI 兼容端点。
	Fallback *FallbackConfig

	// Drift 协议漂移观测层（P1，可选；nil = 不观测）。
	// /status 暴露其状态视图；观测 goroutine 在 main 侧启动。
	Drift *upstream.DriftMonitor
}

// FallbackEndpoint 单个 OpenAI 兼容备用端点（R-107 接入模板）。
type FallbackEndpoint struct {
	BaseURL        string
	APIKey         string
	Models         map[string]bool // 白名单（set）
	TimeoutSeconds int
}

// FallbackConfig 备用上游级联配置（P2）。Endpoints 按序级联。
type FallbackConfig struct {
	Endpoints []FallbackEndpoint
}

// Covers 任一已知端点的白名单是否覆盖该模型。触发判定与级联转发用同一口径，
// 避免「触发时命中、转发时漏判」的分裂。
func (fc *FallbackConfig) Covers(model string) bool {
	for i := range fc.Endpoints {
		if fc.Endpoints[i].Models[model] {
			return true
		}
	}
	return false
}

// loadLive 返回当前运行期快照；Live 为 nil 时用静态字段合成。
func (h *Handler) loadLive() livecfg.Snapshot {
	if h.cfg.Live != nil {
		return h.cfg.Live.Load()
	}
	return livecfg.Snapshot{
		APIKey:           h.cfg.APIKey,
		SoftCooldown:     h.cfg.SoftCooldown,
		RecordClientInfo: h.cfg.RecordClientInfo,
	}
}

// softCooldown 返回当前生效的软冷却基数（热改优先，<=0 回退默认）。
func (h *Handler) softCooldown() time.Duration {
	if d := h.loadLive().SoftCooldown; d > 0 {
		return d
	}
	if h.cfg.SoftCooldown > 0 {
		return h.cfg.SoftCooldown
	}
	return 600 * time.Second
}

// notFoundCooldown 上游 404 的固定短冷却时长。
// 与 SoftCooldown 分流的原因：404 是上游**偶发**路径缺失，不是"本账号在限流"，
// 若共用 soft_rate（600s 起 + 指数升级），一次偶发 404 会把好账号罚 10 分钟并逐次加倍。
// 故固定 60s 防雪崩即可，不随 soft_rate 配置、也不参与软退避指数。
const notFoundCooldown = 60 * time.Second

// ServiceName 网关身份标识。经 /healthz 响应体 service 字段与 X-Service 头同时透出：
// 宿主探测同端口的旧服务/其他服务时，对方即使返回 2xx 也不带本标识，据此可识别
// "假成功"。
//
// 独立化后改为 yg-gateway：这套部署已脱离上游演进，不再需要与原项目互认。
// 若你有脚本依赖旧值做探活判据，改这里或改脚本，两者保持一致即可。
const ServiceName = "yg-gateway"

// Handler 主路由。
type Handler struct {
	cfg     Config
	mux     *http.ServeMux
	degrade degradeGate
	// wafIP WAF IP 级拦截状态机（fail-fast，wafip.go）：短窗多号 WAF 403 →
	// 激活期轮转遇 WAF 403 直接终止（不放大请求量）。进程内状态、重启清零。
	wafIP wafIPGate
	// readonlyRL 只读端点限流器（F-003；nil = 不限流）。在 NewHandler 由
	// cfg.ReadOnlyRateLimit 构建一次，运行期固定。
	readonlyRL *rateLimiter
	// keyRPM per-key RPM 限流器（P0-3）：每把下游 key 独立 1 分钟窗口计数，
	// 上限来自 live snapshot 的 key.RPM（热生效）。nil = 不限流。
	keyRPM *keyRPMGuard
	// modelsCache 动态模型目录缓存（10min TTL + 5min 失败负缓存）。**实例字段**而非
	// 包级全局：每个 NewHandler 自带一份空缓存，用例之间天然隔离（N3，见类型注释）。
	modelsCache modelsCache
}

// NewHandler 构建 handler。
func NewHandler(cfg Config) *Handler {
	if cfg.MaxRotate <= 0 {
		cfg.MaxRotate = 3
	}
	if cfg.SoftCooldown <= 0 {
		cfg.SoftCooldown = 600 * time.Second // 软限流基数（连续触发按指数退避放大）
	}
	if cfg.RefreshSkew <= 0 {
		cfg.RefreshSkew = 10 * time.Minute
	}
	if cfg.PromptMode == "" {
		cfg.PromptMode = "custom" // 缺省 custom：网关自有提示词
	}
	h := &Handler{cfg: cfg, mux: http.NewServeMux()}
	h.readonlyRL = cfg.ReadOnlyRateLimit.build()
	h.keyRPM = newKeyRPMGuard()
	h.mux.HandleFunc("POST /v1/chat/completions", h.withAuth(h.chatCompletions))
	h.mux.HandleFunc("POST /v1/messages", h.withAuth(h.anthropicMessages))
	h.mux.HandleFunc("GET /v1/models", h.withAuth(h.rateLimited(h.models)))
	h.mux.HandleFunc("GET /status", h.withAuth(h.rateLimited(h.status)))
	// /metrics：Prometheus 文本导出（F-710）。与 /status 同鉴权口径——指标含
	// 账号池状态与 token 量，属敏感运维数据，不对未鉴权方开放。
	h.mux.HandleFunc("GET /metrics", h.withAuth(h.rateLimited(h.metricsHandler)))
	h.mux.HandleFunc("GET /healthz", h.healthz)
	// 兜底：未匹配路径与方法错误统一回 OpenAI 错误信封。
	//
	// 为什么需要：Go 1.22 ServeMux 对未注册路径回裸 text/plain "404 page not
	// found"、对已知路径的错误方法回 "405 Method Not Allowed"，OpenAI 兼容客户端
	//（Codex / Cherry Studio / 各类 SDK）解析这类响应会失败，报出的错误信息与真实
	// 原因无关（"invalid character 'p' looking for beginning of value"），排查成本高。
	// 这里统一成 {"error":{message,type,code}} 信封，并区分 404 / 405。
	// 注意：/panel/ 前缀已注册（cfg.Panel 非 nil 时），其内部自行处理 404，不会落到这里。
	h.mux.HandleFunc("/", h.notFound)
	if cfg.Panel != nil {
		h.mux.Handle("/panel/", cfg.Panel) // /panel → /panel/ 由 ServeMux 自动重定向
	}
	return h
}

// notFound 未匹配路径 / 方法错误的 OpenAI 错误信封兜底。
//
// 405 的判定：Go 1.22 ServeMux 在「路径已注册但方法不符」时会把请求交给最长的
// 匹配模式，而 "/" 兜底模式对任意方法都匹配，所以这里无法从 mux 直接得知"是否
// 存在其他方法的同路径"。用已知路由表做显式判定：路径命中已知路由集合且方法与
// 该路径允许的方法不符 → 405；否则 404。显式表比探测 mux 内部状态更稳。
func (h *Handler) notFound(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// 已知路径 → 该路径允许的方法集合。
	known := map[string]map[string]bool{
		"/v1/chat/completions": {"POST": true},
		"/v1/messages":         {"POST": true},
		"/v1/models":           {"GET": true},
		"/status":              {"GET": true},
		"/metrics":             {"GET": true},
		"/healthz":             {"GET": true},
	}
	if methods, ok := known[path]; ok {
		if !methods[r.Method] {
			w.Header().Set("Allow", allowHeader(methods))
			writeOpenAIError(w, http.StatusMethodNotAllowed, "method_not_allowed",
				"method "+r.Method+" not allowed on "+path)
			return
		}
		// 路径与方法都合法却没被 mux 服务：理论上不可达（模式已注册）。
		// 兜底为 404，避免留下无响应的死路。
	}
	writeOpenAIError(w, http.StatusNotFound, "not_found",
		"unknown endpoint: "+r.Method+" "+path)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS：本机网关仅供本机客户端与浏览器内聊天网页调用。
	// 浏览器从 https 聊天网页 fetch http://127.0.0.1:7863 会触发 preflight，
	// 不写 CORS 头会被浏览器拦。回显 Origin（带 Authorization 头时 `*` 不被接受）。
	//
	// 只回显 **https 源**（四维度审查 [P3] 收紧）：合法调用方就是「公网 https 页
	// 经 Private-Network-Access 调本机网关」（见 821a296 的设计初衷）；http 源与
	// null 源（沙箱 iframe / file:// / data:）来源不可验证，不回显即被浏览器拦下。
	// 面板自身全部走相对路径（app.js fetch('/panel/api/...')），同源不受影响。
	if origin := r.Header.Get("Origin"); origin != "" {
		if reflect, ok := reflectableCORSOrigin(origin); ok {
			w.Header().Set("Access-Control-Allow-Origin", reflect)
			w.Header().Add("Vary", "Origin")
			// 保留 Allow-Credentials：全仓无 cookie（鉴权是 Bearer 头），该头没有
			// 可骑乘的环境凭证；删掉它反而会打断 credentials:'include' 的调用方。
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		} else {
			// 不回显的后果是浏览器拦下跨域读——留痕便于排障：合法调用方只会是
			// https 源，这里出现日志说明有 http 源在调，需人工确认是否新场景。
			log.Printf("WARN: [cors] 拒绝回显非 https Origin: %s", origin)
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if h.cfg.RequestLog != nil && r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions" {
		trace := &requestTrace{id: reqlog.NewRequestID(), start: time.Now()}
		if h.loadLive().RecordClientInfo {
			trace.captureClientInfo(r, h.cfg.TrustedProxy)
		}
		r = r.WithContext(context.WithValue(r.Context(), requestTraceKey{}, trace))
		obs := &responseObserver{ResponseWriter: w}
		w.Header().Set("X-Request-Id", trace.id)
		h.cfg.RequestLog.Begin()
		defer func() {
			status := obs.status
			if status == 0 {
				status = http.StatusOK
			}
			h.cfg.RequestLog.Record(trace.event(status))
		}()
		h.mux.ServeHTTP(obs, r)
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := h.authenticate(r)
		if !ok {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "missing or invalid API key")
			return
		}
		// R-108 Key 级来源 IP 白名单：鉴权后、计费/限流前执行。空名单 = 不限（向后兼容，
		// 恒放行）；非空 fail-closed。真实来源 IP 复用 trusted_proxy 的受信代理 XFF
		// 判定提取（防伪造），不重复实现。403 + ip_not_allowed，与 model/realm 约束
		// （400）区分：这是网络来源拒绝，不是请求格式错误。
		if key != nil && !key.IPAllowed(extractClientIP(h.cfg.TrustedProxy, r)) {
			writeOpenAIError(w, http.StatusForbidden, "ip_not_allowed",
				"该密钥不允许从当前来源 IP 访问")
			return
		}
		// Per-key RPM 限流（P0-3）：在鉴权后、业务前检查。未配置 rpm=0 的 key 恒放行。
		// 超限返 429 + Retry-After，与上游 429 语义一致（客户端自带退避）。
		if !h.keyRPM.allow(key) {
			w.Header().Set("Retry-After", "60")
			writeOpenAIError(w, http.StatusTooManyRequests, "rate_limited",
				"API key rate limit exceeded; retry in a minute")
			return
		}
		// Per-key 月 token 硬上限（P1-A）：已用量从 usage.Recorder 按 KeyID 累计查询。
		// quota=0 恒放行（零回归）。超限返 429 + Retry-After=到下个自然月。
		if key != nil && key.MonthlyTokenQuota > 0 && h.cfg.Usage != nil {
			if used := h.cfg.Usage.MonthlyUsed(key.ID); used >= key.MonthlyTokenQuota {
				w.Header().Set("Retry-After", "86400")
				writeOpenAIError(w, http.StatusTooManyRequests, "quota_exceeded",
					"API key monthly token quota exceeded this month")
				return
			}
		}
		// 把命中的 Key 身份放进独立 context（供约束校验/归因，不依赖 requestTrace，
		// 因为 trace 只在 RequestLog 非 nil 时才创建）。
		if key != nil {
			r = r.WithContext(context.WithValue(r.Context(), authKeyKey{}, key))
		}
		// 同时写进 requestTrace（若存在），供日志归档归因。
		if tr := requestTraceFrom(r); tr != nil && key != nil {
			tr.setKey(key)
		}
		next(w, r)
	}
}

// authKeyKey 鉴权命中的 Key 的 context 键（独立于 requestTrace，约束校验用）。
type authKeyKey struct{}

// authKeyFrom 从请求上下文读鉴权命中的 Key（withAuth 写入；nil = 未启用鉴权）。
func authKeyFrom(r *http.Request) *httpauth.Key {
	if r == nil {
		return nil
	}
	k, _ := r.Context().Value(authKeyKey{}).(*httpauth.Key)
	return k
}

// authenticate 统一鉴权（多 Key 索引 + 单 Key 兼容），返回命中的 Key（不含明文）。
// 未启用鉴权时返回 (nil, true)；鉴权失败返回 (nil, false)。
func (h *Handler) authenticate(r *http.Request) (*httpauth.Key, bool) {
	live := h.loadLive()
	return httpauth.Authenticate(r, live.AuthIndex, live.APIKey)
}

// modelKindOf 判定 model 字段的 JSON 类型，用于约束校验时区分「缺 model」与「model 类型非法」。
// 返回：missing（字段缺失）/ null / string / other（数字、布尔、对象、数组等非字符串）。
func modelKindOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "missing"
	}
	b := raw[0]
	switch b {
	case 'n':
		return "null"
	case '"':
		return "string"
	default:
		return "other"
	}
}

// checkKeyConstraints 校验命中的 Key 的 realm / models 约束（ADR-001 §五）。
//
// 在鉴权后、选号前执行：被拒请求零上游消耗。返回空串 = 放行；非空 = 错误码
// （realm_mismatch / model_not_allowed）。错误不泄漏 Key 身份（防探测存活 Key）。
//
// modelKind 区分 model 字段的 JSON 类型（missing/null/string/other），用于白名单启用时
// 精确报错：缺失/空串报「缺 model」，数字/对象等报「model 类型非法」，避免把类型错误
// 误报成缺 model（T6 收尾）。
func (h *Handler) checkKeyConstraints(key *httpauth.Key, reqModel, realm, bareModel, modelKind string) (code, msg string) {
	if key == nil {
		return "", "" // 未启用鉴权，无约束
	}
	// realm 约束：空 = 不限；cn = 只 cn；global = 只 global。
	if key.Realm != "" && key.Realm != realm {
		want := key.Realm
		got := realm
		if want == "cn" {
			return "realm_mismatch", "该密钥仅限国内版模型，当前请求为国际版模型"
		}
		if want == "global" {
			return "realm_mismatch", "该密钥仅限国际版模型，当前请求为国内版模型"
		}
		// 理论上 normalize 已保证 realm 只会是 cn/global/空，这里兜底。
		return "realm_mismatch", "该密钥限定的版本与请求不匹配（" + want + " vs " + got + "）"
	}
	// models 约束三态：nil = 不限 / 非 nil 空 = 全拒 / 非空 = 白名单。
	if key.Models == nil {
		return "", ""
	}
	if len(key.Models) == 0 {
		return "model_not_allowed", "该密钥未开放任何模型"
	}
	// 白名单启用时，model 必须是合法字符串（缺 model / 类型非法都不能跳过检查，对齐 manager）。
	if modelKind == "other" {
		return "model_not_allowed", "model 字段类型非法（必须是字符串）"
	}
	if modelKind == "missing" || modelKind == "null" || reqModel == "" {
		return "model_not_allowed", "该密钥启用了模型白名单，请求必须指定 model"
	}
	if !modelAllowedByWhitelist(key.Models, bareModel) {
		return "model_not_allowed", "模型不在该密钥的白名单内"
	}
	return "", ""
}

// rateLimited 只读端点限流中间件（F-003）。readonlyRL 为 nil 时透传（不限流）。
// 计数键 = extractClientIP 的客户端 IP（未配置受信任代理时即 TCP 对端）；取不到
// IP 时退化为全局单一计数（仍防无脑刷，只是粒度变粗）。
// 注意：与 withAuth 的包裹顺序——鉴权在前，限流在后，避免未鉴权者消耗限流配额。
func (h *Handler) rateLimited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.readonlyRL == nil {
			next(w, r)
			return
		}
		key := extractClientIP(h.cfg.TrustedProxy, r)
		if !h.readonlyRL.allow(key) {
			w.Header().Set("Retry-After", "1")
			writeOpenAIError(w, http.StatusTooManyRequests, "rate_limited",
				"too many requests; retry shortly")
			return
		}
		next(w, r)
	}
}

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	total, healthy, _, _, _ := h.cfg.Pool.CountsDetailed()
	// 用 ServableNow 判定：healthy>0 但全占满在途时 chat 会 503，探活必须同口径，
	// 否则负载均衡器会把流量持续打进无法受理的实例。
	status := http.StatusOK
	if !h.cfg.Pool.ServableNow() {
		status = http.StatusServiceUnavailable
	}
	// realm_servable 域可服务维度：不改判活语义（存在性探活保持不变），
	// 只新增 CN/global 各自可达性供双域部署运维观察（任一域不可用单独告警）。
	realmServable := map[string]bool{
		"cn":     h.cfg.Pool.ServableForRealm("cn"),
		"global": h.cfg.Pool.ServableForRealm("global"),
	}
	// 恒无鉴权（负载均衡/编排探活只需 2xx/503 语义），身份靠 service 字段 + X-Service 头双保险。
	w.Header().Set("X-Service", ServiceName)
	writeJSON(w, status, map[string]any{
		"healthy":        healthy,
		"total":          total,
		"service":        ServiceName,
		"realm_servable": realmServable,
	})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	total, healthy, cooling, disabled, inFlightFull := h.cfg.Pool.CountsDetailed()
	sticky := 0
	if h.cfg.StickyCount != nil {
		sticky = h.cfg.StickyCount()
	}
	redisMode := h.cfg.RedisMode
	if redisMode == "" {
		redisMode = "noop"
	}
	// cost_explore 探索台账（issue #136 §5 可观测性）：累计探索事件数 + 各
	// (域, 模型) 的最近探索时刻（键 "realm|model"）。与 accounts[].model_costs
	// 行对照即可读出「探索→毕业」全链路（单一事实来源，不做双表示）。零回归只增键。
	exploreEvents, exploreLast := h.cfg.Pool.CostExploreStatus()
	body := map[string]any{
		"accounts":       h.cfg.Pool.List(),
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
		// realm_totals 按域分组的计数汇总（双 realm 并存时运维一眼看到各域可用性）：
		// 只新增字段，既有 total/healthy/cooling/disabled/in_flight_full 汇总键不变（零回归）。
		"realm_totals": map[string]map[string]int{
			"cn":     countsMapFrom(h.cfg.Pool.CountsDetailedForRealm("cn")),
			"global": countsMapFrom(h.cfg.Pool.CountsDetailedForRealm("global")),
		},
		"sticky_sessions": sticky,
		"redis_mode":      redisMode,
		// model_locks 当前有未过期模型级限流的 (域, 模型) 全清单：账号池视图回答
		// 「哪些号不能用」，本键回答「哪些模型不能用、锁了几个号、还要锁多久」。
		// 与 ModelBlocked（请求失败时的单模型判定）互补；无锁时为 null。零回归只增键。
		"model_locks": h.cfg.Pool.ModelLockView(),
		// credit_floor 生效的积分保底值（0 = 关闭）。与 accounts[].credits +
		// model_costs 对照即可判定「某号为何对某模型不出票」。零值也显式写出
		// （运维口径：缺失会让人误以为没记录）。
		"credit_floor": h.cfg.Pool.CreditFloor(),
		// cost_explore 事件与 per-model 时间戳（时间值由 encoding/json 写 RFC3339）。
		"cost_explore": map[string]any{
			"events_total": exploreEvents,
			"per_model":    exploreLast,
		},
		// latency 延迟分位数（ADR-003 D-403）：全局 + 按模型的 p50/p90/p95/p99。
		// 补齐"只有均值、看不见长尾"的观测缺口——均值会被大量快请求稀释，
		// 而网关的体验痛点恰在长尾。未启用观测时为 null（零回归只增键）。
		"latency": h.latencySnapshot(),
		// global_in_flight 全局在途计数（ADR-003 D-401）：与 gateway.max_in_flight
		// 对照可判断是否触顶（触顶时新请求收 429）。
		"global_in_flight": h.cfg.Pool.GlobalInFlight(),
		// sticky_paid_hits 粘性命中收费号且池里有免费/未知号的累计次数
		// （ADR-004 遗留轻量版）。纯观测，不自动换号。
		"sticky_paid_hits": h.cfg.Pool.StickyPaidHits(),
	}

	// task_metrics F5 度量基线（REQ-f5-metric-counter）：把首调用上下文与总 token 从
	// unknown 变成 observation。仅当观测到至少一个会话的 usage 时才加入响应——否则
	// 整体省略该键（unknown，不输出 0、不输出 false，与本项目「字段缺失=未知」纪律一致）。
	// first_call_prompt_tokens 仅在该会话确实观测到首轮 prompt_tokens 时输出，否则省略。
	if tmView, ok := upstream.TaskMetrics.Snapshot(); ok {
		tm := map[string]any{
			"tasks":             tmView.Tasks,
			"task_total_tokens": int64(tmView.TaskTotalTokens),
			"task_turns":        tmView.TaskTurns,
		}
		if tmView.FirstCallKnown {
			tm["first_call_prompt_tokens"] = int64(tmView.FirstCallPromptTokens)
		}
		body["task_metrics"] = tm
	}

	// protocol_drift 上游协议漂移观测（R-102）：模块未启用（Drift=nil）时整体省略
	// 该键——键缺失 = 未观测（四态 unknown 的外层表达），不是「无漂移」。模块启用后，
	// 观测器内部再区分 unknown（未播种）/ ok / drift：禁止用键省略当「无漂移」读。
	if h.cfg.Drift != nil {
		body["protocol_drift"] = h.cfg.Drift.Status()
	}

	writeJSON(w, http.StatusOK, body)
}

// latencySnapshot 返回延迟分位数快照；未启用观测（Latency=nil）时返回 nil。
func (h *Handler) latencySnapshot() any {
	if h.cfg.Latency == nil {
		return nil
	}
	return h.cfg.Latency.Snapshot()
}

// countsMapFrom 把 CountsDetailed 五元组打包成 /status 的域分组建模。
func countsMapFrom(total, healthy, cooling, disabled, inFlightFull int) map[string]int {
	return map[string]int{
		"total":          total,
		"healthy":        healthy,
		"cooling":        cooling,
		"disabled":       disabled,
		"in_flight_full": inFlightFull,
	}
}

func (h *Handler) chatCompletions(w http.ResponseWriter, r *http.Request) {
	// 全局准入控制（ADR-003 D-401）：全网关在途请求总数上限，超限立即 429。
	//
	// 为什么放在最前面（读 body 之前）：拒绝要尽可能便宜——不读 body、不解析、
	// 不选号、不打上游。上游慢时请求会堆积（每个都占着 goroutine + 连接等超时），
	// 这是防雪崩的关键一环。未配置上限（默认 0）时 AcquireGlobal 恒返回 true，
	// 行为与改动前逐位一致（零回归）。
	//
	// 为什么返 429 而不排队：排队会掩盖问题（客户端以为在正常等待），且队列本身
	// 要限长/超时/处理取消，复杂度陡增；429 + Retry-After 让客户端按自己的策略
	// 退避，语义与上游 429 一致（客户端已具备该能力）。
	if !h.cfg.Pool.AcquireGlobal() {
		w.Header().Set("Retry-After", "1")
		writeOpenAIError(w, http.StatusTooManyRequests, "server_busy",
			"gateway at capacity; retry shortly")
		return
	}
	defer h.cfg.Pool.ReleaseGlobal()

	// 阶段①（clientIP / 读 body / 单次解析 / 模型与 Key 校验）已抽到
	// chatflow.go 的 prepareChatRequest。**两个 defer 刻意留在本函数**：
	// ReleaseGlobal 的作用域必须是整个请求（搬走会让全局在途名额提前释放），
	// st.done 的作用域也必须是整个请求（搬走会让耗时统计在阶段①就落日志）。
	prep, ok := h.prepareChatRequest(w, r)
	if !ok {
		return
	}
	st := prep.st
	defer st.done()
	// F-305 可观测性收口：重试放大率直方图 + 慢请求计数。defer 恰好执行一次，
	// 覆盖成功/失败/轮转耗尽/fallback 全结局；中途不泄漏、不重复（见 retry_metrics.go）。
	// 用 prep.bareModel 而非下方解构的局部变量：defer 参数在 defer 语句处即求值，
	// 而解构行在后面。
	defer h.observeRequestEnd(st, prep.bareModel)
	// 过渡期解构：让主函数其余部分保持原有局部变量名，把「抽出阶段函数」与
	// 「改变量引用方式」拆成两次独立改动——每次只动一件事，快照测试失败时
	// 才能精确定位是哪一步引入的。
	clientIP, body, reqView := prep.clientIP, prep.body, prep.reqView
	viewOK := prep.viewOK
	modelStr, bareModel, upstreamModel := prep.modelStr, prep.bareModel, prep.upstreamModel
	realm, modelRate, authKey := prep.realm, prep.modelRate, prep.authKey
	declaredTools := prep.declaredTools

	// 阶段② 的键派生（会话级键 / 粘性绑定号 / 轮级键）已抽到 chatflow.go 的
	// deriveSessionKeys——它与粘性开关解耦，且必须在提示词改写之前取。
	sessKey, stickyUID, turnKey := h.deriveSessionKeys(&reqView, modelStr)

	// 运行期状态载体：原 5 个闭包（hasImage / releaseHeld / unbindSticky / fail /
	// recordAttempt）已改为 chatRun 的方法（chatrun.go）——它们共享 heldUID /
	// stickyUID / sessKey / st 等状态，只有挂到结构体上才能跨阶段函数使用，
	// 而不必（也不允许）提升为包级变量。
	//
	// imageRawBody 在此捕获**原始 body**：阶段③ 的提示词改写会动 messages 内容，
	// 而带图判定必须针对客户端原始请求（改写在下方才执行）。
	run := &chatRun{
		h:            h,
		st:           st,
		sessKey:      sessKey,
		bareModel:    bareModel,
		modelStr:     modelStr,
		modelRate:    modelRate,
		authKey:      authKey,
		stickyUID:    stickyUID,
		imageViewOK:  viewOK,
		imageReqView: &reqView,
		imageRawBody: body,
		// 阶段④（轮转循环）所需的只读上下文与读写状态。循环体整体搬到
		// rotateAndServe 后，这些值只能通过 chatRun 传递（Go 没有局部函数）。
		realm:         realm,
		turnKey:       turnKey,
		declaredTools: declaredTools,
		clientIP:      clientIP,
		peekStream:    prep.peekStream,
		body:          body,
		tried:         map[string]bool{},
	}
	// 在途租约兜底释放：函数出口（含成功 return 与 panic）统一释放。
	// 与原 defer 语义一致——原实现只 Release 不重置（出口即结束，重置无意义），
	// releaseHeld 多一次赋空，无副作用。
	defer run.releaseHeld()

	// 阶段③（系统提示词改写 / outbound model 名重写 / 会话头族）已抽到 chatflow.go
	// 的 prepareOutbound——三件事都在轮转循环之外只做一次，循环内每次出站复用同一
	// 份 body 与同一组会话头（换号/重试/降级都不发散）。
	ob := h.prepareOutbound(body, &reqView, upstreamModel, modelStr, sessKey, turnKey,
		r.Header.Get("X-Conversation-Request-ID"), r.Header.Get("X-Trace-ID"))
	body, chatMeta := ob.body, ob.chatMeta
	// degradedApplied 只在轮转循环内被读取/再置位，故直接挂到 run 上，
	// 不在主函数保留一个很快就会变成"声明未使用"的局部变量。
	run.degradedApplied = ob.degradedApplied
	// **必须同步回写 run.body**：阶段③ 的提示词改写/mode 重写产生的是新切片，
	// 而 rotateAndServe 是从 run.body 取出发往上游的 body。漏了这一步，
	// 出站的就是**未被改写**的原始 body——症状是 custom 模式下客户端原始 system
	// 指纹原样发给上游（TestCustomModeFingerprintSanitizePreserved 抓到）。
	// 注意 run.imageRawBody 不受影响：它刻意保留改写前的原始 body 用于带图判定。
	run.body = body

	// 阶段④：轮转主循环（415 行）已抽到 chatrun.go 的 rotateAndServe。
	// 循环内的读写状态都在 run 上：tried/modelBlock/groupFilteredOut/lastErr/body
	// 由该方法的收尾回写，故此处无需再接这些值。
	//
	// **返回值必须看**：循环体里有 7 处 `return`（成功透传、请求级错误透传等），
	// 它们在原实现里退出的是 chatCompletions 本身；搬进方法后只退出该方法。若不
	// 据此 return，控制流会继续走到下面的 fallback 与 finalErrorOf，对已经写好的
	// 响应再构造一次错误——症状是流式响应 body 正常但 Content-Type 被覆盖成
	// application/json（行为快照场景 2 抓到的正是这个）。
	if run.rotateAndServe(w, r, chatMeta) {
		return
	}
	// P2 fallback：CodeBuddy 账号池全挂（选号耗尽 + 轮转失败）时，
	// 若请求模型在备用源白名单里，把请求原样转发到备用 OpenAI 兼容端点。
	// 转发成功（非错误响应）就直接 return，不走下面的错误构造。
	if h.cfg.Fallback != nil && h.cfg.Fallback.Covers(bareModel) {
		if h.fallbackForward(w, r, run.body, bareModel) {
			st.status = http.StatusOK
			st.outcome = reqlog.OutcomeSuccess
			return
		}
	}
	// 末端错误透传（error-passthrough）：上游返回的错误原样透传，不再规范化成固定文案。
	// 上游返回（*upstream.Error）→ error.message 装**上游 body 原文**（code/msg/
	// requestId 原样保留）。HTTP 状态码按 OpenAI 兼容口径映射类别：ErrSoftRate → 429
	// （限流语义、客户端应等待重试），其余保持 503。本地调度类错误（无可用账号/
	// 传输层抖动/非上游返回的 lastErr）→ 保留自有文案 no_healthy_account（本地错误
	// 没有上游原文可透传，不编造）。
	// 末端错误构造已抽到 chatflow.go 的 finalErrorOf（纯计算：只映射，不写响应）。
	// 这里只做写回。拆分的收益不只是行数——错误映射现在可脱离 HTTP 上下文单独测试，
	// 且「分类/文案」与「轮转/响应写回」不再绞在一起（本轮修 11133 分类时正是被
	// 这层绞合绊住的：想看分类得先穿过整个轮转循环）。
	status, code, msg, hint := h.finalErrorOf(run.lastErr, run.modelBlock, run.groupFilteredOut, authKey, bareModel, run.hasImage())
	writeOpenAIErrorHint(w, status, code, msg, hint)
	st.status = status
	st.outcome = reqlog.OutcomeHTTPError
}

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
		h.cfg.Pool.BlockModelBackoff(uid, model, upstream.ModelBlockReason)
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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// flushWriter 把每次 Write 立即刷给客户端（io.Writer 包装）。
//
// 用途：备用上游的 SSE 透传要「来一块吐一块」——默认的 io.Copy 会攒到缓冲满才
// 写，流式体验被破坏（客户端看到的是一坨延迟到达的数据）。与 StreamHint 的
// 逐帧 flush 同口径。
//
// Flusher 缺失时不报错：退化成缓冲写（比直接失败好，且网关前置无反代时
// ResponseWriter 恒实现 Flusher）。
type flushWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
	n  int64
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.n += int64(n)
	if fw.fl != nil {
		fw.fl.Flush()
	}
	return n, err
}

// flushWriter 构造：从 ResponseWriter 取 Flusher（取不到则只写不刷）。
func newFlushWriter(w http.ResponseWriter) *flushWriter {
	fl, _ := w.(http.Flusher)
	return &flushWriter{w: w, fl: fl}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "api_error",
			"code":    code,
		},
	})
}

// wafCooldownBase WAF 403 软冷却基数（建议 60s 起；抖动 ±25% 后落 [45s,75s]，
// 实际进入 CooldownSoftRate 后再按 softStreak 指数、封顶 soft_rate_max）。
// 与 SoftCooldown 分流的原因：WAF 403 是 IP/指纹维频控，信号比 429「账号级限流」轻
// （账号本身健康），但比 404 重（带粘性会连环）；60s 级的快速避让已足够让频控窗口
// 滑过。抖动复用 backoff.go jitterDur（单一来源）。
const wafCooldownBase = 60 * time.Second

// writeOpenAIErrorHint 同 writeOpenAIError，另在 error 对象上附加
// error.gateway_hint（hint 为空串时不带字段——未覆盖形态不编造）。
// message 仍是上游原文透传（hint 只做并列补充，绝不替换/包装 message）。
func writeOpenAIErrorHint(w http.ResponseWriter, status int, code, msg, hint string) {
	if hint == "" {
		writeOpenAIError(w, status, code, msg)
		return
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message":      msg,
			"type":         "api_error",
			"code":         code,
			"gateway_hint": hint,
		},
	})
}
