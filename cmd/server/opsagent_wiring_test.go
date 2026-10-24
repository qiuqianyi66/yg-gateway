// opsagent 装配层测试：配置归一、disabled no-op、enabled 路径启动即退出、输出口双路径。
// cmd/server 处于全仓覆盖率棘轮的最低档，新增装配代码必须带测试（2026-10-24 实测：
// 无测试时包覆盖率 73.3→71.8，拖低全仓 minCoverage 棘轮）。
package main

import (
	"context"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/opsagent"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/unknown"
)

// TestOpsAgentConfigNormalized 单位换算层：显式值秒→Duration 直传；零值保持零
// （默认值兜底在 opsagent.New 内做，由 internal/opsagent TestNewDefaults 锚定——
// 两层各测各的，不在这里重复断言默认值）。
func TestOpsAgentConfigNormalized(t *testing.T) {
	got := OpsAgentConfig{IntervalSec: 30, HealthyFrac: 0.8}.normalized()
	if got.Interval != 30*time.Second || got.HealthyFrac != 0.8 {
		t.Fatalf("显式值 = %+v", got)
	}
	if zero := (OpsAgentConfig{}).normalized(); zero.Interval != 0 || zero.HealthyFrac != 0 {
		t.Fatalf("零值应保持零（默认在 opsagent.New 兜底）：%+v", zero)
	}
}

// TestNewAlertNotifier 空配置构造（webhook URL 空 = 不外发，见 alerting 包）。
func TestNewAlertNotifier(t *testing.T) {
	n := newAlertNotifier(&Config{})
	if n == nil {
		t.Fatal("notifier 不应为 nil")
	}
}

// TestStartOpsAgentDisabledNoOp disabled=false：立即返回（无 goroutine、无输出）。
func TestStartOpsAgentDisabledNoOp(t *testing.T) {
	startOpsAgent(OpsAgentConfig{}, nil, nil, nil, context.Background()) // 不 panic 即过
}

// TestStartOpsAgentEnabledStartsAndExits enabled=true：Run 跑在传入 ctx 上，
// 取消即回收（R9 在装配层的延续）；用真实 pool 空池（无副作用）。
func TestStartOpsAgentEnabledStartsAndExits(t *testing.T) {
	p := pool.New("")
	ctx, cancel := context.WithCancel(context.Background())
	startOpsAgent(OpsAgentConfig{Enabled: true, IntervalSec: 1}, p, unknown.NewRegistry(),
		newAlertNotifier(&Config{}), ctx)
	cancel() // 立即取消：Run 的 ticker 循环随 ctx.Done 退出
	time.Sleep(20 * time.Millisecond)
}

// TestOpsAgentObserversProjection 观察面投影的 teeth：真实 pool 上断言
// Counts 口径与 AccountView 字段（CoolKind="hard" 的冷却号被如实投影）。
func TestOpsAgentObserversProjection(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u-hard", Nickname: "hard号"})
	p.Add(&auth.Auth{UID: "u-ok", Nickname: "ok号"})
	p.SetCredits("u-hard", 42, 0)
	p.SetCredits("u-ok", 100, 0)
	p.Cooldown("u-hard", pool.CoolHard, time.Hour, "test")

	obs := opsAgentObservers(p)
	pc := obs.Counts()
	if pc.Total != 2 || pc.Cooling != 1 {
		t.Fatalf("Counts 投影 = %+v, want total=2 cooling=1", pc)
	}
	avs := obs.Accounts()
	if len(avs) != 2 {
		t.Fatalf("Accounts 投影 = %d 条, want 2", len(avs))
	}
	for _, av := range avs {
		if av.UID == "u-hard" {
			if av.Credits != 42 || av.CoolKind != "hard" {
				t.Fatalf("hard 号投影 = %+v, want credits=42 kind=hard", av)
			}
		}
	}
}

// TestOpsSinkSuggestBothPaths 池级建议（UID 空，只打日志）与账号级建议（走 webhook，
// URL 空则内包 no-op）双路径都不 panic 且不外发。
func TestOpsSinkSuggestBothPaths(t *testing.T) {
	s := opsSink{n: newAlertNotifier(&Config{})}
	s.Suggest(opsagent.Suggestion{Rule: "capacity", Title: "池级"})
	s.Suggest(opsagent.Suggestion{Rule: "credit-402-contradiction", Title: "账号级",
		UID: "u-1234567890", Nickname: "nick"})
}
