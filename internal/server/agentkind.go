// Package server — agentkind.go 把入站 User-Agent 字符串分类成闭词表 agent_kind。
//
// 为什么单立文件：分类表是 V3-10 画像的「语义入口」，词表新增/调整/证据收集
// 都应集中在一个地方，不要散落到 handler/logging 里 if strings.Contains。
//
// 四态纪律：本函数输出是 **inference**——UA 是客户端完全可控的 HTTP 头，
// 任何客户端都能伪装成另一种；词表也不可能覆盖所有客户端。消费方（面板/告警）
// 必须按「近似流量构成」读，禁止按「精确客户端清单」做计费/拦截决策。
//
// 空 UA / 未采集 → 返回 ""（unknown），**不**返回 "other"：
//   - ""     = 没看到 UA（开关关 / UA 空）
//   - "other" = 看到了 UA 但词表不认识
//
// 这两者在 /status 聚合里进不同桶（unknown vs other），不允许合并。
package server

import "strings"

// AgentKind 闭词表常量（REQ-agent-traffic-profile §需求）。
// 新增值必须同步：本表 + ClassifyAgentKind switch 注释 + profile_test.go 表 +
// docs/prd/REQ-agent-traffic-profile-2026-10-26.md 词表。
const (
	AgentKindClaudeCode   = "claude_code"
	AgentKindCodex        = "codex"
	AgentKindOpenAISDK    = "openai_sdk"
	AgentKindAnthropicSDK = "anthropic_sdk"
	AgentKindBrowser      = "browser"
	AgentKindHTTPClient   = "http_client"
	AgentKindOther        = "other"
)

// ClassifyAgentKind 把原始 UA 字符串分类成闭词表值。
// 空/空白 UA → ""（未采集，unknown）。
//
// 匹配规则：UA 整体转小写后做子串包含匹配。**顺序敏感**——更具体的模式
// （产品名/SDK 名）排在前面，兜底（browser/http_client/other）排在后面，
// 避免 `Mozilla/5.0` 这种浏览器 UA 先命中某个 SDK 子串。
//
// 本函数纯函数、无 IO、无时钟：测试表直接钉死所有分支。
func ClassifyAgentKind(ua string) string {
	ua = strings.ToLower(strings.TrimSpace(ua))
	if ua == "" {
		return ""
	}
	switch {
	case strings.Contains(ua, "claude-cli") || strings.Contains(ua, "claude-code"):
		return AgentKindClaudeCode
	case strings.Contains(ua, "codex"):
		return AgentKindCodex
	case strings.Contains(ua, "openai-python") || strings.Contains(ua, "openai-node") ||
		strings.Contains(ua, "openai-go") || strings.Contains(ua, "openai/python") ||
		strings.Contains(ua, "openai/node") || strings.Contains(ua, "openai/go"):
		return AgentKindOpenAISDK
	case strings.Contains(ua, "anthropic-") || strings.Contains(ua, "anthropic-sdk"):
		return AgentKindAnthropicSDK
	case strings.Contains(ua, "mozilla/"):
		return AgentKindBrowser
	case strings.Contains(ua, "curl/") || strings.Contains(ua, "wget/") ||
		strings.Contains(ua, "python-requests/") || strings.Contains(ua, "go-http-client/") ||
		strings.Contains(ua, "node-fetch") || strings.Contains(ua, "undici"):
		return AgentKindHTTPClient
	default:
		return AgentKindOther
	}
}
