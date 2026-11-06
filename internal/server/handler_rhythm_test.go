package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/upstream"
)

// TestStatusExposesRhythmKey 验证 /status 的并发闸观测输出面（F1）：
//   - rhythm 键恒存在（零配置 = {}，键缺失不可被读成「无此观测」——与 proxy_status 同语义）；
//   - 开启配置后（ConcurrencyPerAccount>0）键仍存在且为空表（无在途请求时）。
//
// 牙齿：把 handler.status 里的 rhythm 键删掉，本测试 ok 断言红。
// 在途/排队深度的分派由 upstream 包单测（TestConcurrencyRoundTripperBuckets 的
// RhythmStatus 断言）钉死；本测试只钉 server 输出合同。
func TestStatusExposesRhythmKey(t *testing.T) {
	up := upstream.New()
	p := testPoolWith(&auth.Auth{UID: "u-rhythm", AccessToken: "at", ExpiresAt: 9999999999})
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
	rh, ok := body["rhythm"].(map[string]any)
	if !ok {
		t.Fatalf("rhythm 键缺失或非对象：%v", body["rhythm"])
	}
	if len(rh) != 0 {
		t.Fatalf("零配置时 rhythm 应为 {}，got %v", rh)
	}

	// 装配开启（无在途请求）：键仍存在且为空表（零回归只增键）
	up.ConcurrencyPerAccount = 2
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/status", nil))
	var body2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("json 解析 /status 失败: %v", err)
	}
	rh2, ok := body2["rhythm"].(map[string]any)
	if !ok {
		t.Fatalf("开启配置后 rhythm 键缺失：%v", body2["rhythm"])
	}
	if len(rh2) != 0 {
		t.Fatalf("无在途请求时 rhythm 应为 {}，got %v", rh2)
	}
}
