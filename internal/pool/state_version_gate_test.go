package pool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStateFileVersionGate 钉住 state.json 的版本闸门（ADR-009 同族，B2 实质②的 state 半边）。
//
// 为什么需要它：state.json 存的是账号健康度观测（冷却/熔断/成功率/成本台账）。历史上：
//   - 文件**没有版本字段** → 旧程序读新文件时按零值丢字段，下次落盘覆盖（静默、不可逆）；
//   - load() 对 json.Unmarshal 失败**直接 return** → 损坏文件被当成「没有状态」，观测全丢。
//
// 分档：版本 > 程序支持 → 硬错误（启动方拒绝启动）；缺失(0)/相等 → 正常加载；
// 文件不存在 → 不算错误（首次启动）；文件损坏 → 硬错误。
//
// 还原什么会让它 FAIL：去掉 `if sf.Version > stateFileVersion` → 第一条红；
// 把 unmarshal 失败改回静默 return → 最后一条红。
func TestStateFileVersionGate(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(dir, "state.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("版本比程序新 → 硬错误（回滚到旧版的典型场景）", func(t *testing.T) {
		p := New(write(t, `{"version":999,"accounts":{}}`))
		defer p.Close()
		err := p.LoadErr()
		if err == nil {
			t.Fatal("版本 999 > 程序版本时必须返回错误")
		}
		if !strings.Contains(err.Error(), "999") {
			t.Errorf("错误信息应点名文件版本，got: %v", err)
		}
	})

	t.Run("版本缺失（老文件）→ 正常加载", func(t *testing.T) {
		p := New(write(t, `{"accounts":{}}`))
		defer p.Close()
		if err := p.LoadErr(); err != nil {
			t.Fatalf("无 version 的老文件应可加载，got err=%v", err)
		}
	})

	t.Run("版本等于当前 → 正常加载", func(t *testing.T) {
		p := New(write(t, `{"version":1,"accounts":{}}`))
		defer p.Close()
		if err := p.LoadErr(); err != nil {
			t.Fatalf("当前版本应可加载，got err=%v", err)
		}
	})

	t.Run("文件不存在 → 不算错误（首次启动）", func(t *testing.T) {
		p := New(filepath.Join(dir, "nope.json"))
		defer p.Close()
		if err := p.LoadErr(); err != nil {
			t.Fatalf("文件不存在不该报错，got err=%v", err)
		}
	})

	t.Run("文件损坏 → 硬错误（不再静默当成没有状态）", func(t *testing.T) {
		p := New(write(t, `{not json`))
		defer p.Close()
		if p.LoadErr() == nil {
			t.Fatal("损坏文件必须返回错误；静默忽略等于丢掉全部健康度观测")
		}
	})
}
