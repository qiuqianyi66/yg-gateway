package main

import (
	"os"
	"path/filepath"
	"testing"
)

// loadAccountsAndPool 是启动期装配的**唯一**入口（main() 只负责把错误变成 log.Fatalf）。
// 三条语义在此钉住：
//  1. 账号扫描不完整 → 把 skipped 交给调用方（对齐会退化为「只增不删」，ADR-010 §2.2）；
//  2. state.json 不可用 → 返回错误（拒绝启动，ADR-009）；
//  3. 正常目录 → 无错误。
func TestLoadAccountsAndPool(t *testing.T) {
	t.Run("账号扫描不完整 → 报告 skipped 且不报错", func(t *testing.T) {
		dir := t.TempDir()
		authDir := filepath.Join(dir, "auths")
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(authDir, "workbuddy-bad.json"), []byte("{broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{AuthDir: authDir, StateFile: filepath.Join(dir, "state.json")}

		auths, skipped, p, err := loadAccountsAndPool(cfg)
		if err != nil {
			t.Fatalf("坏账号文件不应让启动失败（语义是跳过 + 只增不删），got %v", err)
		}
		if len(skipped) != 1 {
			t.Errorf("应报告 1 个被跳过的文件，got %d: %v", len(skipped), skipped)
		}
		if len(auths) != 0 {
			t.Errorf("应读出 0 个账号，got %d", len(auths))
		}
		if p == nil {
			t.Fatal("池对象必须返回（即使没有账号）")
		}
		p.Close()
	})

	t.Run("state.json 损坏 → 返回错误（拒绝启动）", func(t *testing.T) {
		dir := t.TempDir()
		authDir := filepath.Join(dir, "auths")
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			t.Fatal(err)
		}
		stateFile := filepath.Join(dir, "state.json")
		if err := os.WriteFile(stateFile, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{AuthDir: authDir, StateFile: stateFile}

		_, _, p, err := loadAccountsAndPool(cfg)
		if err == nil {
			t.Fatal("损坏的 state.json 必须让启动失败")
		}
		if p == nil {
			t.Error("错误路径也应返回池对象（便于上层记录）")
		}
		if p != nil {
			p.Close()
		}
	})

	t.Run("干净目录 → 无错误无 skipped", func(t *testing.T) {
		dir := t.TempDir()
		authDir := filepath.Join(dir, "auths")
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{AuthDir: authDir, StateFile: filepath.Join(dir, "state.json")}

		_, skipped, p, err := loadAccountsAndPool(cfg)
		if err != nil {
			t.Fatalf("干净目录不该报错，got %v", err)
		}
		if len(skipped) != 0 {
			t.Errorf("不该有 skipped，got %v", skipped)
		}
		p.Close()
	})
}
