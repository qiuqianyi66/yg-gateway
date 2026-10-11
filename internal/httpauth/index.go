package httpauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

// Key 多 Key 鉴权的一把密钥（运行时形态，不含明文）。
//
// 明文只在 config.json 的 api_keys[].key 里，BuildIndex 时转成 Digest 摘要；
// 这里以及后续所有运行时结构都不携带 Key 原文（ADR §三 凭证安全契约）。
type Key struct {
	ID     string   // 稳定身份标识（归因/统计用）
	Name   string   // 展示名（可重复、可改）
	Digest []byte   // sha256(key) 的 32 字节摘要
	Models []string // 模型白名单三态：nil=不限 / []string{}=全拒 / 非空=白名单
	Realm  string   // 版本归属 cn/global/空=不限
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
