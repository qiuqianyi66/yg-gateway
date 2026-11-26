// Package provider 定义上游提供方的**静态枚举**：走什么协议族（Kind）、额度怎么算
// （QuotaKind）。装配与注册表由 cmd/server 的 provider_wiring 直接按 config.providers[]
// 建 map 完成——本包不持有任何状态、不做注册表（2026-11-14 精简：Registry 层从未被
// 生产接线，留着是「看起来能用、实际空转」的误导，已删；二期若需要注册表按 ADR-026 重建）。
//
// 定位（ADR-026）：本包只回答「这个上游是什么形状」。**禁止**加入运行期状态
// （剩余额度 / 健康分 / 冷却 / 在途数）——那些仍归 internal/pool。这条边界是
// 并存期（provider 抽象 + 现有 credits 状态机）防止两处概念漂移的唯一约束。
//
// 依赖纪律：叶子层，零 internal 依赖。新增 internal import 前必须更新
// scripts/check-boundaries.ps1 的 allow 表并说明理由。
package provider

// Kind 上游协议族——区分的是**报文形态**，不是厂商。
// 两个不同厂商若都是 OpenAI 兼容，用 KindOpenAICompat 即可，各自靠 config 的 id 区分。
type Kind string

const (
	// KindCodeBuddy 腾讯 WorkBuddy / CodeBuddy 逆向 OAuth 协议。
	// R8 冻结：三段式 UA、wb2api-<ms> conversationId、chatcmpl-wb2api 哨兵
	// 属于该协议族的报文内容，不得因为 provider 化而改名或参数化。
	KindCodeBuddy Kind = "codebuddy"
	// KindOpenAICompat 任意 OpenAI 兼容端点（/chat/completions + Bearer）。
	KindOpenAICompat Kind = "openai_compat"
	// KindArk 火山方舟 Ark（OpenAI 兼容族，但模型目录与额度口径独立登记）。
	KindArk Kind = "ark"
)

// QuotaKind 额度语义——决定「剩余额度」是什么意思，选号策略据此分派。
// 不要把配额语义压进同一种数值：积分是「会到期烧掉的批次」，余额是「充值后
// 递减的钱」，滚动窗口是「5 小时/每周重置的额度」。
type QuotaKind string

const (
	// QuotaCredits 积分批次：有到期时间、可保底、可按模型实测成本分层（CodeBuddy）。
	QuotaCredits QuotaKind = "credits"
	// QuotaMetered 按量计费：余额 / 免费额度，无到期批次（Ark / OpenAI）。
	QuotaMetered QuotaKind = "metered"
	// QuotaWindow 滚动窗口：5 小时 / 每周重置（订阅型账号）。
	QuotaWindow QuotaKind = "window"
)

// Preset 常见 OpenAI 兼容厂商的静态接入参数。
//
// 只收敛「容易写错且相对稳定」的 base_url / kind / quota / 默认模型；凭证永不进
// preset（必须由配置或 auths 提供）。preset 只是配置糖：显式字段优先，厂商改版时
// 仍可在 config 覆盖。新增 preset 必须在 README 写明实测日期与限制，禁止把未验证
// 的模型当成可用承诺。
type Preset struct {
	ID      string
	Kind    Kind
	Quota   QuotaKind
	BaseURL string
	Models  []string
	// Notes 面向运维的限制说明（免费额度/速率/上下文），不参与出站。
	Notes string
}

// Presets 已登记的厂商预设（当前只登记有公开 OpenAI 兼容文档的 Cerebras）。
//
// Cerebras 免费层不是“无限免费”：官方 rate-limit 文档给出 Free Trial $5 额度与
// 分模型 RPM/TPM 上限（gpt-oss-120b 5 RPM / 30K TPM，qwen-3.8-27b 300 RPM /
// 150K TPM）；额度/模型会变，config 仍应显式覆盖 models。
var Presets = map[string]Preset{
	"cerebras": {
		ID:      "cerebras",
		Kind:    KindOpenAICompat,
		Quota:   QuotaMetered,
		BaseURL: "https://api.cerebras.ai/v1",
		Models:  []string{"gpt-oss-120b", "qwen-3.8-27b"},
		Notes:   "Free Trial $5 / rate-limited; verify model IDs before enabling",
	},
}

// LookupPreset 返回预设副本（Models 切片复制，调用方可安全覆盖）。
func LookupPreset(id string) (Preset, bool) {
	p, ok := Presets[id]
	if !ok {
		return Preset{}, false
	}
	p.Models = append([]string(nil), p.Models...)
	return p, true
}
