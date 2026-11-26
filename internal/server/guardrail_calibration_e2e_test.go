package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/reqlog"
)

// TestGuardrailCalibrationEndToEnd 端到端补证：**真实 chat 请求（假上游）→ 归档行
// 同时落 est_prompt_tokens 与 prompt_tokens → /status 出 est_vs_upstream**。
//
// 为什么需要（2026-11-26 第五轮）：护栏的校准闭环此前是空转——`run.guardrailEst`
// 只在 warn 命中时置位（warn 阈值 500k vs 真实估算中位 ~6 万），325 次真实请求
// 一条样本都没落下，`/status` 恒为 `{}`。修复把它改成「所有放行请求都置位」。
// 只测 `applyGuardrail` 置位还不够：中间还有 chatrun → chatStat → reqlog.Event →
// 落盘，以及 handler 的 RecordCalibration 调用点。这条测试把两端连起来。
//
// 还原什么会让它 FAIL：
//   - 把 applyGuardrail 里 `run.st.estPrompt = est` 删掉 → 归档行缺 est_prompt_tokens；
//   - 把 RecordCalibration 的调用条件改回 `run.guardrailEst > 0 &&` 之外再加 warn 判断
//     → /status 无 est_vs_upstream；
//   - 把 status.go 的省略条件改回「仅命中时透出」→ /status 无 guardrail_stats。
func TestGuardrailCalibrationEndToEnd(t *testing.T) {
	// 上游恒走 SSE（本仓上游协议使然）；usage 里给 prompt_tokens=1000 作为「上游实际」。
	const sseUsage = `data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":1000}}` + "\n\n" +
		"data: [DONE]\n\n"

	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Dir: dir, Enabled: true})
	t.Cleanup(rl.Close)

	g := NewGuardrail()
	g.SetContextWarnTokens(500000) // 本请求远低于阈值 → 纯放行（正是此前拿不到样本的路径）
	g.SetContextMaxTokens(1000000)

	h := NewHandler(Config{
		Pool:       testPoolWith(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}),
		Upstream:   newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseUsage, true }),
		RequestLog: rl,
		Guardrail:  g,
	})

	// 400 个 CJK 字符：估算 ≈ 400 + 1（role 信封），远低于 warn。
	body := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("一", 400) + `"}],"stream":false}`
	if rec := doChat(t, h, "", body); rec.Code != 200 {
		t.Fatalf("status=%d want 200，body=%s", rec.Code, rec.Body.String())
	}
	rl.Close() // 刷盘

	// ① 归档行：估算与上游实际成对出现。
	line := readOnlyArchiveLine(t, dir)
	var ev struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		EstPromptTokens  int64 `json:"est_prompt_tokens"`
		EstShadowTokens  int64 `json:"est_shadow_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("归档行非 JSON：%v；行=%s", err, line)
	}
	if ev.EstPromptTokens <= 0 {
		t.Fatalf("归档行缺 est_prompt_tokens（放行路径没置位）；行=%s", line)
	}
	if ev.PromptTokens != 1000 {
		t.Fatalf("prompt_tokens = %d，want 1000（上游 usage 未入账）；行=%s", ev.PromptTokens, line)
	}
	// 影子口径必须同行落档（不参与护栏，但不能因为「不影响判定」就悄悄不写）：
	// 400 个 CJK ≈ 1200 字节 → 1200/3.4 ≈ 353，与主口径 400 同量级但不等。
	if ev.EstShadowTokens <= 0 {
		t.Fatalf("归档行缺 est_shadow_tokens（影子口径没落档）；行=%s", line)
	}

	// ② /status：有样本、零命中，仍须透出 est_vs_upstream。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	var body2 map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body2); err != nil {
		t.Fatalf("status 非 JSON：%v", err)
	}
	gs, ok := body2["guardrail_stats"].(map[string]any)
	if !ok {
		t.Fatalf("有样本时 guardrail_stats 不应省略：%s", rec.Body.String())
	}
	if _, ok := gs["warn_hits"]; ok {
		t.Fatalf("纯放行不应出现 warn_hits：%v", gs)
	}
	ratio, ok := gs["est_vs_upstream"].(map[string]any)
	if !ok {
		t.Fatalf("est_vs_upstream 缺失：%v", gs)
	}
	if ratio["n"].(float64) != 1 {
		t.Fatalf("est_vs_upstream.n = %v，want 1", ratio["n"])
	}
	// 中位比率必须等于归档行里那一对值算出的比率（同一个估算、同一个上游值）。
	want := round2(float64(ev.EstPromptTokens) / float64(ev.PromptTokens))
	if got := ratio["median"].(float64); got != want {
		t.Fatalf("median = %.2f，want %.2f（est=%d / upstream=%d）", got, want, ev.EstPromptTokens, ev.PromptTokens)
	}
}

// round2 与 guardrail.go 的 ratioSummary 同口径（2 位四舍五入），避免测试自己漂。
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// readOnlyArchiveLine 读归档目录里唯一的 requests-*.jsonl，返回唯一一行。
func readOnlyArchiveLine(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "requests-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("归档文件应恰好 1 个，got %v (err=%v)", matches, err)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("读归档失败：%v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("应恰好 1 行事件，got %d 行：%s", len(lines), raw)
	}
	return lines[0]
}
