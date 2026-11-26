// opsagent_tools.go 运维 agent 的只读工具（MVP 全部 ReadOnly=true）。
//
// 工具是装配层闭包：runtime 不 import pool/reqlog，边界仍向内。每个工具只做
// 聚合/裁剪，不返回凭证、不返回完整 UID、不写任何状态。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yg-gateway/internal/opsagent"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
)

func buildOpsRuntimeTools(cfg *Config, p *pool.Pool, rl *reqlog.Recorder) (*opsagent.RuntimeToolRegistry, error) {
	if p == nil {
		return nil, fmt.Errorf("opsagent tools: pool 为空")
	}
	return opsagent.NewRuntimeToolRegistry(
		statusTool(p),
		requestStatsTool(rl),
		recentErrorsTool(cfg),
		deployReportsTool(cfg),
		unknownsTool(p),
	)
}

func statusTool(p *pool.Pool) opsagent.RuntimeToolSpec {
	return opsagent.RuntimeToolSpec{
		Name: "gateway_status", ReadOnly: true,
		Description: "返回账号池计数、按 realm 的健康分布、未消解 unknown 数量。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			total, healthy, cooling, disabled, paused, inFlight := p.CountsDetailedWithPaused()
			realms := map[string]map[string]int{}
			for _, st := range p.List() {
				realm := st.Realm
				if realm == "" {
					realm = "unknown"
				}
				if realms[realm] == nil {
					realms[realm] = map[string]int{}
				}
				realms[realm]["total"]++
				if st.Disabled {
					realms[realm]["disabled"]++
				} else if st.Paused {
					realms[realm]["paused"]++
				} else if st.Cooling {
					realms[realm]["cooling"]++
				} else {
					realms[realm]["healthy"]++
				}
			}
			return jsonToolResult(map[string]any{
				"counts": map[string]int{
					"total": total, "healthy": healthy, "cooling": cooling,
					"disabled": disabled, "paused": paused, "in_flight_full": inFlight,
				},
				"realms":   realms,
				"unknowns": len(p.AllOpenUnknowns()),
			})
		},
	}
}

func requestStatsTool(rl *reqlog.Recorder) opsagent.RuntimeToolSpec {
	return opsagent.RuntimeToolSpec{
		Name: "request_stats", ReadOnly: true,
		Description: "按最近窗口聚合请求状态码、outcome、护栏命中、agent_kind、模型与延迟分位。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"window_minutes":{"type":"integer","minimum":1,"maximum":1440}}}`),
		Execute: func(_ context.Context, raw json.RawMessage) (string, error) {
			if rl == nil {
				return "", fmt.Errorf("request_stats: reqlog 未启用")
			}
			var args struct {
				WindowMinutes int `json:"window_minutes"`
			}
			// 忽略：参数解析错误按零值处理，窗口回落默认 60 分钟，不影响只读聚合。
			_ = decodeToolArgs(raw, &args)
			if args.WindowMinutes <= 0 {
				args.WindowMinutes = 60
			}
			if args.WindowMinutes > 1440 {
				args.WindowMinutes = 1440
			}
			from := time.Now().Add(-time.Duration(args.WindowMinutes) * time.Minute)
			rows, err := rl.ReadArchive(2000, reqlog.Filter{From: from})
			if err != nil {
				return "", fmt.Errorf("request_stats: %w", err)
			}
			return jsonToolResult(aggregateRequestRows(args.WindowMinutes, rows))
		},
	}
}

func aggregateRequestRows(window int, rows []reqlog.Event) map[string]any {
	out := map[string]any{
		"window_minutes": window, "total": len(rows),
		"coverage": "仅归档中该窗口的行；0 行 = 未覆盖，不等于没有请求",
	}
	status := map[string]int{}
	outcome := map[string]int{}
	guard := map[string]int{}
	kinds := map[string]int{}
	models := map[string]int{}
	durations := make([]int64, 0, len(rows))
	var promptTokens, completionTokens int64
	for _, e := range rows {
		status[fmt.Sprintf("%d", e.Status)]++
		outcome[e.Outcome]++
		if e.GuardrailHit != "" {
			guard[e.GuardrailHit]++
		}
		if e.AgentKind != "" {
			kinds[e.AgentKind]++
		}
		if e.Model != "" {
			models[e.Model]++
		}
		durations = append(durations, e.DurationMs)
		promptTokens += e.PromptTokens
		completionTokens += e.CompletionTokens
	}
	out["status"] = status
	out["outcome"] = outcome
	out["guardrail"] = guard
	out["agent_kind"] = kinds
	out["models"] = topCounts(models, 5)
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		out["latency_ms"] = map[string]int64{
			"p50": percentile(durations, 0.50), "p95": percentile(durations, 0.95),
			"max": durations[len(durations)-1],
		}
	}
	out["tokens"] = map[string]int64{"prompt": promptTokens, "completion": completionTokens}
	return out
}

func recentErrorsTool(cfg *Config) opsagent.RuntimeToolSpec {
	return opsagent.RuntimeToolSpec{
		Name: "recent_errors", ReadOnly: true,
		Description: "读取最新一份 stderr 日志，返回最后 N 条 error/warn/panic/413/503 行。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"lines":{"type":"integer","minimum":1,"maximum":200}}}`),
		Execute: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Lines int `json:"lines"`
			}
			// 忽略：参数解析错误按零值处理，行数回落默认 60，不影响只读日志读取。
			_ = decodeToolArgs(raw, &args)
			if args.Lines <= 0 {
				args.Lines = 60
			}
			if args.Lines > 200 {
				args.Lines = 200
			}
			file, lines, err := tailErrorLines(stateSibling(cfg.StateFile, "logs"), args.Lines)
			if err != nil {
				return "", err
			}
			return jsonToolResult(map[string]any{
				"file": file, "matched": len(lines), "lines": lines,
				"coverage": "只读最新 err.log 的最后 5000 行；空 = 该文件未匹配，不是全仓无错误",
			})
		},
	}
}

func deployReportsTool(cfg *Config) opsagent.RuntimeToolSpec {
	return opsagent.RuntimeToolSpec{
		Name: "deploy_reports", ReadOnly: true,
		Description: "读取最近 N 份 deploy-prod JSON 报告，返回 stage/health/smoke/hash。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":20}}}`),
		Execute: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args struct {
				Limit int `json:"limit"`
			}
			// 忽略：参数解析错误按零值处理，份数回落默认 5，不影响只读报告读取。
			_ = decodeToolArgs(raw, &args)
			if args.Limit <= 0 {
				args.Limit = 5
			}
			if args.Limit > 20 {
				args.Limit = 20
			}
			reports, err := readDeployReports(stateSibling(cfg.StateFile, "logs"), args.Limit)
			if err != nil {
				return "", err
			}
			return jsonToolResult(map[string]any{"reports": reports, "count": len(reports)})
		},
	}
}

func unknownsTool(p *pool.Pool) opsagent.RuntimeToolSpec {
	return opsagent.RuntimeToolSpec{
		Name: "unknowns", ReadOnly: true,
		Description: "返回最多 20 条未消解 unknown（question/requires/revision）。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(context.Context, json.RawMessage) (string, error) {
			items := p.AllOpenUnknowns()
			if len(items) > 20 {
				items = items[:20]
			}
			return jsonToolResult(map[string]any{"count": len(p.AllOpenUnknowns()), "items": items})
		},
	}
}

func jsonToolResult(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeToolArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func topCounts(m map[string]int, n int) []map[string]any {
	type kv struct {
		K string
		V int
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].V == items[j].V {
			return items[i].K < items[j].K
		}
		return items[i].V > items[j].V
	})
	if len(items) > n {
		items = items[:n]
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"name": it.K, "count": it.V})
	}
	return out
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func tailErrorLines(dir string, limit int) (string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, fmt.Errorf("recent_errors: read log dir: %w", err)
	}
	var latest string
	var latestMod time.Time
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".err.log") {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		if latest == "" || info.ModTime().After(latestMod) {
			latest, latestMod = filepath.Join(dir, ent.Name()), info.ModTime()
		}
	}
	if latest == "" {
		return "", nil, fmt.Errorf("recent_errors: %s 下没有 .err.log", dir)
	}
	raw, err := os.ReadFile(latest)
	if err != nil {
		return latest, nil, fmt.Errorf("recent_errors: read %s: %w", latest, err)
	}
	all := strings.Split(string(raw), "\n")
	if len(all) > 5000 {
		all = all[len(all)-5000:]
	}
	var out []string
	for _, line := range all {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "warn") ||
			strings.Contains(lower, "panic") || strings.Contains(lower, "413") ||
			strings.Contains(lower, "503") || strings.Contains(lower, "content_blocked") {
			out = append(out, line)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return latest, out, nil
}

func readDeployReports(dir string, limit int) ([]map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("deploy_reports: read dir: %w", err)
	}
	var names []string
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasPrefix(ent.Name(), "deploy-prod-") && strings.HasSuffix(ent.Name(), ".json") {
			names = append(names, ent.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > limit {
		names = names[:limit]
	}
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var report map[string]any
		if json.Unmarshal(raw, &report) != nil {
			continue
		}
		out = append(out, map[string]any{
			"file": name, "stage": report["stage"], "feature": report["feature"],
			"health": report["health"], "smoke": report["smoke"],
			"exe_hash_before": report["exe_hash_before"], "exe_hash_after": report["exe_hash_after"],
			"git_head": report["git_head"], "git_dirty": report["git_dirty"],
		})
	}
	return out, nil
}
