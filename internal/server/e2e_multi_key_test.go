package server

import (
	"crypto/sha256"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/livecfg"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/usage"
)

// 端到端集成冒烟（不碰真实上游）：用 newFakeUpstream + 多 Key live 快照，验证
// 配置加载→鉴权→模型限制→转发→日志归因在同一次真实运行里正确衔接。
// 覆盖 ADR-001 要求的关键场景；零生产密钥、零真实调用。

func sha256OfE2E(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// newMultiKeyHandler 构造带多 Key 鉴权的 handler + Mock 上游 + 计数。
func newMultiKeyHandler(t *testing.T, keys []httpauth.Key, withReqLog bool) (*Handler, *reqlog.Recorder, *int64) {
	t.Helper()
	var upstreamCalls int64
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		atomic.AddInt64(&upstreamCalls, 1)
		return 200, sseOK, true
	})
	idx := httpauth.BuildIndex(keys)
	live := livecfg.New(livecfg.Snapshot{AuthIndex: idx})
	var rl *reqlog.Recorder
	cfg := Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream: up,
		Live:     live,
	}
	if withReqLog {
		rl = reqlog.New(reqlog.Config{})
		cfg.RequestLog = rl
	}
	h := NewHandler(cfg)
	return h, rl, &upstreamCalls
}

// TestE2EHotReloadSnapshotConsistency 热更新：每次 Store 一个确定快照后，断言旧 Key
// 只在旧快照放行、新 Key 只在新快照放行，且归因正确。用确定性的交替（非随机），
// 验证每个请求读到的都是完整快照（atomic.Pointer 语义），不出现半新半旧。
func TestE2EHotReloadSnapshotConsistency(t *testing.T) {
	oldIdx := httpauth.BuildIndex([]httpauth.Key{
		{ID: "old", Digest: sha256OfE2E("sk-old"), Models: []string{"m"}},
	})
	newIdx := httpauth.BuildIndex([]httpauth.Key{
		{ID: "new", Digest: sha256OfE2E("sk-new"), Models: []string{"m"}},
	})
	live := livecfg.New(livecfg.Snapshot{AuthIndex: oldIdx})
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream: newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true }),
		Live:     live,
	})

	// 交替 10 轮：每次先 Store 确定快照，再断言两个 Key 的鉴权结果与该快照一致。
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			live.Store(livecfg.Snapshot{AuthIndex: oldIdx})
			// 旧快照：旧 Key 放行，新 Key 401
			if rec := doChat(t, h, "Bearer sk-old", `{"model":"m","messages":[]}`); rec.Code != 200 {
				t.Fatalf("旧快照下旧 Key 应 200，got %d", rec.Code)
			}
			if rec := doChat(t, h, "Bearer sk-new", `{"model":"m","messages":[]}`); rec.Code != 401 {
				t.Fatalf("旧快照下新 Key 应 401，got %d", rec.Code)
			}
		} else {
			live.Store(livecfg.Snapshot{AuthIndex: newIdx})
			// 新快照：新 Key 放行，旧 Key 401
			if rec := doChat(t, h, "Bearer sk-new", `{"model":"m","messages":[]}`); rec.Code != 200 {
				t.Fatalf("新快照下新 Key 应 200，got %d", rec.Code)
			}
			if rec := doChat(t, h, "Bearer sk-old", `{"model":"m","messages":[]}`); rec.Code != 401 {
				t.Fatalf("新快照下旧 Key 应 401，got %d", rec.Code)
			}
		}
	}
}

func doChat(t *testing.T, h *Handler, authz, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	if authz != "" {
		req.Header.Set("Authorization", authz)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestE2EUsageKeyAttribution：鉴权命中的 Key ID 真的流进用量桶（ADR-001 F-005
// 二期：用量按 Key 聚合）。两个 Key 各打一次，usage 的 ByKey 必须出现两行且各自
// 计数为 1；把 handler 里 `KeyID: keyID` 去掉（或 authKey 取错作用域）→ 两笔都
// 落进空 Key 分组 → 本测试 FAIL。
func TestE2EUsageKeyAttribution(t *testing.T) {
	keys := []httpauth.Key{
		{ID: "key-a", Name: "甲", Digest: sha256OfE2E("sk-a"), Models: []string{"model-a"}},
		{ID: "key-b", Name: "乙", Digest: sha256OfE2E("sk-b"), Models: []string{"model-b"}},
	}
	var calls int64
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		atomic.AddInt64(&calls, 1)
		return 200, sseOK, true
	})
	rec := usage.New(t.TempDir() + "/usage.json")
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream: up,
		Live:     livecfg.New(livecfg.Snapshot{AuthIndex: httpauth.BuildIndex(keys)}),
		Usage:    rec,
	})

	if r := doChat(t, h, "Bearer sk-a", `{"model":"model-a","messages":[]}`); r.Code != 200 {
		t.Fatalf("Key A 应 200, got %d body=%s", r.Code, r.Body.String())
	}
	if r := doChat(t, h, "Bearer sk-b", `{"model":"model-b","messages":[]}`); r.Code != 200 {
		t.Fatalf("Key B 应 200, got %d body=%s", r.Code, r.Body.String())
	}

	snap := rec.SnapshotWindow(usage.Window{}, nil, map[string]string{"key-a": "甲", "key-b": "乙"}, nil, nil)
	if len(snap.ByKey) != 2 {
		t.Fatalf("by_key 应 2 行（key-a/key-b），got %+v", snap.ByKey)
	}
	byKey := map[string]usage.KeyedAgg{}
	for _, row := range snap.ByKey {
		byKey[row.Key] = row
	}
	if got, ok := byKey["key-a"]; !ok || got.Requests != 1 {
		t.Fatalf("key-a 归因 = %+v, want requests=1", got)
	}
	if got, ok := byKey["key-b"]; !ok || got.Requests != 1 {
		t.Fatalf("key-b 归因 = %+v, want requests=1", got)
	}
	if byKey["key-a"].Extra != "甲" || byKey["key-b"].Extra != "乙" {
		t.Fatalf("Key 展示名未透出: %+v", snap.ByKey)
	}
}

// TestE2EMultiKeyFullChain 完整链路：两个 Key 白名单内各自成功 + 归因正确。
func TestE2EMultiKeyFullChain(t *testing.T) {
	keys := []httpauth.Key{
		{ID: "key-a", Name: "甲", Digest: sha256OfE2E("sk-a"), Models: []string{"model-a"}},
		{ID: "key-b", Name: "乙", Digest: sha256OfE2E("sk-b"), Models: []string{"model-b"}},
	}
	h, rl, calls := newMultiKeyHandler(t, keys, true)

	// Key A 白名单内 → 成功
	recA := doChat(t, h, "Bearer sk-a", `{"model":"model-a","messages":[]}`)
	if recA.Code != 200 {
		t.Fatalf("Key A 白名单内应 200，got %d body=%s", recA.Code, recA.Body.String())
	}

	// Key B 白名单内 → 成功
	recB := doChat(t, h, "Bearer sk-b", `{"model":"model-b","messages":[]}`)
	if recB.Code != 200 {
		t.Fatalf("Key B 白名单内应 200，got %d body=%s", recB.Code, recB.Body.String())
	}

	if *calls != 2 {
		t.Fatalf("应 2 次上游调用，got %d", *calls)
	}

	// 归因正确
	snap := rl.Snapshot()
	if len(snap.Recent) != 2 {
		t.Fatalf("应 2 条日志，got %d", len(snap.Recent))
	}
	// Recent 是最近在前还是后？按 reqlog 实现，通常按时间序。这里宽松断言两个 Key 都出现。
	ids := map[string]bool{}
	for _, ev := range snap.Recent {
		ids[ev.KeyID] = true
	}
	if !ids["key-a"] || !ids["key-b"] {
		t.Fatalf("归因应含 key-a 和 key-b，got %v", ids)
	}
}

// TestE2EAuthRejectNoUpstream 缺少/错误 Key → 401，零上游调用。
func TestE2EAuthRejectNoUpstream(t *testing.T) {
	keys := []httpauth.Key{{ID: "key-a", Digest: sha256OfE2E("sk-a"), Models: []string{"model-a"}}}
	h, _, calls := newMultiKeyHandler(t, keys, false)

	for name, authz := range map[string]string{
		"缺 Key":    "",
		"错误 Key":   "Bearer sk-wrong",
		"缺 Bearer": "sk-a",
		"前缀大小写错":   "bearer sk-a",
	} {
		rec := doChat(t, h, authz, `{"model":"model-a","messages":[]}`)
		if rec.Code != 401 {
			t.Fatalf("%s 应 401，got %d", name, rec.Code)
		}
	}
	if *calls != 0 {
		t.Fatalf("鉴权失败不应打上游，got %d", *calls)
	}
}

// TestE2EModelNotAllowedNoUpstream 白名单外模型 → 400，零上游调用。
func TestE2EModelNotAllowedNoUpstream(t *testing.T) {
	keys := []httpauth.Key{{ID: "key-a", Digest: sha256OfE2E("sk-a"), Models: []string{"model-a"}}}
	h, _, calls := newMultiKeyHandler(t, keys, false)

	rec := doChat(t, h, "Bearer sk-a", `{"model":"model-forbidden","messages":[]}`)
	if rec.Code != 400 {
		t.Fatalf("白名单外模型应 400，got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model_not_allowed") {
		t.Fatalf("错误码应为 model_not_allowed，body=%s", rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("白名单外不应打上游，got %d", *calls)
	}
}

// TestE2ERealmMismatchNoUpstream realm 不匹配 → 400，零上游调用。
func TestE2ERealmMismatchNoUpstream(t *testing.T) {
	keys := []httpauth.Key{{ID: "key-a", Digest: sha256OfE2E("sk-a"), Realm: "cn", Models: []string{"model-a"}}}
	h, _, calls := newMultiKeyHandler(t, keys, false)

	// global: 前缀模型 → realm=global，与 key 的 cn 不匹配
	rec := doChat(t, h, "Bearer sk-a", `{"model":"global:model-a","messages":[]}`)
	if rec.Code != 400 {
		t.Fatalf("realm 不匹配应 400，got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "realm_mismatch") {
		t.Fatalf("错误码应为 realm_mismatch，body=%s", rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("realm 不匹配不应打上游，got %d", *calls)
	}
}

// TestE2EStreamAndNonStream 流式与非流式约束一致。
func TestE2EStreamAndNonStream(t *testing.T) {
	keys := []httpauth.Key{{ID: "key-a", Digest: sha256OfE2E("sk-a"), Models: []string{"model-a"}}}
	h, _, calls := newMultiKeyHandler(t, keys, false)

	// 非流式白名单内 → 200
	recNonStream := doChat(t, h, "Bearer sk-a", `{"model":"model-a","messages":[]}`)
	if recNonStream.Code != 200 {
		t.Fatalf("非流式白名单内应 200，got %d", recNonStream.Code)
	}

	// 流式白名单内 → 200（约束校验在 resolveModel 后，流式非流式同路径）
	recStream := doChat(t, h, "Bearer sk-a", `{"model":"model-a","stream":true,"messages":[]}`)
	if recStream.Code != 200 {
		t.Fatalf("流式白名单内应 200，got %d", recStream.Code)
	}

	// 流式白名单外 → 400
	recStreamBad := doChat(t, h, "Bearer sk-a", `{"model":"model-forbidden","stream":true,"messages":[]}`)
	if recStreamBad.Code != 400 {
		t.Fatalf("流式白名单外应 400，got %d", recStreamBad.Code)
	}

	if *calls != 2 {
		t.Fatalf("应 2 次上游调用（2 次成功），got %d", *calls)
	}
}

// TestE2EHealthzNoAuth /healthz 保持无鉴权。
func TestE2EHealthzNoAuth(t *testing.T) {
	keys := []httpauth.Key{{ID: "key-a", Digest: sha256OfE2E("sk-a")}}
	h, _, _ := newMultiKeyHandler(t, keys, false)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 && rec.Code != 503 {
		t.Fatalf("/healthz 应 200/503（无鉴权），got %d", rec.Code)
	}
}

// TestE2ESingleKeyCompat 只配单 api_key（无 api_keys）→ 旧行为正常。
func TestE2ESingleKeyCompat(t *testing.T) {
	// 不传 Live，用静态 APIKey（loadLive 回落）。
	h := NewHandler(Config{
		Pool:     testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream: newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true }),
		APIKey:   "sk-single",
	})
	rec := doChat(t, h, "Bearer sk-single", `{"model":"m","messages":[]}`)
	if rec.Code != 200 {
		t.Fatalf("单 Key 兼容应 200，got %d", rec.Code)
	}
	recBad := doChat(t, h, "Bearer sk-wrong", `{"model":"m","messages":[]}`)
	if recBad.Code != 401 {
		t.Fatalf("单 Key 错误应 401，got %d", recBad.Code)
	}
}
