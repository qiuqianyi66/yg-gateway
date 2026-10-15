// chatflow_test.go chatCompletions 的**行为等价快照**。
//
// 目的：为「把 chatCompletions（871 行）拆成阶段函数」提供重构保护。
// 它不测实现细节，只固定「各类请求 → 响应特征」的映射；重构后必须逐位相同。
// 这是本仓既有的等价性守门手法（同 session.TestViewEquivalenceWithBytePaths、
// upstream.TestPrepareBodySingleRoundTripEquivalence）在 handler 层的对应物。
//
// 覆盖设计（务实而非穷举）：只覆盖 chatCompletions 自身会走到的分支。准入限流、
// fallback、受信任代理等已有专门测试（admission_test.go / fallback_test.go /
// trusted_proxy_test.go）—— 它们本身就是重构的保护，这里重复只会拖慢套件而不
// 增加保护面。
package server

import (
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/upstream"
)

// TestChatCompletionsBehaviorSnapshot 重构保护：各路径分支的响应特征快照。
//
// 每个子用例对应 chatCompletions 里的一段：
//
//	1/2  正常成功（非流式聚合 / 流式透传）—— 阶段 ①②③④
//	3    请求体非 JSON —— 阶段 ① 失败路径
//	4    请求体超限 —— 阶段 ① 失败路径
//	5    上游请求级错误原文透传 —— 阶段 ④⑤
//	6    上游 11133 —— 阶段 ④⑤（钉住 2026-10-15 的 ErrBadParams 修复）
//	7    全池无可用号 —— 阶段 ④⑤ 本地调度错误
//
// 任一子用例在重构后失败，就说明该阶段被改动了语义，必须先解释再继续。
func TestChatCompletionsBehaviorSnapshot(t *testing.T) {
	const (
		okBody     = `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":false}`
		streamBody = `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	)
	oneAcct := func() *auth.Auth {
		return &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	}
	okUp := func(t *testing.T) *upstream.Client {
		t.Helper()
		return newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	}

	t.Run("1 正常非流式：200 且聚合为 chat.completion（非 SSE）", func(t *testing.T) {
		h := NewHandler(Config{Pool: testPoolWith(oneAcct()), Upstream: okUp(t)})
		rec := doChat(t, h, "", okBody)
		if rec.Code != 200 {
			t.Fatalf("status=%d want 200, body=%s", rec.Code, rec.Body.String())
		}
		got := rec.Body.String()
		if !strings.Contains(got, `"object":"chat.completion"`) {
			t.Errorf("非流式应聚合为 chat.completion，got=%s", got)
		}
		if strings.Contains(got, "data: ") {
			t.Errorf("非流式不应返回 SSE 帧，got=%s", got)
		}
	})

	t.Run("2 正常流式：200 且透传 SSE 含 [DONE]", func(t *testing.T) {
		h := NewHandler(Config{Pool: testPoolWith(oneAcct()), Upstream: okUp(t)})
		rec := doChat(t, h, "", streamBody)
		if rec.Code != 200 {
			t.Fatalf("status=%d want 200, body=%s", rec.Code, rec.Body.String())
		}
		got := rec.Body.String()
		if !strings.Contains(got, "data: ") {
			t.Errorf("流式应返回 SSE 帧，got=%s", got)
		}
		if !strings.Contains(got, "[DONE]") {
			t.Errorf("流式必须含 [DONE]（恰好一次），got=%s", got)
		}
		if n := strings.Count(got, "[DONE]"); n != 1 {
			t.Errorf("[DONE] 出现 %d 次，want 1", n)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "event-stream") {
			t.Errorf("Content-Type=%q want text/event-stream", ct)
		}
	})

	t.Run("3 请求体非 JSON：宽容放行到上游（既有语义，**不是** 400）", func(t *testing.T) {
		// 这一条是本快照测试第一个纠正假设的地方：写测试时以为"非 JSON 必 400"，
		// 实测是 200 且请求照常发往上游。查代码确认这是**有意设计**——body 解析
		// 失败（ParseRequestView 返回 viewOK=false）时各消费方回落空值语义，请求
		// 继续由上游判定（注释：「与旧实现各自解析失败逐位一致，不引入新的失败
		// 模式」）。
		//
		// 钉住它的价值：重构若把这里改成"解析失败即 400"，客户端畸形 body 的行为
		// 就变了（影响面比想象大）。快照记录现状，变化必须被解释。
		var called bool
		up := newFakeUpstream(t, func(string) (int, string, bool) {
			called = true
			return 200, sseOK, true
		})
		h := NewHandler(Config{Pool: testPoolWith(oneAcct()), Upstream: up})
		rec := doChat(t, h, "", `not a json`)
		if !called {
			t.Errorf("既有语义是宽容放行（请求继续到上游），未打上游说明行为已变；status=%d body=%s",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("4 请求体超限：413", func(t *testing.T) {
		h := NewHandler(Config{
			Pool:         testPoolWith(oneAcct()),
			Upstream:     okUp(t),
			MaxBodyBytes: 32,
		})
		rec := doChat(t, h, "", `{"model":"m","messages":[{"role":"user","content":"`+strings.Repeat("x", 200)+`"}]}`)
		if rec.Code != 413 {
			t.Fatalf("status=%d want 413, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("5 上游请求级错误：原文透传（不规范化成固定文案）", func(t *testing.T) {
		up := newFakeUpstream(t, func(string) (int, string, bool) {
			return 400, `{"code":11101,"msg":"Unmarshal chat params failed with error: unexpected EOF"}`, false
		})
		h := NewHandler(Config{Pool: testPoolWith(oneAcct()), Upstream: up})
		rec := doChat(t, h, "", okBody)
		if rec.Code != 400 {
			t.Fatalf("status=%d want 400, body=%s", rec.Code, rec.Body.String())
		}
		got := rec.Body.String()
		if !strings.Contains(got, "Unmarshal chat params failed") {
			t.Errorf("上游原文应原样透传，got=%s", got)
		}
	})

	t.Run("6 上游 11133：400 bad_params 且不轮转（钉住本轮修复）", func(t *testing.T) {
		var calls int
		up := newFakeUpstream(t, func(string) (int, string, bool) {
			calls++
			return 400, `{"code":11133,"msg":"Invalid request parameters","extError":{"code":"invalid_request_error","message":"the request parameters were rejected by the model provider"}}`, false
		})
		p := testPoolWith(oneAcct())
		h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 3})
		rec := doChat(t, h, "", okBody)
		if rec.Code != 400 {
			t.Fatalf("status=%d want 400（11133 是请求级错误，不该退化成 503），body=%s", rec.Code, rec.Body.String())
		}
		got := rec.Body.String()
		if !strings.Contains(got, "bad_params") {
			t.Errorf("11133 应归 bad_params，got=%s", got)
		}
		if !strings.Contains(got, "11133") {
			t.Errorf("上游原文（含 11133）应透传，got=%s", got)
		}
		// 关键：11133 是请求级错误，轮转无意义（同 body 换号结果不变）。
		// 修成 ErrBadParams 前这里会是 4（首次 + 3 次轮转）并最终返回 503。
		if calls != 1 {
			t.Errorf("上游被调用 %d 次，want 1（11133 不应轮转）", calls)
		}
	})

	t.Run("7 全池无可用号：503 no_healthy_account", func(t *testing.T) {
		up := newFakeUpstream(t, func(string) (int, string, bool) {
			t.Error("池中无号时不应打上游")
			return 200, sseOK, true
		})
		h := NewHandler(Config{Pool: testPoolWith(), Upstream: up}) // 空池
		rec := doChat(t, h, "", okBody)
		if rec.Code != 503 {
			t.Fatalf("status=%d want 503, body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "no_healthy_account") {
			t.Errorf("本地调度错误应报 no_healthy_account，got=%s", rec.Body.String())
		}
	})
}
