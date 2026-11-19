package server

import "testing"

// FuzzTransformAnthropicToOpenAI 让任意字节串直接进 Anthropic→OpenAI 的请求转换。
//
// 为什么这个函数值得 fuzz：它的入参是**外部客户端**发来的原始 body，完全不可信。
// 网关是 7×24 常驻服务，这里任何一次 panic 都会打死整个进程——不是"这个请求失败"，
// 是"所有请求失败"。同类项目（caddy 的 lexer/formatter、go 标准库的 archive/tar）
// 都把 fuzz 用在"解析外部格式"这一步，原因相同。
//
// 断言只有一条：不 panic、不死循环。转换结果对不对由常规单测管（anthropic_fidelity_test.go），
// fuzz 只管"任何畸形输入都不会把服务带走"。
func FuzzTransformAnthropicToOpenAI(f *testing.F) {
	seeds := []string{
		`{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"glm-5.2","max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
		`{"model":"","messages":[]}`,
		`{"model":"glm-5.2","stream":true,"system":"be brief","messages":[{"role":"user","content":"x"}]}`,
		`{"model":"glm-5.2","tools":[{"name":"t","input_schema":{"type":"object"}}],"messages":[]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AA=="}}]}]}`,
		`{}`,
		`null`,
		`[]`,
		`{"model":`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		// 只要不 panic 就算过；返回值刻意忽略——本 target 不验证语义。
		_, _ = transformAnthropicToOpenAI(body)
		// 同一份输入再走一次流式意图判定，它是转换前的另一条解析路径。
		_, _ = anthropicStreamIntent(body)
	})
}
