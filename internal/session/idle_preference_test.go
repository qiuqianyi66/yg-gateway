package session

import (
	"fmt"
	"testing"
	"time"
)

// TestRouterPrefersIdleAccount 钉住双段分配里的「空闲号优先」（PLAN B3-b）。
//
// 为什么需要它（2026-10-17，独立复审自选变异存活）：`ResolveForModel` 的分配是
// 「先挑未被任何会话绑定的可用号（idle），没有 idle 才退回全池」。这条策略是产品定位
// 「养号友好型选号」的一部分——把新会话尽量摊到空闲号上，避免多个会话挤同一个号而别的号闲着。
// 把 `pool2 := idle` 改成 `pool2 := uids` 后，包内全部测试仍然绿：**没有任何测试看分配分布**。
//
// 构造：先找两个 hashIndex(_, 2) 落在同一桶的 key。若 idle 优先生效，第一个 key 绑到某号后，
// 第二个 key 的 idle 集合里只剩另一个号（桶大小 1）→ 必然分到不同的号；若 idle 优先失效，
// 两个 key 的桶都是全池且桶索引相同 → 必然撞到同一个号。
//
// 还原什么会让它 FAIL：把 `pool2 := idle` 改成 `pool2 := uids`（或删掉 idle 计算）→ 本用例红。
func TestRouterPrefersIdleAccount(t *testing.T) {
	const buckets = 2

	// 找同桶的两个 key（确定性遍历，不依赖随机）。
	var k1, k2 string
	seen := map[int]string{}
	for i := 0; i < 10000; i++ {
		k := fmt.Sprintf("idle-key-%d", i)
		b := hashIndex(k, buckets)
		if prev, ok := seen[b]; ok {
			k1, k2 = prev, k
			break
		}
		seen[b] = k
	}
	if k2 == "" {
		t.Fatal("未找到同桶的两个 key：hashIndex 的行为变了？本用例的前提不成立，请检查 hashIndex")
	}
	if hashIndex(k1, buckets) != hashIndex(k2, buckets) {
		t.Fatalf("前提不成立：%q 与 %q 的桶不同", k1, k2)
	}

	avail := []string{"uid-a", "uid-b"}
	r := New(Config{
		TTL:       time.Hour,
		Store:     newCSSessStore(),
		Available: func() []string { return avail },
	})

	u1, ok1 := r.ResolveForModel(k1, "")
	u2, ok2 := r.ResolveForModel(k2, "")
	if !ok1 || !ok2 {
		t.Fatalf("解析失败：ok1=%v ok2=%v（可用池非空，不应失败）", ok1, ok2)
	}
	if u1 == u2 {
		t.Fatalf("会话 %q 与 %q 都被分配到 %q，而另一个号空闲——「空闲号优先」失效", k1, k2, u1)
	}
}
