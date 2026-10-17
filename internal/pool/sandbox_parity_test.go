package pool

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSandboxFilesInSync 两份测试沙箱文件必须保持同步。
//
// 背景：`internal/pool/main_test.go` 与 `internal/server/main_test.go` 是**手工同步的副本**。
// 为什么不做成公共包：抽出 `internal/testenv` 会新增一层依赖（`check-boundaries.ps1` 的白名单
// 会以"新层未显式声明"拒绝），并让门禁口径「23 包：20 ok + 3 no test files」漂移；而沙箱逻辑
// 每个包只跑一次，代价是几十行。理由写在两个文件顶部的注释里。
//
// 手工同步的代价是「改一份忘了改另一份」——那份包的测试沙箱就悄悄失去隔离能力，而且**不报错**
// （测试照常跑，只是不再隔离环境）。本测试把这条纪律变成机器核对（`docs/PLAN-open-items-2026-10-17.md`
// 的 A5：把「靠人记的 4 行人工 diff」换成断言）。
//
// 已知且不可消除的差异（白名单，改它们要同步改本测试）：
//  1. `package` 行（pool / server）
//  2. 沙箱目录前缀（`yg-pool-test-` / `yg-server-test-`）
//  3. 沙箱日志前缀（`[sandbox] pool:` / `[sandbox] server:`）
//  4. 本包多一个 `TestMain`（server 的 TestMain 住在 `handler_test.go`——它还要关聊天表格日志）
//
// 牙齿：往任一文件里加一行代码（或改一处逻辑）而不同步，本测试必红。
func TestSandboxFilesInSync(t *testing.T) {
	// 注意：本包的 TestMain 会把工作目录 chdir 到沙箱里（隔离生效的证据），所以**不能**用
	// 相对路径读文件——必须用 runtime.Caller 定位本测试文件所在目录。
	dir := sourceDir(t)
	mine := readSandboxSource(t, filepath.Join(dir, "main_test.go"))
	peer := readSandboxSource(t, filepath.Join(dir, "..", "server", "main_test.go"))

	got := dropTestMain(t, normalizeSandboxSource(mine, "pool"))
	want := normalizeSandboxSource(peer, "server")

	if len(got) != len(want) {
		t.Fatalf("两份沙箱文件行数不一致（归一化后 %d vs %d）：改 internal/pool/main_test.go 必须同步改 internal/server/main_test.go（反之亦然）", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("两份沙箱文件在第 %d 行分叉：\n  pool  : %q\n  server: %q\n→ 改了其中一份必须同步改另一份（差异见本测试顶部白名单）",
				i+1, got[i], want[i])
		}
	}
}

func readSandboxSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // 路径由 sourceDir 从测试源文件位置推导，非外部输入
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	return string(b)
}

// sourceDir 返回本测试文件所在目录（= internal/pool）。
// 不用 os.Getwd：TestMain 的沙箱已经 chdir 到临时目录。
func sourceDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败：无法定位测试源文件")
	}
	return filepath.Dir(file)
}

// normalizeSandboxSource 抹平三个白名单差异，并丢掉空行与纯注释行
// （注释行含"改本文件必须同步改 pool 那份"这类互指说明，两侧措辞必然不同且不影响行为）。
func normalizeSandboxSource(src, pkg string) []string {
	raw := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, ln := range raw {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		s := strings.ReplaceAll(ln, "package "+pkg, "package X")
		s = strings.ReplaceAll(s, "yg-"+pkg+"-test-", "yg-X-test-")
		s = strings.ReplaceAll(s, "[sandbox] "+pkg+":", "[sandbox] X:")
		out = append(out, strings.TrimRight(s, " \t"))
	}
	return out
}

// dropTestMain 去掉本包独有的 TestMain 三行（server 的住在 handler_test.go）。
// 找不到就 FATAL——那说明文件结构变了，白名单需要重新对齐，不能静默放过。
func dropTestMain(t *testing.T, lines []string) []string {
	t.Helper()
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "func TestMain(m *testing.M) {" {
			if i+2 >= len(lines) {
				t.Fatalf("TestMain 结构异常（第 %d 行起不足 3 行），请重新对齐本测试的白名单", i+1)
			}
			rest := make([]string, 0, len(lines)-3)
			rest = append(rest, lines[:i]...)
			rest = append(rest, lines[i+3:]...)
			return rest
		}
	}
	t.Fatalf("在 internal/pool/main_test.go 里找不到 TestMain（白名单假设失效）")
	return nil
}
