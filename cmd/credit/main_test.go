package main

import (
	"os"
	"path/filepath"
	"testing"
)

// D-04：损坏/不可读的 auth 文件必须被识别出来（broken=true），由 main 循环补一条
// Error 行——静默 continue 会把坏文件从输出里整体抹掉，总行数与目录文件数对不上。
func TestLoadAuthFileBroken(t *testing.T) {
	dir := t.TempDir()

	// 1) JSON 语法损坏。
	bad := filepath.Join(dir, "workbuddy-bad.json")
	if err := os.WriteFile(bad, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, broken := loadAuthFile(bad); !broken {
		t.Error("JSON 损坏文件必须报 broken")
	}

	// 2) 文件根本不存在（os.ReadFile 失败分支）。
	missing := filepath.Join(dir, "workbuddy-missing.json")
	if _, broken := loadAuthFile(missing); !broken {
		t.Error("缺失文件必须报 broken")
	}

	// 3) 正常文件解析出来（反向确认：没把好文件误判成坏）。
	good := filepath.Join(dir, "workbuddy-good.json")
	if err := os.WriteFile(good, []byte(`{"account":{"uid":"u-1","nickname":"n"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	af, broken := loadAuthFile(good)
	if broken {
		t.Error("正常文件不得报 broken")
	}
	if af.Account.UID != "u-1" {
		t.Errorf("UID = %q, want u-1", af.Account.UID)
	}
}
