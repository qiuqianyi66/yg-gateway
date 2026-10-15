package session

import (
	"encoding/json"
	"testing"
)

// TestViewEquivalenceWithBytePaths 视图路径（View 变体）必须与字节路径逐位等价。
//
// 这是 F-701 的核心守门：热路径改成「只读一次」后，所有消费方取到的值必须与
// 旧实现（各自全文档解析）**完全相同**，否则粘性绑定、轮级聚合键、conversation
// 头族都会静默漂移——那是比慢一点严重得多的回归。
//
// 牙齿：把任一 View 变体的判据改错（例如漏掉 metadata 优先、或漏掉 snake 优先
// 于 camel），对应用例立即 FAIL。
func TestViewEquivalenceWithBytePaths(t *testing.T) {
	bodies := []string{
		// 显式会话标识四形态 + prompt_cache_key
		`{"model":"m","metadata":{"conversation_id":"conv-1"},"messages":[]}`,
		`{"model":"m","metadata":{"conversationId":"conv-2"},"messages":[]}`,
		`{"model":"m","conversation_id":"conv-3"}`,
		`{"model":"m","conversationId":"conv-4"}`,
		`{"model":"m","prompt_cache_key":"cache-5","messages":[]}`,
		`{"model":"m","conversation_id":"conv-s","conversationId":"conv-c"}`,
		// user_id 抑制派生
		`{"model":"m","metadata":{"user_id":"u1"},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","user_id":"u1","messages":[{"role":"user","content":"hi"}]}`,
		// 派生路径（system + 首条 user）
		`{"model":"m","messages":[{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"developer","content":"dev"},{"role":"user","content":"你好"}]}`,
		// 纯图片首条 user（G1 盲区修复路径）
		`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`,
		// 多模态混合 parts（文本 + 非文本，签名含 [type] 占位）
		`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`,
		// 轮级键：最后一条 user 胜出
		`{"model":"m","messages":[{"role":"user","content":"第一问"},{"role":"assistant","content":"答"},{"role":"user","content":"第二问"}]}`,
		// 轮级键：agent 多步（轮内追加 tool 消息不改变键）
		`{"model":"m","messages":[{"role":"user","content":"任务"},{"role":"assistant","tool_calls":[{"id":"c1"}]},{"role":"tool","content":"结果"}]}`,
		// 无可派生内容 / 无 user 消息
		`{"model":"m","messages":[{"role":"system","content":"sys"}]}`,
		`{"model":"m","messages":[{"role":"user","content":""}]}`,
		`{"model":"m","messages":[{"role":"user","content":null}]}`,
		// 边界：空 / 坏 JSON / 非对象
		``,
		`{broken`,
		`["not","object"]`,
		`{"model":"m"}`,
		// 类型异常（typed struct 会整体解析失败，RawMessage 视图不会）
		`{"model":"m","user_id":123,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":123,"conversation_id":"conv-x"}`,
	}

	for _, body := range bodies {
		b := []byte(body)
		v, ok := ParseRequestView(b)
		if !ok {
			// 解析失败：字节路径同样返回空，逐位一致（下面仍比一次，确保都是空）。
			if got := ExtractKey(b); got != "" {
				t.Errorf("body=%s: byte path key=%q, view parse failed (want empty)", body, got)
			}
			continue
		}
		for _, derive := range []bool{true, false} {
			want := ExtractKeyOpt(b, derive)
			got := ExtractKeyOptView(&v, derive)
			if want != got {
				t.Errorf("body=%s derive=%v: ExtractKeyOptView=%q want %q", body, derive, got, want)
			}
		}
		if want, got := ResolveConversationID(b), ResolveConversationIDView(&v); want != got {
			t.Errorf("body=%s: ResolveConversationIDView=%q want %q", body, got, want)
		}
		if want, got := TurnKey(b), TurnKeyView(&v); want != got {
			t.Errorf("body=%s: TurnKeyView=%q want %q", body, got, want)
		}
	}
}

// TestRequestViewModelKinds model 字段的四种 JSON 形态都能被视图正确区分。
// handler 用它判定 missing/null/string/other（约束校验要区分「缺 model」与
// 「model 类型非法」），视图必须原样给出字节，不能替调用方决定类型。
func TestRequestViewModelKinds(t *testing.T) {
	cases := []struct {
		body string
		kind string // missing / null / string / other
		str  string
	}{
		{`{"stream":true}`, "missing", ""},
		{`{"model":null}`, "null", ""},
		{`{"model":"cn:glm-5.2"}`, "string", "cn:glm-5.2"},
		{`{"model":123}`, "other", ""},
		{`{"model":{"a":1}}`, "other", ""},
	}
	for _, c := range cases {
		v, ok := ParseRequestView([]byte(c.body))
		if !ok {
			t.Fatalf("body=%s: parse failed", c.body)
		}
		raw := v.Model()
		kind := "missing"
		if len(raw) > 0 {
			switch raw[0] {
			case 'n':
				kind = "null"
			case '"':
				kind = "string"
			default:
				kind = "other"
			}
		}
		if kind != c.kind {
			t.Errorf("body=%s: model kind=%s want %s", c.body, kind, c.kind)
		}
		if got := v.ModelStr(); got != c.str {
			t.Errorf("body=%s: ModelStr=%q want %q", c.body, got, c.str)
		}
	}
}

// TestRequestViewStream 视图的 stream 判定与旧实现（Unmarshal 到 bool）一致：
// 只有字面 true 为真，字符串 / null / 缺字段 / 数字一律假。
func TestRequestViewStream(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"stream":true}`, true},
		{`{"stream":false}`, false},
		{`{"stream":"true"}`, false},
		{`{"stream":null}`, false},
		{`{"stream":1}`, false},
		{`{"model":"m"}`, false},
	}
	for _, c := range cases {
		var old struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal([]byte(c.body), &old)
		v, ok := ParseRequestView([]byte(c.body))
		if !ok {
			t.Fatalf("body=%s: parse failed", c.body)
		}
		if got := v.Stream(); got != old.Stream {
			t.Errorf("body=%s: View.Stream=%v want %v (old impl)", c.body, got, old.Stream)
		}
	}
}

// TestRequestViewSubtreeLazyAndCached messages 子树「用到才解析、只解析一次」。
// 用解析副作用计数不可行（解析是纯函数），这里断言的是**等价性 + 幂等**：
// 反复取用返回同一份切片，且与一次性解析的结果一致。
func TestRequestViewSubtreeLazyAndCached(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`)
	v, ok := ParseRequestView(body)
	if !ok {
		t.Fatal("parse failed")
	}
	first := v.Messages()
	if len(first) != 2 {
		t.Fatalf("Messages len=%d want 2", len(first))
	}
	second := v.Messages()
	if len(second) != len(first) || &first[0] != &second[0] {
		t.Error("Messages should be parsed once and cached (same backing array)")
	}
	// 缺失子树：反复取用均为 nil，不 panic。
	empty, _ := ParseRequestView([]byte(`{"model":"m"}`))
	if got := empty.Messages(); got != nil {
		t.Errorf("missing messages = %v want nil", got)
	}
	if got := empty.Messages(); got != nil {
		t.Errorf("missing messages (2nd call) = %v want nil", got)
	}
	if got := empty.Tools(); got != nil {
		t.Errorf("missing tools = %v want nil", got)
	}
	if got := empty.Meta(); got != nil {
		t.Errorf("missing metadata = %v want nil", got)
	}
}
