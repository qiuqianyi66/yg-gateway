package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveAtomicErrorTruncatesUID A-1（R2）：SaveAtomic 拒绝空 accessToken 时
// 错误串只含 UID 前 8 位——错误会随 error 链被调用方原样打日志。
//
// 牙齿：把 logfmt.UID8 改回 a.UID → err 字符串包含完整 UID → 本测试 FAIL。
func TestSaveAtomicErrorTruncatesUID(t *testing.T) {
	a := &Auth{
		UID:         "uid_abcdef0123456789",
		FilePath:    filepath.Join(t.TempDir(), "workbuddy_test.json"),
		AccessToken: "",
	}
	err := a.SaveAtomic()
	if err == nil {
		t.Fatal("空 accessToken 应拒绝写回")
	}
	if strings.Contains(err.Error(), "abcdef0123456789") {
		t.Fatalf("错误串泄漏完整 UID: %v", err)
	}
	if !strings.Contains(err.Error(), "uid_abc") {
		t.Fatalf("错误串应含 UID8 前缀 uid_abc, got %v", err)
	}
}

// TestLoadDirReportLocksdownSkippedFiles A-2：读不出/解析失败的凭证文件也要过
// ACL 兜底收紧（坏文件仍残留 token 片段，不能留在继承宽 ACL 下）。
//
// 牙齿：去掉 lockdownSkippedFile 调用 → recorded 为空 → 本测试 FAIL。
func TestLoadDirReportLocksdownSkippedFiles(t *testing.T) {
	dir := t.TempDir()
	// 坏文件：不是合法 JSON。
	bad := filepath.Join(dir, "workbuddy_bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 正常文件：保证 skipped 分支之外路径不受影响。
	good := filepath.Join(dir, "workbuddy_good.json")
	if err := os.WriteFile(good, []byte(`{"auth":{"accessToken":"t","uid":"u1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// 替换 lockdownRunner 记录调用（lockdownOS=windows 时 LockdownFile 会真跑 icacls）。
	origRunner := lockdownRunner
	origOS := lockdownOS
	var recorded []string
	lockdownRunner = func(name string, args ...string) ([]byte, error) {
		recorded = append(recorded, args[0])
		return nil, nil
	}
	lockdownOS = "windows"
	defer func() { lockdownRunner = origRunner; lockdownOS = origOS }()

	auths, skipped, err := LoadDirReport(dir)
	if err != nil {
		t.Fatalf("LoadDirReport: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("解析成功文件数=%d, want 1", len(auths))
	}
	if len(skipped) != 1 || skipped[0] != bad {
		t.Fatalf("skipped=%v, want [bad]", skipped)
	}
	// 坏文件必须出现在 lockdown 调用记录里。
	found := false
	for _, p := range recorded {
		if strings.EqualFold(p, bad) {
			found = true
		}
	}
	if !found {
		t.Fatalf("坏文件 %s 未被 ACL 兜底收紧（recorded=%v）", bad, recorded)
	}
}
