// cache_fallback_e2e_test.go 钉住「无显式会话标识时，出站 prompt_cache_key
// 由内容派生」（chatflow.prepareOutbound → CacheConversationFallback）。
//
// 回归保护（2026-10-29 生产实测）：deepseek-harness 不带会话标识，此前同账号
// 所有请求共用定值缓存键，多个子会话互相覆盖 → 间歇性整段 miss（当天 glm-5.3
// 失效请求吃掉 60% 费用）。本测试断言：不同会话出站 key 不同、同会话稳定、
// 显式 conversation_id 行为不变。
package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/upstream"
)

// captureOutboundCacheKey 用 fake upstream 捕获出站 body 的 prompt_cache_key。
func captureOutboundCacheKey(t *testing.T, body string) string {
	t.Helper()
	var got string
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read outbound body: %v", err)
			}
			var obj map[string]any
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Errorf("outbound body unmarshal: %v", err)
			}
			got, _ = obj["prompt_cache_key"].(string)
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sseOK)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	h := NewHandler(Config{Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}), Upstream: up})
	rec := doChat(t, h, "", body)
	if rec.Code != 200 {
		t.Fatalf("status=%d want 200, body=%s", rec.Code, rec.Body.String())
	}
	if got == "" {
		t.Fatal("出站 body 必须带非空 prompt_cache_key")
	}
	return got
}

func TestChatCompletionsCacheKeyContentDerived(t *testing.T) {
	noConvA := `{"model":"m","messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"task-A"}]}`
	noConvB := `{"model":"m","messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"task-B"}]}`
	withConv := `{"model":"m","conversation_id":"conv-x","messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"task-A"}]}`

	ka := captureOutboundCacheKey(t, noConvA)
	if !strings.HasPrefix(ka, "wb2a-") {
		t.Fatalf("出站 prompt_cache_key 应以 wb2a- 开头, got %q", ka)
	}

	// 同会话（同 body）重复请求 → 同 key（多轮前缀可递增复用）。
	ka2 := captureOutboundCacheKey(t, noConvA)
	if ka2 != ka {
		t.Fatalf("同会话出站 key 应稳定: %q vs %q", ka, ka2)
	}

	// 不同会话（首条 user 不同）→ 不同 key（不共享缓存槽，互不覆盖）。
	kb := captureOutboundCacheKey(t, noConvB)
	if kb == ka {
		t.Fatalf("不同会话出站 key 不得相同（内容派生失效?）: %q", ka)
	}

	// 显式 conversation_id 行为不变：会话段来自 conv-x，不得退化为内容派生键。
	kc := captureOutboundCacheKey(t, withConv)
	if kc == ka {
		t.Fatalf("显式 conversation_id 的出站 key 不应等于无标识请求的 key: %q", kc)
	}
}
