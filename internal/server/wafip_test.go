package server

import (
	"testing"
	"time"
)

// withWafWindow 注入短判定窗，测试结束恢复生产值（wafIPWindow 是进程级 var，
// 不恢复会污染同包其它用例）。
func withWafWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := wafIPWindow
	wafIPWindow = d
	t.Cleanup(func() { wafIPWindow = old })
}

// TestWafIPGateSingleAccountNeverActivates 是这套机制最关键的防误判条款：
// 阈值口径是「窗内**不同** UID 数」，单个账号反复撞 WAF 403 属于账号级偶发，
// 由 ErrWafBlock 软冷却管，**永远不许**把它升级成 IP 级拦截 —— 否则一个坏号
// 会把整条出口路径封掉，所有健康号跟着挨饿。
func TestWafIPGateSingleAccountNeverActivates(t *testing.T) {
	withWafWindow(t, time.Minute)
	var g wafIPGate

	// 同一 uid 连撞 10 次，远超 wafIPThreshold(2)。
	for i := 0; i < 10; i++ {
		if g.noteWaf("uid-same") {
			t.Fatalf("第 %d 次同号命中就激活了 IP 级拦截：单号反复 403 不应触发", i+1)
		}
	}
	if g.active() {
		t.Error("单号反复命中后 active() 仍应为 false")
	}
	if len(g.hits) != 1 {
		t.Errorf("hits 应只记 1 个不同 uid，实际 %d —— 同号重复命中不许累计", len(g.hits))
	}
}

// TestWafIPGateTwoDistinctAccountsActivate 两个不同号在窗内接连命中 → 激活。
// 这是机制的正向路径：BulidH 实测 3 号 1s 全 403，阈值 2 是「多号」的最小证据。
func TestWafIPGateTwoDistinctAccountsActivate(t *testing.T) {
	withWafWindow(t, time.Minute)
	var g wafIPGate

	if g.noteWaf("uid-a") {
		t.Fatal("第一个号命中不应激活")
	}
	if !g.noteWaf("uid-b") {
		t.Fatal("第二个不同号命中应激活 IP 级拦截")
	}
	if !g.active() {
		t.Error("激活后 active() 应为 true")
	}
}

// TestWafIPGateExpiredHitsArePruned 窗外的旧命中必须被剪掉：否则昨天撞过一次的
// 号会和今天撞过的号拼成「多号」，把一次孤立事件误判成 IP 级拦截。
func TestWafIPGateExpiredHitsArePruned(t *testing.T) {
	withWafWindow(t, 50*time.Millisecond)
	var g wafIPGate

	if g.noteWaf("uid-old") {
		t.Fatal("第一个号不应激活")
	}
	time.Sleep(80 * time.Millisecond) // 让 uid-old 落到窗外

	if g.noteWaf("uid-new") {
		t.Fatal("窗外旧命中应被剪掉，只剩 1 个不同号，不应激活")
	}
	if len(g.hits) != 1 {
		t.Errorf("过期命中未被剪枝，hits 仍有 %d 项", len(g.hits))
	}
	if g.active() {
		t.Error("未达阈值不应激活")
	}
}

// TestWafIPGateActiveDoesNotRenew 激活期内的新命中不续期（注释写明「保守：不做
// 主动探测，窗口自然解除」）。续期会让一次风控在持续流量下永不解封。
func TestWafIPGateActiveDoesNotRenew(t *testing.T) {
	withWafWindow(t, time.Minute)
	var g wafIPGate

	g.noteWaf("uid-a")
	g.noteWaf("uid-b") // 激活
	until := g.until

	g.noteWaf("uid-c") // 激活期内新命中
	if !g.until.Equal(until) {
		t.Errorf("激活期内新命中不应续期：until %v -> %v", until, g.until)
	}
}

// TestWafIPGateClearsWindowOnActivate 激活时清空判定窗：解除后需要**全新**的
// 多号命中才能重新判定，不许叠旧账（否则一次误判会自我延续）。
func TestWafIPGateClearsWindowOnActivate(t *testing.T) {
	withWafWindow(t, time.Minute)
	var g wafIPGate

	g.noteWaf("uid-a")
	g.noteWaf("uid-b")

	if len(g.hits) != 0 {
		t.Errorf("激活时应清空判定窗，实际还剩 %d 项", len(g.hits))
	}
}

// TestWafIPGateDeactivatesAfterWindow 自然解除：窗口过去后 active() 为 false，
// 且此时的新命中要重新从零计数（不能因为 until 刚过期就立刻再次激活）。
func TestWafIPGateDeactivatesAfterWindow(t *testing.T) {
	withWafWindow(t, 50*time.Millisecond)
	var g wafIPGate

	g.noteWaf("uid-a")
	g.noteWaf("uid-b") // 激活到 now+50ms

	time.Sleep(80 * time.Millisecond)

	if g.active() {
		t.Fatal("窗口过去后应自然解除")
	}
	if g.noteWaf("uid-c") {
		t.Error("刚解除时的单个新命中不应立即再次激活（判定窗已在激活时清空）")
	}
}
