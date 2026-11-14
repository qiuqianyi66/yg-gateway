package panel

import (
	"strings"
	"testing"
)

// TestAppJSProviderBadgeOmitsWhenMissing F7 的 provider 徽标四态纪律（GWT 1/3）。
//
// 牙齿：把 providerTag 的「缺失返回空串」改成「缺失渲染为"不支持"」→ 本测试 FAIL。
// 缺失（字段未派生）= unknown，不是"不支持"也不是"codebuddy"——只对非 codebuddy 且
// 非空的 provider 显式渲染徽标，其余一律不渲染（省略 = 默认 codebuddy，零噪音）。
func TestAppJSProviderBadgeOmitsWhenMissing(t *testing.T) {
	src := readAppJS(t)
	if !strings.Contains(src, "function renderProviderStrip(") {
		t.Error("缺少 renderProviderStrip（F7 provider 汇总条回退）")
	}
	// 徽标判定必须是「非空且非 codebuddy」，缺失/空/ codebuddy 都不渲染（空串）。
	if !strings.Contains(src, "s.provider && s.provider !== 'codebuddy'") {
		t.Error("provider 徽标判定缺失：应只对非空且非 codebuddy 渲染")
	}
	// 空注册表提示「仅 CodeBuddy」。
	if !strings.Contains(src, "仅 CodeBuddy") {
		t.Error("空 provider 注册表缺少「仅 CodeBuddy」提示")
	}
	// 牙齿断言：缺失不得被渲染成"不支持"。
	if strings.Contains(src, "provider 不支持") || strings.Contains(src, "providerTag.*不支持") {
		t.Error("provider 缺失被渲染成「不支持」：缺失 = 未知，不是不支持")
	}
	// provider 账号不得显示 codebuddy 专属操作（签到/余额/任务）：点了会调 codebuddy
	// API 而失败（F7 打磨修复）。牙齿：把按钮的 isProvider 条件删掉 → 本断言 FAIL。
	for _, frag := range []string{
		`isProvider ? '' : '<button class="xs ghost" data-a="balance"`,
		`isProvider || s.enterprise ? '' : '<button class="xs ghost" data-a="checkin"`,
		`isProvider || s.enterprise ? '' : '<button class="xs ghost" data-a="tasks"`,
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("provider 账号仍会显示 codebuddy 专属操作按钮：缺 %s", frag)
		}
	}
}
