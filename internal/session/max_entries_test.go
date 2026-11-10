package session

import (
	"fmt"
	"testing"
	"time"
)

// S-3：绑定条目必须有上限，否则唯一 conversationId 的批量请求能把内存推成无界（不报错）。
func TestRouterMaxEntriesEvictsOldest(t *testing.T) {
	r := New(Config{MaxEntries: 2})

	r.Bind("k1", "uid-a")
	// 把 k1 钉成"最久未活跃"，让淘汰顺序确定（否则三个 Bind 的 now 可能落在同一纳秒）。
	r.mu.Lock()
	e := r.entries["k1"]
	e.lastActive = time.Now().Add(-time.Hour)
	r.entries["k1"] = e
	r.mu.Unlock()

	r.Bind("k2", "uid-b")
	r.Bind("k3", "uid-c")

	if got := r.Count(); got != 2 {
		t.Fatalf("Count=%d want 2（上限必须生效）", got)
	}
	out := r.Snapshot()
	seen := map[string]string{}
	for _, b := range out {
		seen[b.Key] = b.UID
	}
	if _, ok := seen["k1"]; ok {
		t.Fatalf("k1 未被淘汰（最久未活跃应先走）：%v", seen)
	}
	if seen["k2"] != "uid-b" || seen["k3"] != "uid-c" {
		t.Fatalf("存活绑定不对：%v", seen)
	}

	// 淘汰后 key 可以重新绑定（不是被永久拉黑）。
	r.Bind("k1", "uid-a")
	if got := r.Count(); got != 2 {
		t.Fatalf("重绑后 Count=%d want 2", got)
	}

	// 覆盖已有 key 不腾新位置：只改绑，不产生额外淘汰。
	sizeBefore := r.Count()
	r.Bind("k2", "uid-b2")
	if got := r.Count(); got != sizeBefore {
		t.Fatalf("覆盖已有 key 改变了容量：%d → %d", sizeBefore, r.Count())
	}
}

// 饱和后一次淘汰约 10%（不是单条）：单条会在"每条新 key 一次 O(n) 全表扫描 + 持写锁"下
// 变成锁竞争热点，批量把成本摊到约 10% 的新增上。
func TestRouterEvictsBatchAtSaturation(t *testing.T) {
	const limit = 20
	r := New(Config{MaxEntries: limit})
	base := time.Now().Add(-time.Hour)
	r.mu.Lock()
	for i := 0; i < limit; i++ {
		key := fmt.Sprintf("k%02d", i)
		r.entries[key] = entry{uid: "u", lastActive: base.Add(time.Duration(i) * time.Second)}
	}
	r.mu.Unlock()

	r.Bind("fresh", "u")
	if got := r.Count(); got != limit-2+1 {
		t.Fatalf("Count=%d want %d（应一批淘汰 %d 条再插入）", got, limit-2+1, limit/10)
	}
	seen := map[string]bool{}
	for _, b := range r.Snapshot() {
		seen[b.Key] = true
	}
	for _, gone := range []string{"k00", "k01"} {
		if seen[gone] {
			t.Fatalf("%s 应被淘汰：%v", gone, seen)
		}
	}
	if !seen["k02"] || !seen["fresh"] {
		t.Fatalf("存活集合不对（应保留较新条目）：%v", seen)
	}
}

// 默认上限：没配 MaxEntries 的 Router 也必须受 defaultMaxEntries 约束（配置漏传不是无界）。
func TestRouterDefaultMaxEntries(t *testing.T) {
	r := New(Config{})
	if got := r.maxEntries(); got != defaultMaxEntries {
		t.Fatalf("maxEntries()=%d want %d", got, defaultMaxEntries)
	}
}

// 冷启动超过内存上限时，只淘汰内存、**不许**删 Store 里的持久化记录：
// 那些记录是刚从 Store 读出来的，删掉等于每次重启都永久清一批可恢复绑定。
func TestLoadFromStoreDoesNotDeletePersistedBinds(t *testing.T) {
	const limit = 20
	st := newCountingStore()
	for i := 0; i < limit+5; i++ {
		st.binds[fmt.Sprintf("s%02d", i)] = "uid"
	}
	r := New(Config{MaxEntries: limit, Store: st})

	r.LoadFromStore()

	// 批量淘汰会给下一批插入留出余量，故稳态落在 (limit-10%, limit] 之间，而不是正好 limit。
	if got := r.Count(); got > limit || got < limit-limit/10 {
		t.Fatalf("Count=%d 落在 [%d, %d] 之外（内存仍须受上限约束）", got, limit-limit/10, limit)
	}
	if st.delBinds != 0 {
		t.Fatalf("LoadFromStore 删了 %d 条持久化绑定：冷启动不该清理镜像", st.delBinds)
	}
}
