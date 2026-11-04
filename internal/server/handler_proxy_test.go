package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/upstream"
)

// TestStatusExposesProxyFields 验证 /status 的代理观测输出面（P0-2）：
//   - proxy_status 键恒存在（未发生任何构造/零登记时 = {}，键缺失不可被读成「无此观测」）；
//   - accounts[].proxy_url 为**脱敏值**（R1/R2 红线：userinfo 凭据绝不出 /status）；
//   - 直连账号（ProxyURL 为空）不输出 proxy_url 键（omitempty，空 = 直连表达）。
//
// 牙齿：把 handler.status 里的 proxy_status 键删掉，第一条断言红；把 pool.Status.ProxyURL
// 改成输出原文（e.a.ProxyURL），第二条断言红（会看到 alice/s3cr3t）。
//
// 注：登记→快照的机制（构造即登记、active/failed 分派）由 upstream 包单测
// （TestProxyStatusSnapshot）钉死；本测试只钉 server 输出合同。
func TestStatusExposesProxyFields(t *testing.T) {
	up := upstream.New()
	p := testPoolWith(
		&auth.Auth{UID: "u-proxy", AccessToken: "at", ExpiresAt: 9999999999, ProxyURL: "http://alice:s3cr3t@proxy:8080"},
		&auth.Auth{UID: "u-direct", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/status code=%d body=%s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json 解析 /status 失败: %v", err)
	}

	ps, ok := body["proxy_status"].(map[string]any)
	if !ok {
		t.Fatalf("proxy_status 键缺失或非对象：%v", body["proxy_status"])
	}
	if len(ps) != 0 {
		t.Fatalf("零登记时 proxy_status 应为 {}，got %v", ps)
	}

	accts, ok := body["accounts"].([]any)
	if !ok || len(accts) != 2 {
		t.Fatalf("accounts 应为 2 条：%v", body["accounts"])
	}
	byUID := map[string]map[string]any{}
	for _, a := range accts {
		m, ok := a.(map[string]any)
		if !ok {
			t.Fatalf("account 非对象：%v", a)
		}
		byUID[m["uid"].(string)] = m
	}

	pv, _ := byUID["u-proxy"]["proxy_url"].(string)
	if pv != "http://proxy:8080" {
		t.Fatalf("u-proxy proxy_url = %q，want 脱敏后的 http://proxy:8080", pv)
	}
	if strings.Contains(pv, "alice") || strings.Contains(pv, "s3cr3t") {
		t.Fatalf("u-proxy proxy_url %q 泄露了代理凭据（R1/R2 红线）", pv)
	}
	if _, has := byUID["u-direct"]["proxy_url"]; has {
		t.Fatalf("直连账号不应输出 proxy_url 键：%v", byUID["u-direct"])
	}
}
