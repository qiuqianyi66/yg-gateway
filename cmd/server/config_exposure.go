// config_exposure.go 启动期安全闸：把「空密钥 + 非回环监听 = 裸暴露」从注释提醒
// 变成启动期判据。
//
// 为什么单开一个文件：config.go 已 998 行，逼近 check-depth 的 1000 行文件上限；
// 且本闸的判据（listen 暴露面 + 是否配了密钥）与配置解码无关，混进 config.go 会让
// 那条 998 行的文件继续膨胀。
package main

import (
	"fmt"
	"net"
	"strings"
)

// listenScope 监听地址的暴露面分类（启动期安全闸用）。
type listenScope int

const (
	// listenLoopback 只接受本机回环连接（127.0.0.1 / ::1 / localhost）。
	listenLoopback listenScope = iota
	// listenWildcard 未指定主机段（":7863"）或显式 0.0.0.0 / ::，绑定全部网卡。
	listenWildcard
	// listenConcrete 显式绑定某个具体地址（非回环 IP 或主机名）。
	listenConcrete
)

// listenHostPart 取 listen 的主机段。异常输入原样返回：宁可把整串当主机从严判定，
// 也不因为解析失败就放行。
func listenHostPart(listen string) string {
	listen = strings.TrimSpace(listen)
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	return host
}

// classifyListen 判定监听地址的暴露面。
//
// 为什么不复用配置 normalise 的结果：normalise 只补 ":" 前缀（"7863" → ":7863"），
// 而本闸必须区分 ":7863"（全网卡）与 "127.0.0.1:7863"（回环）—— 两者的安全语义相反。
func classifyListen(listen string) listenScope {
	host := listenHostPart(listen)
	if host == "" {
		return listenWildcard
	}
	if ip := net.ParseIP(host); ip != nil {
		switch {
		case ip.IsUnspecified():
			return listenWildcard // 0.0.0.0 / ::
		case ip.IsLoopback():
			return listenLoopback
		default:
			return listenConcrete
		}
	}
	if strings.EqualFold(host, "localhost") {
		return listenLoopback
	}
	// 主机名：解析结果取决于 /etc/hosts 与 DNS，可能是回环也可能不是 → 从严按非回环。
	return listenConcrete
}

// authConfigured 报告配置里是否存在至少一把生效密钥。口径与 buildAuthSnapshot 对齐：
// 单 api_key 非空，或 api_keys 中有未禁用且 key 非空的条目。
//
// 为什么要与 buildAuthSnapshot 对齐：鉴权是否真的生效，只看运行时装进索引的密钥；
// 若这里用 len(APIKeys)>0 判，会把「全部条目 disabled」误判成已鉴权而放行。
func authConfigured(c *Config) bool {
	if c == nil {
		return false
	}
	if c.APIKey != "" {
		return true
	}
	for _, k := range c.APIKeys {
		if k.Enabled != nil && !*k.Enabled {
			continue
		}
		if k.Key != "" {
			return true
		}
	}
	return false
}

// CheckUnauthenticatedExposure 启动期安全闸：没有生效密钥、且监听不是回环 → 拒绝启动。
//
// 背景：空 api_key 在旧语义下表示「不鉴权」（httpauth.Authenticate 返回 (nil,true) 放行，
// panel.authenticateAdmin 在非多 Key 配置下同样放行），而 Default() 的监听是 ":7863"
// （全网卡）。两者叠加 = 账号管理面与 /v1/chat/completions 对同网段匿名开放。此前只有
// WriteDefault 里一条注释提醒，不是判据；本函数把它变成 fail fast。
//
// 边界：只拦「非回环 + 无密钥」。显式绑回环仍允许空 key 运行 —— 那是本机调试的合法
// 用法，攻击面只剩本机进程。做出这个取舍而不是无差别拒绝，是为了不把 dev 场景一起打死。
func CheckUnauthenticatedExposure(c *Config) error {
	if c == nil || authConfigured(c) {
		return nil
	}
	if c.Security.AllowUnauthenticated {
		return nil
	}
	scope := classifyListen(c.Listen)
	if scope == listenLoopback {
		return nil
	}
	where := "绑定全部网卡"
	if scope == listenConcrete {
		where = "绑定非回环地址"
	}
	return fmt.Errorf(
		"拒绝启动：未配置任何生效的 API Key（api_key 与 api_keys 都为空），但 listen=%q %s —— "+
			"网关与 /panel/ 管理面会对同网段匿名开放。三选一：① 在 config.json 填 api_key"+
			"（或删掉空值让程序重新生成随机 key）；② 改 listen=\"127.0.0.1:7863\" 只允许本机访问；"+
			"③ 前置反代做鉴权，并确认反代确实拦截了未授权请求",
		c.Listen, where)
}
