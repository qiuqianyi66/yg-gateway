package panel

import (
	"os"
	"strings"
	"testing"
)

// readAppJS 读取 app.js 源码（源码级断言用）。
func readAppJS(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	return string(raw)
}

// TestAppJSConfigDirtyGuard 配置页未保存改动的守卫必须存在。
//
// 背景：go() 进配置页会无条件 loadConfig() 重拉并整体回填表单，用户改了字段后
// 点别的导航再点回来，改动被静默覆盖（savebar 的「放弃修改」是显式入口，导航
// 却是隐式入口却同效）。修复需三件齐备：
//
//	① cfgDirty 状态 + 置位（表单 input/change 监听）
//	② go() 在「离开配置页且有脏改动」时 confirm 拦截
//	③ loadConfig 回填后清脏（否则刚加载就被判为有改动）
//
// 牙齿：删掉任一环节 → 本测试 FAIL。
func TestAppJSConfigDirtyGuard(t *testing.T) {
	src := readAppJS(t)
	checks := []struct {
		name string
		want string
	}{
		{"cfgDirty 状态声明", "let cfgDirty = false"},
		{"置脏函数", "function markCfgDirty()"},
		{"清脏函数", "function clearCfgDirty()"},
		{"go() 守卫分支", "if (!skipGuard && view === 'config' && v !== 'config' && cfgDirty)"},
		{"表单 input 置脏", "$('cfgForm').addEventListener('input', ev => {"},
		{"表单 change 置脏", "$('cfgForm').addEventListener('change', markCfgDirty)"},
		{"回填后清脏", "clearCfgDirty();\n  } catch (e) { toast('读取配置失败"},
	}
	for _, c := range checks {
		if !strings.Contains(src, c.want) {
			t.Errorf("缺少「%s」：未找到 %q", c.name, c.want)
		}
	}
	// markCfgDirty 必须在 input 监听体内被调用（而不是只定义了函数）。
	if !strings.Contains(src, "markCfgDirty(); // 任何改动都置脏") {
		t.Error("input 监听未实际调用 markCfgDirty()（只是定义了函数）")
	}
}

// TestAppJSHashRouting 浏览器前进/后退与手改 hash 必须可用。
//
// 此前只有 history.replaceState —— 历史里没有记录，按返回键直接离开面板。
//
// 牙齿：删掉 hashchange 监听 → FAIL。
func TestAppJSHashRouting(t *testing.T) {
	src := readAppJS(t)
	if !strings.Contains(src, "addEventListener('hashchange'") {
		t.Error("缺少 hashchange 监听：浏览器前进/后退与手改 hash 不工作")
	}
	if !strings.Contains(src, "history.pushState(null, '', '#' + a.dataset.view)") {
		t.Error("导航未用 pushState：历史里没有记录，返回键会直接离开面板")
	}
	// 初始深链必须跳过守卫（否则 #config 深链会被守卫拦下）+ 不回顶（避免丢掉锚点位置）
	if !strings.Contains(src, "go(hash in TITLES ? hash : 'accounts', true)") {
		t.Error("初始深链未传 skipGuard=true：深链进配置页会被守卫拦下")
	}
}

// TestAppJSRenderShortCircuit 账号表/模型锁表的渲染短路必须存在。
//
// 背景：每 5s 轮询无条件 tb.innerHTML = ... 全量重绘，会抹掉 hover 高亮、
// 清掉用户选区、打断 title 气泡。修复需「签名比对」+「表内交互时跳过」。
//
// 牙齿：把 renderAccounts 改回直接调 renderAccountsUnsafe（去掉签名判断）→ FAIL。
func TestAppJSRenderShortCircuit(t *testing.T) {
	src := readAppJS(t)
	for _, want := range []string{
		"function accountsSig(list)",
		"if (sig === accRenderSig) return; // 数据无变化：不碰 DOM",
		"if (document.activeElement && tb.contains(document.activeElement)) return;",
		"function renderAccountsUnsafe(list)",
		"if (sig === mlRenderSig) return; // 无变化：不碰 DOM",
		"function renderModelLocksUnsafe(rows)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("渲染短路缺失：未找到 %q", want)
		}
	}
	// renderAccounts 必须走签名判断，不能直接调 Unsafe（否则等于没短路）。
	body := src[strings.Index(src, "function renderAccounts(list)"):]
	body = body[:strings.Index(body, "function renderAccountsUnsafe")]
	if !strings.Contains(body, "accountsSig(list)") {
		t.Error("renderAccounts 未用签名比对，等于没有短路")
	}
}

// TestAppJSRefreshNotUpstream 顶栏「刷新」不得打上游。
//
// 此前 btnRefresh 直接调 balance_all（逐账号查上游余额），而按钮文案是「刷新」——
// 用户只想刷新视图却触发整池上游请求。真实的重操作应独立成按钮 + 二次确认。
//
// 牙齿：把 balance_all 挪回 btnRefresh → FAIL。
func TestAppJSRefreshNotUpstream(t *testing.T) {
	src := readAppJS(t)
	// 取出 btnRefresh 的处理体，确认其中不含 balance_all。
	start := strings.Index(src, "$('btnRefresh').onclick")
	if start < 0 {
		t.Fatal("未找到 btnRefresh 绑定")
	}
	end := strings.Index(src[start:], "\n};")
	if end < 0 {
		t.Fatal("btnRefresh 处理体未找到结尾")
	}
	body := src[start : start+end]
	if strings.Contains(body, "balance_all") {
		t.Error("顶栏「刷新」仍在打上游（balance_all）：用户只想刷新视图却触发整池上游请求")
	}
	// 重操作必须独立存在且带确认。
	if !strings.Contains(src, "$('btnBalanceAll').onclick") {
		t.Error("缺少独立的「刷新余额」按钮绑定")
	}
	if !strings.Contains(src, "将从上游逐账号查询余额") {
		t.Error("「刷新余额」缺少二次确认文案")
	}
	// HTML 侧也要有该按钮。
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), `id="btnBalanceAll"`) {
		t.Error("index.html 缺少 btnBalanceAll 按钮")
	}
}

// TestAppJSViewScrollReset 切视图必须回顶（否则从长页面切走会停在页面中部）。
func TestAppJSViewScrollReset(t *testing.T) {
	src := readAppJS(t)
	if !strings.Contains(src, "if (!skipGuard) window.scrollTo({ top: 0 });") {
		t.Error("go() 缺少切视图回顶（从长页面切走会停在页面中部，看起来像页面坏了）")
	}
}
