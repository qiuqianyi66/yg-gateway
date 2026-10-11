package server

import (
	"testing"

	"yg-gateway/internal/httpauth"
)

// T6 约束校验单元测试（ADR-001 §五）。
// 覆盖 realm 四态 + models 三态 + 缺 model 边界 + 错误不泄漏 Key 身份。

func TestCheckKeyConstraintsNilKey(t *testing.T) {
	h := &Handler{}
	// nil key = 未启用鉴权，无约束，恒放行。
	if code, _ := h.checkKeyConstraints(nil, "any", "cn", "any"); code != "" {
		t.Fatalf("nil key 应放行，got code=%q", code)
	}
}

func TestCheckKeyConstraintsRealm(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		name      string
		keyRealm  string
		reqRealm  string
		wantCode  string
	}{
		{"空 realm 不限", "", "cn", ""},
		{"cn key + cn 请求", "cn", "cn", ""},
		{"cn key + global 请求", "cn", "global", "realm_mismatch"},
		{"global key + global 请求", "global", "global", ""},
		{"global key + cn 请求", "global", "cn", "realm_mismatch"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := &httpauth.Key{Realm: c.keyRealm}
			code, msg := h.checkKeyConstraints(key, "m", c.reqRealm, "m")
			if code != c.wantCode {
				t.Fatalf("code=%q want %q (msg=%q)", code, c.wantCode, msg)
			}
			// 错误不泄漏 Key 身份（id/name 不应出现在 msg 里）
			if code != "" && (msg == "" || containsKeyIdentity(msg, "user-001", "小明")) {
				t.Fatalf("错误 msg 不应泄漏 Key 身份: %q", msg)
			}
		})
	}
}

func TestCheckKeyConstraintsModels(t *testing.T) {
	h := &Handler{}
	cases := []struct {
		name     string
		models   []string
		reqModel string
		bare     string
		wantCode string
	}{
		{"models nil 不限", nil, "m", "m", ""},
		{"models 空切片全拒", []string{}, "m", "m", "model_not_allowed"},
		{"白名单命中", []string{"a", "b"}, "a", "a", ""},
		{"白名单未命中", []string{"a", "b"}, "c", "c", "model_not_allowed"},
		{"白名单 + 缺 model", []string{"a"}, "", "", "model_not_allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := &httpauth.Key{Models: c.models}
			code, msg := h.checkKeyConstraints(key, c.reqModel, "cn", c.bare)
			if code != c.wantCode {
				t.Fatalf("code=%q want %q (msg=%q)", code, c.wantCode, msg)
			}
		})
	}
}

// TestCheckKeyConstraintsRealmAndModels 同时配 realm + models：两者都过才放行。
func TestCheckKeyConstraintsRealmAndModels(t *testing.T) {
	h := &Handler{}
	key := &httpauth.Key{Realm: "cn", Models: []string{"m"}}
	// realm 对 + models 对 → 放行
	if code, _ := h.checkKeyConstraints(key, "m", "cn", "m"); code != "" {
		t.Fatalf("两者都满足应放行，got %q", code)
	}
	// realm 对 + models 错 → model_not_allowed
	if code, _ := h.checkKeyConstraints(key, "other", "cn", "other"); code != "model_not_allowed" {
		t.Fatalf("models 错应 model_not_allowed，got %q", code)
	}
	// realm 错 + models 对 → realm_mismatch（realm 先判）
	if code, _ := h.checkKeyConstraints(key, "m", "global", "m"); code != "realm_mismatch" {
		t.Fatalf("realm 错应 realm_mismatch，got %q", code)
	}
}

// containsKeyIdentity 检查 msg 是否泄漏了 id 或 name。
func containsKeyIdentity(msg string, ids ...string) bool {
	for _, id := range ids {
		if len(id) > 0 && (len(msg) >= len(id) && containsStr(msg, id)) {
			return true
		}
	}
	return false
}

func containsStr(s, sub string) bool {
	if sub == "" {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
