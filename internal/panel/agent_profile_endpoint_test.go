package panel

// agent_profile_endpoint_test.go —— /panel/api/agent_profile 端点。
//
// 路由注册待外部 trends 轮合并后补（panel.go routes() 被外部未提交改动占用），
// 本测试**直接调 handler**（不经 mux），钉死数据契约与四态语义：
//   - 有信号 → present:true + by_agent / top_sessions 与 reqlog.Profile 对齐；
//   - unknown 桶与 other 桶分开（禁合并）；
//   - 零条 / 全无信号 → present:false；
//   - 未接线 RequestLog → 501（与 request_metrics / trends 同口径）。
//
// 期望值全部手写字面量，不复用被测函数输出。

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
)

func apCall(t *testing.T, p *Panel) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/panel/api/agent_profile", nil)
	p.agentProfile(rec, req)
	return rec
}

// apBool JSON 布尔提取（JSON 解出的是 bool）。
func apBool(t *testing.T, v any) bool {
	t.Helper()
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("值不是 bool：%v (%T)", v, v)
	}
	return b
}

// TestPanelAgentProfileSignal 有信号：unknown/other 分桶 + top_sessions 排序。
func TestPanelAgentProfileSignal(t *testing.T) {
	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 4 << 20, RetentionDays: 7})
	t.Cleanup(rl.Close)
	now := time.Now()
	// claude_code 两条会话各 2/1 turns；openai_sdk 一条；UA 空一条（unknown 桶）。
	rl.Record(reqlog.Event{Time: now, Account: "u1", Model: "m", Kind: reqlog.KindChat, OK: true,
		SessionFP: "fpAAA", AgentKind: "claude_code", PromptTokens: 100, CompletionTokens: 50})
	rl.Record(reqlog.Event{Time: now.Add(time.Minute), Account: "u1", Model: "m", Kind: reqlog.KindChat, OK: true,
		SessionFP: "fpAAA", AgentKind: "claude_code", PromptTokens: 200, CompletionTokens: 80})
	rl.Record(reqlog.Event{Time: now.Add(2 * time.Minute), Account: "u2", Model: "m", Kind: reqlog.KindChat, OK: true,
		SessionFP: "fpBBB", AgentKind: "openai_sdk", PromptTokens: 30, CompletionTokens: 10})
	rl.Record(reqlog.Event{Time: now.Add(3 * time.Minute), Account: "u3", Model: "m", Kind: reqlog.KindChat, OK: true,
		PromptTokens: 7}) // 无 fp / 无 kind → unknown 桶散客
	rl.Close()

	p := chPanel(t, Config{Pool: pool.New(""), RequestLog: rl})
	out := chWant(t, apCall(t, p), http.StatusOK)
	if !apBool(t, out["present"]) {
		t.Fatalf("present = false, want true")
	}
	prof, ok := out["profile"].(map[string]any)
	if !ok {
		t.Fatalf("profile 缺失/非对象：%#v", out["profile"])
	}
	byAgent, ok := prof["by_agent"].(map[string]any)
	if !ok {
		t.Fatalf("by_agent 缺失：%#v", prof)
	}
	cc, ok := byAgent["claude_code"].(map[string]any)
	if !ok {
		t.Fatalf("缺 claude_code 桶：%#v", byAgent)
	}
	if chNum(t, cc["requests"]) != 2 || chNum(t, cc["sessions"]) != 1 ||
		chNum(t, cc["prompt_tokens"]) != 300 || chNum(t, cc["completion_tokens"]) != 130 {
		t.Fatalf("claude_code 桶 = %+v", cc)
	}
	un, ok := byAgent["unknown"].(map[string]any)
	if !ok {
		t.Fatalf("缺 unknown 桶（UA 未采集）——必须与 other 分开：%#v", byAgent)
	}
	if chNum(t, un["requests"]) != 1 {
		t.Fatalf("unknown 桶 = %+v", un)
	}
	if _, hasOther := byAgent["other"]; hasOther {
		t.Fatalf("unknown UA 不得并入 other 桶：%#v", byAgent)
	}
	top := chRows(t, prof["top_sessions"])
	if len(top) != 2 {
		t.Fatalf("top_sessions = %d, want 2（无 fp 散客不计入榜）", len(top))
	}
	if chStr(t, top[0]["fp"]) != "fpAAA" || chNum(t, top[0]["turns"]) != 2 {
		t.Fatalf("top[0] = %+v, want fpAAA turns=2（turns 降序）", top[0])
	}
	if chStr(t, top[1]["fp"]) != "fpBBB" {
		t.Fatalf("top[1] = %+v", top[1])
	}
}

// TestPanelAgentProfileNoSignal 有归档但全无信号 → present:false（不输出空榜）。
func TestPanelAgentProfileNoSignal(t *testing.T) {
	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 4 << 20, RetentionDays: 7})
	t.Cleanup(rl.Close)
	rl.Record(reqlog.Event{Time: time.Now(), Account: "u1", Model: "m", OK: true}) // V3-10 之前的历史行
	rl.Close()

	p := chPanel(t, Config{Pool: pool.New(""), RequestLog: rl})
	out := chWant(t, apCall(t, p), http.StatusOK)
	if apBool(t, out["present"]) {
		t.Fatalf("present = true, want false（全无信号应整块省略）")
	}
	if _, hasProfile := out["profile"]; hasProfile {
		t.Fatalf("present:false 时不应带 profile 块：%#v", out)
	}
}

// TestPanelAgentProfileNoRequestLog 未接线 RequestLog → 501（与 request_metrics 同口径）。
func TestPanelAgentProfileNoRequestLog(t *testing.T) {
	p := chPanel(t, Config{Pool: pool.New("")})
	out := chWant(t, apCall(t, p), http.StatusNotImplemented)
	if got := chErrMsg(t, out); got != "request logger not available" {
		t.Fatalf("err = %q", got)
	}
}

// TestPanelAgentProfileRouteWired 经完整路由（mux）可达：防止未来改动把路由
// 注册弄丢（handler 直达测试测不到接线）。
func TestPanelAgentProfileRouteWired(t *testing.T) {
	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 4 << 20, RetentionDays: 7})
	t.Cleanup(rl.Close)
	rl.Record(reqlog.Event{Time: time.Now(), Account: "u1", Model: "m", Kind: reqlog.KindChat, OK: true,
		SessionFP: "fpX", AgentKind: "browser", PromptTokens: 1})
	rl.Close()

	p := chPanel(t, Config{Pool: pool.New(""), RequestLog: rl})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/agent_profile", ""), http.StatusOK)
	if !apBool(t, out["present"]) {
		t.Fatalf("present = false, want true（路由已接线且数据可见）")
	}
}
