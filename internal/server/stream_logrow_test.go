package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/reqlog"
)

// TestStreamFailureStatusReachesLogRow 钉住「流式失败在**日志行**里记 502」（A6 S1/S4）。
//
// 为什么需要它：`chatrun` 在空流/超大帧分支写 `st.status = 502`，而这个值**不进 reqlog**
// （reqlog 的 Status 取 handler 层 HTTP 码，流式响应头早已发出 → 恒 200）。它唯一的出口是
// `logChatRowEx` 的 status 字段。所以变异 S1/S4（把 502 改成 200）此前无人咬住：
// 唯一能看见它的地方是 stderr 日志行，而没有任何测试读过那一行。
//
// 这条测试同时是一份**口径文档**：流式失败想被「看见」，只能靠日志行——
// 想让面板/归档也看到 502，必须把 st.status 带进 Event（会改归档与面板统计口径，另议）。
//
// 还原什么会让它 FAIL：把空流或超大帧分支的 `http.StatusBadGateway` 改成 `http.StatusOK`
// → 对应用例找不到 502 而红。
func TestStreamFailureStatusReachesLogRow(t *testing.T) {
	oldEnabled := chatLogEnabled
	chatLogEnabled = true
	defer func() { chatLogEnabled = oldEnabled }()

	oldOut := chatLogOut
	defer func() { chatLogOut = oldOut }()

	const frameLimit = 1 << 20
	cases := []struct {
		name    string
		body    string
		want502 bool
	}{
		{"空流 → 日志行记 502", "data: [DONE]\n\n", true},
		{"超大帧 → 日志行记 502", "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" +
			strings.Repeat("a", frameLimit+4096) + "\"}}]}\n\n", true},
		{"正常流 → 日志行不记 502", sseOK, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			chatLogOut = &buf

			up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, tc.body, true })
			h := NewHandler(Config{
				Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}),
				Upstream:   up,
				RequestLog: reqlog.New(reqlog.Config{}),
			})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/chat/completions",
				strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`))
			h.ServeHTTP(rec, req)

			out := buf.String()
			has502 := strings.Contains(out, "502")
			if has502 != tc.want502 {
				t.Fatalf("日志行 502=%v，期望 %v；日志输出=%q", has502, tc.want502, out)
			}
			_ = http.StatusBadGateway // 语义提示：这里关心的正是它有没有出现在日志行里
		})
	}
}
