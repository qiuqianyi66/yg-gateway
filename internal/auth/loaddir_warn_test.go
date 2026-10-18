package auth

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadDirReportsSkippedFiles 钉住「坏文件被跳过时必须留痕」（ADR-010）。
//
// 为什么需要它：LoadDir 对读不出/解析失败的文件是 `continue`（不因为一个坏文件挡住启动，
// 这是合理的历史行为），但**历史上完全静默**。而它的输出会被 cmd/server 传给
// `pool.SyncToDir` 做**破坏性**对齐（删状态 + 落盘）：一个静默跳过的文件 = 该账号被当成删了，
// 健康度观测永久丢失。所以「跳过」可以，但不能无声。
//
// 还原什么会让它 FAIL：把两处 `log.Printf("WARN: [auth] 跳过...")` 去掉 → 本用例红。
func TestLoadDirReportsSkippedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-bad1.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-bad2.json"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	auths, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("坏文件不该让 LoadDir 返回错误（历史行为：跳过并继续），got %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("两个文件都不可用，应返回 0 个账号，got %d", len(auths))
	}

	out := buf.String()
	if got := strings.Count(out, "跳过无法解析的账号文件"); got != 2 {
		t.Errorf("两个坏文件都应留下 WARN，实际 %d 条；输出=%q", got, out)
	}
	if !strings.Contains(out, "workbuddy-bad1.json") || !strings.Contains(out, "workbuddy-bad2.json") {
		t.Errorf("WARN 必须点名文件路径（否则没法查是哪个文件坏了）；输出=%q", out)
	}
}
