// Package livecfg 运行期可变配置的并发安全持有者。
//
// 背景：进程启动时读入的配置是普通字段（读多写零），但管理面板允许在线改配置，
// 于是少量"可热生效"的字段需要有并发安全的读写点。此处用不可变快照 + atomic 指针：
// 读方 Load 拿到一致视图，写方 Store 整体替换，无锁无数据竞争。
//
// 只承载**读路径深、热改需求强**的少数字段；池参数/排程参数等各有既有 setter
// （pool.SetBreaker、scheduler.Reconfigure 等），不重复收编到这里。
package livecfg

import (
	"sync/atomic"
	"time"

	"yg-gateway/internal/httpauth"
)

// Snapshot 一次读取的不可变配置视图。
type Snapshot struct {
	APIKey               string          // 网关/面板共同鉴权密钥（单 Key 兼容）；空 = 不鉴权
	APIKeys              []httpauth.Key  // 多 Key 运行时形态（已含摘要）；空 = 无多 Key
	AuthIndex            *httpauth.Index // 多 Key 鉴权索引（BuildSnapshot 时构建；nil = 无多 Key）
	SoftCooldown         time.Duration   // 429 软冷却基数（<=0 时调用方回退内置默认）
	SanitizeFingerprints bool            // 出站请求体指纹脱敏
	RecordClientInfo     bool            // 请求日志是否记录调用来源（客户端 IP / UA）
	// ModelAliases 客户端模型名 → 上游实际名映射（热生效；nil = 无映射）。
	ModelAliases map[string]string
}

// Holder 原子持有当前快照。
type Holder struct {
	p atomic.Pointer[Snapshot]
}

// New 以初始快照构建。
func New(s Snapshot) *Holder {
	h := &Holder{}
	h.Store(s)
	return h
}

// Load 返回当前快照（Holder 为 nil 或从未 Store 时返回零值快照，调用方无需判空）。
func (h *Holder) Load() Snapshot {
	if h == nil {
		return Snapshot{}
	}
	if s := h.p.Load(); s != nil {
		return *s
	}
	return Snapshot{}
}

// Store 整体替换快照。
func (h *Holder) Store(s Snapshot) { h.p.Store(&s) }

// BuildSnapshot 从「单 Key + 多 Key 列表」构建一份完整的鉴权快照（ADR-001 §六）。
//
// 这是**热更新的原子单元**：调用方在独立对象里完成校验、摘要、索引构建后，用
// 返回值一次性 Store；旧快照在切换前保持可用，绝不在已发布结构上原地修改。
//
// keys 必须是已带完整 Digest 的运行时 Key（调用方负责 sha256，见 httpauth.BuildIndex）。
// 空列表 → AuthIndex 为 nil（无多 Key，走单 Key 兼容）。
func BuildSnapshot(singleKey string, keys []httpauth.Key) Snapshot {
	var idx *httpauth.Index
	if len(keys) > 0 {
		idx = httpauth.BuildIndex(keys)
	}
	return Snapshot{
		APIKey:    singleKey,
		APIKeys:   keys,
		AuthIndex: idx,
	}
}
