package httpauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// 多 Key 索引的测试。T1 先写测试再实现：本文件依赖的 Key/Index/BuildIndex/Match
// 尚未实现，编译失败即为「有牙齿」的第一道验证；实现后测试转绿。

func keyReq(authz string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	return r
}

func TestBuildIndexMatch(t *testing.T) {
	k1 := Key{ID: "k1", Name: "甲", Digest: digest("sk-alpha-111")}
	k2 := Key{ID: "k2", Name: "乙", Digest: digest("sk-beta-222")}
	idx := BuildIndex([]Key{k1, k2})

	cases := []struct {
		name  string
		authz string
		want  string // 期望命中的 Key ID；空 = 不命中
	}{
		{"命中 k1", "Bearer sk-alpha-111", "k1"},
		{"命中 k2", "Bearer sk-beta-222", "k2"},
		{"未知 key", "Bearer sk-wrong", ""},
		{"缺头", "", ""},
		{"缺 Bearer 前缀", "sk-alpha-111", ""},
		{"前缀大小写不符", "bearer sk-alpha-111", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := idx.Match(keyReq(c.authz))
			if c.want == "" {
				if ok {
					t.Fatalf("Match(%q) = (%q, true), want 不命中", c.authz, got.ID)
				}
				return
			}
			if !ok || got.ID != c.want {
				t.Fatalf("Match(%q) = (%q, %v), want (%q, true)", c.authz, got.ID, ok, c.want)
			}
		})
	}
}

// TestBuildIndexPrefixCollision 前缀碰撞不覆盖：两把 Key 的摘要前缀相同，索引桶必须
// 容下两个候选（切片非单值）。这里直接构造「前 6 字节相同、第 7 字节不同」的摘要，
// 验证 BuildIndex 的桶结构——不依赖真实 sha256 碰撞（那需要 2^48 空间，不可测）。
func TestBuildIndexPrefixCollision(t *testing.T) {
	d1 := make([]byte, 32)
	d2 := make([]byte, 32)
	d1[6] = 0x10 // 前 6 字节全 0 → 12 个 hex 前缀相同；第 7 字节不同 → 完整摘要不同
	d2[6] = 0x20

	if hexPrefix(d1) != hexPrefix(d2) {
		t.Fatalf("构造失败：前缀不一致 %q vs %q", hexPrefix(d1), hexPrefix(d2))
	}

	k1 := Key{ID: "k1", Digest: d1}
	k2 := Key{ID: "k2", Digest: d2}
	idx := BuildIndex([]Key{k1, k2})

	p := hexPrefix(d1)
	bucket := idx.buckets[p]
	if len(bucket) != 2 {
		t.Fatalf("前缀桶应含 2 个候选（碰撞不覆盖），got %d", len(bucket))
	}
	// 两个候选都在，且是不同 ID（未被覆盖）。
	ids := map[string]bool{}
	for _, k := range bucket {
		ids[k.ID] = true
	}
	if !ids["k1"] || !ids["k2"] {
		t.Fatalf("碰撞桶候选丢失，got %v", ids)
	}
}

// TestBuildIndexEmpty 空索引不 panic、不命中任何请求。
func TestBuildIndexEmpty(t *testing.T) {
	idx := BuildIndex(nil)
	if !idx.empty {
		t.Fatal("空索引应标记 empty=true")
	}
	if got, ok := idx.Match(keyReq("Bearer anything")); ok {
		t.Fatalf("空索引 Match 命中 %q，want 不命中", got.ID)
	}
}

// TestBuildIndexDuplicates 重复 key（同明文）不覆盖：桶内保留两个候选。
func TestBuildIndexDuplicates(t *testing.T) {
	k1 := Key{ID: "k1", Digest: digest("sk-dup")}
	k2 := Key{ID: "k2", Digest: digest("sk-dup")}
	idx := BuildIndex([]Key{k1, k2})
	got, ok := idx.Match(keyReq("Bearer sk-dup"))
	if !ok {
		t.Fatal("重复 key 应命中")
	}
	if got.ID != "k1" && got.ID != "k2" {
		t.Fatalf("重复 key 命中的 ID = %q，want k1 或 k2", got.ID)
	}
}

// TestVerifyBearerCompat 现有单 Key 签名必须保持行为不变（回归锚）。
func TestVerifyBearerCompat(t *testing.T) {
	if !VerifyBearer(keyReq("Bearer sk-abc"), "sk-abc") {
		t.Error("单 Key 正确应放行")
	}
	if VerifyBearer(keyReq("Bearer sk-wrong"), "sk-abc") {
		t.Error("单 Key 错误应拒绝")
	}
	if !VerifyBearer(keyReq(""), "") {
		t.Error("空 key 应放行（未启用鉴权）")
	}
}

// TestHexPrefix 前缀长度与形状：32 字节摘要 → 12 位 hex。
func TestHexPrefix(t *testing.T) {
	d := digest("x")
	if len(d) != 32 {
		t.Fatalf("sha256 digest 长度 = %d, want 32", len(d))
	}
	if p := hexPrefix(d); len(p) != 12 {
		t.Fatalf("hexPrefix 长度 = %d, want 12", len(p))
	}
}

// TestAuthenticate 统一鉴权入口：多 Key 索引 + 单 Key 兼容。
func TestAuthenticate(t *testing.T) {
	idx := BuildIndex([]Key{
		{ID: "k1", Digest: digest("sk-multi")},
	})

	cases := []struct {
		name      string
		index     *Index
		singleKey string
		authz     string
		wantOK    bool
		wantID    string // 命中时期望 ID；未启用鉴权时为 "" 且 wantOK=true
	}{
		{"多Key命中", idx, "", "Bearer sk-multi", true, "k1"},
		{"多Key未命中", idx, "", "Bearer sk-wrong", false, ""},
		{"无索引+单Key命中", nil, "sk-single", "Bearer sk-single", true, "single"},
		{"无索引+单Key错误", nil, "sk-single", "Bearer sk-wrong", false, ""},
		{"无索引+空key放行", nil, "", "Bearer anything", true, ""},
	// 无索引+空key缺头也放行", nil, "", "", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Authenticate(keyReq(c.authz), c.index, c.singleKey)
			if ok != c.wantOK {
				t.Fatalf("Authenticate ok=%v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if c.wantID == "" {
				if got != nil {
					t.Fatalf("未启用鉴权应返回 nil key，got %+v", got)
				}
				return
			}
			if got == nil || got.ID != c.wantID {
				t.Fatalf("Authenticate key.ID=%v, want %q", got, c.wantID)
			}
		})
	}
}

// TestAuthenticateFallbackSingleKey index 非空但漏纳单 Key 时，Authenticate 兜底
// VerifyBearer（防调用方装配遗漏，ADR-001 §五 健壮性）。
func TestAuthenticateFallbackSingleKey(t *testing.T) {
	// index 只有多 Key，没有单 Key（模拟调用方漏纳）。
	idx := BuildIndex([]Key{{ID: "k1", Digest: digest("sk-multi")}})
	// 用单 Key 请求：正常路径 index 未命中，但应兜底 VerifyBearer 命中 single。
	got, ok := Authenticate(keyReq("Bearer sk-single"), idx, "sk-single")
	if !ok || got == nil || got.ID != "single" {
		t.Fatalf("漏纳单 Key 时应兜底命中 single，got %+v ok=%v", got, ok)
	}
}
