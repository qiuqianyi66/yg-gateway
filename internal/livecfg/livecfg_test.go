package livecfg

import (
	"crypto/sha256"
	"sync"
	"testing"

	"yg-gateway/internal/httpauth"
)

// httpauthDigest 计算 Key 明文的 SHA256 摘要（测试辅助，与 httpauth.digest 同口径）。
func httpauthDigest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// T3 测试：Snapshot 扩展 + 原子热更新。
// 契约：ADR-001 §六（并发与热更新）。

// TestSnapshotLoadStoreRoundtrip 快照 Load/Store 往返。
func TestSnapshotLoadStoreRoundtrip(t *testing.T) {
	h := New(Snapshot{APIKey: "k"})
	if got := h.Load().APIKey; got != "k" {
		t.Fatalf("Load().APIKey = %q, want k", got)
	}
	h.Store(Snapshot{APIKey: "k2"})
	if got := h.Load().APIKey; got != "k2" {
		t.Fatalf("after Store, Load().APIKey = %q, want k2", got)
	}
}

// TestSnapshotNilHolder 空 Holder 返回零值，不 panic。
func TestSnapshotNilHolder(t *testing.T) {
	var h *Holder
	if got := h.Load().APIKey; got != "" {
		t.Fatalf("nil Holder Load().APIKey = %q, want 空", got)
	}
}

// TestSnapshotConcurrentStoreLoad -race 下并发 Load + Store，验证不读到半新半旧快照
// （atomic.Pointer 保证每次 Load 拿到一致视图）。这里用竞态检测跑，逻辑上断言
// 读到的 APIKey 要么是 A 要么是 B，绝不可能是中间态（字符串赋值本身原子，但结构体
// 快照的字段一致性靠 atomic.Pointer 保证）。
func TestSnapshotConcurrentStoreLoad(t *testing.T) {
	h := New(Snapshot{APIKey: "A"})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if v := h.Load().APIKey; v != "A" && v != "B" {
						t.Errorf("读到中间态 APIKey=%q", v)
						return
					}
				}
			}
		}()
	}
	// 写线程交替 Store A/B
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			if i%2 == 0 {
				h.Store(Snapshot{APIKey: "A"})
			} else {
				h.Store(Snapshot{APIKey: "B"})
			}
		}
		close(stop)
	}()
	wg.Wait()
}

// TestBuildAuthSnapshot 从 httpauth.Key 列表构建快照，索引可用。
func TestBuildAuthSnapshot(t *testing.T) {
	keys := []httpauth.Key{
		{ID: "k1", Digest: httpauthDigest("sk-a")},
	}
	snap := BuildSnapshot("single", keys)
	if snap.APIKey != "single" {
		t.Fatalf("APIKey=%q want single", snap.APIKey)
	}
	if len(snap.APIKeys) != 1 || snap.APIKeys[0].ID != "k1" {
		t.Fatalf("APIKeys=%#v want [k1]", snap.APIKeys)
	}
}
