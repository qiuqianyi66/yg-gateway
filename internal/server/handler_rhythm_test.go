package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
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

// TestRhythmRelabelReadable 钉死 rhythm 桶名可读化（rhythmRelabel）：
// 已匹配账号的桶名换成「昵称(uid8)」/ 无昵称纯 uid8；global 与未知桶原样保留；
// 桶数量与数值不变（只换展示名，不改语义）。
//
// 牙齿：把 rhythmRelabel 改成 `return raw` → 第一条「🐻(0250aab9) 不存在」红。
func TestRhythmRelabelReadable(t *testing.T) {
	const (
		uidBear = "0250aab9-3697-47c9-9133-3a2cc7ec3af1"
		uidDot  = "269f9704-e083-4c68-9ae8-2cbd9228140d"
		uidAnon = "abcdef12-3456-7890-abcd-ef1234567890"
	)
	accounts := []pool.Status{
		{UID: uidBear, Nickname: "🐻"},
		{UID: uidDot, Nickname: "."},
		{UID: uidAnon}, // 无昵称 → 只显示 uid8
	}
	raw := map[string]any{
		upstream.MachineIDForUID(uidBear): map[string]int{"in_flight": 1, "queue_depth": 2},
		upstream.MachineIDForUID(uidAnon): map[string]int{"in_flight": 0, "queue_depth": 0},
		"global":                          map[string]int{"in_flight": 0, "queue_depth": 0},
		"deadbeefnotanaccount":            map[string]int{"in_flight": 3, "queue_depth": 0},
	}
	got := rhythmRelabel(raw, accounts)

	if len(got) != len(raw) {
		t.Fatalf("桶数变了：%d → %d（重命名不得丢桶/合桶）", len(raw), len(got))
	}
	v, ok := got["🐻(0250aab9)"].(map[string]int)
	if !ok || v["in_flight"] != 1 || v["queue_depth"] != 2 {
		t.Fatalf("账号桶未可读化或数值变了：%v", got)
	}
	if _, ok := got["abcdef12"]; !ok {
		t.Fatalf("无昵称账号应显示 uid8：%v", got)
	}
	if _, ok := got["global"]; !ok {
		t.Fatalf("global 桶必须原样保留：%v", got)
	}
	if _, ok := got["deadbeefnotanaccount"]; !ok {
		t.Fatalf("未知桶必须原样保留（未知就是未知）：%v", got)
	}
	if _, ok := got[upstream.MachineIDForUID(uidBear)]; ok {
		t.Fatal("已匹配账号的桶名仍是 machine-id（重命名未生效）")
	}
}
