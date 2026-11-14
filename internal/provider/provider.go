// Package provider 定义上游提供方的**静态**描述与注册表：谁（ID）、走什么
// 协议（Kind）、额度怎么算（QuotaKind）、模型目录从哪来。
//
// 定位（ADR-026）：本包只回答「这个上游是什么形状」。**禁止**加入运行期状态
// （剩余额度 / 健康分 / 冷却 / 在途数）——那些仍归 internal/pool。这条边界是
// 并存期（provider 抽象 + 现有 credits 状态机）防止两处概念漂移的唯一约束。
//
// 依赖纪律：叶子层，零 internal 依赖。新增 internal import 前必须更新
// scripts/check-boundaries.ps1 的 allow 表并说明理由。
package provider

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Kind 上游协议族——区分的是**报文形态**，不是厂商。
// 两个不同厂商若都是 OpenAI 兼容，用 KindOpenAICompat 即可，各自靠 Descriptor.ID 区分。
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

func (k Kind) valid() bool {
	switch k {
	case KindCodeBuddy, KindOpenAICompat, KindArk:
		return true
	default:
		return false
	}
}

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

func (q QuotaKind) valid() bool {
	switch q {
	case QuotaCredits, QuotaMetered, QuotaWindow:
		return true
	default:
		return false
	}
}

// Descriptor 一个 provider 的静态描述。字段语义固定，扩展需走 ADR。
type Descriptor struct {
	ID string `json:"id"`
	// Kind 协议族；必填。
	Kind Kind `json:"kind"`
	// Quota 额度语义；必填。决定选号策略（P1 起生效）。
	Quota QuotaKind `json:"quota"`
	// ChatBaseURL 出站基地址。空 = 用协议族默认地址——CodeBuddy 的默认值
	// 集中在 internal/upstream，不在此重复，避免两处漂移。
	ChatBaseURL string `json:"chat_base_url,omitempty"`
	// Models 该 provider 的模型 ID 列表。**空 = 未知，不是「不支持」**：
	// 消费方按字段缺失（unknown）处理，禁止用 `len(Models)==0` 判定不支持
	// （AGENTS.md 四态纪律；同 /v1/models supported_efforts 为空的口径）。
	Models []string `json:"models,omitempty"`
	// Enabled 显式启用闸门。零值 false = 未启用（保守默认：新登记不自动接流量）。
	Enabled bool `json:"enabled"`
}

// 注册表错误：全部可 errors.Is 判定，调用方不必解析文案。
var (
	ErrEmptyID      = errors.New("provider: empty id")
	ErrDuplicateID  = errors.New("provider: duplicate id")
	ErrInvalidKind  = errors.New("provider: invalid kind")
	ErrInvalidQuota = errors.New("provider: invalid quota kind")
)

// Validate 校验描述符自洽性。不做「Enabled 与 Kind 组合」这类策略判断——
// 那是装配层的职责。
func (d Descriptor) Validate() error {
	if d.ID == "" {
		return ErrEmptyID
	}
	if !d.Kind.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidKind, d.Kind)
	}
	if !d.Quota.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidQuota, d.Quota)
	}
	return nil
}

// Registry provider 注册表（并发安全；读多写少）。
// 零值不可用，必须经 NewRegistry 构造（惰性 map 会在并发写时 panic）。
type Registry struct {
	mu    sync.RWMutex
	items map[string]Descriptor
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]Descriptor)}
}

// Register 登记一个 provider。校验失败或 ID 重复时不写入，返回错误。
// Models 会被复制一份：调用方后续改自己的切片不应影响已登记内容。
func (r *Registry) Register(d Descriptor) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Models != nil {
		d.Models = append([]string(nil), d.Models...)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[d.ID]; ok {
		return fmt.Errorf("%w: %q", ErrDuplicateID, d.ID)
	}
	r.items[d.ID] = d
	return nil
}

// Get 按 ID 查描述符。未知 ID 返回 (Descriptor{}, false)，不 panic。
func (r *Registry) Get(id string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.items[id]
	if !ok {
		return Descriptor{}, false
	}
	return cloneDescriptor(d), true
}

// List 返回全部描述符，按 ID 字典序稳定输出（面板/状态输出需要确定性）。
// 返回的是副本，调用方修改不影响注册表。
func (r *Registry) List() []Descriptor {
	r.mu.RLock()
	out := make([]Descriptor, 0, len(r.items))
	for _, d := range r.items {
		out = append(out, cloneDescriptor(d))
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len 已登记数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

// cloneDescriptor 复制 Models 切片，保证注册表内容不被外部持有者改写。
func cloneDescriptor(d Descriptor) Descriptor {
	if d.Models != nil {
		d.Models = append([]string(nil), d.Models...)
	}
	return d
}
