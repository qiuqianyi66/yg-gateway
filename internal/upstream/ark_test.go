package upstream

import "testing"

// TestArkEndpointReusesOpenAICompat：Ark 必须复用 OpenAICompatEndpoint 的
// URL 拼装 / Bearer 鉴权 / Accept 透传，不另起 HTTP 客户端（R6 出站收口）。
func TestArkEndpointReusesOpenAICompat(t *testing.T) {
	e := ArkEndpoint{
		OpenAICompatEndpoint: OpenAICompatEndpoint{
			BaseURL: "https://ark.cn-beijing.volces.com/api/v3/",
			APIKey:  "sk-ark",
		},
		Models: []string{"doubao-seed-2-1-pro"},
	}
	if got := e.ChatCompletionsURL(); got != "https://ark.cn-beijing.volces.com/api/v3/chat/completions" {
		t.Errorf("ChatCompletionsURL = %q", got)
	}
	req, err := e.NewChatRequest(t.Context(), nil, "text/event-stream")
	if err != nil {
		t.Fatalf("NewChatRequest: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-ark" {
		t.Errorf("Authorization = %q，want Bearer sk-ark", got)
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q，want 透传 text/event-stream", got)
	}
}
