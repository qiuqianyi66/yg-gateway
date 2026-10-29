// promo_apply_test.go applyModelPromotions 挂载逻辑的直接断言（M3 growth 域测试基线）。
// 补齐点：promoClock/promoActive 已有 coverage_parse_test.go 直接覆盖，但「多条命中
// 取 priority 最高、badge-only 不挂 factor、目录外模型不挂」的挂载语义无直接测试。
// 时段判定（promoActive）已单测，此处构造无 schedule 的 promo 使 now 不参与判定。
package upstream

import (
	"testing"
)

// TestApplyModelPromotionsPriorityAndMounting 挂载语义表驱动：
//  1. 同模型多条命中 → priority 最高者胜出（glm-5.2：p=100 夜间五折压过 p=50 badge-only）；
//  2. discount 命中 → PromoFactor + PromoCredits + Badge + Hover 全挂；
//  3. badge-only（无 discount）→ 只挂 PromoLabel + PromoNote，PromoFactor 留 nil；
//  4. promo 声明了目录外模型（unknown-model）→ 不挂（out 无该键即无操作）；
//  5. disabled 的 promo → 完全不挂（hy4-preview-f 被 disabled 跳过，由 badge-only
//     另一条同模型 promo 挂载——验证 disabled 与 enabled 并存时只取 enabled）。
func TestApplyModelPromotionsPriorityAndMounting(t *testing.T) {
	pp := func(f float64) *float64 { return &f }
	_ = pp
	badge := func(label string) *struct {
		Label string `json:"label"`
	} {
		return &struct {
			Label string `json:"label"`
		}{Label: label}
	}
	hover := func(text string) *struct {
		TextZh string `json:"textZh"`
	} {
		return &struct {
			TextZh string `json:"textZh"`
		}{TextZh: text}
	}
	discount := func(credits string, factor float64) *struct {
		DiscountedCredits string  `json:"discountedCredits"`
		Factor            float64 `json:"factor"`
	} {
		return &struct {
			DiscountedCredits string  `json:"discountedCredits"`
			Factor            float64 `json:"factor"`
		}{DiscountedCredits: credits, Factor: factor}
	}

	out := map[string]ModelInfo{
		"glm-5.2":       {},
		"hy3":           {},
		"hy4-preview-f": {},
		"kimi-k3-1":     {},
	}
	promos := []v3ModelPromotion{
		{Enabled: true, Priority: 50, ModelIDs: []string{"glm-5.2"}, Badge: badge("白天错峰")},
		{Enabled: true, Priority: 100, ModelIDs: []string{"glm-5.2"}, Badge: badge("夜间五折"), Discount: discount("x0.5", 0.5)},
		{Enabled: true, Priority: 80, ModelIDs: []string{"hy3", "unknown-model"}, Badge: badge("测试"), Discount: discount("x0.3", 0.3)},
		{Enabled: false, Priority: 90, ModelIDs: []string{"hy4-preview-f"}, Badge: badge("不该挂")},
		{Enabled: true, Priority: 60, ModelIDs: []string{"hy4-preview-f"}, Badge: badge("hy4 限免"), Hover: hover("限时免费说明")},
	}

	applyModelPromotions(out, promos)

	glm := out["glm-5.2"]
	if glm.PromoLabel != "夜间五折" {
		t.Errorf("glm-5.2 PromoLabel=%q want 夜间五折（priority 100 压过 50）", glm.PromoLabel)
	}
	if glm.PromoFactor == nil || *glm.PromoFactor != 0.5 {
		t.Errorf("glm-5.2 PromoFactor=%v want 0.5", glm.PromoFactor)
	}
	if glm.PromoCredits != "x0.5" {
		t.Errorf("glm-5.2 PromoCredits=%q want x0.5", glm.PromoCredits)
	}

	hy3 := out["hy3"]
	if hy3.PromoLabel != "测试" || hy3.PromoFactor == nil || *hy3.PromoFactor != 0.3 || hy3.PromoCredits != "x0.3" {
		t.Errorf("hy3 挂载不完整：label=%q factor=%v credits=%q", hy3.PromoLabel, hy3.PromoFactor, hy3.PromoCredits)
	}

	// badge-only：label + hover 挂，factor/credits 留空（错峰类无机器可读价）。
	hy4 := out["hy4-preview-f"]
	if hy4.PromoLabel != "hy4 限免" {
		t.Errorf("hy4-preview-f PromoLabel=%q want hy4 限免（disabled 那条不参与）", hy4.PromoLabel)
	}
	if hy4.PromoNote != "限时免费说明" {
		t.Errorf("hy4-preview-f PromoNote=%q want 限时免费说明", hy4.PromoNote)
	}
	if hy4.PromoFactor != nil || hy4.PromoCredits != "" {
		t.Errorf("badge-only 不应挂 factor/credits：factor=%v credits=%q", hy4.PromoFactor, hy4.PromoCredits)
	}

	if k := out["kimi-k3-1"]; k.PromoLabel != "" || k.PromoFactor != nil || k.PromoCredits != "" {
		t.Errorf("未命中任何 promo 的模型不应挂载：%+v", k)
	}
}

// TestApplyModelPromotionsEmptyNoop 空 promo 列表 / 空目录 → 无操作（不 panic、不改动）。
func TestApplyModelPromotionsEmptyNoop(t *testing.T) {
	out := map[string]ModelInfo{"m": {}}
	applyModelPromotions(out, nil)
	applyModelPromotions(nil, []v3ModelPromotion{{Enabled: true, ModelIDs: []string{"m"}}})
	if out["m"].PromoLabel != "" {
		t.Error("空列表不应挂载")
	}
}
