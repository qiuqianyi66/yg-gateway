// main_test.go — 每包一个测试沙箱（T5）：把测试进程的「环境」与真实机器隔离，
// 并加一条 R1 守卫（测试不得写真实 auths/ 目录）。
//
// 为什么需要：测试失败常常不在断言里，而在环境里——HOME / 工作目录 / 临时目录指向
// 哪，决定了代码往哪写文件、读到谁的旧缓存。把它们固定成本次测试独占的
// $env:TEMP 子目录后，结果只取决于代码，不再取决于机器状态；退出时整体删除。
//
// 为什么不抽 internal/testenv（也照抄 magpie 的 testenv.Main 结构）：
//  1) 零新依赖是硬约束，而新增包会让 `go list ./...` 多出一层 →
//     scripts/check-boundaries.ps1 的白名单会以「新层未显式声明」拒绝（该脚本不在
//     本任务范围，不许改）；同时门禁口径「23 包：20 ok + 3 no test files」会漂移。
//  2) 沙箱逻辑只在 TestMain 里跑一次，代价是每包几十行；引入一个包则要改门禁脚本
//     与棘轮基线——取舍明显。
// internal/pool/main_test.go 与本文件**逐字相同**（除包名与沙箱名前缀），但少一个
// TestMain：本包的 TestMain 住在 handler_test.go（它还要关掉聊天表格日志），
// 那里调用的就是本文件的 runTestSandbox。改本文件时必须同步改 pool 那份。

package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	// sandboxMarkerEnv 沙箱标记：被测代码/子进程可用它自证「我在测试沙箱里」。
	sandboxMarkerEnv = "YG_TEST_SANDBOX_DIR"
	// escapeProbeEnv 负向探针开关（默认关闭）：置 1 时故意往真实 auths/ 写一个文件，
	// 用于证明 R1 守卫**有牙齿**。可复现命令：
	//   $env:YG_TEST_AUTHS_ESCAPE_PROBE='1'; go test ./internal/server/ -run TestEwmaLatency
	// 预期：退出码 1 + 「R1 守卫命中」；未置该变量时同一命令退出码 0。
	escapeProbeEnv = "YG_TEST_AUTHS_ESCAPE_PROBE"
)

// runTestSandbox 把本进程的 HOME / USERPROFILE / TMPDIR|TMP|TEMP / 工作目录指向本次
// 测试独占的沙箱，跑测试，校验不变量后清理，返回退出码。
// 任一不变量被破坏即返回非 0——守卫宁可误报，不可静默放过。
func runTestSandbox(m *testing.M) int {
	// 守卫基准必须在改环境**之前**取：对比的基准是「进程启动时的真实机器状态」。
	guard, err := newAuthsGuard()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox] 无法建立 auths/ 基准：%v\n", err)
		return 1
	}
	sandbox, err := os.MkdirTemp("", "yg-server-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox] 创建沙箱失败：%v\n", err)
		return 1
	}
	home := filepath.Join(sandbox, "home")
	tmp := filepath.Join(sandbox, "tmp")
	work := filepath.Join(sandbox, "work")
	for _, d := range []string{home, tmp, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "[sandbox] 创建沙箱子目录失败：%v\n", err)
			removeSandbox(sandbox)
			return 1
		}
	}
	origWd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox] 取工作目录失败：%v\n", err)
		removeSandbox(sandbox)
		return 1
	}
	// HOME/USERPROFILE（家目录）、TMPDIR/TMP/TEMP（os.TempDir）、工作目录 → 沙箱：
	// 任何「往家目录/临时目录/相对路径写文件」的代码都落在沙箱里，不碰真实机器。
	// 连工作目录一起改的理由：相对路径写文件会静默污染仓库根（本仓出现过门禁脚本在
	// 仓库根盖出字面名文件的事故）。本包测试已确认无相对路径依赖（grep os.Chdir /
	// "../" / testdata 均无命中）与相对 testdata 读取；将来若需要，改用绝对路径。
	for k, v := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"TMPDIR": tmp, "TMP": tmp, "TEMP": tmp,
		sandboxMarkerEnv: sandbox,
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintf(os.Stderr, "[sandbox] 设置 %s 失败：%v\n", k, err)
			removeSandbox(sandbox)
			return 1
		}
	}
	if err := os.Chdir(work); err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox] 切换工作目录失败：%v\n", err)
		removeSandbox(sandbox)
		return 1
	}
	// 入口自检（逃出即报错）：环境刚改完，cwd/HOME/TEMP 必须已在沙箱内。
	// 这条同时是「沙箱真的生效了」的机器证据，不靠人读代码相信。
	if bad := outsideSandbox(sandbox, work); len(bad) != 0 {
		fmt.Fprintf(os.Stderr, "[sandbox] 环境隔离未生效：%v\n", bad)
		restoreWd(origWd)
		removeSandbox(sandbox)
		return 1
	}
	fmt.Fprintf(os.Stderr, "[sandbox] server: 沙箱=%s（HOME/cwd/TEMP 已隔离，auths/ 守卫已装载）\n", sandbox)

	code := m.Run()

	// 出口自检必须在「回原工作目录」之前：cwd/HOME/TEMP 三项都应仍在沙箱内
	//（测试若 chdir 到沙箱外/覆盖 HOME，这里报错——「逃出即报错」）。
	if bad := outsideSandbox(sandbox, ""); len(bad) != 0 {
		fmt.Fprintf(os.Stderr, "[sandbox] 测试改动了环境并逃出沙箱：%v\n", bad)
		code = 1
	}
	// 出沙箱：先回原工作目录（Windows 上删除「自己所在目录」会失败），再核对守卫。
	restoreWd(origWd)
	probePath, probeCreatedDir := "", false
	if os.Getenv(escapeProbeEnv) == "1" {
		// 负向探针：故意越界写一次，下面守卫必须命中（详见 escapeProbeEnv 注释）。
		probePath, probeCreatedDir, err = writeEscapeProbe(guard.dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[sandbox] 负向探针写入失败：%v\n", err)
			code = 1
		}
	}
	if diff, err := guard.changed(); err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox] auths/ 指纹比对失败：%v\n", err)
		code = 1
	} else if diff != "" {
		fmt.Fprintf(os.Stderr, "[sandbox] ❌ R1 守卫命中：%s\n", diff)
		fmt.Fprintf(os.Stderr, "[sandbox] 测试不得写真实凭证目录（R1）：把写入目标改到沙箱（%s）或 t.TempDir()。\n", sandbox)
		code = 1
		if os.Getenv(escapeProbeEnv) == "1" {
			fmt.Fprintf(os.Stderr, "[sandbox] （本次为负向探针的**预期**命中：守卫有牙齿的证据）\n")
		}
	} else if os.Getenv(escapeProbeEnv) == "1" {
		fmt.Fprintf(os.Stderr, "[sandbox] ❌ 负向探针未被守卫捕获：守卫失效，必须修守卫本身\n")
		code = 1
	}
	if probePath != "" {
		// 探针残留必须清干净（真实 auths/ 里不留任何东西），清理后复检守卫基准。
		if err := os.Remove(probePath); err != nil {
			fmt.Fprintf(os.Stderr, "[sandbox] 探针文件清理失败：%v（请手工删除 %s）\n", err, probePath)
			code = 1
		}
		if probeCreatedDir {
			if err := os.Remove(guard.dir); err != nil {
				fmt.Fprintf(os.Stderr, "[sandbox] 探针新建的目录清理失败：%v（请手工删除 %s）\n", err, guard.dir)
				code = 1
			}
		}
		if diff, err := guard.changed(); err != nil || diff != "" {
			fmt.Fprintf(os.Stderr, "[sandbox] 探针清理后 auths/ 仍未回到初始状态：%s (%v)\n", diff, err)
			code = 1
		}
	}
	removeSandbox(sandbox)
	return code
}

// outsideSandbox 返回「不在沙箱内」的环境项描述（空 = 全部在内）。
// wd 为空时用当前工作目录（出口校验用）。
func outsideSandbox(sandbox, wd string) []string {
	if wd == "" {
		cur, err := os.Getwd()
		if err != nil {
			return []string{fmt.Sprintf("取工作目录失败：%v", err)}
		}
		wd = cur
	}
	var bad []string
	check := func(name, p string) {
		if p == "" {
			bad = append(bad, name+" 为空")
			return
		}
		rel, err := filepath.Rel(sandbox, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			bad = append(bad, fmt.Sprintf("%s=%s 不在沙箱 %s 内", name, p, sandbox))
		}
	}
	check("cwd", wd)
	check("HOME", os.Getenv("HOME"))
	check("TEMP", os.Getenv("TEMP"))
	return bad
}

// authsGuard 记录真实 auths/（仓库根下，R1 的凭证目录）在进程启动时的指纹。
type authsGuard struct {
	dir     string
	existed bool
	fp      []string
}

// newAuthsGuard 取基准。仓库根 = 包目录的上一级的上一级（go test 的 cwd 恒为包目录），
// 因此必须在 os.Chdir 之前调用。
func newAuthsGuard() (*authsGuard, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(filepath.Dir(filepath.Dir(wd)), "auths")
	fp, existed, err := fingerprintDir(dir)
	if err != nil {
		return nil, err
	}
	return &authsGuard{dir: dir, existed: existed, fp: fp}, nil
}

// changed 返回人类可读的差异（空 = 未变化）。
// 「目录从无到有」「从有到无」同样算变化——否则新建 auths/ 的越界写会漏网。
func (g *authsGuard) changed() (string, error) {
	fp, existed, err := fingerprintDir(g.dir)
	if err != nil {
		return "", err
	}
	switch {
	case !existed && !g.existed:
		return "", nil
	case !existed:
		return fmt.Sprintf("目录 %s 在本次运行中被删除", g.dir), nil
	case !g.existed:
		return fmt.Sprintf("目录 %s 在本次运行中被新建（此前不存在）", g.dir), nil
	case strings.Join(fp, "\n") == strings.Join(g.fp, "\n"):
		return "", nil
	}
	before := map[string]bool{}
	for _, x := range g.fp {
		before[x] = true
	}
	after := map[string]bool{}
	for _, x := range fp {
		after[x] = true
	}
	var added, gone []string
	for _, x := range fp {
		if !before[x] {
			added = append(added, x)
		}
	}
	for _, x := range g.fp {
		if !after[x] {
			gone = append(gone, x)
		}
	}
	return fmt.Sprintf("目录 %s 发生变化：新增/改动 %s；消失/改动 %s",
		g.dir, head(added, 5), head(gone, 5)), nil
}

// fingerprintDir 取目录指纹：每项的「相对路径 | 大小 | mtime 纳秒」，排序后成切片。
// existed=false 表示目录不存在（不视为错误：本仓 auths/ 常不存在，因为它只在
// 生产实例目录里，仓库根版本被 .gitignore 排除）。
func fingerprintDir(dir string) ([]string, bool, error) {
	st, statErr := os.Stat(dir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, false, nil
		}
		return nil, false, statErr
	}
	if !st.IsDir() {
		return []string{"<存在同名非目录项>"}, true, nil
	}
	var fp []string
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		fp = append(fp, fmt.Sprintf("%s|%d|%d", rel, info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if walkErr != nil {
		return nil, true, walkErr
	}
	sort.Strings(fp)
	return fp, true, nil
}

// writeEscapeProbe 往真实 auths/ 写一个探针文件（仅负向验证 R1 守卫用）。
// 返回探针路径与「目录是本探针新建的」标记（清理时要一并删掉，保证零残留）。
func writeEscapeProbe(dir string) (string, bool, error) {
	_, statErr := os.Stat(dir)
	if statErr != nil && !os.IsNotExist(statErr) {
		return "", false, statErr
	}
	createdDir := os.IsNotExist(statErr)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", createdDir, err
	}
	p := filepath.Join(dir, "sandbox-escape-probe.tmp")
	if err := os.WriteFile(p, []byte("R1 守卫负向探针（应被 TestMain 检出并删除）\n"), 0o600); err != nil {
		return p, createdDir, err
	}
	return p, createdDir, nil
}

// head 截断展示，避免守卫报告被大目录刷屏。
func head(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(xs[:n:n], fmt.Sprintf("…（共 %d 项）", len(xs)))
}

// restoreWd 回原工作目录；失败只报告（后续 RemoveAll 可能因此失败，但不掩盖真正的问题）。
func restoreWd(origWd string) {
	if err := os.Chdir(origWd); err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox] 恢复工作目录失败：%v\n", err)
	}
}

// removeSandbox 删除沙箱（含 t.TempDir() 产物）。失败即报告：残留目录会被下一次
// 运行当成「新的独立沙箱」，不会污染结论，但会慢慢堆在 %TEMP% 里，需要可见。
func removeSandbox(sandbox string) {
	if err := os.RemoveAll(sandbox); err != nil {
		fmt.Fprintf(os.Stderr, "[sandbox] 沙箱清理失败：%v（残留 %s）\n", err, sandbox)
	}
}
