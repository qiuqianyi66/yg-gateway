package main

import (
	"testing"

	"yg-gateway/internal/pool"
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
