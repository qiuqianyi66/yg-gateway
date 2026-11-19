package upstream

import (
	"encoding/json"
	"testing"
)

// FuzzPrepareBody 让任意字节串进**出站请求规范化管线**。
//
// 这条路径才是线上先炸的地方：客户端请求先经 prepareBody 改写（强制 stream、
// 翻译 max_completion_tokens、补 stream_options、规范 tool_choice……），
// 然后才轮到 prompt 层。2026-11-19 生产日志里两次 panic 的栈顶都是
// `prepareBodyAST → obj["stream"] = true`，而当时 prompt 层已经修好，
// 说明只 fuzz 表层会漏掉真正的根因。
//
// 断言：不 panic、不死循环；输入不是合法 JSON 对象时必须原样返回。
func FuzzPrepareBody(f *testing.F) {
	seeds := []string{
		`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"glm-5.2","max_completion_tokens":128,"messages":[]}`,
		`{"model":"glm-5.2","stream_options":{"include_usage":false},"messages":[]}`,
		`{"model":"glm-5.2","tool_choice":"auto","tools":[{"type":"function","function":{"name":"t"}}]}`,
		`null`,
		`{}`,
		`[]`,
		`"x"`,
		`7`,
		`{"model":`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		got := PrepareBodyOptWithEffortsAndDefaultAndCache(src, true, nil, nil, "u1", "c1")
		got2 := PrepareBodyOptWithEfforts(src, false, nil)
		if !isJSONObj(src) {
			if string(got) != string(src) {
				t.Fatalf("大入口改写了非对象输入：in=%q out=%q", src, got)
			}
			if string(got2) != string(src) {
				t.Fatalf("小入口改写了非对象输入：in=%q out=%q", src, got2)
			}
		}
		// 注入 cache key 的入口也要过一遍：它对 nil map 赋值是另一个崩溃点。
		_ = InjectPromptCacheKey(src, "u1", "c1")
	})
}

func isJSONObj(b []byte) bool {
	var probe map[string]any
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	return probe != nil
}
