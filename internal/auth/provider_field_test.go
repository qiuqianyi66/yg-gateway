package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProviderIDDefaultsToCodeBuddy：旧 auth 文件（无 provider 键）解析后必须回落 codebuddy。
// 这是 F2 的零回归锚点——Provider 字段为空是历史合法态，不能被下游读成「未知 provider」。
func TestProviderIDDefaultsToCodeBuddy(t *testing.T) {
	a, err := Parse([]byte(`{"accessToken":"tok","uid":"u1"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := a.ProviderID(); got != "codebuddy" {
		t.Errorf("ProviderID() = %q，want codebuddy（空值必须回落）", got)
	}
	if a.APIKeyConfigured() {
		t.Error("无 api_key 时 APIKeyConfigured() 应为 false")
	}
}

func TestParseProviderAPIKeyNested(t *testing.T) {
	raw := `{"auth":{"accessToken":"tok"},"account":{"uid":"u1"},"provider":"ark","api_key":"sk-abc"}`
	a, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse nested: %v", err)
	}
	if got := a.ProviderID(); got != "ark" {
		t.Errorf("ProviderID() = %q，want ark", got)
	}
	if !a.APIKeyConfigured() {
		t.Error("有 api_key 时 APIKeyConfigured() 应为 true")
	}
	if a.APIKey != "sk-abc" {
		t.Errorf("APIKey 解析错误")
	}
}

func TestParseProviderAPIKeyFlat(t *testing.T) {
	raw := `{"accessToken":"tok","uid":"u1","provider":"openai_compat","api_key":"sk-xyz"}`
	a, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse flat: %v", err)
	}
	if got := a.ProviderID(); got != "openai_compat" {
		t.Errorf("ProviderID() = %q，want openai_compat", got)
	}
	if a.APIKey != "sk-xyz" {
		t.Errorf("APIKey 解析错误")
	}
}

// TestSaveAtomicPreservesProviderAPIKey：刷新写回不得丢失多 provider 凭证字段。
func TestSaveAtomicPreservesProviderAPIKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.json")
	a := &Auth{AccessToken: "tok", UID: "u1", FilePath: p, Provider: "ark", APIKey: "sk-keep"}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	b, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse roundtrip: %v", err)
	}
	if b.ProviderID() != "ark" || !b.APIKeyConfigured() || b.APIKey != "sk-keep" {
		t.Errorf("往返后 provider/api_key 丢失: %q / %v", b.ProviderID(), b.APIKeyConfigured())
	}
}

// TestProviderIDNilReceiver：nil 接收者必须安全（选号/状态路径会持有 *Auth 指针，可能为 nil）。
func TestProviderIDNilReceiver(t *testing.T) {
	var a *Auth
	if got := a.ProviderID(); got != "codebuddy" {
		t.Errorf("nil.ProviderID() = %q，want codebuddy", got)
	}
	if a.APIKeyConfigured() {
		t.Error("nil.APIKeyConfigured() 应为 false")
	}
}

// TestParseProviderAPIKeyOnlyAccount Ark 类 provider 用 api_key 认证、不需要 OAuth
// accessToken。打磨中发现此缺口：config 配了 ark，但 auths/ 里放不进账号（Parse 强制
// accessToken 非空）→ 池里永远没有 ark 号 → 多 provider 路由形同虚设。
//
// 牙齿：删掉 Parse 里的 provider/api_key 放行分支（还原 bug）→ 第一个断言 FAIL。
func TestParseProviderAPIKeyOnlyAccount(t *testing.T) {
	// 只有 provider + api_key（无 accessToken）→ 必须通过。
	a, err := Parse([]byte(`{"provider":"ark","api_key":"sk-ark","account":{"uid":"ark-1"}}`))
	if err != nil {
		t.Fatalf("Ark 账号（无 accessToken）应可 Parse: %v", err)
	}
	if a.ProviderID() != "ark" || !a.APIKeyConfigured() {
		t.Errorf("Ark 账号解析错误: provider=%q apiKey=%v", a.ProviderID(), a.APIKeyConfigured())
	}

	// codebuddy 账号无 accessToken → 仍拒绝（零回归）。
	if _, err := Parse([]byte(`{"uid":"u1"}`)); err == nil {
		t.Error("codebuddy 账号缺 accessToken 应拒绝")
	}

	// 非 codebuddy 但 api_key 也空 → 拒绝（无凭证账号不得进池）。
	if _, err := Parse([]byte(`{"provider":"ark","account":{"uid":"ark-1"}}`)); err == nil {
		t.Error("ark 账号缺 api_key 应拒绝")
	}
}

// TestSaveAtomicProviderAPIKeyOnly 纯 api_key 账号可写回（不因 accessToken 空而拒绝）。
func TestSaveAtomicProviderAPIKeyOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ark.json")
	a := &Auth{UID: "ark-1", FilePath: p, Provider: "ark", APIKey: "sk-ark"}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic 纯 api_key 账号应成功: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(raw)
	if err != nil {
		t.Fatalf("roundtrip Parse: %v", err)
	}
	if b.ProviderID() != "ark" || b.APIKey != "sk-ark" {
		t.Errorf("往返丢失: provider=%q apiKey=%q", b.ProviderID(), b.APIKey)
	}
}
