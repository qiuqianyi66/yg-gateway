package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 阻断项 2（第一轮）+ 阻断项 1（第二轮）：面板 GET /panel/api/config 不得泄漏密钥明文，
// 且 api_key 必须留空（不返回占位符，避免前端把占位符当真实值提交覆盖原密钥）。
// loadConfigMasked 加载配置后：api_key 留空 + api_key_configured 状态字段。

func TestLoadConfigMaskedNoKeyLeak(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{
		"api_key": "sk-secret-single",
		"api_keys": [
			{"id":"k1","key":"sk-secret-multi-1","name":"甲","models":["m1"],"realm":"cn"},
			{"id":"k2","key":"sk-secret-multi-2"}
		]
	}`), 0o600)

	masked, err := loadConfigMasked(fp)
	if err != nil {
		t.Fatal(err)
	}
	m := masked.(map[string]any)
	raw, _ := json.Marshal(masked)
	s := string(raw)

	// 不得含任何真实密钥明文
	for _, secret := range []string{"sk-secret-single", "sk-secret-multi-1", "sk-secret-multi-2"} {
		if containsSubstr(s, secret) {
			t.Fatalf("脱敏配置泄漏密钥 %q: %s", secret, s)
		}
	}
	// api_key 必须留空（不返回真实值，也不返回 "********" 占位符）
	if v, ok := m["api_key"].(string); !ok || v != "" {
		t.Fatalf("api_key 应留空（阻断项 1），got %#v", m["api_key"])
	}
	// api_key_configured 状态字段应为 true
	if v, ok := m["api_key_configured"].(bool); !ok || !v {
		t.Fatalf("api_key_configured 应为 true，got %#v", m["api_key_configured"])
	}
	// api_keys[].key 应留空 + key_configured 状态
	keys, ok := m["api_keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("api_keys 应保留 2 项，got %#v", m["api_keys"])
	}
	k0 := keys[0].(map[string]any)
	if v, ok := k0["key"].(string); !ok || v != "" {
		t.Fatalf("api_keys[0].key 应留空，got %#v", k0["key"])
	}
	if v, ok := k0["key_configured"].(bool); !ok || !v {
		t.Fatalf("api_keys[0].key_configured 应为 true，got %#v", k0["key_configured"])
	}
	// 不得含占位符（"********" 会被前端误提交）
	if containsSubstr(s, "********") {
		t.Fatalf("脱敏配置不得含占位符（会被前端当真实值提交）: %s", s)
	}
	// id/name/realm/models 等非密钥字段应保留
	for _, keep := range []string{"k1", "k2", "甲", "m1", "cn"} {
		if !containsSubstr(s, keep) {
			t.Fatalf("非密钥字段 %q 被误脱敏: %s", keep, s)
		}
	}
}

// TestLoadConfigMaskedEmptyKey 空 api_key：保持空 + api_key_configured=false。
func TestLoadConfigMaskedEmptyKey(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	os.WriteFile(fp, []byte(`{"api_key":""}`), 0o600)

	masked, err := loadConfigMasked(fp)
	if err != nil {
		t.Fatal(err)
	}
	m := masked.(map[string]any)
	if v, ok := m["api_key"].(string); !ok || v != "" {
		t.Fatalf("空 api_key 应保持空串，got %#v", m["api_key"])
	}
	if v, ok := m["api_key_configured"].(bool); !ok || v {
		t.Fatalf("api_key_configured 应为 false，got %#v", m["api_key_configured"])
	}
}

func containsSubstr(s, sub string) bool {
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
