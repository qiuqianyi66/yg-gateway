// config_keys.go 多 Key 配置的校验与归一化（自 config.go 迁出，ROADMAP v3 V3-5：
// config.go 顶格压线，纯机械搬移零行为变化——逐字节保留原注释与逻辑）。
package main

import (
	"fmt"
	"net"
	"strings"

	"yg-gateway/internal/httpauth"
)

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
