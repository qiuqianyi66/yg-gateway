package server

import "testing"

// FuzzResponsesToChat 让任意字节串进 Responses API → Chat Completions 的转换。
//
// 同样是外部客户端输入。这条链比 anthropic 那条更长：它要先判断是不是 Responses
// 形状，再逐字段搬移，中间还有 tools/input/file 等多态分支，每一层都可能对
// 「字段存在但类型不对」做类型断言。fuzz 的目标就是逼出那些断言。
//
// 断言：不 panic、不死循环。
func FuzzResponsesToChat(f *testing.F) {
	seeds := []string{
		`{"model":"glm-5.2","input":"hi"}`,
		`{"model":"glm-5.2","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
		`{"model":"glm-5.2","input":[{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
		`{"model":"glm-5.2","stream":true,"input":"hi"}`,
		`{"model":"glm-5.2","tools":[{"type":"function","name":"t","parameters":{}}]}`,
		`{"model":"glm-5.2","input":{"role":"user","content":"hi"}}`,
		`{"input":123}`,
		`{"model":[]}`,
		`{}`,
		`null`,
		`{"model":`,
		``,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		_, _ = responsesToChat(body)
		// streamOf 是同一层前置判定，也吃这份 body。
		_ = streamOf(body)
	})
}
