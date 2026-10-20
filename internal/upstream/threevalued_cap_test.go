package upstream

import "testing"

// capTrue 断言能力字段是**显式 true**。
//
// 为什么需要一个专门的 helper：这三个字段是三值（true / false / nil=未知）。
// 直接写 !v 编译不过；若写成 "v==nil || !*v" 之外的宽松形式（比如把 nil 当 false），
// 就等于在测试里重演被修掉的 bug。nil 与 false 都必须算失败，但**原因不同**，要分开报。
func capTrue(t *testing.T, field string, v *bool) bool {
	t.Helper()
	if v == nil {
		t.Errorf("%s = nil（上游未声明 → 未知），want 显式 true", field)
		return false
	}
	if !*v {
		t.Errorf("%s = false, want true", field)
		return false
	}
	return true
}

// boolp 构造 *bool（测试表内联取值用，对齐包内 PromoFactor 的 &变量 惯例）。
func boolp(v bool) *bool { return &v }

// TestParseGlobalModelLooseCapabilityThreeValued 能力字段必须保持三值：
// 显式 true / 显式 false / 键缺失(nil=未知)，三者互不塌缩。
//
// 回归背景：旧实现 `mi.X, _ = obj["x"].(bool)` 把「键缺失」塌缩成 false，
// 于是"目录没说"被当成"目录说不支持"，下游据此 filter 会静默误伤。
func TestParseGlobalModelLooseCapabilityThreeValued(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want func(t *testing.T, mi ModelInfo) string // 返回空串=通过，否则为原因
	}{
		{
			// 每个用例都必须带 id：parseGlobalModelLoose 在 id 缺失时提前返回空
			// ModelInfo（契约如此），能力字段压根不会被解析。
			name: "显式 true",
			raw:  map[string]any{"id": "m1", "supportsImages": true, "supportsReasoning": true},
			want: func(t *testing.T, mi ModelInfo) string {
				if !capTrue(t, "supportsImages", mi.SupportsImages) {
					return "应 true"
				}
				if !capTrue(t, "supportsReasoning", mi.SupportsReasoning) {
					return "应 true"
				}
				return ""
			},
		},
		{
			name: "显式 false",
			raw:  map[string]any{"id": "m1", "supportsImages": false, "supportsReasoning": false},
			want: func(t *testing.T, mi ModelInfo) string {
				if mi.SupportsImages == nil || *mi.SupportsImages {
					return "应显式 false"
				}
				if mi.SupportsReasoning == nil || *mi.SupportsReasoning {
					return "应显式 false"
				}
				return ""
			},
		},
		{
			name: "键缺失 → nil（未知），不得塌缩成 false",
			raw:  map[string]any{"id": "m1"},
			want: func(t *testing.T, mi ModelInfo) string {
				if mi.SupportsImages != nil {
					return "缺失应为 nil"
				}
				if mi.SupportsReasoning != nil {
					return "缺失应为 nil"
				}
				return ""
			},
		},
		{
			name: "值类型不是 bool（上游给了字符串）→ nil，不得塌缩成 false",
			raw:  map[string]any{"id": "m1", "supportsImages": "true"},
			want: func(t *testing.T, mi ModelInfo) string {
				if mi.SupportsImages != nil {
					return "非 bool 应为 nil（unknown）"
				}
				return ""
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mi, ok := parseGlobalModelLoose(c.raw)
			if !ok {
				t.Fatalf("%s: parseGlobalModelLoose 返回 false（id 应已给出）", c.name)
			}
			if reason := c.want(t, mi); reason != "" {
				t.Fatalf("%s: %s (±%v)", c.name, reason, mi)
			}
		})
	}
}

// TestHintDoesNotAssertUnsupportedWhenUnknown 目录未声明 supports_images（nil=未知）时，
// 必须退回**中性**参数提示，不得断言"该模型不支持图片"。
//
// 牙齿：把 GatewayHint 里的 `ctx.ModelSupportsImages != nil` 去掉 → 本用例 FAIL。
func TestHintDoesNotAssertUnsupportedWhenUnknown(t *testing.T) {
	const body = `{"extError":{"code":11133}}`
	const pickModel = "model glm-5.2 does not support images; pick one with supports_images=true from /v1/models"
	const neutral = "request parameters were rejected by the model provider; check message format and model capabilities"

	cases := []struct {
		name string
		ctx  HintContext
		want string
	}{
		{"显式声明不支持 → 点名换模型", HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: true, ModelSupportsImages: boolp(false)}, pickModel},
		{"显式声明支持 → 中性", HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: true, ModelSupportsImages: boolp(true)}, neutral},
		{"目录未声明(nil=未知) → 中性，不得断言不支持", HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: true}, neutral},
		{"目录未收录 → 中性", HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: false, ModelSupportsImages: boolp(false)}, neutral},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := GatewayHint(ErrSoftRate, body, c.ctx); got != c.want {
				t.Fatalf("GatewayHint = %q, want %q", got, c.want)
			}
		})
	}
}
