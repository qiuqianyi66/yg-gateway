package server

import "testing"

// TestResolveModelForeignColonPrefixIsNotRealm 守 resolveModel 最容易误判的一条：
// 只剥 `cn:` / `global:` 两种前缀，**其余带冒号的模型名一律当裸名**。
//
// 为什么重要：模型名里带冒号是真实存在的（`ollama:llama3`、`account:model` 形态）。
// 如果实现改成「剥第一个冒号前的任意前缀」，这类名字会被截成 `llama3` —— 上游收到
// 一个它不认识的模型名，而网关不会报错，只会去选号然后 400。属于静默改坏请求。
func TestResolveModelForeignColonPrefixIsNotRealm(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		wantRealm string
		wantBare  string
	}{
		{"无冒号 → cn + 原串", "hy3", "cn", "hy3"},
		{"cn 前缀被剥离", "cn:hy3", "cn", "hy3"},
		{"global 前缀被剥离", "global:hy3", "global", "hy3"},
		// 这一条是重点：非枚举前缀必须原样保留（含冒号）。
		{"其他冒号前缀视为裸名", "ollama:llama3", "cn", "ollama:llama3"},
		{"带空格的外来前缀也算裸名", "openai:gpt-4o", "cn", "openai:gpt-4o"},
		// 大小写敏感：注释写明「前缀必须是精确的小写枚举」。
		{"大写 CN 不算 realm", "CN:hy3", "cn", "CN:hy3"},
		{"大小写混写不算 realm", "Global:hy3", "cn", "Global:hy3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			realm, bare := resolveModel(c.model)
			if realm != c.wantRealm || bare != c.wantBare {
				t.Errorf("resolveModel(%q) = (%q, %q)，期望 (%q, %q)",
					c.model, realm, bare, c.wantRealm, c.wantBare)
			}
		})
	}
}

// TestResolveModelStripsRateSuffix 倍率后缀剥离：/v1/models 返回的 id 带 " ×0"，
// 客户端勾选后原样发回来，必须剥掉才能交给上游。
func TestResolveModelStripsRateSuffix(t *testing.T) {
	cases := []struct {
		name      string
		model     string
		wantRealm string
		wantBare  string
	}{
		{"裸名带后缀", "hy3 ×0", "cn", "hy3"},
		{"cn 前缀 + 后缀", "cn:hy3 ×0", "cn", "hy3"},
		{"global 前缀 + 后缀", "global:hy3 ×0.5", "global", "hy3"},
		{"无后缀原样（旧客户端）", "cn:hy3", "cn", "hy3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			realm, bare := resolveModel(c.model)
			if realm != c.wantRealm || bare != c.wantBare {
				t.Errorf("resolveModel(%q) = (%q, %q)，期望 (%q, %q)",
					c.model, realm, bare, c.wantRealm, c.wantBare)
			}
		})
	}
}

// TestHasImagePartContract 覆盖 hasImagePart 的四条契约。它是 hasImage() 的
// **回落路径**（请求视图不可用时才走），原本 0.0% —— 一条真在用的分支从没被跑过。
//
// 判据「宁缺勿滥」：判不出带图就不给「模型不支持图片」的提示。所以畸形形态
// 必须一律 false，而不是猜 true。
func TestHasImagePartContract(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "顶层 image_url part → true",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`,
			want: true,
		},
		{
			name: "多消息中任一携带 → true",
			body: `{"messages":[{"role":"system","content":[{"type":"text","text":"sys"}]},{"role":"user","content":[{"type":"image_url","image_url":{"url":"http://x/y.png"}}]}]}`,
			want: true,
		},
		{
			name: "只有文本 part → false",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"纯文本"}]}]}`,
			want: false,
		},
		{
			name: "content 是字符串形态 → false（不是数组，不可能含 part）",
			body: `{"messages":[{"role":"user","content":"just a string"}]}`,
			want: false,
		},
		{
			name: "body 不可解析 → false（宁缺勿滥）",
			body: `{"messages":`,
			want: false,
		},
		{
			name: "没有 messages 字段 → false",
			body: `{"model":"hy3"}`,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasImagePart([]byte(c.body)); got != c.want {
				t.Errorf("hasImagePart() = %v，期望 %v\nbody: %s", got, c.want, c.body)
			}
		})
	}
}
