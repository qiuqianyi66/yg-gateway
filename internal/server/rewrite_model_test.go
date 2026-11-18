package server

import (
	"encoding/json"
	"testing"
)

// TestRewriteModelGuardClauses 覆盖 rewriteModel 的四条「原样返回」防护。
//
// 这个函数在主请求路径上（chatCompletions 调它改写 outbound body），原本 0.0%
// 覆盖。四条防护里有两条是安全阀：body 不可解析时**不能**二次错误化，
// 否则一个上游本可自行处理的请求会被网关先 400 掉。
func TestRewriteModelGuardClauses(t *testing.T) {
	cases := []struct {
		name string
		body string
		bare string
		want string // 空 = 期望原样返回（用 body 自身比较）
	}{
		{name: "空 body 原样", body: "", bare: "glm-5.2"},
		{name: "bare 为空原样", body: `{"model":"vendor/alibaba/glm-5.2"}`, bare: ""},
		{name: "body 不可解析原样返回（不二次错误化）", body: `{"model":`, bare: "glm-5.2"},
		{name: "model 不是字符串原样", body: `{"model":123}`, bare: "glm-5.2"},
		{name: "model 已等于 bare 原样", body: `{"model":"glm-5.2","messages":[]}`, bare: "glm-5.2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(rewriteModel([]byte(c.body), c.bare))
			if got != c.body {
				t.Errorf("应原样返回\n got: %s\nwant: %s", got, c.body)
			}
		})
	}
}

// TestRewriteModelRewritesOnlyModel 正向路径：只改 model，其余字段必须保留。
// 若这里丢了 messages，客户端发来的对话就被网关吃掉了 —— 静默的数据损坏。
func TestRewriteModelRewritesOnlyModel(t *testing.T) {
	body := `{"model":"vendor/alibaba/glm-5.2","messages":[{"role":"user","content":"hi"}],"max_tokens":64}`
	got := rewriteModel([]byte(body), "glm-5.2")

	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v\n%s", err, got)
	}
	if m, _ := obj["model"].(string); m != "glm-5.2" {
		t.Errorf("model 应改成 bare，实际 %q", m)
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Errorf("messages 必须原样保留，实际 %v", obj["messages"])
	}
	if mt, _ := obj["max_tokens"].(float64); mt != 64 {
		t.Errorf("max_tokens 必须保留，实际 %v", obj["max_tokens"])
	}
}

// TestRewriteModelNumberPrecisionIsKnownLoss 钉住一条**已知限制**：
// rewriteModel 走 map[string]any 往返，所有数字变 float64。超出 float64 精确
// 整数范围（2^53）的字段会丢位。
//
// 这里不断言「不会丢」——它确实会丢。用例的作用是把现状固定下来，避免日后
// 有人以为它是无损的。真修需要换成 json.Decoder + Token 流式改写，属另一批。
func TestRewriteModelNumberPrecisionIsKnownLoss(t *testing.T) {
	body := `{"model":"vendor/alibaba/glm-5.2","seed":1234567890123456789}`
	got := rewriteModel([]byte(body), "glm-5.2")

	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	seed, _ := obj["seed"].(float64)
	if int64(seed) == 1234567890123456789 {
		t.Skip("当前实现保住了精度，本用例的已知限制说明需要更新")
	}
	t.Logf("已知限制确认：seed 1234567890123456789 → %v（map 往返丢精度）", seed)
}
