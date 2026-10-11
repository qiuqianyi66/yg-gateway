// Package httpauth 网关与面板共用的 Bearer 鉴权原语。
//
// 单独成包的原因：server（/v1/*、/status）与 panel（/panel/api/*）两处鉴权
// 必须完全同口径——此前各自复制了一份"字符串直接比较"的实现，既容易漂移，
// 又都带计时侧信道。统一到这里后，口径只有一份，且天然常量时间比较。
package httpauth

import (
	"crypto/subtle"
	"net/http"
)

// bearerPrefix 认证方案前缀（大小写敏感，与 HTTP 规范及既有实现一致）。
const bearerPrefix = "Bearer "

// VerifyBearer 校验请求头是否携带正确的 Bearer 密钥（单 Key 兼容签名，行为与 2.0.0 一致）。
//
// key 为空表示"未启用鉴权"，恒返回 true（调用方据此放行）。
// 比较用 SHA-256 摘要 + subtle.ConstantTimeCompare：
//   - 常量时间，不因前缀匹配长度而泄露信息；
//   - 先摘要再比较，长度差异被吸收进摘要（不会因长度不同提前返回）；
//   - 摘要本身不可逆，即便有侧信道也拿不到密钥原文。
//
// 多 Key 场景请用 Index.BuildIndex + Match（见 index.go）；本函数保留旧签名，
// 供单 Key 兼容路径与既有测试使用，内部委托给「单 Key 的 Index」以复用同一比较原语。
func VerifyBearer(r *http.Request, key string) bool {
	if key == "" {
		return true
	}
	idx := BuildIndex([]Key{{ID: "single", Digest: digest(key)}})
	_, ok := idx.Match(r)
	if ok {
		return true
	}
	// 缺头/方案不对：仍走一次摘要比较，保持耗时形状一致（与 2.0.0 缺头路径同形状）。
	subtle.ConstantTimeCompare(digest(""), digest(key))
	return false
}
