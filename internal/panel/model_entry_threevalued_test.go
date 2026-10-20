package panel

import (
	"net/http"
	"testing"

	"yg-gateway/internal/upstream"
)

// tvDeadTransport 任何请求立即失败：让被测函数无法真的联网，
// 同时也避免传 nil client 时下游可能直接解引用。
type tvDeadTransport struct{}

func (tvDeadTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrUseLastResponse
}

// TestPanelModelEntryCapabilityKeysAreThreeValued 面板模型条目的能力字段必须**三值**透出：
//
//	显式 true  → true
//	显式 false → false
//	nil（上游未声明 = 未知）→ **键整体省略**
//
// 为什么单测必须钉在**这一层**（而不是只测前端 JS）：曾经的错误形态就是 panel.go
// 无条件 emit `supports_images: mi.SupportsImages`，把 nil 落成显式 false，于是前端
// 的 `m.supports_images === false` 命中，未知能力模型被当成"不支持"筛掉。而当时唯一的
// 相关测试 TestAppJSModelFilterUnknownCapability 是**直接喂对象给 JS 函数**的，绕过了
// 这层序列化 → 测试绿、生产坏。所以这里补的是当年缺失的那一层守卫。
//
// 牙齿：把 panelModelEntry 里两个 if 改回字面量无条件 emit → 「未声明」用例 FAIL。
func TestPanelModelEntryCapabilityKeysAreThreeValued(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name    string
		mi      upstream.ModelInfo
		wantAny interface{} // nil = 期望键整体省略
	}{
		{name: "未声明(nil) → 键必须省略", mi: upstream.ModelInfo{ID: "m"}, wantAny: nil},
		{name: "显式 true → true", mi: upstream.ModelInfo{ID: "m", SupportsImages: &yes}, wantAny: true},
		{name: "显式 false → false（与省略可区分）", mi: upstream.ModelInfo{ID: "m", SupportsImages: &no}, wantAny: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			entry := panelModelEntry("cn", c.mi, nil, "", &http.Client{Transport: tvDeadTransport{}})
			got, present := entry["supports_images"]
			if c.wantAny == nil {
				if present {
					t.Fatalf("supports_images = %v（键存在），want **省略**："+
						"未知不得落成 false，否则前端会把未知模型当不支持筛掉", got)
				}
			} else {
				if !present {
					t.Fatalf("supports_images 键缺失，want %v", c.wantAny)
				}
				if got != c.wantAny {
					t.Fatalf("supports_images = %v, want %v", got, c.wantAny)
				}
			}
			// 另一半：nil 时 supports_reasoning 同样不得出现。
			if _, present := entry["supports_reasoning"]; present && c.mi.SupportsReasoning == nil {
				t.Fatal("supports_reasoning：未声明时应省略，实际出现了")
			}
		})
	}
}
