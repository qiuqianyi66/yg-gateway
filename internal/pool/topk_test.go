package pool

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"testing"
)

// TestTopKByMatchesFullSort top-k 选择必须与「全排序后取前 k」逐位一致（含并列值）。
// 牙齿：把 topKBy 的插入逻辑改坏（例如比较方向反了）→ 本测试在某个 n 上 FAIL。
func TestTopKByMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261121, 5))
	for n := 0; n <= 60; n++ {
		items := make([]int, n)
		for i := range items {
			items[i] = rng.IntN(20) // 刻意制造并列
		}
		for k := 0; k <= 5; k++ {
			got := topKBy(slices.Clone(items), k, cmp.Compare)
			want := slices.Clone(items)
			slices.Sort(want)
			if len(want) > k {
				want = want[:k]
			}
			if !slices.Equal(got, want) {
				t.Fatalf("n=%d k=%d got=%v want=%v（topK 必须等于全排序前 k）", n, k, got, want)
			}
		}
	}
}

// TestTopKByDoesNotAllocate 热路径 helper 不应额外分配（固定数组/原地前缀）。
func TestTopKByDoesNotAllocate(t *testing.T) {
	items := make([]int, 46)
	for i := range items {
		items[i] = 46 - i
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		topKBy(items, 5, cmp.Compare)
	}); allocs != 0 {
		t.Fatalf("topKBy allocs=%v want 0", allocs)
	}
}
