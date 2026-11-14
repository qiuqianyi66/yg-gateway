package server

import (
	"strings"
	"testing"
	"time"
)

// 牙齿测试：客户端在 model / 昵称里塞换行，表格流水行必须仍是一行。
//
// 攻击面：POST /v1/chat/completions 的 model 是客户端完全可控的 JSON 字符串，
// `"model": "a\n2026-11-14 INFO: 账号已禁用"` 经 json.Unmarshal 会解出真实换行，
// 直接写进 stdout 就能伪造出第二行审计记录。昵称同理（来自 auths/<uid>.json）。
//
// 还原修复（去掉 logChatRowEx 里 model / acct 的 logfmt.OneLine）→ 本测试 FAIL
// （输出 >1 行）。
func TestChatRowModelCannotForgeSecondLine(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRow(0, time.Second,
			"evil\n2026-11-14 INFO: 账号已禁用",
			"sync", "uid12345678", "nick\r\n伪造行", 200, 10)
	})
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("表格行应是 1 行（仅结尾换行），实际 %d 行:\n%s", n, out)
	}
	// 只压平不删除：内容仍在同一行上（便于 grep 追查原始 payload）。
	// 注意 model 会被 Truncate 截到 chatModelWidth(26)，故只断言未截断的前缀部分。
	if !strings.Contains(out, "evil 2026-11-14 INFO:") {
		t.Fatalf("压平后原文应保留在同一行，实际输出:\n%s", out)
	}
}

// 扩展字段（rid / clientIP / UA）同样不能换行——它们分别来自请求头与代理头。
func TestChatRowExtraFieldsCannotForgeSecondLine(t *testing.T) {
	withChatLog(t)
	out := captureStdout(t, func() {
		logChatRowEx(0, time.Second, "m", "sync", "uid12345678", "n", 200, 10,
			"rid\nfake", "success", 1, 0, false, "1.2.3.4\nfake", "curl/8\nfake")
	})
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("扩展字段行应是 1 行，实际 %d 行:\n%s", n, out)
	}
}
