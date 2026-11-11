package auth

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// StrayCredentialFiles 返回 auths/ 目录里「疑似凭证但不在纳管范围」的文件路径。
//
// 为什么需要（2026-11-11 实读发现）：LoadDirReport 的 glob 是 workbuddy*.json，
// 只认以 .json 结尾的文件。运维手工留的 workbuddy-<uid>.json.bak-20261023 这类
// 残留**既不参与加载、也不在 ACL 兜底范围内**——它可能仍含明文 accessToken，
// 却会被整盘备份/云同步原样带走。这类文件不能静默存在（R10）。
//
// 判定：同目录下匹配 workbuddy*、非目录、且不以 .json 结尾的文件。
// 只报告不删除——它可能是运维有意保留的旧版凭证，删掉是破坏性动作。
func StrayCredentialFiles(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "workbuddy*"))
	if err != nil {
		return nil, err
	}
	var strays []string
	for _, m := range matches {
		if strings.HasSuffix(m, ".json") {
			continue // 纳管范围，交由 LoadDirReport 处理
		}
		info, statErr := os.Lstat(m)
		if statErr != nil || info.IsDir() {
			continue
		}
		strays = append(strays, m)
	}
	return strays, nil
}

// WarnStrayCredentialFiles 报告并对残留文件做幂等 ACL 收紧（不删除）。
//
// 收紧的理由与 lockdownSkippedFile 同：残留文件一旦含 token，宽继承 ACL 就等于
// 把凭证交给本机所有登录用户。LockdownFile 用 icacls /grant:r，重复执行无害。
func WarnStrayCredentialFiles(dir string) {
	strays, err := StrayCredentialFiles(dir)
	if err != nil {
		log.Printf("WARN: [auth] 扫描 %s 的凭证残留失败: %v", dir, err)
		return
	}
	for _, f := range strays {
		log.Printf("WARN: [auth] 疑似凭证残留 %s：不参与加载、不受备份过滤，请移出 auths/ 或删除", f)
		if err := LockdownFile(f); err != nil {
			log.Printf("WARN: [auth] 残留文件 ACL 收紧失败 %s: %v", f, err)
		}
	}
}
