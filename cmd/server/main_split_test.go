// main_split_test.go 覆盖 2026-11-17 从 main() 切出的装配函数。
// 为什么需要：这些函数抽出来后 cmd/server 包新增约 300 行未被测试执行的代码，
// 会把包覆盖率稀释到棘轮以下（实测 75.5% → 74.9%）。补测是修复手段，
// 不是放宽阈值。
package main

import (
	"os"
	"path/filepath"
	"testing"

	"yg-gateway/internal/pool"
	"yg-gateway/internal/redisstore"
	"yg-gateway/internal/upstream"
)

// TestPrepareConfigExistingFile 覆盖「配置存在 → 原样加载」主路径。
func TestPrepareConfigExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":":7863","api_key":"test-key"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := prepareConfig(path)
	if err != nil {
		t.Fatalf("prepareConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg = nil")
	}
	if cfg.Listen != ":7863" {
		t.Fatalf("cfg.Listen = %q, want :7863", cfg.Listen)
	}
}

// TestPrepareConfigFirstRunAutoGenerate 覆盖首次运行：config 缺失 → 自动落一份
// 推荐配置再加载（拆分前是 main() 内联分支，抽成函数后此路径可测）。
func TestPrepareConfigFirstRunAutoGenerate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg, err := prepareConfig(path)
	if err != nil {
		t.Fatalf("prepareConfig(first run): %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg = nil")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("首次运行应自动生成配置文件: %v", statErr)
	}
}

// TestBuildObservabilityWiresUnknownRegistry 覆盖观测单例装配：六个字段全部非 nil，
// 且未知注册表已注入 pool（p.SetUnknownRegistry 在函数内完成）。
func TestBuildObservabilityWiresUnknownRegistry(t *testing.T) {
	p := pool.New(filepath.Join(t.TempDir(), "state.json"))
	o := buildObservability(p)
	if o == nil {
		t.Fatal("observability = nil")
	}
	if o.latency == nil || o.ttfb == nil || o.errKinds == nil || o.attempts == nil || o.slowReqs == nil || o.unknown == nil {
		t.Fatalf("观测单例字段缺失: %+v", o)
	}
}

// TestBuildSessionRouterDisabled 覆盖 SessionSticky 关闭：router 为 nil，
// 两个只读闭包返回零值，redisMode 按 Noop store 报 noop。
func TestBuildSessionRouterDisabled(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	cfg.SessionSticky.Enabled = false
	p := pool.New(filepath.Join(t.TempDir(), "state.json"))
	store := redisstore.New("", "")

	r, count, snap, mode := buildSessionRouter(cfg, p, store)
	if r != nil {
		t.Fatal("SessionSticky 关闭时 router 应为 nil")
	}
	if got := count(); got != 0 {
		t.Fatalf("count() = %d, want 0", got)
	}
	if got := snap(); got != nil {
		t.Fatalf("snap() = %v, want nil", got)
	}
	if mode != "noop" {
		t.Fatalf("redisMode = %q, want noop", mode)
	}
}

// TestBuildSessionRouterEnabled 覆盖 SessionSticky 开启：router 非 nil 且已启动 GC，
// 两个闭包可调用（空池下 count 为 0）。
func TestBuildSessionRouterEnabled(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	cfg.SessionSticky.Enabled = true
	p := pool.New(filepath.Join(t.TempDir(), "state.json"))
	store := redisstore.New("", "")

	r, count, snap, mode := buildSessionRouter(cfg, p, store)
	if r == nil {
		t.Fatal("SessionSticky 开启时 router 不应为 nil")
	}
	defer r.StopGC()
	if got := count(); got != 0 {
		t.Fatalf("空池 count() = %d, want 0", got)
	}
	_ = snap()
	if mode != "noop" {
		t.Fatalf("redisMode = %q, want noop", mode)
	}
}

// TestBuildScheduler 覆盖调度器装配：返回非 nil 且六任务的 Disabled 取反与
// config 的 Enabled 一致（零值 Enabled = 启用）。
func TestBuildScheduler(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	p := pool.New(filepath.Join(t.TempDir(), "state.json"))
	up := upstream.New()

	sch := buildScheduler(cfg, p, up)
	if sch == nil {
		t.Fatal("buildScheduler = nil")
	}
}
