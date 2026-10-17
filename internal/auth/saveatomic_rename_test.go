package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSaveAtomicPropagatesRenameFailure 钉住「凭证落盘失败必须报错」（R10：禁吞业务错误）。
//
// 为什么需要它（2026-10-17，独立复审自选变异 H3 存活）：`SaveAtomic` 的落盘路径是
// 「写 `*.json.tmp` → `os.Rename(tmp, FilePath)`」。原有四个 SaveAtomic 测试
// （RefusesBlankAccessToken / RequiresFilePath / PermissionHintAndTmpTarget /
// ReplacesOldContentAndMode）**全部打在写 tmp 那一侧**，`os.Rename` 的返回值零断言。
// 于是把最后一行改成 `_ = os.Rename(...); return nil` 后，`go test ./internal/auth/` 全绿——
// 意味着「凭证轮换根本没落盘」会被静默当成功，上层的 refresh 流程毫不知情，
// 且磁盘上留一个含**新 token** 的 `*.json.tmp`（0600）残留。
//
// 还原什么会让它 FAIL：把 `return os.Rename(tmp, a.FilePath)` 改成
// `_ = os.Rename(tmp, a.FilePath); return nil`（把错误吞掉）→ 本用例立刻红。
//
// 构造手法：把 FilePath 做成一个**非空目录**——tmp 文件能正常写（同目录下可写），
// 但 rename 到「一个已有非空目录」必然失败（Windows 与 POSIX 一致），且不需要注入依赖。
func TestSaveAtomicPropagatesRenameFailure(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-rename-target.json")
	// 先在目标路径造一个非空目录：rename 到它必定失败（ENOTEMPTY / EEXIST 语义）。
	if err := os.Mkdir(fp, 0o700); err != nil {
		t.Fatalf("前置：建目录失败：%v", err)
	}
	if err := os.WriteFile(filepath.Join(fp, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatalf("前置：写占位文件失败：%v", err)
	}

	a := &Auth{
		AccessToken:  "at-new",
		RefreshToken: "rt-new",
		ExpiresAt:    9999999999,
		UID:          "u1",
		FilePath:     fp,
	}
	if err := a.SaveAtomic(); err == nil {
		t.Fatal("rename 目标不可替换时必须返回错误；返回 nil = 凭证落盘失败被静默当成功（R10）")
	}
}
