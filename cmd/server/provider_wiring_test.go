package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/server"
	"yg-gateway/internal/upstream"
)

// TestBuildProviderRouting 覆盖 provider_wiring.go 的函数体：启用条目进 endpoints、
// 未启用条目跳过、空 config 零回归。模型白名单闭包的正确性由 internal/pool 的
// provider_pick_test.go 覆盖（同一 providerOfModel 机制的包内测试）。
func TestBuildProviderRouting(t *testing.T) {
	p := pool.New("")
	if got := buildProviderRouting(&Config{}, p); len(got) != 0 {
		t.Fatalf("空 config 应返回空 map，got %v", got)
	}

	cfg := &Config{Providers: []ProviderConfig{
		{ID: "ark", BaseURL: "https://ark/api/v3", APIKey: "sk-a", Models: []string{"doubao-pro", "global:doubao-lite"}, Enabled: true},
		{ID: "off", BaseURL: "https://x", APIKey: "sk-b", Models: []string{"m"}, Enabled: false},
	}}
	p2 := pool.New("")
	eps := buildProviderRouting(cfg, p2)
	if len(eps) != 1 {
		t.Fatalf("启用条目数 = %d, want 1", len(eps))
	}
	ep, ok := eps["ark"]
	if !ok {
		t.Fatal("ark 端点缺失")
	}
	if ep.BaseURL != "https://ark/api/v3" || ep.APIKey != "sk-a" {
		t.Errorf("ark 端点装配错误: %+v", ep)
	}
	if _, ok := eps["off"]; ok {
		t.Error("未启用条目不应进 endpoints")
	}
}

// TestConfigOnlyProviderRequest 配置里只写 preset+api_key，也能从入站到出站跑通：
// 自动虚拟账号 → 模型路由 → provider 端点（Bearer 使用该 key）。
func TestConfigOnlyProviderRequest(t *testing.T) {
	var gotAuth, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[]}`))
	}))
	defer up.Close()

	raw := fmt.Sprintf(`{"providers":[{"preset":"cerebras","base_url":%q,"api_key":"sk-c","enabled":true}]}`, up.URL)
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	p := pool.New("")
	eps := buildProviderRouting(cfg, p)
	h := server.NewHandler(server.Config{Pool: p, Upstream: upstream.New(), Providers: eps})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer sk-c" {
		t.Errorf("出站鉴权=%q want Bearer sk-c", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"gpt-oss-120b"`) {
		t.Errorf("出站 body 模型错误: %s", gotBody)
	}
}

// provider 上游 5xx 不能被末端退化成 no_healthy_account；原文应可读。
func TestProviderUpstreamErrorPassthrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"provider overloaded","requestId":"prov-503"}}`))
	}))
	defer up.Close()

	raw := fmt.Sprintf(`{"providers":[{"preset":"cerebras","base_url":%q,"api_key":"sk-c","enabled":true}]}`, up.URL)
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	p := pool.New("")
	h := server.NewHandler(server.Config{Pool: p, Upstream: upstream.New(), Providers: buildProviderRouting(cfg, p)})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "provider overloaded") || !strings.Contains(body, "prov-503") {
		t.Fatalf("provider 5xx 原文丢失: %s", body)
	}
	if strings.Contains(body, "no_healthy_account") {
		t.Fatalf("provider 5xx 被误报成本地无号: %s", body)
	}
}

// provider 401 不能被伪装成“网关没号”或客户端 key 失效：502 + upstream_error。
func TestProviderUpstream401IsBadGateway(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid provider key","code":"invalid_api_key"}}`))
	}))
	defer up.Close()

	raw := fmt.Sprintf(`{"providers":[{"preset":"cerebras","base_url":%q,"api_key":"sk-c","enabled":true}]}`, up.URL)
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	p := pool.New("")
	h := server.NewHandler(server.Config{Pool: p, Upstream: upstream.New(), Providers: buildProviderRouting(cfg, p)})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("code=%d want 502, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "invalid provider key") || !strings.Contains(body, "upstream_error") {
		t.Fatalf("provider 401 语义不完整: %s", body)
	}
}

// provider 出站遇到客户端取消时不能罚号/写 503（与 codebuddy 路径同语义）。
func TestProviderClientCancelNotPenalized(t *testing.T) {
	raw := `{"providers":[{"preset":"cerebras","base_url":"http://127.0.0.1:1","api_key":"sk-c","enabled":true}]}`
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	p := pool.New("")
	h := server.NewHandler(server.Config{Pool: p, Upstream: upstream.New(), Providers: buildProviderRouting(cfg, p)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "no_healthy_account") {
		t.Fatalf("客户端取消被写成 503: code=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, st := range p.List() {
		if st.Provider == "cerebras" && (st.ConsecutiveFails != 0 || st.ErrTotal != 0) {
			t.Fatalf("provider 客户端取消被罚号: fails=%d err_total=%d", st.ConsecutiveFails, st.ErrTotal)
		}
	}
}

func TestBuildProviderRoutingVirtualAccounts(t *testing.T) {
	cfg := &Config{Providers: []ProviderConfig{
		{ID: "ark", BaseURL: "https://ark/api/v3", APIKey: "sk-a", Enabled: true},
		{ID: "cerebras", BaseURL: "https://api.cerebras.ai/v1", APIKey: "sk-c", Enabled: true},
		{ID: "off", BaseURL: "https://x", APIKey: "sk-off", Enabled: false},
	}}
	p := pool.New("")
	buildProviderRouting(cfg, p)
	list := p.List()
	providers := map[string]int{}
	for _, st := range list {
		providers[st.Provider]++
	}
	if providers["ark"] != 1 || providers["cerebras"] != 1 || providers["off"] != 0 {
		t.Fatalf("provider 虚拟账号数错误: %+v (list=%+v)", providers, list)
	}

	// 已有 auths 账号时不再建虚拟账号（多 key 形态优先）。
	p2 := pool.New("")
	p2.Add(&auth.Auth{UID: "a1", Provider: "ark", APIKey: "sk-acct"})
	buildProviderRouting(cfg, p2)
	for _, st := range p2.List() {
		if st.Provider == "ark" && st.UID == "provider:ark" {
			t.Fatalf("已有 ark 账号时不应再建虚拟账号: %+v", p2.List())
		}
	}
}

// TestProviderSummaryView 覆盖 providerSummaryView（panel 汇总条数据源）：注册表
// 元数据 + 各 provider 账号数（codebuddy 账号按空 Provider 回落计数）。
func TestProviderSummaryView(t *testing.T) {
	list := []pool.Status{
		{UID: "a1", Provider: "ark"},
		{UID: "a2", Provider: "ark"},
		{UID: "a3"}, // Provider 空 → codebuddy
	}
	providers := []ProviderConfig{
		{ID: "ark", Kind: "openai_compat", Quota: "metered", Models: []string{"m1"}, Enabled: true},
		{ID: "off", Kind: "openai_compat", Quota: "metered", Enabled: false},
	}
	got := providerSummaryView(providers, list)
	if len(got) != 2 {
		t.Fatalf("providers = %d, want 2", len(got))
	}
	if got[0]["id"] != "ark" || got[0]["enabled"] != true || got[0]["account_count"] != 2 {
		t.Errorf("ark 汇总错误: %+v", got[0])
	}
	if got[1]["id"] != "off" || got[1]["enabled"] != false || got[1]["account_count"] != 0 {
		t.Errorf("off 汇总错误: %+v", got[1])
	}
	if v := providerSummaryView(nil, list); v != nil {
		t.Errorf("空 providers 应返回 nil，got %v", v)
	}
}
