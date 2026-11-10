package session

import (
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

// 默认上限：没配 MaxEntries 的 Router 也必须受 defaultMaxEntries 约束（配置漏传不是无界）。
func TestRouterDefaultMaxEntries(t *testing.T) {
	r := New(Config{})
	if got := r.maxEntries(); got != defaultMaxEntries {
		t.Fatalf("maxEntries()=%d want %d", got, defaultMaxEntries)
	}
}
