package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResponsesStreamFailedEvents 覆盖 /v1/responses 流式路径的上游 error 帧转译。
//
// `failedEvents` 原本 0.0% —— 一个专门处理错误的函数从未被验证过。它的三条契约
// 都写在代码里，但没有一条被测过。
func TestResponsesStreamFailedEvents(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &responsesStreamWriter{inner: rec, st: newRespStreamState()}

	frame := "data: {\"model\":\"glm-5.2\",\"error\":{\"message\":\"quota exceeded\",\"type\":\"rate_limit\"}}\n\n"
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatalf("Write 返回错误: %v", err)
	}

	out := rec.Body.String()

	// 契约 1：错误帧必须转成 response.failed，不能当普通 chunk 丢掉。
	if !strings.Contains(out, `"type":"response.failed"`) {
		t.Errorf("error 帧应转成 response.failed 事件，实际:\n%s", out)
	}
	// 契约 2：上游 error 对象的原文必须带给客户端（唯一排障线索）。
	if !strings.Contains(out, "quota exceeded") {
		t.Errorf("error 对象应原样透传给客户端:\n%s", out)
	}
	// 契约 3：流还没 started 时，model 取自 error 帧本身，否则 response 对象里没有 model。
	if !strings.Contains(out, `"model":"glm-5.2"`) {
		t.Errorf("未 started 时应从 error 帧补 model:\n%s", out)
	}
	if !strings.Contains(out, `"status":"failed"`) {
		t.Errorf("response 对象的 status 必须是 failed:\n%s", out)
	}
}

// TestResponsesStreamFailedSuppressesCompletion 上游先发 error 帧、随后仍补一个
// [DONE]（上游实现不一致时会出现）—— 绝不能因此再发 response.completed。
//
// 否则客户端会先看到 failed 再看到 completed，后者可能覆盖失败状态，
// 把一个已经失败的请求读成成功。`failedEvents` 与 `completionEvents` 都靠
// `st.done` 互斥，这条用例就是把那个互斥钉住。
func TestResponsesStreamFailedSuppressesCompletion(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &responsesStreamWriter{inner: rec, st: newRespStreamState()}

	if _, err := w.Write([]byte("data: {\"error\":{\"message\":\"boom\"}}\n\n")); err != nil {
		t.Fatalf("Write 返回错误: %v", err)
	}
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		t.Fatalf("Write 返回错误: %v", err)
	}

	out := rec.Body.String()

	if !strings.Contains(out, `"type":"response.failed"`) {
		t.Fatalf("应发 response.failed:\n%s", out)
	}
	if strings.Contains(out, `"type":"response.completed"`) {
		t.Errorf("failed 之后不应再发 response.completed（会把失败读成成功）:\n%s", out)
	}
}

// TestResponsesStreamFailedOnlyOnce 重复 error 帧只转译一次，且不会续写
// sequence_number 之外的任何事件。上游重试或网关重放时会出现连续错误帧。
func TestResponsesStreamFailedOnlyOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &responsesStreamWriter{inner: rec, st: newRespStreamState()}

	for i := 0; i < 3; i++ {
		if _, err := w.Write([]byte("data: {\"error\":{\"message\":\"boom\"}}\n\n")); err != nil {
			t.Fatalf("Write 返回错误: %v", err)
		}
	}

	out := rec.Body.String()

	if n := strings.Count(out, `"type":"response.failed"`); n != 1 {
		t.Errorf("response.failed 应只发一次，实际 %d 次:\n%s", n, out)
	}
}
