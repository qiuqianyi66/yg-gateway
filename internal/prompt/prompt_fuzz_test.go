package prompt

import (
	"encoding/json"
	"testing"
)

// FuzzRewrite 让任意字节串进 prompt 改写。
//
// 这个函数的特殊之处在于它处理的是**用户请求体**，而且是在转发给上游**之前**
// 原样返回给调用方继续使用的那一份。它必须对不是 JSON、是 JSON 但不是对象、
// 数组、字符串、数字、null、深层嵌套全部保持"不改崩"：哪怕它选择不注入，
// 也必须把原 body 原样吐回去，否则请求就在网关这一层坏掉了。
//
// 断言：不 panic；当输入不是合法 JSON 对象时，输出必须与输入逐字节相同
// （这是"不注入"的正确降级行为，比单纯的"不崩"更难伪造）。
func FuzzRewrite(f *testing.F) {
	seeds := []string{
		`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"glm-5.2","messages":[]}`,
		`{"messages":[{"role":"system","content":"x"}]}`,
		`[]`,
		`"string"`,
		`123`,
		`null`,
		`{}`,
		`{"model":`,
		``,
		`  {"model":"a"}  `,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	const sys = "you are a test system prompt"

	f.Fuzz(func(t *testing.T, body []byte) {
		out := Rewrite(body, sys)
		out2 := Append(body, sys)

		// 非法 JSON 或非对象时必须原样返回——不注入是允许的，改坏不是。
		if !isJSONObject(body) {
			if string(out) != string(body) {
				t.Fatalf("Rewrite 改写了非 JSON 对象输入：in=%q out=%q", body, out)
			}
			if string(out2) != string(body) {
				t.Fatalf("Append 改写了非 JSON 对象输入：in=%q out=%q", body, out2)
			}
		}
	})
}

// isJSONObject 复刻被测函数自己的判定口径，只用于在断言里区分两类输入。
// 注意：Rewrite 自身用 json.Unmarshal 到 map[string]any，而 "null" 会**成功解出
// nil map**，所以这里必须把 nil 也判成"不是对象"，否则断言口径与被测代码不一致。
func isJSONObject(b []byte) bool {
	var probe map[string]any
	if err := json.Unmarshal(b, &probe); err != nil {
		return false
	}
	return probe != nil
}
