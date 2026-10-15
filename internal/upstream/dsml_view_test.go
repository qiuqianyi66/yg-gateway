package upstream

import (
	"reflect"
	"testing"

	"yg-gateway/internal/session"
)

// TestToolNameAllowlistViewEquivalence 视图路径与字节路径必须产出**同一份名单**。
//
// F-701 守门：标记修复层靠这份名单判定「正文里的原生标记是不是工具调用」。名单
// 一旦漂移（少了历史工具名、或多了噪声），修复层会在最需要它的场景失效或误判。
//
// 牙齿：把视图路径漏掉 functions 来源或 messages 历史来源 → 对应用例 FAIL。
func TestToolNameAllowlistViewEquivalence(t *testing.T) {
	bodies := []string{
		// OpenAI 形态 tools[].function.name
		`{"tools":[{"type":"function","function":{"name":"read_file"}},{"type":"function","function":{"name":"write_file"}}]}`,
		// Responses 风格裸 tools[].name
		`{"tools":[{"name":"exec_command"}]}`,
		// 旧版 functions[].name
		`{"functions":[{"name":"legacy_tool"}]}`,
		// 会话历史里出现过的工具名（tools 声明丢失时的唯一来源）
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"from_history"}}]}]}`,
		// 旧版 function_call
		`{"messages":[{"role":"assistant","function_call":{"name":"legacy_call"}}]}`,
		// 三来源混合 + 重复名（去重语义一致）
		`{"tools":[{"function":{"name":"dup"}}],"functions":[{"name":"dup"}],"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"dup"}}]}]}`,
		// 空名单（有 tools 但没名字 → nil）
		`{"tools":[{"type":"function"}]}`,
		`{"tools":[]}`,
		// 完全无来源
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		// 类型异常条目（不能被整体解析失败带走）
		`{"tools":[{"function":{"name":"ok"}},{"function":123},{"name":"bare"}]}`,
		`{"messages":[{"role":"assistant","tool_calls":"not-array"}]}`,
	}
	for _, body := range bodies {
		b := []byte(body)
		want := ToolNameAllowlist(b)
		v, ok := session.ParseRequestView(b)
		if !ok {
			t.Fatalf("body=%s: view parse failed", body)
		}
		got := ToolNameAllowlistView(&v)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("body=%s:\n view=%v\n want=%v", body, got, want)
		}
	}
}

// TestToolNameAllowlistViewNil 视图为 nil（请求体解析失败）时返回 nil，
// 与字节路径「坏 JSON → nil」同语义。
func TestToolNameAllowlistViewNil(t *testing.T) {
	if got := ToolNameAllowlistView(nil); got != nil {
		t.Errorf("nil view = %v want nil", got)
	}
}
