package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 阻断项 2：面板 GET /panel/api/config 不得泄漏 api_key / api_keys[].key 明文。
// loadConfigMasked 加载配置后脱敏，这里验证脱敏结果不含真实密钥值。

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
	raw, _ := json.Marshal(masked)
	s := string(raw)

	// 不得含任何真实密钥明文
	for _, secret := range []string{"sk-secret-single", "sk-secret-multi-1", "sk-secret-multi-2"} {
		if containsSubstr(s, secret) {
			t.Fatalf("脱敏配置泄漏密钥 %q: %s", secret, s)
		}
	}
	// 脱敏占位符应存在
	if !containsSubstr(s, "********") {
		t.Fatalf("脱敏占位符缺失: %s", s)
	}
	// id/name/realm/models 等非密钥字段应保留
	for _, keep := range []string{"k1", "k2", "甲", "m1", "cn"} {
		if !containsSubstr(s, keep) {
			t.Fatalf("非密钥字段 %q 被误脱敏: %s", keep, s)
		}
	}
}

// TestLoadConfigMaskedEmptyKey 空 api_key 保持空（前端据此判断是否已鉴权）。
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
