package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
)

// TestProviderSummary 覆盖 /status 的 providers[] 汇总：注册表元数据 + 各 provider
// 账号数（codebuddy 账号按空 Provider 回落计数）。空注册表 → nil（键省略 = 仅 codebuddy）。
func TestProviderSummary(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "a1", Provider: "ark"})
	p.Add(&auth.Auth{UID: "a2", Provider: "ark"})
	p.Add(&auth.Auth{UID: "a3"}) // Provider 空 → ProviderID() 回落 codebuddy
	h := NewHandler(Config{
		Pool: p,
		Providers: map[string]ProviderEndpoint{
			"ark": {Kind: "openai_compat", Quota: "metered", Models: []string{"m1"}, Enabled: true},
		},
	})

	ps := h.providerSummary()
	if len(ps) != 1 {
		t.Fatalf("providers = %d, want 1", len(ps))
	}
	ark := ps[0]
	if ark["id"] != "ark" || ark["kind"] != "openai_compat" || ark["quota"] != "metered" ||
		ark["enabled"] != true || ark["account_count"] != 2 {
		t.Errorf("ark 汇总错误: %+v", ark)
	}

	// 空 Providers → nil（/status 省略 providers 键）
	if got := NewHandler(Config{Pool: p}).providerSummary(); got != nil {
		t.Errorf("空 Providers 应返回 nil，got %v", got)
	}
}

// TestStatusExposesProviderFields 覆盖 /status 的 accounts[].provider 透传 + 顶层
// providers[]。ark 账号带 provider 字段，codebuddy 账号（空 Provider）也输出
// "codebuddy"（Status.Provider 由 ProviderID() 派生，恒非空）。
func TestStatusExposesProviderFields(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "ark1", Provider: "ark"})
	p.Add(&auth.Auth{UID: "cb1"}) // codebuddy
	h := NewHandler(Config{
		Pool: p,
		Providers: map[string]ProviderEndpoint{
			"ark": {Kind: "openai_compat", Quota: "metered", Enabled: true},
		},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Accounts  []map[string]any `json:"accounts"`
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	prov := map[string]string{}
	for _, a := range body.Accounts {
		prov[a["uid"].(string)] = a["provider"].(string)
	}
	if prov["ark1"] != "ark" {
		t.Errorf("ark1.provider = %q, want ark", prov["ark1"])
	}
	if prov["cb1"] != "codebuddy" {
		t.Errorf("cb1.provider = %q, want codebuddy（空 Provider 回落）", prov["cb1"])
	}
	if len(body.Providers) != 1 || body.Providers[0]["id"] != "ark" {
		t.Errorf("顶层 providers = %v, want [ark]", body.Providers)
	}
}
