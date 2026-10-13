package panel

// Key 重置接口（POST /panel/api/key-reset）测试：
// 未注入 ResetKey → 501；参数校验 → 400；ResetKey 错误透传 → 400；
// 与 /panel/api/config 同鉴权（未带 key → 401）。
// 该接口是「接入配置」弹窗「重置密钥」的后端通道：前端不持有明文，
// 全量提交会误删其他 Key，故由服务端直接改目标条目（main 注入实现）。

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKeyResetEndpoint(t *testing.T) {
	errNotFound := errors.New("未找到 id=nope 的 Key")
	cases := []struct {
		name     string
		resetFn  func(id, newKey string) error
		body     string
		wantCode int
		wantSub  string
	}{
		{"未注入 ResetKey → 501", nil, `{"id":"k1","key":"sk-new"}`, http.StatusNotImplemented, "not available"},
		{"成功", func(id, newKey string) error { return nil }, `{"id":"k1","key":"sk-new"}`, http.StatusOK, `"ok":true`},
		{"id 为空 → 400", func(id, newKey string) error { return nil }, `{"id":"","key":"sk-new"}`, http.StatusBadRequest, "id 不能为空"},
		{"key 空白 → 400", func(id, newKey string) error { return nil }, `{"id":"k1","key":"  "}`, http.StatusBadRequest, "新密钥不能为空"},
		{"body 非法 → 400", func(id, newKey string) error { return nil }, `{`, http.StatusBadRequest, "parse body"},
		{"ResetKey 错误透传 → 400", func(id, newKey string) error { return errNotFound }, `{"id":"nope","key":"sk-new"}`, http.StatusBadRequest, "未找到"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := New(Config{Version: "test", APIKey: "test-key", ResetKey: c.resetFn})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/panel/api/key-reset", strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer test-key")
			p.ServeHTTP(rec, req)
			if rec.Code != c.wantCode {
				t.Fatalf("code=%d want %d; body=%s", rec.Code, c.wantCode, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.wantSub) {
				t.Errorf("body=%s want contains %q", rec.Body.String(), c.wantSub)
			}
		})
	}
}

// 与 /panel/api/config 同鉴权：未带面板 key → 401（不得裸奔重置密钥）。
func TestKeyResetRequiresAuth(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key", ResetKey: func(id, newKey string) error { return nil }})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("POST", "/panel/api/key-reset", strings.NewReader(`{"id":"k1","key":"sk-new"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d want 401", rec.Code)
	}
}
