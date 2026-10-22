// cors.go CORS 预检/来源回显：allowHeader + reflectableCORSOrigin。
// 从 handler.go 机械拆出（2026-10-22 R-202），签名与行为零变化。
package server

import (
	"net/url"
	"strings"
)

// allowHeader 把允许的方法集合拼成 Allow 头（稳定顺序，便于断言）。
func allowHeader(methods map[string]bool) string {
	order := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	var out []string
	for _, m := range order {
		if methods[m] {
			out = append(out, m)
		}
	}
	return strings.Join(out, ", ")
}

// reflectableCORSOrigin 判定 Origin 是否可回显：仅 https 源放行。
// 浏览器自己发来的 Origin 已是规范化串（scheme://host[:port]，无 path/userinfo），
// 这里只校验 scheme 与 host 非空；回显值用原始串，浏览器会拿它与自身 Origin 精确
// 比对， smuggling 空间为零。null 源（沙箱 iframe / file:// / data:）与 http 源
// 一律拒绝——来源不可验证的调用方没有理由拿到本机网关的跨域读权限。
func reflectableCORSOrigin(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	return origin, true
}
