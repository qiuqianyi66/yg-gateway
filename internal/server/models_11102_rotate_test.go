package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// TestChat11102UnknownCatalogRotates 目录为空（modelCatalogUnknown）+ 11102 →
// 不得 fail-fast 400，继续换号到健康号成功。
//
// 背景（#12 e2e 缺口）：11102 三值判定逻辑在 chatrun.go:813-828（仅
// modelCatalogAbsent fail-fast，Unknown 继续换号）+ models_handler.go:79-121，
// 但 isKnownModel 三值只有函数级单测（models_catalog_state_test.go），
// "unknown → 继续换号"这条 chatrun 端到端分支此前零覆盖（handler_test.go 无 11102 用例）。
//
// 牙齿：把 chatrun.go:815 的 modelCatalogAbsent 判据误扩到 Unknown（目录空）时，
// 真实模型（换号本可成功）被误 400——本测试第一条断言立即红。
func TestChat11102UnknownCatalogRotates(t *testing.T) {
	var mu sync.Mutex
	tried := map[string]int{}
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		mu.Lock()
		tried[authz]++
		mu.Unlock()
		if authz == "Bearer at-bad" {
			// 11102「该后端无此模型」确定性答复（400 形态，upstream/client_test.go:84 同款）。
			// 不用 404：chatPaths 对 404/405 走换路径分支（client.go:680），CN 单路径换无可换
			// 会撞 :709 panic（该分支为历史遗留死路，见变更记录 observation）。
			return 400, `{"code":11102,"msg":"model [glm-5.2] service info not found"}`, false
		}
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "u11102", AccessToken: "at-bad", ExpiresAt: 9999999999},
		&auth.Auth{UID: "uhealth", AccessToken: "at-good", ExpiresAt: 9999999999},
	)
	// 让 11102 号积分更高被先选中（确定性：首个尝试的是坏号）
	p.SetCredits("u11102", 2000, 0)
	p.SetCredits("uhealth", 1000, 0)
	h := NewHandler(Config{Pool: p, Upstream: up, SoftCooldown: time.Minute})
	// 目录为空：不 store → modelCatalogUnknown（假上游 ChatStream 不触发目录拉取）

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","messages":[]}`)))

	if rec.Code == http.StatusBadRequest {
		t.Fatalf("目录空 + 11102 被误判为模型不存在而 400（应继续换号）: body=%s", rec.Body)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s（应换到健康号 200）", rec.Code, rec.Body)
	}
	mu.Lock()
	defer mu.Unlock()
	if tried["Bearer at-bad"] != 1 || tried["Bearer at-good"] != 1 {
		t.Errorf("tried=%v（11102 号应被尝试一次后换到健康号）", tried)
	}
}
