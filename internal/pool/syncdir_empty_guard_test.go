package pool

import (
	"testing"

	"yg-gateway/internal/auth"
)

// TestSyncToDirRefusesEmptyScan 钉住对齐的「空集下限」（ADR-010）。
//
// 为什么需要它：SyncToDir 是**破坏性**操作（删账号状态 + 立即落盘 state.json，不可逆），
// 而它的输入来自 auth.LoadDir —— 后者对读不出/解析失败的文件**静默跳过**，返回短列表 + nil error。
// 于是「目录挂错 / 权限不足 / 文件全被改名 / 全部损坏」都会被读成「所有账号都删了」，
// 启动时一次性永久丢掉全部健康度观测（冷却、熔断、成功率、成本台账）。
//
// 语义：扫描结果为空且池内非空 → 拒绝剔除并 WARN（需真清空请走面板 Remove 或停服手改 state.json）；
// 非空扫描 → 照常对齐（该删的删）。
//
// 还原什么会让它 FAIL：去掉 `if len(auths) == 0 && len(p.byUID) > 0 { ... return }` → 第一条红。
func TestSyncToDirRefusesEmptyScan(t *testing.T) {
	mk := func(uid string) *auth.Auth {
		return &auth.Auth{UID: uid, AccessToken: "at-" + uid, ExpiresAt: 9999999999}
	}

	t.Run("空扫描结果不得清空池", func(t *testing.T) {
		p := New("")
		defer p.Close()
		p.Add(mk("u1"))
		p.Add(mk("u2"))

		p.SyncToDir(nil)

		if _, ok := p.Status("u1"); !ok {
			t.Fatal("空扫描结果把 u1 删了——这是「目录读不到」最可能的形态，不该等同于「账号被删」")
		}
		if _, ok := p.Status("u2"); !ok {
			t.Fatal("空扫描结果把 u2 删了")
		}
		if total, _, _, _, _ := p.CountsDetailed(); total != 2 {
			t.Errorf("池内账号数应保持 2，got %d", total)
		}
	})

	t.Run("非空扫描照常对齐（消失的删、新的加）", func(t *testing.T) {
		p := New("")
		defer p.Close()
		p.Add(mk("u1"))

		p.SyncToDir([]*auth.Auth{mk("u2")})

		if _, ok := p.Status("u1"); ok {
			t.Error("不在扫描结果里的 u1 应被剔除")
		}
		if _, ok := p.Status("u2"); !ok {
			t.Error("扫描结果里的 u2 应被加入")
		}
	})
}
