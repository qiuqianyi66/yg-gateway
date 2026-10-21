package auth

import (
	"fmt"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
)

// lockdownOS 平台注入点（测试替换；默认真实运行时平台）。
// 为什么独立成变量而非直接读 runtime.GOOS：LockdownFile 的「非 Windows 跳过」
// 分支在 Linux CI 上必须可测，注入点让测试能把假平台塞进来。
var lockdownOS = runtime.GOOS

// lockdownRunner 执行外部进程的注入点（测试替换；默认真跑 icacls）。
// 返回合并的 stdout+stderr（icacls 失败信息在 stderr，CombinedOutput 一并带出
// 供 WARN 上下文可读）。
var lockdownRunner = func(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// LockdownFile 收紧凭证文件 ACL：仅服务运行账户 + SYSTEM 可访问（Windows）。
//
// 背景（README「凭证文件权限（Windows 特有坑）」）：Go 的 0o600 不改写 Windows
// 继承 ACL，面板 OAuth 新建的 auths/*.json 默认对 BUILTIN\Users（本机所有登录
// 用户）可读——明文 accessToken/refreshToken 拿到即可冒用账号。本函数与
// lock-auths.ps1 同语义：
//
//	icacls <file> /inheritance:r /grant:r "<me>:(F)" /grant:r "NT AUTHORITY\SYSTEM:(F)"
//
// /inheritance:r 断开父目录继承并移除继承来的 ACE；/grant:r 替换该主体的现有
// 授权（只收窄不赋权）。选 icacls 而非 x/sys/windows ACL API 的理由见
// docs/adr/ADR-012-auth-acl-lockdown.md（与既有脚本同语义 / 失败信息可读 /
// exec 不经 shell 无引号转义问题）。
//
// 非 Windows 返回 nil（跳过是特性不是错误，不 WARN——容器/Linux 部署无此威胁
// 模型，auths/ 权限由挂载目录/umask 负责；每次落盘都打 WARN 只会淹没真告警）。
//
// 失败返回错误（R10：调用方必须记录，不得静默吞）。
func LockdownFile(path string) error {
	if lockdownOS != "windows" {
		return nil
	}
	me, err := user.Current()
	if err != nil {
		return fmt.Errorf("lockdown %s: current user: %w", path, err)
	}
	if strings.TrimSpace(me.Username) == "" {
		return fmt.Errorf("lockdown %s: current user name is empty", path)
	}
	out, err := lockdownRunner("icacls", path,
		"/inheritance:r",
		"/grant:r", me.Username+":(F)",
		"/grant:r", `NT AUTHORITY\SYSTEM:(F)`)
	if err != nil {
		return fmt.Errorf("lockdown %s (icacls): %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}
