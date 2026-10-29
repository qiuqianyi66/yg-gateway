package panel

// bench_endpoint_test.go —— /panel/api/benchmark 网关级 benchmark 端点。
//
// 覆盖：真实 reqlog 归档（临时目录）+ 真实 pool（注入账号）→ 经鉴权路由
// 断言三指标 JSON 结构 / 四态字段（kind/samples）与 unknown 语义。期望值
// 全部手写字面量，不复用被测函数输出（内部计算逻辑由 internal/bench 单测
// 覆盖，此处只验证端点装配与字段透出）。

import (
	"net/http"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
)

// TestPanelBenchmarkEndpoints 全链路：归档事件 + 账号现态 → 三指标透出。
func TestPanelBenchmarkEndpoints(t *testing.T) {
	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 4 << 20, RetentionDays: 7})
	t.Cleanup(rl.Close)

	// 近窗口内 6 条事件：model m1 5 条（4 成 1 败）、m2 1 条（1 成）。
	now := time.Now()
	for i := 0; i < 4; i++ {
		rl.Record(reqlog.Event{Time: now.Add(-time.Duration(i+1) * time.Hour), Account: "u1", Model: "m1", OK: true})
	}
	rl.Record(reqlog.Event{Time: now.Add(-30 * time.Minute), Account: "u1", Model: "m1", OK: false})
	rl.Record(reqlog.Event{Time: now, Account: "u1", Model: "m2", OK: true})
	rl.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "u1", Nickname: "甲"})
	pl.SetCredits("u1", 500, 100) // 100 将到期 → 选中偏置可算

	p := chPanel(t, Config{Pool: pl, RequestLog: rl})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/benchmark", ""), http.StatusOK)

	if chNum(t, out["events"]) != 6 {
		t.Fatalf("events = %v, want 6", out["events"])
	}
	// M1 选号偏置：事件账号 u1 在快照中，samples=1，kind=derivation。
	pb, ok := out["pick_expiring_bias"].(map[string]any)
	if !ok {
		t.Fatalf("pick_expiring_bias 缺失或非对象：%v", out["pick_expiring_bias"])
	}
	if chStr(t, pb["kind"]) != "derivation" {
		t.Fatalf("pick_bias kind = %v, want derivation", pb["kind"])
	}
	if chNum(t, pb["samples"]) != 1 {
		t.Fatalf("pick_bias samples = %v, want 1", pb["samples"])
	}
	// M2 健康：7 天成功率 5/6=0.83；健康占比 1/1=1。
	h, ok := out["health"].(map[string]any)
	if !ok {
		t.Fatalf("health 缺失或非对象")
	}
	sr, ok := h["success_rate_7d"].(map[string]any)
	if !ok {
		t.Fatalf("success_rate_7d 缺失")
	}
	if chNum(t, sr["value"]) != 0.83 {
		t.Fatalf("success7d = %v, want 0.83", sr["value"])
	}
	if chNum(t, sr["samples"]) != 6 {
		t.Fatalf("success7d samples = %v, want 6", sr["samples"])
	}
	hr, ok := h["healthy_ratio"].(map[string]any)
	if !ok {
		t.Fatalf("healthy_ratio 缺失")
	}
	if chNum(t, hr["value"]) != 1 {
		t.Fatalf("healthy = %v, want 1", hr["value"])
	}
	// M3 路由：m1 5 样本 0.8（derivation）；m2 1 样本 unknown。
	rt, ok := out["route_success"].(map[string]any)
	if !ok {
		t.Fatalf("route_success 缺失")
	}
	m1, ok := rt["m1"].(map[string]any)
	if !ok {
		t.Fatalf("route m1 缺失")
	}
	if chNum(t, m1["value"]) != 0.8 || chNum(t, m1["samples"]) != 5 {
		t.Fatalf("route m1 = %+v, want 0.8/5", m1)
	}
	m2, ok := rt["m2"].(map[string]any)
	if !ok {
		t.Fatalf("route m2 缺失")
	}
	if chStr(t, m2["kind"]) != "unknown" {
		t.Fatalf("route m2 kind = %v, want unknown（1 样本 <5）", m2["kind"])
	}
}

// TestPanelBenchmarkNoRequestLog benchmark 在未接线归档时回 501（与 trends 同口径）。
func TestPanelBenchmarkNoRequestLog(t *testing.T) {
	p := chPanel(t, Config{Pool: pool.New("")})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/benchmark", ""), http.StatusNotImplemented)
	if got := chErrMsg(t, out); got != "request logger not available" {
		t.Fatalf("err = %q", got)
	}
}
