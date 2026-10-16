package server

import (
	"strings"
	"testing"
)

// TestAnthropicStreamIntentUnknownIsNotFalse 钉住 P3：`"stream":"true"`（字符串）这类**类型错**
// 必须表达为「未知 + 留痕」，而不是静默当成「非流式」。
//
// 为什么只留痕不改分支：改分支选择是行为变更（可能把非流式请求改派到流式路径），
// 属于单独 PRD 的范围。本测试同时钉住「行为保持」这一点。
//
// 还原什么会让它 FAIL：把实现改回 `_ = json.Unmarshal(body, &peek)` 的旧形态
// （类型错 → note 为空）→ 第 3、4 例红。
func TestAnthropicStreamIntentUnknownIsNotFalse(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStream bool
		wantNote   bool // 是否有诊断说明（= 是否识别为「未知」）
		noteMust   string
	}{
		{
			name:       "stream=true（布尔）→ 流式，无 note",
			body:       `{"model":"m","stream":true,"messages":[]}`,
			wantStream: true,
		},
		{
			name:       "stream=false（布尔）→ 非流式，无 note",
			body:       `{"model":"m","stream":false,"messages":[]}`,
			wantStream: false,
		},
		{
			name:       "stream 缺失 → 默认非流式，无 note（缺省不是异常）",
			body:       `{"model":"m","messages":[]}`,
			wantStream: false,
		},
		{
			name:       "stream=\"true\"（字符串，类型错）→ 未知：行为保持非流式 + 有 note",
			body:       `{"model":"m","stream":"true","messages":[]}`,
			wantStream: false,
			wantNote:   true,
			noteMust:   "类型非法",
		},
		{
			name:       "stream=1（数字，类型错）→ 未知",
			body:       `{"model":"m","stream":1,"messages":[]}`,
			wantStream: false,
			wantNote:   true,
			noteMust:   "类型非法",
		},
		{
			name:       "body 非法 JSON → 未知 + 有 note（后续 transform 会 400）",
			body:       `{"model":`,
			wantStream: false,
			wantNote:   true,
			noteMust:   "不是合法 JSON",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream, note := anthropicStreamIntent([]byte(tc.body))
			if stream != tc.wantStream {
				t.Errorf("stream=%v，want %v（行为必须保持既有分支选择）", stream, tc.wantStream)
			}
			if (note != "") != tc.wantNote {
				t.Fatalf("note=%q，want 有 note=%v（未知必须留痕，不能静默降级）", note, tc.wantNote)
			}
			if tc.noteMust != "" && !strings.Contains(note, tc.noteMust) {
				t.Errorf("note=%q 应包含 %q（要能让运维/客户端知道问题在哪）", note, tc.noteMust)
			}
		})
	}
}
