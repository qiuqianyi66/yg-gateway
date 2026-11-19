package pool

import (
	"testing"

	"yg-gateway/internal/auth"
)

// TestPickTiedBelowLeaderNotStarved 守住「非第一名之间的并列也会被字典序饿死」这个缺口。
//
// 现有实现（pick.go 等权重洗牌分支）只比较 ws[0].w 与其余项：
//
//	eq := false
//	for i := 1; i < len(ws); i++ {
//	    if ws[i].w == ws[0].w { eq = true; break }
//	}
//
// 于是它只能发现「与第一名并列」，发现不了「第二名及以后互相并列」。构造一个最常见
// 的真实形态：批量导入的账号 credits 相同、都未使用过，只有一个号积分更高。
// 排序键是 (cost1k, 权重降序, UID)，于是 6 个并列号按 UID 字典序排，top5 截断后
// UID 最大的两个永远进不了候选集；LRU 兜底又只在 top5 全员刚被用过时才触发。
//
// 后果不是报错，是**某些号永久不被使用**——正好是注释里声称要修的那个病。
func TestPickTiedBelowLeaderNotStarved(t *testing.T) {
	withNoPickGap(t)

	p := New("")
	uids := []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7"}
	for _, uid := range uids {
		p.Add(&auth.Auth{UID: uid})
	}
	// u1 积分高（权重 16 = 1 + 10*10000/10000 + idle 5），
	// u2..u7 不设积分（权重 6 = 1 + 0 + idle 5），彼此**精确相等**。
	p.SetCredits("u1", 10000, 0)

	const rounds = 3000
	counts := map[string]int{}
	for i := 0; i < rounds; i++ {
		a := p.Pick()
		if a == nil {
			t.Fatal("Pick returned nil")
		}
		counts[a.UID]++
	}

	// 断言：每个并列号都必须被选中过。现状下 u6/u7 恒为 0。
	for _, uid := range uids[1:] {
		if counts[uid] == 0 {
			t.Fatalf("%s 从未被选中（%d 轮）；并列号被 UID 字典序截断在 top5 之外。counts=%v",
				uid, rounds, counts)
		}
	}
}
