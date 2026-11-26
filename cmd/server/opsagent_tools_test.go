// opsagent_tools_test.go 只读工具的聚合/裁剪契约。
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yg-gateway/internal/reqlog"
)

func TestOpsRuntimeToolHandlers(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	p, err := newPool(state)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{StateFile: state}
	logsDir := filepath.Join(filepath.Dir(state), "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "20261126.err.log"), []byte("ERROR boom\nok\nWARN 503\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, _ := json.Marshal(map[string]any{"stage": "done", "feature": "f", "git_dirty": false})
	if err := os.WriteFile(filepath.Join(logsDir, "deploy-prod-20261126-164121.json"), report, 0o600); err != nil {
		t.Fatal(err)
	}

	status := statusTool(p)
	if _, err := status.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	unknowns := unknownsTool(p)
	if _, err := unknowns.Execute(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	errorsTool := recentErrorsTool(cfg)
	if _, err := errorsTool.Execute(context.Background(), json.RawMessage(`{"lines":1}`)); err != nil {
		t.Fatal(err)
	}
	deploys := deployReportsTool(cfg)
	if _, err := deploys.Execute(context.Background(), json.RawMessage(`{"limit":1}`)); err != nil {
		t.Fatal(err)
	}

	rl := reqlog.New(reqlog.Config{Dir: logsDir, Enabled: true})
	rl.Record(reqlog.Event{Status: 200, Outcome: "ok", Model: "m", DurationMs: 5})
	stats := requestStatsTool(rl)
	if _, err := stats.Execute(context.Background(), json.RawMessage(`{"window_minutes":60}`)); err != nil {
		t.Fatal(err)
	}
	rl.Close()

	if _, err := buildOpsRuntimeTools(cfg, p, rl); err != nil {
		t.Fatal(err)
	}
}

func TestOpsRuntimeToolHelpers(t *testing.T) {
	if _, err := jsonToolResult(make(chan int)); err == nil {
		t.Fatal("不可序列化结果应报错")
	}
	if err := decodeToolArgs([]byte(`{bad`), &struct{}{}); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if percentile(nil, 0.5) != 0 || percentile([]int64{1}, 2) != 1 || percentile([]int64{1, 2}, 0) != 1 {
		t.Fatal("percentile 边界错误")
	}
	if rawArgsString(nil) != "{}" || rawArgsString(json.RawMessage(`{"x":1}`)) != `{"x":1}` {
		t.Fatal("rawArgsString 错误")
	}
	if truncateString("abc", 2) != "ab…" || truncateString("abc", 0) != "abc" {
		t.Fatal("truncateString 错误")
	}
}

func TestOpsRuntimeToolMissingLogs(t *testing.T) {
	cfg := &Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
	if _, err := recentErrorsTool(cfg).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("无 err.log 应报错")
	}
	if _, err := deployReportsTool(cfg).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("无日志目录应报错")
	}
}

func TestAggregateRequestRows(t *testing.T) {
	rows := []reqlog.Event{
		{Status: 200, Outcome: "ok", AgentKind: "deepseek-harness", Model: "hy4", DurationMs: 100},
		{Status: 200, Outcome: "ok", AgentKind: "deepseek-harness", Model: "hy4", DurationMs: 200},
		{Status: 503, Outcome: "upstream_error", Model: "hy4", DurationMs: 300, GuardrailHit: "blocked"},
		{Status: 413, Outcome: "guardrail", GuardrailHit: "blocked", Model: "glm", DurationMs: 50},
	}
	out := aggregateRequestRows(60, rows)
	if out["total"].(int) != 4 {
		t.Fatalf("total=%v", out["total"])
	}
	if out["status"].(map[string]int)["503"] != 1 || out["guardrail"].(map[string]int)["blocked"] != 2 {
		t.Fatalf("status/guard=%#v / %#v", out["status"], out["guardrail"])
	}
	lat := out["latency_ms"].(map[string]int64)
	if lat["p50"] != 100 || lat["p95"] != 300 || lat["max"] != 300 {
		t.Fatalf("latency=%#v", lat)
	}
	if !strings.Contains(out["coverage"].(string), "不等于没有请求") {
		t.Fatalf("coverage=%q", out["coverage"])
	}
}

func TestTailErrorLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.err.log")
	lines := []string{"start", "ERROR content_blocked 400", "normal", "WARN 413 blocked", "panic: x"}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	file, got, err := tailErrorLines(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(file, "a.err.log") || len(got) != 2 {
		t.Fatalf("file=%s got=%#v", file, got)
	}
	if !strings.Contains(got[0], "WARN") || !strings.Contains(got[1], "panic") {
		t.Fatalf("got=%#v", got)
	}
}

func TestReadDeployReports(t *testing.T) {
	dir := t.TempDir()
	write := func(name, stage string) {
		raw, _ := json.Marshal(map[string]any{"stage": stage, "feature": "f", "git_dirty": false})
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("deploy-prod-20261125-214655.json", "done")
	write("deploy-prod-20261126-164121.json", "done-new")
	write("not-a-report.json", "x")
	reports, err := readDeployReports(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0]["file"] != "deploy-prod-20261126-164121.json" {
		t.Fatalf("reports=%#v", reports)
	}
	if reports[0]["stage"] != "done-new" {
		t.Fatalf("stage=%v", reports[0]["stage"])
	}
}

func TestBuildOpsRuntimeToolsRequiresPool(t *testing.T) {
	if _, err := buildOpsRuntimeTools(&Config{}, nil, nil); err == nil {
		t.Fatal("pool 为空应报错")
	}
}

func TestRequestStatsNilRecorder(t *testing.T) {
	tool := requestStatsTool(nil)
	_, err := tool.Execute(t.Context(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("reqlog 为空应报错")
	}
}
