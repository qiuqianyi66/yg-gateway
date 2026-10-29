package server

import "strings"

// resolveModel 解析模型名协议（PLAN D6）：
//
//	分布式前缀： "[realm:]model"
//
// 取第一个 ":"，前段恰为 "cn"/"global" 才剥离；否则视为裸名，realm=cn、bare=原串。
// 大小写敏感（前缀必须是精确的小写枚举）。bare 即出站/选号/账本使用的裸模型名。
//
// 导出为 ResolveModel（cmd/server/main.go 粘性闭包需要），包内简写 resolveModel。
func resolveModel(model string) (realm, bare string) {
	idx := strings.IndexByte(model, ':')
	if idx < 0 {
		return "cn", stripRateSuffix(model)
	}
	prefix := model[:idx]
	if prefix != "cn" && prefix != "global" {
		return "cn", stripRateSuffix(model)
	}
	return prefix, stripRateSuffix(model[idx+1:])
}

// stripRateSuffix 剥掉 /v1/models 在 id 上拼的展示倍率后缀。
// 网关返回 "cn:hy3 ×0"，Cherry Studio 弹窗据此直接显示倍率；客户端勾选后发请求，
// model 字段带 " ×0" 后缀，这里剥掉还原成上游认识的裸模型名 "hy3"。
// 旧客户端发不带后缀的 id（"cn:hy3"）不受影响——找不到空格就原样返回。
// 上游模型 id 均为 [a-z0-9-]+ 不含空格，按第一个空格截断安全。
func stripRateSuffix(bare string) string {
	if i := strings.IndexByte(bare, ' '); i >= 0 {
		return bare[:i]
	}
	return bare
}

// ResolveModel 是 resolveModel 的导出面（跨包调用）。
func ResolveModel(model string) (realm, bare string) { return resolveModel(model) }

// applyModelAliases 按配置把客户端裸模型名替换成上游实际认识的名。
//
// 只影响出站 body 的 model 字段；选号/粘性/白名单/用量/回包 delta.Model 全部沿用
// 客户端原始裸名——否则同一模型在观测口径里分裂成两个 key，EWMA/选号/白名单全乱。
// 未配置或无命中时原样返回（零回归）。
func applyModelAliases(bare string, aliases map[string]string) string {
	if len(aliases) == 0 {
		return bare
	}
	if up, ok := aliases[bare]; ok && up != "" {
		return up
	}
	return bare
}

// modelAllowedByWhitelist 判定客户端裸模型名是否在 Key 的白名单内（精确 + 后缀通配）。
// 抽成纯函数便于单测；nil 表示不限（返回 true）。
func modelAllowedByWhitelist(whitelist []string, bare string) bool {
	if whitelist == nil {
		return true
	}
	if len(whitelist) == 0 {
		return false
	}
	for _, m := range whitelist {
		if m == bare || m == "*" {
			return true
		}
		if strings.HasSuffix(m, "*") && len(m) > 1 && strings.HasPrefix(bare, m[:len(m)-1]) {
			return true
		}
	}
	return false
}
