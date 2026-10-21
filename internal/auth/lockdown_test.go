package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withLockdownHarness 替换 lockdownOS/lockdownRunner 为假实现并捕获调用；
// Cleanup 恢复全局（本文件所有测试**不得** t.Parallel——全局注入点与包内
// 其他测试共享，Go 保证非 Parallel 测试先于 Parallel 测试串行执行，替换期间
// 无并发触碰者）。
func withLockdownHarness(t *testing.T, osName string, runErr error) *[][]string {
	t.Helper()
	oldOS := lockdownOS
	oldRunner := lockdownRunner
	var calls [][]string
	lockdownOS = osName
	lockdownRunner = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if runErr != nil {
			return []byte("simulated icacls failure"), runErr
		}
		return nil, nil
	}
	t.Cleanup(func() {
		lockdownOS = oldOS
		lockdownRunner = oldRunner
	})
	return &calls
}

// TestLockdownFile_NonWindows_Noop 非 Windows 必须跳过：不调 icacls、无错误
// （跳过是特性不是错误，Linux 每次落盘不该 WARN）。
func TestLockdownFile_NonWindows_Noop(t *testing.T) {
	calls := withLockdownHarness(t, "linux", nil)
	if err := LockdownFile(`C:\auths\x.json`); err != nil {
		t.Fatalf("non-windows LockdownFile = %v, want nil", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("non-windows must not invoke icacls, got %d calls", len(*calls))
	}
}

// TestLockdownFile_Windows_Args Windows 必须按 lock-auths.ps1 同语义调用：
// icacls <file> /inheritance:r /grant:r "<me>:(F)" /grant:r "NT AUTHORITY\SYSTEM:(F)"。
func TestLockdownFile_Windows_Args(t *testing.T) {
	calls := withLockdownHarness(t, "windows", nil)
	path := `C:\auths\wb.json`
	if err := LockdownFile(path); err != nil {
		t.Fatalf("LockdownFile = %v, want nil", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 icacls call, got %d", len(*calls))
	}
	got := (*calls)[0]
	want := []string{
		"icacls", path,
		"/inheritance:r",
		"/grant:r", "<me>:(F)",
		"/grant:r", "NT AUTHORITY\\SYSTEM:(F)",
	}
	if len(got) != len(want) {
		t.Fatalf("args len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if i == 4 {
			// 当前用户来自真实 os/user.Current()，具体值随平台变；断言形状
			// 「非空 + :(F) 结尾」即可（icacls 需要的就是 SID 反查名）。
			if got[i] == "" || !strings.HasSuffix(got[i], ":(F)") {
				t.Errorf("arg[4] = %q, want non-empty ending in :(F)", got[i])
			}
			continue
		}
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestLockdownFile_Windows_Error icacls 失败必须返回错误（R10：调用方不得静默吞）。
func TestLockdownFile_Windows_Error(t *testing.T) {
	calls := withLockdownHarness(t, "windows", errors.New("boom"))
	path := `C:\auths\wb.json`
	err := LockdownFile(path)
	if err == nil {
		t.Fatal("LockdownFile = nil, want error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q should mention path %q", err, path)
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 icacls call, got %d", len(*calls))
	}
}

// TestSaveAtomic_LockdownInvoked SaveAtomic 写回成功后必须触发一次收紧，
// 目标文件 = FilePath（F-101 验收：新建/刷新凭证后 ACL 自动收紧）。
func TestSaveAtomic_LockdownInvoked(t *testing.T) {
	calls := withLockdownHarness(t, "windows", nil)
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-save.json")
	a := &Auth{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    1,
		Domain:       "www.workbuddy.ai",
		realm:        "global",
		UID:          "u1",
		FilePath:     fp,
	}
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic = %v, want nil", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("SaveAtomic must lock down exactly once, got %d calls", len(*calls))
	}
	if got := (*calls)[0][1]; got != fp {
		t.Errorf("lockdown target = %q, want %q", got, fp)
	}
}

// TestLoadDirReport_LockdownAll 启动兜底：每个解析成功的存量文件都必须被收紧
// 至少一次（含未触发 backfill 的文件——兜底的契约就是「历史文件全覆盖」）。
func TestLoadDirReport_LockdownAll(t *testing.T) {
	calls := withLockdownHarness(t, "windows", nil)
	dir := t.TempDir()
	fixtures := map[string]string{
		"workbuddy-c1.json": `{"auth":{"accessToken":"at","refreshToken":"r","expiresAt":1,"domain":""},"account":{"uid":"c1"}}`,
		"workbuddy-c2.json": `{"auth":{"accessToken":"at","refreshToken":"r","expiresAt":1,"domain":"www.workbuddy.ai","realm":"cn"},"account":{"uid":"c2"}}`,
	}
	for name, body := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	auths, skipped, err := LoadDirReport(dir)
	if err != nil {
		t.Fatalf("LoadDirReport = %v", err)
	}
	if len(auths) != 2 || len(skipped) != 0 {
		t.Fatalf("want 2 auths / 0 skipped, got %d / %d", len(auths), len(skipped))
	}
	for _, f := range []string{filepath.Join(dir, "workbuddy-c1.json"), filepath.Join(dir, "workbuddy-c2.json")} {
		found := false
		for _, call := range *calls {
			if call[1] == f {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("startup lockdown missing file %s (calls=%v)", f, *calls)
		}
	}
}
