package httpauth

import (
	"net/http/httptest"
	"strconv"
	"testing"
)

// T7 性能基准（ADR-001 §七 第 8 项）。
// 记录鉴权热路径耗时与每请求分配。碰撞桶大小影响桶内比较次数，这里用 1、10、100
// 三个规模，测量前缀索引的定位 + 桶内常量时间比较的分配开销。

func benchKeys(n int) []Key {
	keys := make([]Key, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, Key{ID: "k" + strconv.Itoa(i), Digest: digest("sk-bench-" + strconv.Itoa(i))})
	}
	return keys
}

func BenchmarkMatch1Key(b *testing.B) {
	idx := BuildIndex(benchKeys(1))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer sk-bench-0")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Match(r)
	}
}

func BenchmarkMatch10Keys(b *testing.B) {
	idx := BuildIndex(benchKeys(10))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer sk-bench-5")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Match(r)
	}
}

func BenchmarkMatch100Keys(b *testing.B) {
	idx := BuildIndex(benchKeys(100))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer sk-bench-50")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Match(r)
	}
}

// BenchmarkBuildIndex 索引构建开销（含摘要计算）。
func BenchmarkBuildIndex(b *testing.B) {
	keys := benchKeys(100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildIndex(keys)
	}
}

// BenchmarkVerifyBearer 单 Key 兼容路径必须零分配（2.0.0 行为不回归，ADR §七 第 8 项）。
func BenchmarkVerifyBearer(b *testing.B) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer sk-abc123")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		VerifyBearer(r, "sk-abc123")
	}
}
