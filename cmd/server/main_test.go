package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestConfigFileGenuinelyMissing 覆盖 2026-10-23 实弹：prompt.file 等附属文件缺失
// 时 Load 返回 fs.ErrNotExist 包装错误，但 config 文件本身存在——此时绝不能走
// auto-generate 兜底（会把正常配置丢弃成默认值、账号池清空）。只有 config 文件
// 本身不存在才属于首次运行。
func TestConfigFileGenuinelyMissing(t *testing.T) {
	dir := t.TempDir()
	realCfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(realCfg, []byte(`{"listen": ":7863"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	missingCfg := filepath.Join(dir, "nope.json")
	notExistErr := fs.ErrNotExist
	// fs.ErrNotExist 直接作为错误值（errors.Is 恒真），模拟 Load 的 %w 包装路径。
	wrapped := func() error { return errors.Join(notExistErr) }()

	cases := []struct {
		name    string
		path    string
		loadErr error
		want    bool
	}{
		{
			name:    "config 文件存在 + ErrNotExist（prompt.file 缺失误判场景）→ 不兜底",
			path:    realCfg,
			loadErr: wrapped,
			want:    false,
		},
		{
			name:    "config 文件不存在 + ErrNotExist → 首次运行，自动生成",
			path:    missingCfg,
			loadErr: wrapped,
			want:    true,
		},
		{
			name:    "加载错误非 ErrNotExist（如 JSON 语法错）→ 不兜底",
			path:    realCfg,
			loadErr: errors.New("invalid character"),
			want:    false,
		},
		{
			name:    "config 路径是目录 + ErrNotExist → 不兜底（stat 返回的是目录而非缺失）",
			path:    dir,
			loadErr: wrapped,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := configFileGenuinelyMissing(tc.path, tc.loadErr); got != tc.want {
				t.Fatalf("configFileGenuinelyMissing(%q, %v) = %v, want %v",
					tc.path, tc.loadErr, got, tc.want)
			}
		})
	}
}
