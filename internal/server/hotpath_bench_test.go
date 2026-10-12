package server

import (
	"encoding/json"
	"testing"

	"yg-gateway/internal/session"
	"yg-gateway/internal/upstream"
)

// 量化「单个请求体被重复 JSON 解析」的成本（优化前的基线）。
// 这 6 次解析在 chatCompletions 热路径上串行发生，每次都全量解析整个 body：
//   peek(stream/model) / modelRaw(原始类型) / ToolNameAllowlist /
//   ExtractKey(会话键) / TurnKey(轮级键) / hasImagePart(是否带图)
// 大 body（长对话 + tool_calls）时开销显著。

func benchBody(size int) []byte {
	// 构造 realistic 请求：多轮 messages + tools + 长文本内容
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	msgs := make([]msg, 0, size)
	for i := 0; i < size; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, msg{Role: role, Content: string(make([]byte, 400))})
	}
	b, _ := json.Marshal(map[string]any{
		"model":    "cn:glm-5.2",
		"stream":   true,
		"messages": msgs,
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "read_file"}},
			{"type": "function", "function": map[string]any{"name": "write_file"}},
		},
	})
	return b
}

// BenchmarkBodyParseCurrent 现状：6 次独立解析（优化前的基线数字）。
func BenchmarkBodyParseCurrent(b *testing.B) {
	body := benchBody(40)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var peek struct {
			Stream bool   `json:"stream"`
			Model  string `json:"model"`
		}
		_ = json.Unmarshal(body, &peek)

		var modelRaw struct {
			Model json.RawMessage `json:"model"`
		}
		_ = json.Unmarshal(body, &modelRaw)

		_ = upstream.ToolNameAllowlist(body)
		_ = session.ExtractKey(body)
		_ = session.TurnKey(body)
		_ = hasImagePart(body)
	}
}

// BenchmarkBodyParseOnce 优化后：1 次解析复用（对照基线）。
// 目标：证明合并为单次解析后的收益量级。
func BenchmarkBodyParseOnce(b *testing.B) {
	body := benchBody(40)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var once struct {
			Stream bool   `json:"stream"`
			Model  string `json:"model"`
			Tools  []struct {
				Name     string `json:"name"`
				Function *struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &once)
		// 单次解析后各消费方从同一结构体取，不再各自 Unmarshal。
		_ = once.Model
		_ = once.Stream
		_ = len(once.Tools)
		_ = len(once.Messages)
	}
}
