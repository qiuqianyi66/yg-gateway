package panel

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"

	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/livecfg"
)

// 阻断项 3：普通下游 Key（api_keys 里的）不能访问面板完整权限。
// 面板鉴权只认单 api_key（管理员凭证），不认多 Key 索引。

func sha256Digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// TestPanelAuthRejectsDownstreamKey 面板只认单 api_key，普通多 Key 进不了面板。
func TestPanelAuthRejectsDownstreamKey(t *testing.T) {
	idx := httpauth.BuildIndex([]httpauth.Key{
		{ID: "downstream-1", Digest: sha256Digest("sk-downstream")},
	})
	live := livecfg.New(livecfg.Snapshot{
		APIKey:    "sk-admin",
		AuthIndex: idx,
	})
	p := New(Config{Version: "test", APIKey: "sk-admin", Live: live})

	// 普通下游 Key → 401（不能进面板）
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/panel/api/overview", nil)
	req.Header.Set("Authorization", "Bearer sk-downstream")
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("普通下游 Key 应 401（不能进面板），got %d", rec.Code)
	}

	// 管理员 Key → 通过鉴权层（不再是 401）
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/panel/api/nonexistent", nil)
	req2.Header.Set("Authorization", "Bearer sk-admin")
	p.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusUnauthorized {
		t.Error("管理员 Key 应通过面板鉴权层")
	}
}

// TestPanelAuthEmptyKeyAllows 单 api_key 为空时，面板放行（2.0.0 旧语义，非多 Key 语义）。
func TestPanelAuthEmptyKeyAllows(t *testing.T) {
	idx := httpauth.BuildIndex([]httpauth.Key{
		{ID: "downstream", Digest: sha256Digest("sk-downstream")},
	})
	live := livecfg.New(livecfg.Snapshot{APIKey: "", AuthIndex: idx})
	p := New(Config{Version: "test", APIKey: "", Live: live})

	// api_key 空 → 面板不鉴权（即使有多 Key 索引，面板也不走它）
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/nonexistent", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Error("api_key 空时面板应放行（旧语义），不应被多 Key 索引触发鉴权")
	}
}
