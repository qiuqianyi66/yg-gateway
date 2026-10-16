package main

import (
	"errors"
	"strings"
	"testing"
)

// TestPollFailureMessageDistinguishesRejectionFromWaiting 钉住 P4：
// 4xx 不能一概说成「登录未完成」——真 401/403 时继续等待永远不会成功，
// 且用户看不到上游状态码。
//
// 还原什么会让它 FAIL：把实现改回 `if status == 0 || status >= 500 { ... } else { "登录未完成…" }`
// 的旧分档 → 401/403 两例会拿到「登录未完成」，`strings.Contains(msg, "不会被拒")` 类断言红。
func TestPollFailureMessageDistinguishesRejectionFromWaiting(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		mustContain []string
		mustNot     []string
	}{
		{
			name:        "401 被拒：必须点明「不是还没登录完」",
			status:      401,
			mustContain: []string{"登录被上游拒绝", "401", "继续等待不会成功"},
			mustNot:     []string{"请确认已在浏览器完成登录再按 y"},
		},
		{
			name:        "403 同上",
			status:      403,
			mustContain: []string{"登录被上游拒绝", "403"},
			mustNot:     []string{"请确认已在浏览器完成登录再按 y"},
		},
		{
			name:        "404：提示 realm/域不匹配，而不是「还没登录完」",
			status:      404,
			mustContain: []string{"端点不存在", "404"},
			mustNot:     []string{"请确认已在浏览器完成登录再按 y"},
		},
		{
			name:        "500：上游故障，不带登录语义",
			status:      500,
			mustContain: []string{"token endpoint error"},
			mustNot:     []string{"登录未完成"},
		},
		{
			name:        "0（连不上）：上游故障，不带登录语义",
			status:      0,
			mustContain: []string{"token endpoint error"},
			mustNot:     []string{"登录未完成"},
		},
		{
			name:        "200 + 业务 code 非 0（正常等待态）：保留等待提示，并带出上游 code/msg",
			status:      200,
			mustContain: []string{"登录未完成", "login ing"},
		},
		{
			name:        "400：仍按等待态提示，但带出状态码（能区分「在等」与「别的业务错」）",
			status:      400,
			mustContain: []string{"登录未完成", "400"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := pollFailureMessage(tc.status, errors.New("code=1 msg=login ing"))
			for _, want := range tc.mustContain {
				if !strings.Contains(msg, want) {
					t.Errorf("status=%d 的提示 %q 应包含 %q", tc.status, msg, want)
				}
			}
			for _, bad := range tc.mustNot {
				if strings.Contains(msg, bad) {
					t.Errorf("status=%d 的提示 %q 不应包含 %q（会误导用户继续等待）", tc.status, msg, bad)
				}
			}
		})
	}
}
