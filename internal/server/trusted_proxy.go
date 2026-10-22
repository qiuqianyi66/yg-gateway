// trusted_proxy.go 受信任反向代理来源判定（F-002）。
//
// 背景（ProjectDiscovery「Abusing Reverse Proxies」点名的向量）：网关若直接暴露
// （前面没有一层会重写/剥离 X-Forwarded-For 的反代），客户端可以自己伪造
// X-Forwarded-For / X-Real-IP，而 ExtractClientIP 无条件取首段、injectClientIP
// 再把它透传给上游——伪造 IP 就此污染上游的风控与计费判据（甚至在某些配置下
// 构成上游侧 SSRF/元数据访问）。
//
// 加固：只有请求来源落在「受信任代理」集合内，才采信代理头；否则忽略代理头、
// 回落 TCP 对端（RemoteAddr）。直连部署（无反代）不配置任何受信任代理即安全。
package server

import (
	"net"
	"net/http"
	"strings"
)

// TrustedProxyMatcher 判定一个来源 IP 是否属于受信任的反向代理。
// 支持精确 IP 与 CIDR 两种形态（如 "127.0.0.1"、"10.0.0.0/8"、"::1"）。
// 零值（nil）语义：不信任任何代理头，全部回落 TCP 对端。
type TrustedProxyMatcher struct {
	ips   map[string]struct{}
	cidrs []*net.IPNet
}

// NewTrustedProxyMatcher 从配置字符串解析受信任代理来源。
// 输入形态：逗号分隔的 IP 或 CIDR（如 "127.0.0.1,10.0.0.0/8,::1"）。
// 非法条目忽略（解析宽松：一条写错不阻断整体启动），全部非法则返回空 matcher
// （等价于不信任任何代理头，仍是安全默认）。
func NewTrustedProxyMatcher(spec string) *TrustedProxyMatcher {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	m := &TrustedProxyMatcher{ips: map[string]struct{}{}}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			if _, ipnet, err := net.ParseCIDR(part); err == nil {
				m.cidrs = append(m.cidrs, ipnet)
			}
			continue
		}
		if ip := parseHostIP(part); ip != nil {
			m.ips[ip.String()] = struct{}{}
		}
	}
	if len(m.ips) == 0 && len(m.cidrs) == 0 {
		return nil
	}
	return m
}

// parseHostIP 把 "IP"、"[::1]" 或 "IP:port" 的 host 形态解析为 net.IP。
// 裸 IPv6 字面量（"[::1]" 无端口形态）经 SplitHostPort 会失败、host 原样保留方括号，
// net.ParseIP 不接受带括号形态——必须先 Trim "[]"，否则 ::1 受信代理永不命中。
func parseHostIP(s string) net.IP {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "[]")
	return net.ParseIP(s)
}

// Match 报告 src 是否命中受信任代理集合。
// src 可为 "IP" 或 "IP:port"（从 RemoteAddr 直接取即可）；空串恒 false。
func (m *TrustedProxyMatcher) Match(src string) bool {
	if m == nil {
		return false
	}
	host := src
	if h, _, err := net.SplitHostPort(src); err == nil {
		host = h
	}
	ip := parseHostIP(host)
	if ip == nil {
		return false
	}
	if _, ok := m.ips[ip.String()]; ok {
		return true
	}
	for _, cidr := range m.cidrs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// extractClientIP 按受信任代理判定提取客户端真实 IP（F-002 的安全提取入口）。
//
// 规则：
//   - m 为 nil（未配置受信任代理）→ 忽略代理头，回落 TCP 对端 RemoteAddr。
//     这是直连部署的安全默认：客户端伪造的 X-Forwarded-For 不被采信、不透传上游。
//   - m 非 nil 且请求来源命中受信任代理 → 解析代理头，从**右往左**跳过受信任代理，
//     取第一个非受信任 IP 作为真实客户端（见「追加语义」说明）。
//   - m 非 nil 但来源未命中（伪造/直连混用）→ 忽略代理头，回落 RemoteAddr。
//
// 追加语义（重要，防 XFF 伪造）：
//
//	若受信任代理用「追加」方式拼 XFF（如 nginx 的 $proxy_add_x_forwarded_for 会把
//	客户端送来的 XFF 原样保留、再在末尾追加真实对端），那么「首段」仍是客户端
//	可控的伪造值——取首段会绕过本防护。正确做法是从右往左跳过受信任代理，取第一个
//	非受信任 IP：追加语义下它才是真实客户端；重写语义下（XFF 只有一个真实客户端
//	IP）结果相同，无回归。
//
// 与 upstream.ExtractClientIP 的差别：后者无条件取代理头首段，只该在**已确认
// 来源可信**之后调用；本函数是那层「确认可信」的闸门，且处理追加语义。
func extractClientIP(m *TrustedProxyMatcher, r *http.Request) string {
	if r == nil {
		return ""
	}
	if m != nil && m.Match(r.RemoteAddr) {
		if ip := trustedClientIP(m, r); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// trustedClientIP 从代理头解析真实客户端 IP：从右往左跳过受信任代理，取第一个
// 非受信任 IP。XFF 可能逗号分隔多段（"client, proxy1, proxy2"），从右往左读到
// 第一个非受信任段即返回；全为受信任段（异常）时返回最右段。
func trustedClientIP(m *TrustedProxyMatcher, r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		// 无 XFF 时回 X-Real-IP（单值，无追加语义问题）
		return strings.TrimSpace(r.Header.Get("X-Real-IP"))
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		if !m.Match(part) {
			return part // 第一个非受信任 IP = 真实客户端
		}
	}
	// 全为受信任段（理论上不该发生），退回最右段
	return strings.TrimSpace(parts[len(parts)-1])
}
