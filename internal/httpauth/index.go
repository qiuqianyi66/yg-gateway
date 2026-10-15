package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// SingleKeyID 单 Key（api_key 兼容）合成条目的身份 ID。
// buildAuthSnapshot 与 Authenticate 兜底路径都用它，保持身份一致（不散落魔数）。
const SingleKeyID = "single"

// Key 多 Key 鉴权的一把密钥（运行时形态，不含明文）。
//
// 明文只在 config.json 的 api_keys[].key 里，BuildIndex 时转成 Digest 摘要；
// 这里以及后续所有运行时结构都不携带 Key 原文（ADR §三 凭证安全契约）。
type Key struct {
	ID     string   // 稳定身份标识（归因/统计用）
	Name   string   // 展示名（可重复、可改）
	Digest []byte   // sha256(key) 的 32 字节摘要
	Models []string // 模型白名单三态：nil=不限 / []string{}=全拒 / 非空=白名单（支持 * 后缀）
	Realm  string   // 版本归属 cn/global/空=不限
	RPM    int      // 该 Key 每分钟最大请求数；0 = 不限
	// MonthlyTokenQuota 该 Key 当月 token 硬上限；0 = 不限。
	// 已用量由 usage.Recorder 按 ID 累计（跨重启持久化）。
	MonthlyTokenQuota int64
	// Group 账号池分组标签（P1-B）。空 = 不限池（全部账号可选）；
	// 非空 = 选号只从同 Group 账号里挑，实现 key 与账号组绑定隔离。
	Group string
}

// prefixLen 摘要前缀长度（hex 字符数）。对齐 workbuddy-manager 的 12 位前缀，
// 16^12 空间下碰撞桶极小，候选集足够窄（ADR D-202）。
const prefixLen = 12

// hexPrefix 返回摘要前 prefixLen 个 hex 字符（用于索引桶定位）。
// 前缀只缩小候选集，**不是身份认证本身**；最终校验始终是比较完整摘要。
func hexPrefix(d []byte) string {
	const hexDigits = "0123456789abcdef"
	if len(d) == 0 {
		return ""
	}
	out := make([]byte, prefixLen)
	for i := 0; i < prefixLen; i++ {
		// 摘要每个字节拆成两个 hex 字符；超出摘要长度时补 '0'（防御性，实际摘要恒 32 字节）。
		var b byte
		if i/2 < len(d) {
			b = d[i/2]
			if i%2 == 1 {
				b &= 0x0f
			} else {
				b >>= 4
			}
		}
		out[i] = hexDigits[b]
	}
	return string(out)
}

// Index 多 Key 鉴权索引：摘要前缀 → 候选桶。
//
// 前缀可能碰撞，桶是切片（非单值），碰撞不覆盖（ADR D-203）。map 平均 O(1) 定位，
// 桶内逐项对完整摘要做常量时间比较。
type Index struct {
	buckets map[string][]*Key
	// empty 标记是否「未启用鉴权」（无任何 Key）。true 时 Match 恒放行。
	empty bool
}

// BuildIndex 从运行时 Key 列表构建索引。
//
// 传入的 Key 必须已带完整 Digest（调用方负责 sha256 计算）。BuildIndex 只做索引，
// 不复制、不持有明文。空列表 → empty=true（未启用鉴权，与旧「空 api_key 放行」语义一致）。
func BuildIndex(keys []Key) *Index {
	idx := &Index{buckets: make(map[string][]*Key)}
	if len(keys) == 0 {
		idx.empty = true
		return idx
	}
	for i := range keys {
		k := keys[i]
		p := hexPrefix(k.Digest)
		idx.buckets[p] = append(idx.buckets[p], &k)
	}
	return idx
}

// Match 校验请求并返回命中的 Key。
//
// 逻辑：提取 Bearer → 计算完整摘要 → 前缀定位桶 → 桶内常量时间比较完整摘要。
// 未命中返回 (nil, false)。empty 索引恒返回 (nil, false)（由调用方决定放行语义，
// 与旧 VerifyBearer 的空 key 放行一致，见 VerifyBearer 的委托）。
func (idx *Index) Match(r *http.Request) (*Key, bool) {
	if idx == nil || idx.empty {
		return nil, false
	}
	authz := r.Header.Get("Authorization")
	if !strings.HasPrefix(authz, bearerPrefix) {
		return nil, false
	}
	tok := authz[len(bearerPrefix):]
	sum := sha256.Sum256([]byte(tok))
	full := sum[:]
	bucket := idx.buckets[hexPrefix(full)]
	for _, k := range bucket {
		// 完整摘要常量时间比较（R5）。前缀只是缩小候选集。
		if subtle.ConstantTimeCompare(k.Digest, full) == 1 {
			return k, true
		}
	}
	return nil, false
}

// digest 返回 s 的 SHA-256 定长摘要（供常量时间比较与索引前缀）。
func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// Authenticate 统一鉴权入口：多 Key 索引 + 单 Key 兼容，handler 与 panel 共用，
// 避免两处各写一份漂移（ADR-001 §五「一次匹配」）。
//
// 语义（ADR-001 §四 兼容策略）：
//   - index 非空 → 走多 Key 索引匹配；命中返回该 Key。
//   - index 为空（无多 Key）→ 回退单 Key 语义：singleKey 空 = 不鉴权（放行，返回 nil,true）；
//     非空 = 用 VerifyBearer 校验单 Key。
//   - 单 Key 命中返回一个合成 Key（ID="single"，不携带明文）。
//
// 前置条件：index 非空时，调用方应已把 singleKey 作为合成条目（ID="single"）纳入
// index（见 cmd/server/main.go 的 buildAuthSnapshot）。本函数在 index 未命中且
// singleKey 非空时**额外兜底**一次 VerifyBearer——这只在调用方漏纳单 Key 时才触发，
// 正常路径零开销（index 命中即返回）。
//
// 返回 (nil, false) = 鉴权失败（调用方回 401）；(nil, true) = 未启用鉴权（放行）。
// 返回 (key, true) = 命中某把 Key（key 不含明文，只含 id/name/约束）。
func Authenticate(r *http.Request, index *Index, singleKey string) (*Key, bool) {
	if index != nil {
		if k, ok := index.Match(r); ok {
			return k, true
		}
		// index 未命中：兜底单 Key（若调用方漏纳单 Key 到索引）。正常路径 index
		// 已含单 Key，这里只在异常装配下触发一次额外的 VerifyBearer（零分配）。
		if singleKey != "" && VerifyBearer(r, singleKey) {
			return &Key{ID: SingleKeyID, Digest: digest(singleKey)}, true
		}
		return nil, false
	}
	// 无多 Key：单 Key 兼容路径。
	if singleKey == "" {
		return nil, true // 未启用鉴权，放行
	}
	if VerifyBearer(r, singleKey) {
		return &Key{ID: SingleKeyID, Digest: digest(singleKey)}, true
	}
	return nil, false
}
