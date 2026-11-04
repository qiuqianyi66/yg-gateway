package upstream

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
)

// TestChatStreamAllPaths404NoPanic 钉住潜伏 panic（P0-3 e2e 踩实后登记为观察未修）：
// 上游对**全部**候选路径回 404/405 时，chatStream 此前循环耗尽撞
// panic("unreachable: chatPaths is never empty")——chatPaths 恒非空只保证至少一条
// 路径，不保证路径不全 404/405。修复后应正常返回 ≥400 错误，不 panic。
//
// 牙齿：把 404/405 分支的 `&& i < len(paths)-1` 去掉（无条件 continue），测试 panic。
func TestChatStreamAllPaths404NoPanic(t *testing.T) {
	calls := 0
	client := &Client{
		HTTP: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: 404,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"code":404,"msg":"not found"}`)),
			}, nil
		})},
		ChatBaseCN:    "https://chat.example",
		GlobalEnabled: true,
	}

	// global：两条候选路径（/console/chat/completions → /v2/chat/completions），
	// 全部 404 → 请求 2 次、正常返回 404 错误（此前 panic）。
	global := &auth.Auth{AccessToken: "at", Domain: "www.workbuddy.ai"}
	global.BackfillRealm()
	_, status, _, err := client.ChatStream(global, []byte(`{"model":"glm-5.2","messages":[]}`), "", ChatMeta{})
	if err == nil {
		t.Fatalf("全部路径 404 应返回错误，got nil（status=%d）", status)
	}
	if status != 404 {
		t.Fatalf("status = %d，want 404", status)
	}
	if calls != 2 {
		t.Fatalf("global 两路径应各请求一次，got %d 次", calls)
	}

	// cn：单路径 404 → 请求 1 次、正常返回（此前同样撞 panic）。
	calls = 0
	cn := &auth.Auth{AccessToken: "at", Domain: "copilot.tencent.com"}
	cn.BackfillRealm()
	_, status, _, err = client.ChatStream(cn, []byte(`{"model":"glm-5.2","messages":[]}`), "", ChatMeta{})
	if err == nil {
		t.Fatalf("cn 单路径 404 应返回错误，got nil（status=%d）", status)
	}
	if status != 404 {
		t.Fatalf("status = %d，want 404", status)
	}
	if calls != 1 {
		t.Fatalf("cn 单路径应只请求一次，got %d 次", calls)
	}
}
