package server

import (
	"testing"

	"yg-gateway/internal/upstream"
)

// TestModelCatalogVerdictTriState 钉住 11102 白名单判定的**三值**语义。
//
// 背景（2026-11-02 独立复审 🟡-1）：原 `isKnownModel` 的 false 同时表示「目录里确实没有」
// 与「目录还没拉到」。chatrun 用 `!isKnownModel(...)` 当否定判据 → 冷启动时把真实模型
// 直接 400 掉（换号本可成功），与函数自己注释的意图相反。本用例把三值钉死：
//
//	modelCatalogKnown   → 可写负缓存（确定在目录里）
//	modelCatalogAbsent  → 可 fail-fast（确定不在目录里）
//	modelCatalogUnknown → 两者都不许做（继续尝试 / 不缓存）
//
// 牙齿：把空目录那条改回 modelCatalogAbsent（或让 isKnownModel 的三值退化成两值），
// 第一条断言立刻红——那正是"缺失被读成否定"的形态。
func TestModelCatalogVerdictTriState(t *testing.T) {
	// 假上游恒 500：保证目录缓存保持"空"（拉取失败 → snapshot() 返回 nil）。
	h := NewHandler(Config{Upstream: newFakeUpstream(t, func(string) (int, string, bool) {
		return 500, "boom", false
	})})

	// 1) 目录为空 = unknown（不是"不存在"）。
	if got := h.modelCatalogVerdict("glm-5.2"); got != modelCatalogUnknown {
		t.Fatalf("空目录: verdict = %v，应为 modelCatalogUnknown", got)
	}
	if h.isKnownModel("glm-5.2") {
		t.Fatal("空目录: isKnownModel 应为 false（不知道 ≠ 知道）")
	}

	// 2) 目录非空且含该模型 = known。
	h.modelsCache.store([]upstream.ModelInfo{{ID: "glm-5.2"}})
	if got := h.modelCatalogVerdict("glm-5.2"); got != modelCatalogKnown {
		t.Fatalf("目录含该模型: verdict = %v，应为 modelCatalogKnown", got)
	}
	if !h.isKnownModel("glm-5.2") {
		t.Fatal("目录含该模型: isKnownModel 应为 true")
	}

	// 3) 目录非空但不含该模型 = 确定的"不存在"（可 fail-fast）。
	if got := h.modelCatalogVerdict("nonexistent-model-xyz"); got != modelCatalogAbsent {
		t.Fatalf("目录不含该模型: verdict = %v，应为 modelCatalogAbsent", got)
	}
	if h.isKnownModel("nonexistent-model-xyz") {
		t.Fatal("目录不含该模型: isKnownModel 应为 false")
	}
}
