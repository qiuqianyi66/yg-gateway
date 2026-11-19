package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBodyIsJSONObject 形状判定的边界：只有首字符是 `{` 才算对象。
//
// 覆盖 `null` 这个关键盲区：它解到 map/struct 时 err == nil 且目标为零值，
// 「解一次看有没有报错」判不出来。
func TestBodyIsJSONObject(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"model":"m"}`, true},
		{"  \n\t{\"model\":\"m\"}", true},
		{"{", true}, // 截断但形状是对象，交给后续 unmarshal 报错
		{"null", false},
		{"[]", false},
		{`"x"`, false},
		{"7", false},
		{"true", false},
		{"", false},
		{" \n\t ", false},
	}
	for _, c := range cases {
		if got := bodyIsJSONObject([]byte(c.body)); got != c.want {
			t.Errorf("bodyIsJSONObject(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

// TestNonJSONObjectBodyRejectedAtEntry 四个接 JSON body 的入口都必须就地 400。
//
// 牙齿：删掉任一处 rejectIfNotJSONObject 调用 → 该路径的 `null` 会走到假上游拿 200
// （或 count_tokens 静默返回 0 token）→ 本测试 FAIL。实测：注释掉 chatflow.go 的
// 调用后 chat 路径即 FAIL。
func TestNonJSONObjectBodyRejectedAtEntry(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: up})

	paths := []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/responses",
		"/v1/messages/count_tokens",
	}
	bodies := []string{"null", "[]", `"x"`, "7", "", " \n\t"}

	for _, path := range paths {
		for _, body := range bodies {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s body=%q 应 400，got %d body=%s", path, body, rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("%s body=%q 响应不是 JSON 信封：%v", path, body, err)
			}
			if env.Error.Code != "invalid_request" {
				t.Fatalf("%s body=%q error.code=%q，want invalid_request", path, body, env.Error.Code)
			}
		}
	}
}

// TestJSONObjectBodyNotRejected 不误伤：对象形状的 body 不能被这条守卫拦下。
//
// 只断言「不是被形状守卫拒的」——各入口对字段的其它校验（缺 model 等）不在本测试范围。
func TestJSONObjectBodyNotRejected(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: up})

	paths := []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/responses",
		"/v1/messages/count_tokens",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"m","messages":[]}`))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), "must be a JSON object") {
			t.Fatalf("%s 的对象形 body 被形状守卫误拦：%s", path, rec.Body.String())
		}
	}
}
