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

// TestPanelAuthEmptyKeyMultiEnabled 多 Key 已启用但 api_key 为空：面板应 fail closed
// （拒绝匿名访问），不能因为管理员密钥为空就开放管理权限（阻断项 2，第二轮）。
func TestPanelAuthEmptyKeyMultiEnabled(t *testing.T) {
	idx := httpauth.BuildIndex([]httpauth.Key{
		{ID: "downstream", Digest: sha256Digest("sk-downstream")},
	})
	live := livecfg.New(livecfg.Snapshot{APIKey: "", AuthIndex: idx})
	p := New(Config{Version: "test", APIKey: "", Live: live})

	// 多 Key 启用 + api_key 空 → 面板匿名访问应 401（fail closed）
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/config", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("多 Key 模式 api_key 空应 401（fail closed），got %d", rec.Code)
	}
}

// TestPanelAuthEmptyKeyLegacy 旧版配置（无多 Key）且 api_key 空：保留 2.0.0 放行语义。
func TestPanelAuthEmptyKeyLegacy(t *testing.T) {
	// 无 Live 快照（Live=nil 表示无多 Key 索引），api_key 空
	p := New(Config{Version: "test", APIKey: ""})
	rec := httptest.NewRecorder()
	// 用 /panel/api/config（LoadConfig nil → 501，不碰 nil Pool）；若过鉴权层返回 501（非 401）。
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/config", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Error("旧版配置 api_key 空应放行（2.0.0 兼容），不应被拒绝")
	}
}
