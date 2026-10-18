package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLedgerVersionGate 钉住 usage.json 的版本闸门（ADR-009）。
//
// 为什么需要它：7×24 常驻 + 滚动换版/回滚的部署形态下，「旧程序读新文件」是常态而不是意外。
// 旧程序的 `load()` 只看 JSON 结构：新版本加的字段它不认识 → **按零值读进来、下次落盘写回去
// 就永久丢了**（静默、不可逆）。历史上这条路径完全没有防护，也没有测试。
//
// 分档（与 LoadErr 注释一致）：
//   - 文件版本 > 程序版本 → 硬错误（调用方拒绝启动）
//   - 文件版本 < 程序版本 → 正常加载，并留一份 `.pre-upgrade-v<N>.bak`（迁移前备份）
//   - 版本缺失（0，老文件）/ 等于当前 → 正常加载、不备份
//   - 文件损坏 → 硬错误
//
// 还原什么会让它 FAIL：去掉 `if f.Version > fileVersion { return ... }` → 第一条红；
// 去掉备份逻辑 → 第二条红。
func TestLedgerVersionGate(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(dir, "usage.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("文件版本比程序新 → 硬错误（拒绝启动）", func(t *testing.T) {
		p := write(t, `{"version":999,"buckets":[]}`)
		r := New(p)
		err := r.LoadErr()
		if err == nil {
			t.Fatal("版本 999 > 程序版本时必须返回错误（否则旧程序会静默丢字段）")
		}
		if !strings.Contains(err.Error(), "999") {
			t.Errorf("错误信息应点名文件版本，got: %v", err)
		}
	})

	t.Run("文件版本更旧 → 正常加载 + 迁移前备份", func(t *testing.T) {
		p := write(t, `{"version":1,"buckets":[]}`)
		r := New(p)
		if err := r.LoadErr(); err != nil {
			t.Fatalf("旧版本应可加载，got err=%v", err)
		}
		bak := p + ".pre-upgrade-v1.bak"
		raw, err := os.ReadFile(bak)
		if err != nil {
			t.Fatalf("应留下迁移前备份 %s：%v", filepath.Base(bak), err)
		}
		if !strings.Contains(string(raw), `"version":1`) {
			t.Errorf("备份内容应是原文件，got=%s", raw)
		}
	})

	t.Run("版本等于当前 → 正常加载且不备份", func(t *testing.T) {
		p := write(t, `{"version":`+itoa(fileVersion)+`,"buckets":[]}`)
		r := New(p)
		if err := r.LoadErr(); err != nil {
			t.Fatalf("当前版本应可加载，got err=%v", err)
		}
		if _, err := os.Stat(p + ".pre-upgrade-v" + itoa(fileVersion) + ".bak"); err == nil {
			t.Error("版本相同不该产生备份")
		}
	})

	t.Run("版本缺失（0，老文件）→ 正常加载", func(t *testing.T) {
		p := write(t, `{"buckets":[]}`)
		if err := New(p).LoadErr(); err != nil {
			t.Fatalf("缺 version 的老文件应可加载，got err=%v", err)
		}
	})

	t.Run("文件损坏 → 硬错误（不再静默从零开始）", func(t *testing.T) {
		p := write(t, `{not json`)
		if err := New(p).LoadErr(); err == nil {
			t.Fatal("损坏文件必须返回错误；静默从零开始等于丢光台账")
		}
	})
}

// itoa 避免为一行数字再引 strconv（本文件只需要它）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
