package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 多 API Key 配置解析测试（T2）。覆盖六态空值契约 + id/realm/models 三态校验。
// 契约依据：ADR-001 §四（兼容策略）与 PRD v2 §四（空值语义表）。

// apiKeyEntry 构造一段 api_keys 数组条目 JSON。
func apiKeyEntry(id, key, name, realm string, models string) string {
	parts := []string{`"id":` + str(id), `"key":` + str(key)}
	if name != "" {
		parts = append(parts, `"name":`+str(name))
	}
	if realm != "" {
		parts = append(parts, `"realm":`+str(realm))
	}
	if models != "" {
		parts = append(parts, `"models":`+models)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func str(s string) string {
	// 用 encoding/json 保证转义正确（测试代码里手工拼 JSON 易出错）。
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func loadWith(body string, t *testing.T) (c *Config, err error) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "c.json")
	if err := os.WriteFile(fp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(fp)
}

// TestAPIKeysLegacyMissing 键缺失 = 旧版兼容（APIKeys nil，零迁移）。
func TestAPIKeysLegacyMissing(t *testing.T) {
	c, err := loadWith(`{"api_key":"k"}`, t)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKeys != nil {
		t.Fatalf("api_keys 键缺失应保持 nil（旧版兼容），got %#v", c.APIKeys)
	}
	if c.APIKey != "k" {
		t.Errorf("api_key=%q want k", c.APIKey)
	}
}

// TestAPIKeysExplicitEmptyFailsFast 显式 [] = 配置错误，fail fast（不静默降级为免鉴权）。
func TestAPIKeysExplicitEmptyFailsFast(t *testing.T) {
	if _, err := loadWith(`{"api_key":"k","api_keys":[]}`, t); err == nil {
		t.Fatal("显式 api_keys:[] 应 fail fast")
	}
}

// TestAPIKeysNullFailsFast 显式 null 按 PRD/ADR 契约 fail fast（阻断项 4）。
// 与「键缺失」区分：null 是显式配置错误，不是旧版兼容。
func TestAPIKeysNullFailsFast(t *testing.T) {
	if _, err := loadWith(`{"api_key":"k","api_keys":null}`, t); err == nil {
		t.Fatal("显式 api_keys:null 应 fail fast（契约：null 是配置错误，不是键缺失）")
	}
}

// TestAPIKeysAllEmptyEntriesFailsFast 数组存在但全是空 key 条目 → fail fast。
func TestAPIKeysAllEmptyEntriesFailsFast(t *testing.T) {
	body := `{"api_key":"","api_keys":[` +
		apiKeyEntry("k1", "", "", "", "") + `,` +
		apiKeyEntry("k2", "", "", "", "") + `]}`
	if _, err := loadWith(body, t); err == nil {
		t.Fatal("全空 key 条目应 fail fast")
	}
}

// TestAPIKeysMixedEmptyFiltered 数组里混有空 key 条目：过滤后仍有效条目，放行。
func TestAPIKeysMixedEmptyFiltered(t *testing.T) {
	body := `{"api_key":"","api_keys":[` +
		apiKeyEntry("k1", "", "", "", "") + `,` +
		apiKeyEntry("k2", "sk-real", "乙", "cn", "") + `]}`
	c, err := loadWith(body, t)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.APIKeys) != 1 || c.APIKeys[0].ID != "k2" {
		t.Fatalf("过滤空条目后应剩 1 个有效 Key k2，got %#v", c.APIKeys)
	}
}

// TestAPIKeysDuplicateIDFailsFast 重复 id fail fast（id 是稳定归因键，不能重复）。
func TestAPIKeysDuplicateIDFailsFast(t *testing.T) {
	body := `{"api_keys":[` +
		apiKeyEntry("dup", "sk-a", "", "", "") + `,` +
		apiKeyEntry("dup", "sk-b", "", "", "") + `]}`
	if _, err := loadWith(body, t); err == nil {
		t.Fatal("重复 id 应 fail fast")
	}
}

// TestAPIKeysInvalidID 非法 id 字符 fail fast（R4 白名单 [A-Za-z0-9_-] ≤64）。
func TestAPIKeysInvalidID(t *testing.T) {
	for _, id := range []string{"", "has space", "含中文", "a/b", strings.Repeat("x", 65)} {
		body := `{"api_keys":[` + apiKeyEntry(id, "sk-a", "", "", "") + `]}`
		if _, err := loadWith(body, t); err == nil {
			t.Errorf("非法 id %q 应 fail fast", id)
		}
	}
}

// TestAPIKeysInvalidRealm 非法 realm fail fast（cn/global/空 之外）。
func TestAPIKeysInvalidRealm(t *testing.T) {
	body := `{"api_keys":[` + apiKeyEntry("k1", "sk-a", "", "us", "") + `]}`
	if _, err := loadWith(body, t); err == nil {
		t.Fatal("非法 realm 应 fail fast")
	}
}

// TestAPIKeysModelsThreeState models 三态：缺失=nil / 空[]=非nil空 / 非空=白名单。
func TestAPIKeysModelsThreeState(t *testing.T) {
	// 缺失 → nil（不限）
	c1, err := loadWith(`{"api_keys":[`+apiKeyEntry("k1", "sk-a", "", "", "")+`]}`, t)
	if err != nil {
		t.Fatal(err)
	}
	if c1.APIKeys[0].Models != nil {
		t.Fatalf("models 缺失应 nil（不限），got %#v", c1.APIKeys[0].Models)
	}

	// 显式 [] → 非 nil 空切片（全拒）
	c2, err := loadWith(`{"api_keys":[`+apiKeyEntry("k1", "sk-a", "", "", "[]")+`]}`, t)
	if err != nil {
		t.Fatal(err)
	}
	if c2.APIKeys[0].Models == nil || len(c2.APIKeys[0].Models) != 0 {
		t.Fatalf("models:[] 应非 nil 空切片（全拒），got %#v", c2.APIKeys[0].Models)
	}

	// 非空 → 白名单
	c3, err := loadWith(`{"api_keys":[`+apiKeyEntry("k1", "sk-a", "", "", `["m1","m2"]`)+`]}`, t)
	if err != nil {
		t.Fatal(err)
	}
	if len(c3.APIKeys[0].Models) != 2 {
		t.Fatalf("models 非空应白名单，got %#v", c3.APIKeys[0].Models)
	}
}

// TestAPIKeysEnvDoesNotOverride 环境变量 WB2A_API_KEY 只覆盖单值 api_key，不覆盖 api_keys。
func TestAPIKeysEnvDoesNotOverride(t *testing.T) {
	t.Setenv("WB2A_API_KEY", "env-key")
	body := `{"api_keys":[` + apiKeyEntry("k1", "sk-a", "", "", "") + `]}`
	c, err := loadWith(body, t)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "env-key" {
		t.Errorf("api_key=%q want env-key（env 覆盖单值）", c.APIKey)
	}
	// api_keys 不受 env 影响：仍是配置里的 sk-a
	if len(c.APIKeys) != 1 || c.APIKeys[0].Key != "sk-a" {
		t.Fatalf("api_keys 不应被 env 覆盖，got %#v", c.APIKeys)
	}
}

// TestAPIKeysSingleAndMultiCoexist 单值 + 多 Key 共存，两者都保留。
func TestAPIKeysSingleAndMultiCoexist(t *testing.T) {
	body := `{"api_key":"single", "api_keys":[` + apiKeyEntry("k1", "sk-multi", "", "", "") + `]}`
	c, err := loadWith(body, t)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIKey != "single" {
		t.Errorf("api_key=%q want single", c.APIKey)
	}
	if len(c.APIKeys) != 1 || c.APIKeys[0].Key != "sk-multi" {
		t.Fatalf("api_keys=%#v want [sk-multi]", c.APIKeys)
	}
}

// TestAPIKeysReservedID 保留名 "single"（旧单 api_key 合成身份）不能作为用户 id（🟡 审查项）。
func TestAPIKeysReservedID(t *testing.T) {
	body := `{"api_keys":[` + apiKeyEntry("single", "sk-a", "", "", "") + `]}`
	if _, err := loadWith(body, t); err == nil {
		t.Fatal("保留 id \"single\" 应 fail fast（与旧单 api_key 合成身份冲突）")
	}
}

// TestAPIKeysDuplicateKeyFailsFast 重复密钥 fail fast（阻断项 1）：
// 同一密钥值出现在两条多 Key 之间，或出现在单 api_key 与多 Key 之间，都会让 Match
// 命中第一把 Key 而绕过另一把的 models/realm 约束，必须在配置层拒绝。
func TestAPIKeysDuplicateKeyFailsFast(t *testing.T) {
	// 两条多 Key 重复
	body1 := `{"api_keys":[` +
		apiKeyEntry("k1", "sk-dup", "", "", `["m1"]`) + `,` +
		apiKeyEntry("k2", "sk-dup", "", "", `["m2"]`) + `]}`
	if _, err := loadWith(body1, t); err == nil {
		t.Fatal("两条多 Key 使用同一密钥值应 fail fast")
	}

	// 单 api_key 与多 Key 重复
	body2 := `{"api_key":"sk-dup", "api_keys":[` + apiKeyEntry("k1", "sk-dup", "", "", `["m1"]`) + `]}`
	if _, err := loadWith(body2, t); err == nil {
		t.Fatal("单 api_key 与多 Key 使用同一密钥值应 fail fast")
	}
}

// TestAPIKeysPaddedSingleKeyDuplicateRejected 钉住「顶层 api_key 与多 Key 的归一化口径一致」。
//
// 为什么需要它（2026-10-17，独立复审自选变异 B3-c 存活）：`api_keys[].key` 会被 TrimSpace 后
// 落库，而顶层 `api_key` 从不 trim（运行时摘要取原值）。重复检测若按原值比较，
// `api_key:" sk-dup "` + `api_keys[].key:"sk-dup"` 会被判成「不同密钥」→ 管理员意图里的
// 同一把密钥静默变成两把，约束是否生效取决于客户端发哪一个。
//
// 入口用 ParseConfig（**单次** normalize）：Load 会 normalize 两遍，第二遍时 k.Key 已被
// 首遍 trim，正好把这个缺口掩盖掉（独立复审实测：同一个变异在 Load 下「咬住」、在 ParseConfig
// 下才暴露）。这也是本用例特意不走 loadWith 的原因。
//
// 还原什么会让它 FAIL：把比较改回 `key == c.APIKey`（不归一化）→ 两个子例都红。
func TestAPIKeysPaddedSingleKeyDuplicateRejected(t *testing.T) {
	bodies := []string{
		`{"api_key":" sk-dup ", "api_keys":[` + apiKeyEntry("k1", "sk-dup", "", "", `["m1"]`) + `]}`,
		`{"api_key":"sk-dup", "api_keys":[` + apiKeyEntry("k1", "  sk-dup  ", "", "", `["m1"]`) + `]}`,
	}
	for _, body := range bodies {
		if _, err := ParseConfig([]byte(body)); err == nil {
			t.Errorf("顶层 api_key 与多 Key 归一化后同值时必须 fail fast，got nil；body=%s", body)
		}
	}
}
