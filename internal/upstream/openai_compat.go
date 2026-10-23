package upstream

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// OpenAICompatEndpoint 是 OpenAI 兼容备用上游的接入模板（R-107，P2）。
//
// 模板语义：任何实现 `/chat/completions` 形状的端点（DeepSeek 官方 / OpenRouter /
// vLLM 自建 / 任意 OpenAI 兼容 API）都可按此接入——只需要 BaseURL + APIKey，
// 不需要懂 CodeBuddy 报文。fallback 配置的 endpoints 列表按序级联时，每一项
// 都是一个独立的 OpenAICompatEndpoint。
//
// 边界（路线图纪律）：本模板只覆盖「OpenAI 兼容」这一族。不做「接所有 provider」——
// 差异化定位不是通用 AI 网关（见 AGENTS.md Product Direction）；新增其它 provider
// 族时应另建适配文件，而不是把本文件膨胀成万能适配层。
type OpenAICompatEndpoint struct {
	BaseURL string
	APIKey  string
}

// ChatCompletionsURL 返回该端点的 chat/completions 完整 URL。
func (e OpenAICompatEndpoint) ChatCompletionsURL() string {
	return strings.TrimRight(e.BaseURL, "/") + "/chat/completions"
}

// NewChatRequest 按 OpenAI 兼容规范组装一次 chat/completions 请求：
// Content-Type=application/json + Bearer 鉴权 + 透传 Accept（SSE 客户端会带
// text/event-stream）。调用方拿到请求后直接 http.Client.Do。
//
// 抽成模板的理由：URL 拼装、鉴权头、Accept 透传这三件事在级联列表的每一项上
// 都重复——各自手写一遍就是漂移温床（某一项漏透传 Accept，SSE 就退化成非流式）。
func (e OpenAICompatEndpoint) NewChatRequest(ctx context.Context, body io.Reader, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.ChatCompletionsURL(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	return req, nil
}

// Do 发起该端点的一次 chat/completions 请求（R-107 级联转发的出站收口）。
//
// 为什么 server 包不直接 http.DefaultClient.Do：R6 要求出站请求集中在
// internal/upstream——备用端点的组装（URL/鉴权/Accept 透传）与实际发出
// （Client.Do）都收敛在本模板里，server 的级联转发只做编排，不持有 HTTP 客户端。
// 超时由调用方经 ctx 控制（ctx.WithTimeout）。
func (e OpenAICompatEndpoint) Do(ctx context.Context, body io.Reader, accept string) (*http.Response, error) {
	req, err := e.NewChatRequest(ctx, body, accept)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
