// config.go 加载 JSON 配置 + 环境变量覆盖。
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/prompt"
)

// ModelPrice 一个模型的 OpenRouter 美元参考价（$/1M tokens）。纯展示层。
type ModelPrice struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	CacheRead float64 `json:"cache_read,omitempty"`
}

// Config 顶层配置。
// defaultBreakerProbeGap 熔断 half-open 试探间隔缺省值（ADR-003 D-400）。
// 30s：坏号被重新熔断前的最大暴露窗口，同时给"已恢复的好号"足够快的回归速度。
const defaultBreakerProbeGap = "30s"

// defaultEwmaAlpha / defaultEwmaMinSamples pool.ewma 缺省值（ADR-005 D-502）。
// 必须与 internal/pool entry.go 的 defaultEwmaAlpha / defaultEwmaMinSamples 一致。
const (
	defaultEwmaAlpha      = 0.1
	defaultEwmaMinSamples = 5
)

// EwmaConfig pool.ewma 配置段（ADR-005 D-502）。
// 默认全关/回落默认：键缺席时 Enabled=false、Alpha/MinSamples 回落默认，
// 与「未配置 = 现状行为」逐位一致。
type EwmaConfig struct {
	Enabled    bool    `json:"enabled"`     // true = 成功率/延迟信号参与选号加权；默认 false
	Alpha      float64 `json:"alpha"`       // EWMA 平滑系数 (0,1]；0 = 回落默认 0.1
	MinSamples int     `json:"min_samples"` // 每账号样本下限；<=0 = 回落默认 5
}

type Config struct {
	Listen    string `json:"listen"`     // ":7863"
	APIKey    string `json:"api_key"`    // 空 = 不鉴权（单 Key 兼容）
	AuthDir   string `json:"auth_dir"`   // ./auths
	StateFile string `json:"state_file"` // ./data/state.json
	// APIKeys 多 Key 鉴权（ADR-001）。nil = 键缺失/null（旧版兼容，单 Key 语义）；
	// 非 nil 空切片 = 显式空数组（配置错误，normalize fail fast）。
	// 每个条目的 Key 明文只在配置源；运行时索引只存摘要（见 httpauth.Index）。
	// omitempty：WriteDefault 生成默认配置时不输出 api_keys（避免生成 "api_keys":null
	// 又被 Load 判成 fail fast 的显式 null——阻断项 4 修复引入的回归）。
	APIKeys []APIKeyConfig `json:"api_keys,omitempty"`
	// apiKeysFieldSet 记录 JSON 里是否显式出现了 api_keys 键（区分「键缺失」与「显式 null」）。
	// json:"-" 不参与序列化；由 ParseConfigInto 在 unmarshal 前探测原始 JSON 设置。
	apiKeysFieldSet bool `json:"-"`

	// ModelAliases 客户端模型名 → 上游实际模型名映射。
	//
	// 用途：客户端写 `glm` 这类短名，网关转发前替换成上游真正认识的 `glm-5.3-flash`。
	// 只影响出站 body 的 model 字段；不影响选号/白名单/用量归因（白名单仍匹配客户端
	// 原始声明的裸模型名，用户配 key 时写什么名就放行什么名）。
	// 映射在 resolveModel（剥 cn:/global: 前缀）之后应用，key 用客户端原始完整名
	// （不含前缀）与裸名两种形式都查：先查完整 modelStr，再查 bareModel，命中即用。
	ModelAliases map[string]string `json:"model_aliases,omitempty"`

	// Alerting 异常外发告警（webhook）。空配置 = 不发（零回归）。
	// 触发点：账号积分跌破阈值、账号触发熔断。POST 一个通用 JSON 到 WebhookURL，
	// Server酱/TG bot/飞书自定义机器人/Discord 都能接（body 是 {text,...}，
	// 真要接特定平台格式可在外面套一层 n8n/中转）。
	Alerting struct {
		// WebhookURL 告警接收端点。空 = 不发任何告警。
		WebhookURL string `json:"webhook_url"`
		// MinCreditThreshold 账号积分低于该值时发「余额告急」告警。
		// <=0 = 关闭该触发。默认 0（关）。
		MinCreditThreshold int64 `json:"min_credit_threshold"`
		// ConsecutiveFailures 账号连续失败达该次数时发「触发熔断」告警。
		// <=0 = 关闭该触发。默认 0（关）。
		ConsecutiveFailures int `json:"consecutive_failures"`
		// DedupMinutes 同一账号同类告警的去重窗口（分钟）。窗口内重复触发不重发。
		// <=0 回落 60。
		DedupMinutes int `json:"dedup_minutes"`
	} `json:"alerting"`

	OpsAgent OpsAgentConfig `json:"opsagent"` // 运维 agent（默认关；结构见 config_opsagent.go）

	// Fallback 备用上游级联（P2）：CodeBuddy 账号池全挂（选号耗尽）时，
	// 把请求原样转发到一个独立的 OpenAI 兼容端点（DeepSeek 官方 / OpenRouter /
	// 任意 OpenAI 兼容 API）。空配置 = 关闭（零回归）。
	//
	// 触发条件：选号走完所有账号仍未拿到可用号（acct==nil 出循环）。
	// 只对 Models 白名单里列出的模型生效（避免把 CodeBuddy 专属模型发到备用源 404）。
	//
	// R-107：endpoints 配置后按序级联（列表顺序即优先级）；未配置时回退单端点
	// （base_url/api_key/models/timeout_seconds），旧配置文件零回归。
	Fallback struct {
		// Enabled 是否启用备用级联。
		Enabled bool `json:"enabled"`
		// BaseURL 备用端点 base URL（如 https://api.deepseek.com/v1）。
		// 最终请求 = {BaseURL}/chat/completions。
		BaseURL string `json:"base_url"`
		// APIKey 备用端点的 Bearer token（sk-xxx）。
		APIKey string `json:"api_key"`
		// Models 备用源支持的模型白名单。请求模型不在列表里 → 不切备用，照旧报错。
		Models []string `json:"models"`
		// TimeoutSeconds 备用源请求超时。<=0 回落 120。
		TimeoutSeconds int `json:"timeout_seconds"`
		// Endpoints 多端点级联列表（R-107）：配置后按顺序逐个尝试，
		// 第一个覆盖该模型且转发成功的端点终结请求。空 = 回退单端点旧配置。
		Endpoints []FallbackEndpointConfig `json:"endpoints"`
	} `json:"fallback"`

	// Drift 上游协议漂移观测（R-102，P1）：周期探测模型目录端点，播种响应形状
	// 基线后比对——发现目录条数腰斩/探测连续失败即判漂移并记 WARN。
	// 纯观测层：不改写出站报文，不阻塞主链路。
	Drift struct {
		// Enabled 是否启用漂移观测。默认 false（零回归）。
		Enabled bool `json:"enabled"`
		// IntervalSeconds 探测周期。<=0 回落 1800（30 分钟）。
		IntervalSeconds int `json:"interval_seconds"`
		// SnapshotPath 协议快照文件路径。空 = 与 state 文件同目录
		// （protocol-snapshot.json，跟随 state_file 迁移，与 usage.json 同规则）。
		SnapshotPath string `json:"snapshot_path"`
	} `json:"drift"`

	Panel struct {
		// PackageDetailLimit 积分构成页单账号默认展示的最近到期包数；<=0 回落 5。
		PackageDetailLimit int `json:"package_detail_limit"`
	} `json:"panel"`

	Logging struct {
		// RequestArchiveEnabled 请求元数据 JSONL 归档开关，缺省 true。
		RequestArchiveEnabled bool `json:"request_archive_enabled"`
		// RequestRetentionDays 归档保留天数，缺省 7；<=0 回落默认。
		RequestRetentionDays int `json:"request_retention_days"`
		// RequestArchiveMaxMB 归档总上限（MiB），缺省 100；<=0 回落默认。
		RequestArchiveMaxMB int `json:"request_archive_max_mb"`
		// RequestClientInfo 是否在请求日志（归档事件 + stdout 流水行 + 面板运行
		// 日志）里记录调用来源：客户端 IP 与 User-Agent。缺省 true。
		//
		// 为什么做成开关而不是恒开：来源信息是排查"谁在打网关"的第一手线索，
		// 但它比 token 计数敏感（IP 属个人信息），共享部署/多租户场景可能需要
		// 关掉。关闭后 Event.ClientIP/UserAgent 保持为空，归档里不出现该字段。
		// 热生效（经 livecfg 快照），无需重启。
		RequestClientInfo bool `json:"request_client_info"`
	} `json:"logging"`

	// Observability 可观测性阈值（F-305）。纯观测计数，不改写请求路径。
	Observability struct {
		// SlowRequestThresholdSeconds 端到端慢请求阈值（秒）：超过它的完成请求
		// +1 到 yg_gateway_slow_requests_total。
		//
		// 缺省 120 由「Load 先取 Default() 再 Unmarshal 覆盖」保证：存量 config
		// 无此键时字段保留 Default 里的 120。显式配置 <=0 = 关闭慢请求计数——
		// normalize 故意不做 <=0 clamp（加了就无法表达「关闭」），handler 侧
		// 判定 threshold>0 才计数（旧配置零回归）。
		//
		// 取值理由：延迟直方图（internal/metrics）桶顶就在 120s——正常长生成再长
		// 也很少越过这个量级；越过的正是「用户等得反常」的尾部分布，该信号要抓的
		// 就是它。与 header_timeout（60s）刻意错开：正常长生成不被误报为慢。
		SlowRequestThresholdSeconds int `json:"slow_request_threshold_seconds"`
	} `json:"observability"`

	Usage struct {
		// CreditCNYRate 积分兑人民币汇率：1 积分 = 多少元。
		// 0 = 不折算（前端不显示 ¥ 列）。默认 0.01（1 积分 ≈ ¥0.01）。
		// 纯展示层配置：不改选号/计费，只在面板用量页把积分旁折成人民币。
		CreditCNYRate float64 `json:"credit_cny_rate"`
		// Pricing 模型 OpenRouter 美元参考价（$/1M tokens），key 为裸模型名（不含 cn:/global: 前缀）。
		// 折算面板"这些 token 按公开价买要花多少美元"；纯展示，不参与选号/计费。
		// 未知模型不在表中 → 不折算（不伪造 $ 数）。
		Pricing            map[string]ModelPrice `json:"pricing,omitempty"`
		WalCheckpointAfter int                   `json:"wal_checkpoint_after"` // WAL 记录达 N 条重写基线并截断；装配期，<=0 回落 20000
	} `json:"usage"`

	Server struct {
		// ReadTimeout 入站请求读取（含 body 上传）总时长上限（issue #100）。
		// http.Server 的 ReadTimeout 覆盖整个请求读取：大上下文/文件块请求经
		// 反代链转发时上传可超过旧固定值 60s，被掐后客户端拿到
		// 400 "read body: ... i/o timeout"。缺省 "300s"；"0" = 不限制
		//（慢速 body 可无限占用连接，自担风险）；改动需重启进程。
		ReadTimeout string `json:"read_timeout"` // "300s"；"0" = 不限制
		// MaxBodyBytes /v1/chat/completions 入站请求体字节上限（F-001）。
		// 超限返回 413，读取阶段即被 MaxBytesReader 截断（超大 body 不整体进内存）。
		// 指针语义区分「未配置」与「显式关闭」：
		//   nil        = 未配置 → normalize 回落默认 64MB（安全默认，DoS 防护恒开）
		//   &0（写 0） = 显式不设上限（用户明确要旧行为，自担风险）
		//   &N         = N 字节上限
		// 改动需重启进程。
		MaxBodyBytes *int64 `json:"max_body_bytes"`
	} `json:"server"`

	Security struct {
		// TrustedProxy 受信任反向代理来源（F-002）：逗号分隔的 IP 或 CIDR
		//（如 "127.0.0.1,10.0.0.0/8,::1"）。空 = 不信任任何代理头。
		//
		// 语义：仅当请求来源落在该集合内，才采信 X-Forwarded-For / X-Real-IP
		// 作为客户端真实 IP（用于日志 + 透传上游）；否则忽略代理头、回落 TCP 对端。
		// 这是防「伪造 XFF 污染上游风控/计费」的安全默认（ProjectDiscovery
		// 「Abusing Reverse Proxies」向量）。直连部署保持空；套反代时填反代地址段。
		// 改动需重启进程。
		TrustedProxy string `json:"trusted_proxy"`
		// ReadOnlyRateLimit 只读端点（/v1/models、/status）限流：window 内最多 limit 次，
		// 按来源 IP 计数，超阈返回 429。limit<=0 或 window<=0 = 不限流（旧行为）。
		ReadOnlyRateLimit struct {
			Limit  int    `json:"limit"`  // 窗口内最大请求数；0 = 不限流
			Window string `json:"window"` // "1m" 等 duration；空/非法 = 不限流
		} `json:"read_only_rate_limit"`
	} `json:"security"`

	Cooldown struct {
		// hard_credit / err_threshold / err_cooldown 三个历史键已退役：
		// 硬冷却固定为次日 04:00（CooldownUntilTomorrow4AM），连续错误语义并入熔断器。
		// 旧 config 中的这些键因 JSON 未知字段而自然忽略，不报错。
		SoftRate string `json:"soft_rate"` // "600s"，软限流冷却基数
		// SoftRateMax 软冷却指数退避的封顶，默认 "2h"。
		// 空值回落默认，非法值报错（处理风格同 soft_rate）。
		SoftRateMax string `json:"soft_rate_max"` // "2h"
	} `json:"cooldown"`

	Schedule struct {
		CheckinHours   []int `json:"checkin_hours"`   // [9,21]
		TravelHours    []int `json:"travel_hours"`    // [9,21]
		ActivityHours  []int `json:"activity_hours"`  // [10]
		KeepaliveHours []int `json:"keepalive_hours"` // [22]
		BlackcatHours  []int `json:"blackcat_hours"`  // [23] 夜猫子窗口（23:00–08:00 计数）
		GrowthHours    []int `json:"growth_hours"`    // [1] 成长任务队列（Sequential 族每日零点解锁，01:00 自动扫描执行）
		// CheckinEnabled/TravelEnabled/ActivityEnabled/KeepaliveEnabled/BlackcatEnabled 显式禁用开关（缺省 true）。
		//
		// 为什么用独立 bool 而不是空数组/哨兵值表意"禁用"：
		//   - 空数组与 null 在老语义里已被"未配置 → 回落默认"占用，改判会静默翻转
		//     所有老 config 的行为（用户只想删掉一行，结果关掉了签到）；bool 缺省 true
		//     则对老配置零影响，向后完全兼容。
		//   - 开关与取值解耦：禁用时仍保留用户显式配的小时，重新启用无需补配。
		//   - 无需猜测哨兵（[-1] 之类），非法小时一律报错并提示改用本开关。
		// 旧 config 里的该键因 JSON 未知字段而自然忽略，不报错。
		CheckinEnabled   bool `json:"checkin_enabled"`   // 缺省 true；false = 关签到
		TravelEnabled    bool `json:"travel_enabled"`    // 缺省 true；false = 完全停猫猫旅行
		ActivityEnabled  bool `json:"activity_enabled"`  // 缺省 true；false = 停活跃上报
		KeepaliveEnabled bool `json:"keepalive_enabled"` // 缺省 true；false = 关 token 保活
		BlackcatEnabled  bool `json:"blackcat_enabled"`  // 缺省 true；false = 关夜猫子
		GrowthEnabled    bool `json:"growth_enabled"`    // 缺省 true；false = 关成长任务自动排程

		// IncludeDisabledInTasks 让「保号类」定时任务（签到 / 活跃上报 / token 保活 /
		// 余额刷新）对**已禁用（disabled）**的账号也执行。
		//
		// 为什么需要它：面板「禁用」的语义是「不再参与选号」（见面板确认文案），但这四类
		// 任务此前一律 `if st.Disabled { continue }`，等于把「停用流量」放大成「停止一切
		// 上游保号行为」——被禁用的号拿不到签到积分、不续 token、余额也不再刷新；而
		// ReenableIfCredits 明确不复活 disabled 账号（见 pool.state.go），于是签到这条唯一
		// 的自动回血路径也断了，账号只能靠人工「解冻」回来。
		//
		// 对「一次只放开一个号、用禁用做流量开关」的轮换用法（同 IP 多号防风控），闲置
		// 待命的号恰恰是最需要签到的那批——本开关即为该用法提供出口。
		//
		// 缺省 false = 保持既有行为，对老配置零影响。打开后禁用号仍会签到 / 保活，但
		// **依旧不参与选号**：pool 选号侧的 disabled 过滤不受本开关影响。
		IncludeDisabledInTasks bool `json:"include_disabled_in_tasks"`

		// 余额后台周期刷新：两次签到时点之间 credits 也能保持新鲜（面板/状态观测用）。
		// 解冻语义同签到（余额 > 0 的冷却账号自动解冻），但不做签到不刷 token。
		BalanceRefreshEnabled bool `json:"balance_refresh_enabled"` // 缺省 true；false = 关闭
		BalanceRefreshMinutes int  `json:"balance_refresh_minutes"` // 缺省 5；<=0 回落 5
	} `json:"schedule"`

	Global struct {
		// Enabled global realm 路由开关。缺省 true：Realm() 正常把 realm=global/
		// domain=workbuddy.ai 的账号判为 global 并路由 global base/路径。
		// 显式 "enabled": false 关闭（逃生门，纯 CN 锁定：即便 auth 写了 realm=global
		// 也不路由，auth.Realm() 双保险的第一道闸）。纯 CN 部署行为不变：CN 账号
		// 恒判 cn，global base 只在 realm=global 的账号上被使用。
		Enabled bool `json:"enabled"`
		// ChatBase / BillingBase 国际版上游 base 覆盖；空 = 回落内置默认
		// https://www.workbuddy.ai（internal/upstream.defaultGlobalBase）。
		ChatBase    string `json:"chat_base"`
		BillingBase string `json:"billing_base"`
	} `json:"global"`

	Upstream struct {
		// TimeoutSeconds 短 RPC（refresh/checkin/balance/FetchModels）总时长上限，默认 120。
		TimeoutSeconds int `json:"timeout_seconds"`
		// HeaderTimeoutSeconds 聊天 SSE 首字节前（响应头）上限；<=0 回落 TimeoutSeconds。
		HeaderTimeoutSeconds int `json:"header_timeout_seconds"`
		// IdleTimeoutSeconds 聊天 SSE 流中空闲上限（活跃吐数据续命不掐）；<=0 回落默认 300。
		IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
		// StreamTotalTimeoutSeconds 聊天 SSE **整请求**总时长上限（F-707）。
		// 与 HeaderTimeoutSeconds（只管首字节前）、IdleTimeoutSeconds（只管流中静默）
		// 构成三件套：补上「上游持续滴字节但永不结束」这一形态——那种流会不断刷新
		// IdleTimeout，请求/账号租约/全局在途名额被无限期占住。
		// <=0 = 不限（保持旧行为）。装配期字段（需重启）。
		StreamTotalTimeoutSeconds int `json:"stream_total_timeout_seconds"`
		// SSEKeepAliveSeconds 聊天 SSE 心跳间隔（F-706）：流中静默超该值向客户端写
		// `: ping` 注释帧，防中间层按空闲掐连长静默流（长思考链模型首字节前常等
		// 数十秒）。<=0 = 关闭（默认，零回归）。装配期字段（需重启）。
		SSEKeepAliveSeconds int `json:"sse_keepalive_seconds"`
		// UserAgent 出站 User-Agent 显式覆盖（非空时全路径生效，优先于默认三段式）。
		// 全部出站请求生效：chat/refresh/checkin/balance/report/travel/FetchModels。
		// 默认值已对齐官方 WorkBuddy 桌面形态（三段式），用户仍可配完全自定义值改写。
		UserAgent string `json:"user_agent"`
		// ClientVersion WorkBuddy 客户端版本段（出站 UA 的 `WorkBuddy/<ver>` 与归属头
		// X-IDE-Version）。空 = 内置默认（对齐官方 5.5.4 分发包）。
		ClientVersion string `json:"client_version"`
		// CliVersion 出站 UA 中 `CLI/<ver>` 段的版本。空 = 内置默认（官方内置 CLI 2.137.1）。
		CliVersion string `json:"cli_version"`
		// ClientName 用量归属头取值（X-Product / X-IDE-Name / X-IDE-Type / X-IDE-Version）。
		// 空 = 旧行为 X-Product="SaaS" 不设 X-IDE-*；配 "WorkBuddy" 则四头跟随。
		ClientName string `json:"client_name"`
		// DeviceToken 设备风控 Token（X-Device-Token 头）全局兜底；空 = 不注入。
		// 每号 auth 文件的 device_token 键优先于本项。
		DeviceToken string `json:"device_token"`
		// DeviceTokenFile device token 文件路径兜底（宿主落盘的桌面端 token，5 分钟读取缓存）。
		DeviceTokenFile string `json:"device_token_file"`
		// PassthroughIP 是否透传客户端 IP 给上游（默认 false，反代安全边界）。
		PassthroughIP bool `json:"passthrough_ip"`
	} `json:"upstream"`

	Features struct {
		// SanitizeBlacklistFingerprints 出站请求体黑名单指纹脱敏（默认 true；false 完全还原）。
		SanitizeBlacklistFingerprints bool `json:"sanitize_blacklist_fingerprints"`
	} `json:"features"`

	Prompt struct {
		// Mode passthrough（默认）= 透传客户端原始 system（降级重试仍会切到 Degraded）；
		// custom = 网关用自有系统提示词替换客户端 system/developer；
		// append = 两者并用：开头连续 system/developer 块后插网关 system，既有消息逐字不动（issue #129）。
		Mode string `json:"mode"` // "passthrough" / "custom" / "append"
		// File 提示词文件路径；空 = 内置默认 defaultprompt.md；
		// 路径非空但不可读 → 启动报错（fail fast，避免静默回落到内置默认）。
		File string `json:"file"`
	} `json:"prompt"`

	// PromptText 解析后的系统提示词文本（custom/append 模式使用）。
	PromptText string `json:"-"`
	Upstash    struct {
		URL   string `json:"url"`   // 空 = 纯内存模式；支持完整 rediss:// URL 或 https://xxx.upstash.io host
		Token string `json:"token"` // url 非完整连接串时用于组装 rediss://default:<token>@<host>:6379
	} `json:"upstash"`

	Pool struct {
		MaxInFlight        int    `json:"max_in_flight"`        // 单账号最大在途请求数，0 = 不限
		MaxInFlightGlobal  int    `json:"max_in_flight_global"` // global 域单账号在途上限（WAF 风控紧域压低并发），0 = 回落默认 2
		BreakerThreshold   int    `json:"breaker_threshold"`    // 连续失败次数触发熔断，默认 3
		BreakerCooldown    string `json:"breaker_cooldown"`     // 基础熔断时长，默认 "30m"
		BreakerCooldownMax string `json:"breaker_cooldown_max"` // 指数退避封顶，默认 "6h"
		// BreakerProbeGap 熔断到期后的 half-open 试探间隔（ADR-003 D-400）。
		// 熔断号到期后不再一次性全量放回，而是每该间隔至多放行 1 次试探；
		// 成功即全量恢复、失败则重新熔断（退避加倍）。默认 "30s"；"0" 关闭
		// half-open（退化为「到期即全量」，与旧行为逐位一致）。
		BreakerProbeGap string `json:"breaker_probe_gap"`
		// GlobalMaxInFlight 全网关在途请求总数上限（ADR-003 D-401）：超限立即
		// 429（不排队），防上游慢时请求无限堆积。默认 0 = 不限（保持旧行为）。
		// 建议按「账号数 × max_in_flight」的 ~1.5 倍设置。
		GlobalMaxInFlight int `json:"global_max_in_flight"`
		// 连败降权（issue #114）：ErrClient/传输层这类「不罚号」失败连续计数，达阈
		// 临时出池。与冷却/熔断并存取更长者不叠加。默认 5 次 / 10m。
		DegradeThreshold   int     `json:"degrade_threshold"`    // 连败次数触发降权，默认 5
		DegradeCooldown    string  `json:"degrade_cooldown"`     // 降权时长（固定，非指数退避），默认 "10m"
		DegradeCooldownMax string  `json:"degrade_cooldown_max"` // 降权时长的上限钳制，默认 "2h"（仅当 cooldown 超该值才钳制）
		IdleWeightPerHour  float64 `json:"idle_weight_per_hour"` // 闲置补偿：每小时未用 +0.5 权重
		IdleWeightMax      float64 `json:"idle_weight_max"`      // 闲置补偿封顶，默认 5.0
		// PreferExpiring 快过期加权开关，默认 true：expiring_soon 窗口内有有效批次时
		// 权重 ×2（slots=2，见 pool 路由加权）；不按到期排序、与金额无关（issue #101 口径）。
		// 关闭后完全不使用到期信息选号。
		PreferExpiring    bool `json:"prefer_expiring"`
		RecentUsageFactor bool `json:"recent_usage_factor"` // 近期用量衰减（REQ-routing P2）：权重×2^(-衰减token/200k)，半衰期 1h，默认关
		// ExpiringSoon 快过期积分窗口（如 "168h"=7天）：签到/余额刷新时，到期时间在
		// 此窗口内的批次令账号命中上述 ×3 加权；窗口开大 → 命中账号变多、
		// 偏好被稀释。空/0 = 禁用该加权门槛。
		ExpiringSoon string `json:"expiring_soon"`
		// CostExploreInterval costTier 条件探索窗口（issue #136 方案 a′）：tier 0
		// 垄断层存在且 tier 1 有成员时，距上次探索 ≥ 窗口则本次 pick 生效层切
		// tier 1-only（探索=搭车改道，零新增上游请求；成功即毕业，失败走既有
		// 错误策略）。默认 "30m"（≤48 次/天/模型）；"0" 关停（完全回到现状行为）；
		// 空值回落默认。
		CostExploreInterval string `json:"cost_explore_interval"`
		// CreditFloor 积分保底：账号余额低于该值时，对实测收费模型（tier 2）不再
		// 参与选号——防止收费请求把余额打穿、连免费模型都 402 冷却到次日签到。
		// tier 0（免费）/ tier 1（无观测）不受限；签到回血越过 floor 自动恢复。
		// 默认 0 = 关闭；负值钳 0。
		CreditFloor int64 `json:"credit_floor"`
		// Ewma 成功率/延迟 EWMA 表现回灌（ADR-005 D-502）：把「最近表现」参与
		// 选号加权。默认关闭（零回归）；开启后因子被钳制在温和区间，不会把账号
		// 权重清零（饿死防护）。字段均热生效（SetEwma），改配置无需重启。
		Ewma EwmaConfig `json:"ewma"`
	} `json:"pool"`

	SessionSticky struct {
		Enabled    bool   `json:"enabled"`     // 默认 true
		TTL        string `json:"ttl"`         // 会话绑定 TTL，默认 "30m"
		GCInterval string `json:"gc_interval"` // 会话 GC 周期，默认 "5m"
		// Derive 内容派生会话键开关（默认 true = 保持既有行为）。
		// 用 *bool 三态：nil（键缺失）= 默认 true，显式 false = 关闭派生。
		// 关闭后只有客户端显式给 conversation_id/conversationId/prompt_cache_key
		// 才粘性；其余回落纯加权轮换（防"固定 system + 短开场"把无关对话
		// 误判为同一会话、全量钉到同一个账号）。
		Derive *bool `json:"derive"`
	} `json:"session_sticky"`

	// 解析后
	SoftRateDur         time.Duration `json:"-"`
	SoftRateMaxDur      time.Duration `json:"-"`
	BreakerCooldownDur  time.Duration `json:"-"`
	BreakerCooldownMaxD time.Duration `json:"-"`
	// BreakerProbeGapDur 解析后的 half-open 试探间隔（ADR-003 D-400）；0 = 关闭。
	BreakerProbeGapDur  time.Duration `json:"-"`
	DegradeCooldownDur  time.Duration `json:"-"`
	DegradeCooldownMaxD time.Duration `json:"-"`
	SessionTTL          time.Duration `json:"-"`
	SessionGCInterval   time.Duration `json:"-"`
	// SessionStickyDerive 解析后的内容派生会话键开关（默认 true）。
	SessionStickyDerive    bool          `json:"-"`
	BalanceRefreshInterval time.Duration `json:"-"` // 0 = 不启动（enabled=false）
	ExpiringSoonDur        time.Duration `json:"-"`
	// CostExploreIntervalDur 解析后的 costTier 探索窗口（issue #136）；0 = 关停。
	CostExploreIntervalDur time.Duration `json:"-"`
	// ServerReadTimeoutDur 解析后的入站请求读取上限（issue #100）；0 = 不限制。
	ServerReadTimeoutDur time.Duration `json:"-"`
	// ReadOnlyRateLimitWindow 解析后的只读端点限流窗口（F-003）；0 = 不限流。
	ReadOnlyRateLimitWindow time.Duration `json:"-"`
	// MaxBodyBytesEffective 解析后的 body 上限（F-001）：nil（未配置）回落默认 64MB，
	// 显式 0 = 不限（0 传给 handler 表示关闭 MaxBytesReader）。
	MaxBodyBytesEffective int64 `json:"-"`
}

// FallbackEndpointConfig 级联列表中的单个备用端点（R-107）。
type FallbackEndpointConfig struct {
	BaseURL        string   `json:"base_url"`
	APIKey         string   `json:"api_key"`
	Models         []string `json:"models"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// APIKeyConfig 一把多 Key 的配置形态（ADR-001 §三）。
//
// Key 是明文（配置源手填）；运行时转摘要（httpauth.BuildIndex）。Models 三态
// 语义靠 Go slice 的 nil 区分（见 PRD v2 §三）：
//
//	nil（键缺失/null）      = 不限
//	非 nil 空切片（显式 []） = 全拒
//	非空                     = 白名单
type APIKeyConfig struct {
	ID     string   `json:"id"`     // 稳定身份标识，必填，[A-Za-z0-9_-] ≤64，不重复
	Key    string   `json:"key"`    // 明文 Key；空条目 normalize 忽略
	Name   string   `json:"name"`   // 展示名（可空）
	Models []string `json:"models"` // 三态模型白名单（支持 * 后缀通配，如 "glm-*"）
	Realm  string   `json:"realm"`  // cn/global/空=不限；其他值 fail fast
	RPM    int      `json:"rpm"`    // 该 Key 每分钟最大请求数；0 = 不限（旧行为）
	// MonthlyTokenQuota 该 Key 当月 token 硬上限（usage.Recorder 按 KeyID 持久化累计）；0 = 不限，超限拒新请求。
	MonthlyTokenQuota int64 `json:"monthly_token_quota"`
	// UsageAlertTokens 当月 token 用量告警阈值（ROADMAP v3 V3-2，只告警不拦截）；0 = 关。
	UsageAlertTokens int64 `json:"usage_alert_tokens"`
	// Group 账号池分组标签（P1-B）：该 Key 只能用同 Group 的账号。空 = 不限池。
	Group string `json:"group"`
	// AllowedCIDRs 该 Key 允许的来源 IP/CIDR 白名单（R-108）。省略/nil = 不限（向后兼容）；
	// 非空 = fail-closed，来源不在名单内即拒。非法 CIDR 在 normalize 阶段 fail fast（禁静默忽略）。
	// 真实来源 IP 由 server 层复用受信代理 XFF 判定提取，本字段只声明允许范围。
	AllowedCIDRs []string `json:"allowed_cidrs,omitempty"`
	Enabled      *bool    `json:"enabled"` // 启停开关：nil/true=启用；false=禁用（不进鉴权索引，但保留在 config 里）
}

// Default 默认配置。
func Default() *Config {
	c := &Config{
		Listen:    ":7863",
		APIKey:    "",
		AuthDir:   "./auths",
		StateFile: "./data/state.json",
	}
	c.Cooldown.SoftRate = "600s"
	c.Cooldown.SoftRateMax = "2h"
	c.Server.ReadTimeout = "300s"
	c.Panel.PackageDetailLimit = 5
	c.Logging.RequestArchiveEnabled = true
	c.Logging.RequestRetentionDays = 7
	c.Logging.RequestArchiveMaxMB = 100
	// 慢请求阈值默认 120s（取值理由见 observability 字段注释）。
	c.Observability.SlowRequestThresholdSeconds = 120
	// 积分兑人民币默认汇率 1 积分 ≈ ¥0.01（纯展示层，不改选号）。
	c.Usage.CreditCNYRate = 0.01
	// 缺省 true 靠显式赋值实现（同 Schedule 开关）：JSON 里键缺席时字段保留此值，
	// 只有显式 false 才关闭来源记录。
	c.Logging.RequestClientInfo = true
	c.Schedule.CheckinHours = []int{9, 21}
	c.Schedule.TravelHours = []int{9, 21}
	c.Schedule.ActivityHours = []int{10}
	c.Schedule.KeepaliveHours = []int{22}
	c.Schedule.BlackcatHours = []int{23}
	c.Schedule.GrowthHours = []int{1}
	// 开关「缺省 true」靠这几行实现：Load 先取 Default() 再 json.Unmarshal 覆盖，
	// 键缺席（或为 null）时字段原样保留 true，只有显式 false 才关。
	c.Schedule.CheckinEnabled = true
	c.Schedule.GrowthEnabled = true
	c.Schedule.TravelEnabled = true
	c.Schedule.ActivityEnabled = true
	c.Schedule.KeepaliveEnabled = true
	c.Schedule.BlackcatEnabled = true
	c.Schedule.BalanceRefreshEnabled = true
	c.Schedule.BalanceRefreshMinutes = 5
	c.Upstream.TimeoutSeconds = 120
	// HeaderTimeoutSeconds/IdleTimeoutSeconds 默认 0（未设置态），回落见 normalize()。
	c.Upstream.HeaderTimeoutSeconds = 0
	c.Upstream.IdleTimeoutSeconds = 0
	// Global.Enabled 缺省 true（纯 CN 行为不变：CN 账号恒判 cn，global base 不被使用）；
	// ChatBase/BillingBase 缺省空（回落内置默认）。
	c.Global.Enabled = true
	c.Features.SanitizeBlacklistFingerprints = true
	c.Prompt.Mode = "passthrough" // 缺省 passthrough：透传客户端原始 system（对齐上游；custom 由用户显式选择）
	c.Pool.MaxInFlight = 3
	// MaxInFlightGlobal 缺省 2：global 域 WAF 风控更紧，压低单号并发（WAF 403 修复
	// P1-1）；0/负数 normalize 回落默认（与 max_in_flight 的 0=不限语义不同，分档键
	// 的 0 没有合理语义，回退分档默认最稳）。
	c.Pool.MaxInFlightGlobal = 2
	c.Pool.BreakerThreshold = 3
	c.Pool.BreakerCooldown = "30m"
	c.Pool.BreakerCooldownMax = "6h"
	// BreakerProbeGap 缺省 "30s"（ADR-003 D-400）：熔断到期后限速试探，避免
	// 到期瞬间全量洪峰把坏号二次打挂（二次熔断会让退避翻倍、最长 6h）。
	c.Pool.BreakerProbeGap = defaultBreakerProbeGap
	// GlobalMaxInFlight 缺省 0 = 不限（ADR-003 D-401）：默认不改变既有行为。
	c.Pool.GlobalMaxInFlight = 0
	c.Pool.DegradeThreshold = 5
	c.Pool.DegradeCooldown = "10m"
	c.Pool.DegradeCooldownMax = "2h"
	c.Pool.IdleWeightPerHour = 0.5
	c.Pool.IdleWeightMax = 5.0
	c.Pool.PreferExpiring = true
	c.Pool.ExpiringSoon = "168h" // 快过期窗口默认 7 天：官方活动奖励积分多在两周内过期
	// costTier 探索默认 30m（issue #136：垄断破除 + 搭车改道零新增请求）；"0" 关停。
	c.Pool.CostExploreInterval = "30m"
	// EWMA 表现回灌默认关闭（ADR-005 D-502）：不改变既有行为；Alpha/MinSamples
	// 默认值必须与 internal/pool entry.go 的 defaultEwma* 一致。
	c.Pool.Ewma = EwmaConfig{Enabled: false, Alpha: defaultEwmaAlpha, MinSamples: defaultEwmaMinSamples}
	c.SessionSticky.Enabled = true
	c.SessionSticky.TTL = "30m"
	c.SessionSticky.GCInterval = "5m"
	return c
}

// defaultPricing 内置 OpenRouter 美元参考价（$/1M tokens）。
// 数值为 2025-2026 年公开价的近似值，仅供"白嫖感知"展示——不是实时报价，
// 用户应在 config.json usage.pricing 里按实际模型覆盖。
// key 为裸模型名（不含 cn:/global: 前缀），与 usage 桶的 Model 字段同口径。
func defaultPricing() map[string]ModelPrice {
	return map[string]ModelPrice{
		// GLM 系列（智谱）
		"glm-5.3-flash": {Input: 0.15, Output: 0.6, CacheRead: 0.015},
		"glm-5.3":       {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"glm-5.2":       {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"glm-5.1":       {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"glm-5v-turbo":  {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		// MiniMax
		"minimax-m3": {Input: 0.3, Output: 1.2, CacheRead: 0.03},
		// DeepSeek
		"deepseek-v4-flash":   {Input: 0.15, Output: 0.6, CacheRead: 0.015},
		"deepseek-v4.1-flash": {Input: 0.15, Output: 0.6, CacheRead: 0.015},
		"deepseek-v4-pro":     {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		// Kimi（月之暗面）
		"kimi-k2.6":         {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"kimi-k2.7":         {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"kimi-k2.8-preview": {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		"kimi-k3-1":         {Input: 0.6, Output: 2.4, CacheRead: 0.06},
		// 混元（腾讯）
		"hy3":         {Input: 0.1, Output: 0.1, CacheRead: 0.01},
		"hy3-x":       {Input: 0.1, Output: 0.1, CacheRead: 0.01},
		"hy4-preview": {Input: 1.1, Output: 1.1, CacheRead: 0.11},
		// 阶跃
		"step-5-preview": {Input: 0.6, Output: 2.4, CacheRead: 0.06},
	}
}

// Load 从文件读，再用 WB2A_* env 覆盖。
func Load(path string) (*Config, error) {
	c := Default()
	if path != "" {
		// 目录检查：Docker bind mount 在宿主机文件缺失时会静默创建同名目录，
		// 直接 ReadFile 会报 "Incorrect function" 之类晦涩错误，这里给出可操作提示。
		if st, statErr := os.Stat(path); statErr == nil && st.IsDir() {
			return nil, fmt.Errorf("config %s 是目录而非文件——"+
				"Docker 部署时若宿主机缺少 config.json，bind mount 会创建同名目录。"+
				"请先 `cp config.example.json config.json` 或删除该目录（程序会自动生成配置）", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if _, err := ParseConfigInto(raw, c); err != nil {
			return nil, err
		}
	}
	applyEnv(c)
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseConfigInto 把 JSON 覆盖到 c 上并 normalize（不做 env、不读文件）。
// 面板保存配置走这条路径：与 Load 完全同一套解析/校验逻辑，避免两处漂移。
func ParseConfigInto(raw []byte, c *Config) (*Config, error) {
	// 探测 api_keys 键是否显式出现（区分「键缺失」与「显式 null」，阻断项 4）。
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err == nil {
		if _, ok := probe["api_keys"]; ok {
			c.apiKeysFieldSet = true
		}
	}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseConfig 基于默认值解析一段配置 JSON（等价于 Load 的文件分支，但不读环境变量）。
func ParseConfig(raw []byte) (*Config, error) {
	return ParseConfigInto(raw, Default())
}

// WriteDefault 在 path 落一份推荐配置（首次运行自动生成，双击即开免手工复制样例）。
// 值取自 Default()（含超时/熔断/签到排程等推荐值），api_key 用 crypto/rand 随机生成：
// 安全默认优于示例占位符（listen 绑定 0.0.0.0，空 key 会把网关裸暴露给局域网）。
// 返回生成的 key 供启动日志透出。已存在时经 O_EXCL 原子拒绝，绝不改写用户配置。
func WriteDefault(path string) (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("gen api_key: %w", err)
	}
	key := "sk-" + base64.RawURLEncoding.EncodeToString(raw)
	c := Default()
	c.APIKey = key
	_ = c.normalize() // Default() 全合法，normalize 仅补齐 header/idle 超时的展示值
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("mkdir config dir: %w", err)
		}
	}
	// O_EXCL 原子拒绝覆盖：即使调用方漏判"不存在"，也绝不悄悄改写用户已有配置。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(out); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	return key, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("WB2A_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := os.Getenv("WB2A_API_KEY"); v != "" {
		c.APIKey = v
	}
	if v := os.Getenv("WB2A_AUTH_DIR"); v != "" {
		c.AuthDir = v
	}
	if v := os.Getenv("WB2A_STATE_FILE"); v != "" {
		c.StateFile = v
	}
	if v := os.Getenv("WB2A_SOFT_RATE"); v != "" {
		c.Cooldown.SoftRate = v
	}
	if v := os.Getenv("WB2A_SOFT_RATE_MAX"); v != "" {
		c.Cooldown.SoftRateMax = v
	}
	if v := os.Getenv("WB2A_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.TimeoutSeconds = n
		}
	}
	if v := os.Getenv("WB2A_HEADER_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.HeaderTimeoutSeconds = n
		}
	}
	if v := os.Getenv("WB2A_IDLE_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Upstream.IdleTimeoutSeconds = n
		}
	}
	if v := os.Getenv("WB2A_USER_AGENT"); v != "" {
		c.Upstream.UserAgent = v
	}
	if v := os.Getenv("WB2A_CLIENT_VERSION"); v != "" {
		c.Upstream.ClientVersion = v
	}
	if v := os.Getenv("WB2A_CLI_VERSION"); v != "" {
		c.Upstream.CliVersion = v
	}
	if v := os.Getenv("WB2A_CLIENT_NAME"); v != "" {
		c.Upstream.ClientName = v
	}
	if v := os.Getenv("WB2A_DEVICE_TOKEN"); v != "" {
		c.Upstream.DeviceToken = v
	}
	if v := os.Getenv("WB2A_DEVICE_TOKEN_FILE"); v != "" {
		c.Upstream.DeviceTokenFile = v
	}
	if v := os.Getenv("WB2A_PASSTHROUGH_IP"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Upstream.PassthroughIP = b
		}
	}
	if v := os.Getenv("WB2A_SANITIZE_FINGERPRINTS"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Features.SanitizeBlacklistFingerprints = b
		}
	}
	if v := os.Getenv("WB2A_PROMPT_MODE"); v != "" {
		c.Prompt.Mode = v
	}
	if v := os.Getenv("WB2A_PROMPT_FILE"); v != "" {
		c.Prompt.File = v
	}
	if v := os.Getenv("WB2A_EXPIRING_SOON"); v != "" {
		c.Pool.ExpiringSoon = v
	}
	if v := os.Getenv("WB2A_PREFER_EXPIRING"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.Pool.PreferExpiring = b
		}
	}
}

func (c *Config) normalize() error {
	var err error
	// 美元参考价合并：内置默认表为底，用户 usage.pricing 按 key 覆盖同名模型。
	// 不区分"用户没配"与"用户配了空表"——空表 = 用户显式不要默认价。
	if c.Usage.Pricing == nil {
		c.Usage.Pricing = defaultPricing()
	} else {
		for k, v := range defaultPricing() {
			if _, ok := c.Usage.Pricing[k]; !ok {
				c.Usage.Pricing[k] = v
			}
		}
	}
	if err := c.normalizeAPIKeys(); err != nil {
		return err
	}
	if c.Panel.PackageDetailLimit <= 0 {
		c.Panel.PackageDetailLimit = 5
	}
	if c.Logging.RequestRetentionDays <= 0 {
		c.Logging.RequestRetentionDays = 7
	}
	if c.Logging.RequestArchiveMaxMB <= 0 {
		c.Logging.RequestArchiveMaxMB = 100
	}
	// 入站读取上限（issue #100）：空值回落默认 300s；"0" 合法（不限制）；
	// 负值无语义，fail fast（静默钳 0 会把保护悄悄关掉）。
	if c.Server.ReadTimeout == "" {
		c.Server.ReadTimeout = "300s"
	}
	if c.ServerReadTimeoutDur, err = time.ParseDuration(c.Server.ReadTimeout); err != nil {
		return fmt.Errorf("server.read_timeout: %w", err)
	}
	if c.ServerReadTimeoutDur < 0 {
		return fmt.Errorf("server.read_timeout: 负时长 %q 无意义", c.Server.ReadTimeout)
	}
	// 入站 body 上限（F-001）：nil（未配置）回落默认 64MB，DoS 防护恒开；
	// 显式 0 = 不限（旧行为，用户明确关闭）；负值无意义 fail fast。
	if c.Server.MaxBodyBytes == nil {
		c.MaxBodyBytesEffective = 64 << 20 // 64MB 安全默认
	} else {
		if *c.Server.MaxBodyBytes < 0 {
			return fmt.Errorf("server.max_body_bytes: 负值 %d 无意义（0=不设限，正数=上限）", *c.Server.MaxBodyBytes)
		}
		c.MaxBodyBytesEffective = *c.Server.MaxBodyBytes
	}
	// 只读端点限流窗口（F-003）：limit<=0 或 window 空/非法 = 不限流（旧行为）。
	// window 合法但 limit<=0 时静默不限流（配置了窗口但没给额度 = 用户想关限流）；
	// window 非法且 limit>0 时报错（给了额度但窗口写错 = 用户想限流但配错了）。
	if c.Security.ReadOnlyRateLimit.Limit > 0 {
		if c.Security.ReadOnlyRateLimit.Window == "" {
			return fmt.Errorf("security.read_only_rate_limit.window: 给定了 limit 但 window 为空")
		}
		if c.ReadOnlyRateLimitWindow, err = time.ParseDuration(c.Security.ReadOnlyRateLimit.Window); err != nil {
			return fmt.Errorf("security.read_only_rate_limit.window: %w", err)
		}
		if c.ReadOnlyRateLimitWindow <= 0 {
			return fmt.Errorf("security.read_only_rate_limit.window: 非正时长 %q 无意义", c.Security.ReadOnlyRateLimit.Window)
		}
	}
	if c.SoftRateDur, err = time.ParseDuration(c.Cooldown.SoftRate); err != nil {
		return fmt.Errorf("cooldown.soft_rate: %w", err)
	}
	// 空值回落默认 2h（Default() 已置值；此兜底覆盖显式 "" 与 Default() 被绕过的场景）。
	if c.Cooldown.SoftRateMax == "" {
		c.Cooldown.SoftRateMax = "2h"
	}
	if c.SoftRateMaxDur, err = time.ParseDuration(c.Cooldown.SoftRateMax); err != nil {
		return fmt.Errorf("cooldown.soft_rate_max: %w", err)
	}
	if c.BreakerCooldownDur, err = time.ParseDuration(c.Pool.BreakerCooldown); err != nil {
		return fmt.Errorf("pool.breaker_cooldown: %w", err)
	}
	if c.BreakerCooldownMaxD, err = time.ParseDuration(c.Pool.BreakerCooldownMax); err != nil {
		return fmt.Errorf("pool.breaker_cooldown_max: %w", err)
	}
	// half-open 试探间隔（ADR-003 D-400）："0" 合法（关闭），故不能沿用
	// ParseDuration 直接失败——空串才回落默认。
	if c.Pool.BreakerProbeGap == "" {
		c.Pool.BreakerProbeGap = defaultBreakerProbeGap
	}
	if c.BreakerProbeGapDur, err = time.ParseDuration(c.Pool.BreakerProbeGap); err != nil {
		return fmt.Errorf("pool.breaker_probe_gap: %w", err)
	}
	if c.BreakerProbeGapDur < 0 {
		return fmt.Errorf("pool.breaker_probe_gap: 负值 %s 无意义（0=关闭）", c.Pool.BreakerProbeGap)
	}
	if c.DegradeCooldownDur, err = time.ParseDuration(c.Pool.DegradeCooldown); err != nil {
		return fmt.Errorf("pool.degrade_cooldown: %w", err)
	}
	if c.DegradeCooldownMaxD, err = time.ParseDuration(c.Pool.DegradeCooldownMax); err != nil {
		return fmt.Errorf("pool.degrade_cooldown_max: %w", err)
	}
	if c.SessionTTL, err = time.ParseDuration(c.SessionSticky.TTL); err != nil {
		return fmt.Errorf("session_sticky.ttl: %w", err)
	}
	if c.SessionGCInterval, err = time.ParseDuration(c.SessionSticky.GCInterval); err != nil {
		return fmt.Errorf("session_sticky.gc_interval: %w", err)
	}
	// 派生会话键三态（ADR-004）：键缺失/显式 true → 开（保持既有行为）；
	// 显式 false → 关。nil 与 false 必须区分，故用 *bool。
	c.SessionStickyDerive = c.SessionSticky.Derive == nil || *c.SessionSticky.Derive
	// 快过期窗口：空 = 禁用（ExpiringSoonDur 0）；非空必须可解析（拼写错误 fail fast）。
	if c.Pool.ExpiringSoon != "" {
		if c.ExpiringSoonDur, err = time.ParseDuration(c.Pool.ExpiringSoon); err != nil {
			return fmt.Errorf("pool.expiring_soon: %w", err)
		}
	}
	if c.ExpiringSoonDur < 0 {
		c.ExpiringSoonDur = 0
		c.Pool.ExpiringSoon = "0"
	}
	// costTier 探索窗口（issue #136）：空值回落默认 30m（Default 已置；此兜底覆盖
	// 显式 ""）；"0" 是合法值（关停，完全回到现状行为），不回落；负值钳 0 同关停
	//（"−5m" 无合理语义）。
	if c.Pool.CostExploreInterval == "" {
		c.Pool.CostExploreInterval = "30m"
	}
	if c.CostExploreIntervalDur, err = time.ParseDuration(c.Pool.CostExploreInterval); err != nil {
		return fmt.Errorf("pool.cost_explore_interval: %w", err)
	}
	if c.CostExploreIntervalDur < 0 {
		c.CostExploreIntervalDur = 0
	}
	// 积分保底：负值钳 0（= 关闭）。0 是合法默认（关闭），无需空值回落。
	if c.Pool.CreditFloor < 0 {
		c.Pool.CreditFloor = 0
	}
	// pool.ewma 归一（ADR-005 D-502）：alpha 0/负值回落默认、>1 报错；min_samples
	// <=0 回落默认。enabled 无归一（bool 无非法态）。
	if c.Pool.Ewma.Alpha <= 0 {
		c.Pool.Ewma.Alpha = defaultEwmaAlpha
	}
	if c.Pool.Ewma.Alpha > 1 {
		return fmt.Errorf("pool.ewma.alpha: 必须在 (0,1]，got %v", c.Pool.Ewma.Alpha)
	}
	if c.Pool.Ewma.MinSamples <= 0 {
		c.Pool.Ewma.MinSamples = defaultEwmaMinSamples
	}
	if c.Pool.BreakerThreshold <= 0 {
		c.Pool.BreakerThreshold = 3
	}
	// 连败降权参数缺省归一（非法/未设置回落默认，与 breaker_threshold 同风格）。
	if c.Pool.DegradeThreshold <= 0 {
		c.Pool.DegradeThreshold = 5
	}
	if c.Pool.DegradeCooldown == "" {
		c.Pool.DegradeCooldown = "10m"
	}
	if c.Pool.DegradeCooldownMax == "" {
		c.Pool.DegradeCooldownMax = "2h"
	}
	// global 在途分档：0/负数视为未设置回落默认 2（WAF 403 修复 P1-1）。
	if c.Pool.MaxInFlightGlobal <= 0 {
		c.Pool.MaxInFlightGlobal = 2
	}
	if c.Pool.IdleWeightPerHour <= 0 {
		c.Pool.IdleWeightPerHour = 0.5
	}
	if c.Pool.IdleWeightMax <= 0 {
		c.Pool.IdleWeightMax = 5.0
	}
	if c.Upstream.TimeoutSeconds <= 0 {
		c.Upstream.TimeoutSeconds = 120
	}
	// header 缺省回落 timeout（保"首字节前换号"既有语义）；idle 缺省走内置大值。
	// 任务书约定：0 一律视为"未设置"走默认，真正的"禁用"留待后续（避免歧义）。
	if c.Upstream.HeaderTimeoutSeconds <= 0 {
		c.Upstream.HeaderTimeoutSeconds = c.Upstream.TimeoutSeconds
	}
	if c.Upstream.IdleTimeoutSeconds <= 0 {
		c.Upstream.IdleTimeoutSeconds = 300
	}
	// 整请求总时长上限与 SSE 心跳：<=0 = 关闭（零回归，不设默认值）。
	// 有意不给默认：两者都会改变线上流的行为（掐长流 / 多写注释帧），
	// 应由部署方按自己的中间层形态显式开启。
	if c.Upstream.StreamTotalTimeoutSeconds < 0 {
		c.Upstream.StreamTotalTimeoutSeconds = 0
	}
	if c.Upstream.SSEKeepAliveSeconds < 0 {
		c.Upstream.SSEKeepAliveSeconds = 0
	}
	if !strings.HasPrefix(c.Listen, ":") && !strings.Contains(c.Listen, ":") {
		c.Listen = ":" + c.Listen
	}
	// 空数组与 null 反序列化后覆盖掉 Default() 的排程值（键缺席才保留），在此补齐。
	// 空 = 未配置 → 回落默认；「禁用」一律走 *_enabled=false，两者互不混淆。
	if len(c.Schedule.CheckinHours) == 0 {
		c.Schedule.CheckinHours = []int{9, 21}
	}
	if len(c.Schedule.TravelHours) == 0 {
		c.Schedule.TravelHours = []int{9, 21}
	}
	if len(c.Schedule.ActivityHours) == 0 {
		c.Schedule.ActivityHours = []int{10}
	}
	if len(c.Schedule.KeepaliveHours) == 0 {
		c.Schedule.KeepaliveHours = []int{22}
	}
	if len(c.Schedule.BlackcatHours) == 0 {
		c.Schedule.BlackcatHours = []int{23}
	}
	if len(c.Schedule.GrowthHours) == 0 {
		c.Schedule.GrowthHours = []int{1}
	}
	// 余额后台刷新：启用时 minutes<=0 回落默认 5；关闭时 interval 保持 0（不启动）。
	if c.Schedule.BalanceRefreshEnabled {
		if c.Schedule.BalanceRefreshMinutes <= 0 {
			c.Schedule.BalanceRefreshMinutes = 5
		}
		c.BalanceRefreshInterval = time.Duration(c.Schedule.BalanceRefreshMinutes) * time.Minute
	}
	// 告警去重窗口兜底：<=0 回落 60 分钟（同一账号同类事件 1h 内不重发）。
	if c.Alerting.DedupMinutes <= 0 {
		c.Alerting.DedupMinutes = 60
	}
	if c.Alerting.MinCreditThreshold < 0 {
		c.Alerting.MinCreditThreshold = 0
	}
	if c.Alerting.ConsecutiveFailures < 0 {
		c.Alerting.ConsecutiveFailures = 0
	}
	if err := c.validateScheduleHours(); err != nil {
		return err
	}
	return c.normalizePrompt()
}

// normalizeAPIKeys 校验并归一化多 Key 配置（ADR-001 §四 空值语义 + §五 约束）。
//
// 空值语义（靠 Go slice 的 nil 区分，无需自定义反序列化）：
//
//	APIKeys == nil          → 键缺失（旧版兼容，跳过校验）
//	APIKeys != nil && len 0 → 显式 []，配置错误 fail fast
//	过滤空 key 条目后为 0     → 全空条目，配置错误 fail fast
//
// 阻断项 1（重复密钥）：同一个密钥值同时出现在 api_key（单值）与 api_keys[].key，
// 或出现在两条多 Key 之间，会导致 Match 命中第一把 Key 而绕过另一把的 models/realm
// 约束。这里**拒绝重复的有效密钥**，不静默合并约束、不依赖索引插入顺序。
func (c *Config) normalizeAPIKeys() error {
	// 键缺失：旧版兼容（无多 Key）。
	if c.APIKeys == nil && !c.apiKeysFieldSet {
		return nil
	}
	// 显式 null：按 PRD/ADR 契约 fail fast（不能静默降级为免鉴权，阻断项 4）。
	if c.APIKeys == nil && c.apiKeysFieldSet {
		return fmt.Errorf("api_keys: 显式 null 不是合法配置；要么删除该键（回到单 api_key），要么提供有效数组")
	}
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("api_keys: 显式空数组；要关闭多 Key 请删除该键（回到单 api_key），而不是配空数组")
	}
	seenID := map[string]bool{}
	seenKey := map[string]string{}                      // 明文 key → id（重复检测）
	filtered := make([]APIKeyConfig, 0, len(c.APIKeys)) // 新切片：append 不污染 c.APIKeys 底层数组
	for _, k := range c.APIKeys {
		key := strings.TrimSpace(k.Key)
		if key == "" {
			continue // 空 key 条目忽略
		}
		id := strings.TrimSpace(k.ID)
		if id == "" {
			return fmt.Errorf("api_keys[%s]: id 不能为空", k.ID)
		}
		if !validKeyID(id) {
			return fmt.Errorf("api_keys[%s]: id 仅允许 [A-Za-z0-9_-] 且 ≤64 字符", id)
		}
		if id == httpauth.SingleKeyID {
			return fmt.Errorf("api_keys[%s]: id %q 是保留名（旧版单 api_key 的合成身份），请换一个 id", id, httpauth.SingleKeyID)
		}
		if seenID[id] {
			return fmt.Errorf("api_keys: id %q 重复（id 是稳定归因键，必须唯一）", id)
		}
		seenID[id] = true
		// 重复密钥检测（阻断项 1）：与单值 api_key 或已见多 Key 重复 → fail fast。
		//
		// 两边归一化口径必须一致（2026-10-17，B3-c）：k.Key 会被 TrimSpace 后落库
		// （见下方 `k.Key = key`），而顶层 api_key 从不 trim（运行时摘要直接取原值，
		// 见 main.go 的 sha256.Sum256([]byte(c.APIKey))）。若这里按原值比，
		// `api_key: " sk-dup "` 配 `api_keys[].key: "sk-dup"` 会被判成「不同密钥」：
		// 管理员意图里的同一把密钥静默变成两把，约束是否生效取决于客户端发哪个——
		// 既违背本检测的初衷，也让排查失去落点。故比较时对 c.APIKey 也 TrimSpace
		// （**只影响比较**，运行时仍用原值，不改鉴权语义）。
		if c.APIKey != "" && key == strings.TrimSpace(c.APIKey) {
			return fmt.Errorf("api_keys[%s]: key 与顶层 api_key 重复；同一密钥不能同时作为单值 Key 与受限多 Key（会绕过 models/realm 约束）", id)
		}
		if prevID, dup := seenKey[key]; dup {
			return fmt.Errorf("api_keys: key 重复（id %q 与 %q 使用同一密钥）；同一密钥值只能出现一次，避免约束被另一把绕过", prevID, id)
		}
		seenKey[key] = id
		if r := strings.ToLower(strings.TrimSpace(k.Realm)); r != "" && r != "cn" && r != "global" {
			return fmt.Errorf("api_keys[%s]: realm %q 不是合法值（cn / global / 空）", id, k.Realm)
		}
		// R-108: 逐条校验来源 IP/CIDR 白名单。非法即 fail fast（对齐 realm 校验语义，
		// 禁止静默忽略——配错名单却当成「不限」放行会绕过预期的网络隔离）。
		// 空串条目视为未填写，剔除（与「空 key 条目忽略」同宽松度）；裸 IP 按 /32 接受。
		// 四态纪律：输入 nil（键省略）保持 nil = 不限，不转成空切片（与 Models 的三态
		// 区分一致地表达「省略 = 不配置」）。
		if k.AllowedCIDRs != nil {
			cidrs := make([]string, 0, len(k.AllowedCIDRs))
			for i, raw := range k.AllowedCIDRs {
				c := strings.TrimSpace(raw)
				if c == "" {
					continue
				}
				if _, _, err := net.ParseCIDR(c); err != nil && net.ParseIP(c) == nil {
					return fmt.Errorf("api_keys[%s].allowed_cidrs[%d]: %q 不是合法 IP/CIDR", id, i, raw)
				}
				cidrs = append(cidrs, c)
			}
			k.AllowedCIDRs = cidrs
		}
		k.ID = id
		k.Key = key
		k.Realm = strings.ToLower(strings.TrimSpace(k.Realm))
		filtered = append(filtered, k)
	}
	if len(filtered) == 0 {
		return fmt.Errorf("api_keys: 没有有效条目（key 全为空）；要么提供至少一把有效 Key，要么删除该键")
	}
	c.APIKeys = filtered
	return nil
}

// validKeyID 校验 id 白名单（R4）：[A-Za-z0-9_-] 且 ≤64。
func validKeyID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' {
			continue
		}
		return false
	}
	return true
}

// normalizePrompt 校验 prompt.mode 并按 file 加载提示词文本（custom/append 模式）。
//
// mode 非法（非 passthrough/custom/append）启动报错，避免静默回落到某一分支；
// custom/append 模式下 file 非空但不可读 → 报错（fail fast），file 空 → 用内置默认
// （两模式共用同一加载路径，PromptText 均非空）。
// passthrough 模式不加载文本（透传客户端原始 system，文本在降级时用 prompt.Degraded）。
func (c *Config) normalizePrompt() error {
	switch m := strings.ToLower(strings.TrimSpace(c.Prompt.Mode)); m {
	case "", "passthrough":
		c.Prompt.Mode = "passthrough"
	case "custom":
		c.Prompt.Mode = "custom"
	case "append":
		c.Prompt.Mode = "append"
	default:
		return fmt.Errorf("prompt.mode: %q 不是合法值（passthrough / custom / append）", c.Prompt.Mode)
	}
	if c.Prompt.Mode == "custom" || c.Prompt.Mode == "append" {
		text, err := prompt.Load(c.Prompt.Mode, c.Prompt.File)
		if err != nil {
			return err
		}
		c.PromptText = text
	}
	return nil
}

// validateScheduleHours 校验排程小时落在 0-23。
//
// 为什么不用 `[-1]` 之类的哨兵值表意"禁用"：非法小时被静默吞掉时，用户以为关掉了签到，
// 实际可能被当成另一个整点照常执行；这里直接快速失败，并在错误信息里指向正确的开关
// （checkin_enabled / keepalive_enabled），避免用户靠猜哨兵值来配。
func (c *Config) validateScheduleHours() error {
	if err := checkHourRange("schedule.checkin_hours", "checkin_enabled", c.Schedule.CheckinHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.travel_hours", "travel_enabled", c.Schedule.TravelHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.activity_hours", "activity_enabled", c.Schedule.ActivityHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.keepalive_hours", "keepalive_enabled", c.Schedule.KeepaliveHours); err != nil {
		return err
	}
	if err := checkHourRange("schedule.blackcat_hours", "blackcat_enabled", c.Schedule.BlackcatHours); err != nil {
		return err
	}
	return checkHourRange("schedule.growth_hours", "growth_enabled", c.Schedule.GrowthHours)
}

func checkHourRange(field, switchKey string, hours []int) error {
	for _, h := range hours {
		if h < 0 || h > 23 {
			return fmt.Errorf("%s: %d 不是合法小时（0-23）；如要关闭该任务请设 schedule.%s=false", field, h, switchKey)
		}
	}
	return nil
}
