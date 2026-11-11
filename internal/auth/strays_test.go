package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStrayCredentialFiles 残留判定：以 .json 结尾的纳管文件不算残留，
// workbuddy-*.json.bak-* 这类必须被报出来，目录与非 workbuddy 前缀忽略。
func TestStrayCredentialFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("workbuddy-a.json")              // 纳管
	write("workbuddy-b.json.bak-20261023") // 残留
	write("workbuddy-c.json.tmp")          // 残留
	write("other.json.bak")                // 非 workbuddy 前缀，不在本函数职责内
	if err := os.MkdirAll(filepath.Join(dir, "workbuddy-d"), 0o700); err != nil {
		t.Fatal(err)
	}

	strays, err := StrayCredentialFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(strays) != 2 {
		t.Fatalf("期望 2 个残留，实际 %d: %v", len(strays), strays)
	}
	for _, s := range strays {
		base := filepath.Base(s)
		if base != "workbuddy-b.json.bak-20261023" && base != "workbuddy-c.json.tmp" {
			t.Fatalf("意外的残留判定结果: %s", base)
		}
	}
}

// TestStrayCredentialFilesEmptyDir 空目录（或无 auths 目录）不该报错也不该 TypeError。
func TestStrayCredentialFilesEmptyDir(t *testing.T) {
	strays, err := StrayCredentialFiles(filepath.Join(t.TempDir(), "not-exist"))
	if err != nil {
		t.Fatalf("不存在的目录应返回空集与 nil，got err=%v", err)
	}
	if len(strays) != 0 {
		t.Fatalf("期望空集，实际 %v", strays)
	}
}
