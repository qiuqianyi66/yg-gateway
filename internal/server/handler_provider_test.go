package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// TestServeProviderCompatStreamsToProvider：非 codebuddy 账号的请求必须出站到
// 对应 provider 端点（Bearer 鉴权 + 响应透传），而非 codebuddy 的 WorkBuddy 客户端。
func TestServeProviderCompatStreamsToProvider(t *testing.T) {
	var gotAuth string
	arkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"ok\":true}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer arkSrv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	h := NewHandler(Config{
		Pool:      p,
		Providers: map[string]ProviderEndpoint{"ark": {BaseURL: arkSrv.URL, APIKey: "sk-ark"}},
	})

	run := &chatRun{
		h:         h,
		st:        &chatStat{},
		bareModel: "ark-model",
		body:      []byte(`{"model":"ark-model"}`),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	acct := &auth.Auth{UID: "a1", Provider: "ark"}

	if !run.serveProviderCompat(rr, req, acct, h.cfg.Providers["ark"], time.Now()) {
		t.Fatal("serveProviderCompat 应返回 true（成功透传）")
	}
	if gotAuth != "Bearer sk-ark" {
		t.Errorf("出站 Authorization = %q，want Bearer sk-ark（必须走 provider 端点而非 codebuddy）", gotAuth)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("响应状态 = %d，want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "data: [DONE]") {
		t.Errorf("SSE 流未透传完整，body = %q", rr.Body.String())
	}
}

// TestServeProviderCompatNonStreamUsage：非流式响应解析 usage 并记入用量（keyID=provider）。
func TestServeProviderCompatNonStreamUsage(t *testing.T) {
	arkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer arkSrv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	h := NewHandler(Config{
		Pool:      p,
		Providers: map[string]ProviderEndpoint{"ark": {BaseURL: arkSrv.URL, APIKey: "sk-ark"}},
	})
	run := &chatRun{
		h:         h,
		st:        &chatStat{},
		bareModel: "ark-model",
		body:      []byte(`{"model":"ark-model"}`),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	acct := &auth.Auth{UID: "a1", Provider: "ark"}

	if !run.serveProviderCompat(rr, req, acct, h.cfg.Providers["ark"], time.Now()) {
		t.Fatal("非流式成功应返回 true")
	}
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"total_tokens":15`) {
		t.Errorf("非流式响应透传错误: code=%d body=%q", rr.Code, rr.Body.String())
	}
}

// TestServeProviderCompatUpstreamError：上游 4xx 返回 false（调用方 NoteError + 换号）。
func TestServeProviderCompatUpstreamError(t *testing.T) {
	arkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer arkSrv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	h := NewHandler(Config{
		Pool:      p,
		Providers: map[string]ProviderEndpoint{"ark": {BaseURL: arkSrv.URL, APIKey: "sk-ark"}},
	})
	run := &chatRun{h: h, st: &chatStat{}, bareModel: "ark-model", body: []byte(`{}`)}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	acct := &auth.Auth{UID: "a1", Provider: "ark"}

	if run.serveProviderCompat(rr, req, acct, h.cfg.Providers["ark"], time.Now()) {
		t.Fatal("上游 401 应返回 false（失败换号）")
	}
}

// TestServeProviderCompatAccountKeyOverridesProviderKey 账号级 api_key 优先于 provider
// 级（双真相源消歧）：一个火山引擎账号一个 key 的常见形态用账号的；共享 key 时才回退
// config.providers[].api_key。
//
// 牙齿：删掉 handler_provider.go 里 `if acct.APIKey != "" { key = acct.APIKey }`
// （还原 bug）→ 断言 FAIL（出站会变成 Bearer sk-provider）。
func TestServeProviderCompatAccountKeyOverridesProviderKey(t *testing.T) {
	var gotAuth string
	arkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer arkSrv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "a1", Provider: "ark", APIKey: "sk-acct"})
	h := NewHandler(Config{
		Pool:      p,
		Providers: map[string]ProviderEndpoint{"ark": {BaseURL: arkSrv.URL, APIKey: "sk-provider"}},
	})

	run := &chatRun{
		h:         h,
		st:        &chatStat{},
		bareModel: "ark-model",
		body:      []byte(`{"model":"ark-model"}`),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	acct := &auth.Auth{UID: "a1", Provider: "ark", APIKey: "sk-acct"}

	if !run.serveProviderCompat(rr, req, acct, h.cfg.Providers["ark"], time.Now()) {
		t.Fatal("serveProviderCompat 应返回 true")
	}
	if gotAuth != "Bearer sk-acct" {
		t.Errorf("出站 Authorization = %q，want Bearer sk-acct（账号级 key 应覆盖 provider 级）", gotAuth)
	}
}

// TestModelsIncludesProviderModels /v1/models 必须列出启用 provider 的 models[]
// （否则 Cursor/Cline 等客户端看不到、无法选）。禁用 provider 不列出（防误触）。
//
// 牙齿：删掉 models() 里的 appendProviderModels 调用（还原 bug）→ 本测试 FAIL。
func TestModelsIncludesProviderModels(t *testing.T) {
	p := pool.New("")
	h := NewHandler(Config{
		Pool:     p,
		Upstream: upstream.New(),
		Providers: map[string]ProviderEndpoint{
			"ark": {Models: []string{"doubao-pro-32k", "doubao-lite-32k"}, Enabled: true},
			"off": {Models: []string{"hidden-model"}, Enabled: false},
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var body struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids := map[string]string{}
	for _, d := range body.Data {
		ids[d.ID] = d.OwnedBy
	}
	for _, want := range []string{"doubao-pro-32k", "doubao-lite-32k"} {
		if ids[want] != "ark" {
			t.Errorf("/v1/models 缺 %q 或 owned_by 错: %v", want, ids)
		}
	}
	if _, ok := ids["hidden-model"]; ok {
		t.Error("禁用 provider 的模型不应出现在 /v1/models")
	}
}
