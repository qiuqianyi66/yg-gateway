package panel

import (
	"os"
	"os/exec"
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

// TestAppJSToastA11yAndCap toast 必须有 aria-live（读屏）+ 堆叠上限 + 手动关闭。
func TestAppJSToastA11yAndCap(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), `aria-live="polite"`) {
		t.Error("toast 容器缺 aria-live：读屏用户感知不到任何操作结果")
	}
	src := readAppJS(t)
	for _, want := range []string{
		"while (host.children.length > 5) host.firstChild.remove();", // 堆叠上限
		"cls === 'err' ? 8000 : 3600",                                // 错误停留更久
		"x.className = 'tst-x';",                                     // 手动关闭
	} {
		if !strings.Contains(src, want) {
			t.Errorf("toast 改进缺失：未找到 %q", want)
		}
	}
}

// TestAppJSSemanticLandmarks 语义标签：<main> / <h1> / th scope。
func TestAppJSSemanticLandmarks(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	s := string(html)
	for _, want := range []string{`<main class="main">`, `<h1 id="ttl">`, `</main>`} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少语义标签 %q（读屏无法用 landmark 跳转）", want)
		}
	}
	if n := strings.Count(s, `<th scope="col">`); n < 12 {
		t.Errorf("表格 th 的 scope 覆盖不足：只有 %d 个", n)
	}
}

// TestAppJSModalEscAndBackdrop 六个弹窗都必须有 Esc / 遮罩关闭出口。
//
// 背景：此前只有 Key 的两个弹窗有出口，addVeil/taskVeil/vcVeil/keyVeil 没有——
// 键盘用户只能摸鼠标。data-lock 的密钥门是唯一例外（必须输入密钥才能关）。
//
// 牙齿：把 VEIL_CLOSERS 里的任一项删掉（如 taskVeil）→ FAIL。
func TestAppJSModalEscAndBackdrop(t *testing.T) {
	src := readAppJS(t)
	for _, veil := range []string{"addVeil", "taskVeil", "vcVeil", "keyEditVeil", "keyShowVeil", "keyVeil"} {
		if !strings.Contains(src, veil+": () =>") {
			t.Errorf("弹窗 %s 未登记关闭回调（无 Esc / 遮罩出口）", veil)
		}
	}
	// Esc 必须从后往前关（一次关最上层那个），且跳过 data-lock。
	if !strings.Contains(src, "for (let i = ids.length - 1; i >= 0; i--)") {
		t.Error("Esc 未按层级从后往前关闭")
	}
	if !strings.Contains(src, "if (el.dataset.lock) continue;") {
		t.Error("Esc 未跳过 data-lock 弹窗（密钥门可被误关）")
	}
}

// TestAppJSExportCSVEscaping CSV 导出必须按 RFC 4180 转义 + 带 BOM。
//
// 牙齿：csvCell 不做引号翻倍、或漏掉 BOM → FAIL。
// 用 node/vm 执行真实函数，验证转义结果（不是断言源码字符串）。
func TestAppJSExportCSVEscaping(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; csv escaping test skipped")
	}
	script := `const fs = require('fs');
const vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const start = src.indexOf('function csvCell');
const end = src.indexOf('function exportStamp');
if (start < 0 || end < 0 || end < start) throw new Error('csv region not found');
const blobs = [];
const ctx = {
  Blob: function (parts, opts) { this.parts = parts; this.opts = opts; blobs.push(this); },
  URL: { createObjectURL: () => 'blob:x', revokeObjectURL: () => {} },
  document: { createElement: () => ({ click(){}, remove(){}, style:{} }), body: { appendChild(){} } },
  setTimeout: () => {},
  String, RegExp, JSON, Date,
};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + '\nthis.csvCell=csvCell;this.exportCSV=exportCSV;', ctx);
const cases = ['plain', 'a,b', 'say "hi"', 'l1\nl2', null, 42];
const cells = cases.map(c => ctx.csvCell(c));
ctx.exportCSV(['c1', 'c2'], [['a,b', 'x"y']], 't.csv');
const blob = blobs[0];
const text = blob.parts[0];
process.stdout.write(JSON.stringify({
  cells: cells,
  hasBOM: text.charCodeAt(0) === 0xFEFF,
  body: text.slice(1),
  mime: blob.opts.type,
}));`
	f, err := os.CreateTemp(t.TempDir(), "csv-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(script); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, err := exec.Command(node, f.Name(), "app.js").Output()
	if err != nil {
		t.Fatalf("csv node test failed: %v\n%s", err, nodeDiag(err, out))
	}
	got := strings.TrimSpace(string(out))
	// 期望（JSON 输出里 \n 是真实换行序列、\" 是转义引号）：
	//   a,b      → "a,b"        （含逗号 → 包裹）
	//   say "hi" → "say ""hi""  （含引号 → 包裹且内部翻倍）
	//   l1\nl2   → "l1\nl2"     （含换行 → 包裹）
	//   plain    → plain        （普通值不包裹）
	//   null     → 空
	for _, want := range []string{
		`\"a,b\"`,            // 含逗号 → 包裹
		`\"say \"\"hi\"\"\"`, // 含引号 → 包裹且翻倍
		`\"l1\nl2\"`,         // 含换行 → 包裹
		`"plain"`,            // 普通值不额外包裹
		`""`,                 // null → 空
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CSV 转义缺失 %s\n实际输出: %s", want, got)
		}
	}
	if !strings.Contains(got, `"hasBOM":true`) {
		t.Error("CSV 缺少 BOM：Excel 打开中文会乱码")
	}
	if !strings.Contains(got, "text/csv") {
		t.Error("CSV MIME 类型不对")
	}
}

// TestAppJSFirstLoadErrorVisible 首屏 overview 失败必须可见且可就地重试。
//
// 背景：start() 调 loadOverview(true)（quiet），而 catch 是 `if (!quiet) toast(...)`
// ——首屏失败一个字都不提示，统计卡永远停在「-」、账号表空白，用户只能猜。
//
// 牙齿：把 loadOverview 的 catch 改回单行 `if (!quiet) toast(...)` → FAIL。
func TestAppJSFirstLoadErrorVisible(t *testing.T) {
	src := readAppJS(t)
	// 必须区分「首次（无数据）」与「轮询失败」
	if !strings.Contains(src, "if (!overviewData) {") {
		t.Error("loadOverview 未区分首屏失败与轮询失败")
	}
	if !strings.Contains(src, "loadErrorHTML('读取账号池失败'") {
		t.Error("首屏失败未渲染可见错误块（仍是静默空白）")
	}
	if !strings.Contains(src, "bindRetry(tb, () => loadOverview(false))") {
		t.Error("首屏失败错误块没有就地重试")
	}
	// 日志页此前 `catch (e) { /* 概览已提示 */ }` 完全吞错。
	// 注意：新代码的注释里会**引用**这句旧代码作说明，所以不能直接搜字符串，
	// 而要确认 catch 分支里确实渲染了错误块。
	if !strings.Contains(src, "loadErrorHTML('读取日志失败'") {
		t.Error("日志页失败未渲染可见错误块（仍在静默吞错）")
	}
}

// TestAppJSSkeletonAndRetry 骨架屏与就地重试的对外契约。
func TestAppJSSkeletonAndRetry(t *testing.T) {
	src := readAppJS(t)
	for _, want := range []string{
		"function skelRows(n, cols)",
		"function skelCards(n)",
		"function skelKpis(n)",
		"function loadingMsg(text)",
		"function loadErrorHTML(title, detail, retryLabel)",
		"function bindRetry(host, fn)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("缺少 %q", want)
		}
	}
	// 用量页与积分页失败必须就地重试
	if !strings.Contains(src, "loadErrorHTML('读取用量失败', e.message, '重试')") {
		t.Error("用量页失败缺就地重试")
	}
	if !strings.Contains(src, "loadErrorHTML('读取积分构成失败', e.message, '重试')") {
		t.Error("积分构成页失败缺就地重试")
	}
	// 骨架屏样式必须在 index.html 里（否则 app.js 生成的 class 无样式）
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	for _, want := range []string{".skel {", ".load-error {", "@keyframes skel-shimmer", ".skel-msg .spinner"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html 缺少骨架/错误态样式 %q", want)
		}
	}
}

// TestAppJSEmptyStateCTA 空态必须给出主操作按钮（不是只有文案）。
func TestAppJSEmptyStateCTA(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), `id="btnScanAllEmpty"`) {
		t.Error("任务中心空态缺 CTA 按钮")
	}
	src := readAppJS(t)
	if !strings.Contains(src, "$('btnScanAllEmpty').onclick") {
		t.Error("空态 CTA 未绑定（点了没反应）")
	}
	if !strings.Contains(src, "async function runScanAll(btn)") {
		t.Error("扫描逻辑未提取为共用函数（header 与空态各写一份会漂移）")
	}
}

// TestAppJSPollingHygiene 轮询三件套：防慢响应覆盖 / 页面隐藏暂停 / 主题监听收窄。
//
// 牙齿：分别删掉 abortable 使用、visibilitychange 监听、matchMedia 监听 → FAIL。
func TestAppJSPollingHygiene(t *testing.T) {
	src := readAppJS(t)
	// ① 防慢响应覆盖：轮询路径必须用 abortable + isAbort
	for _, want := range []string{
		"function abortable(key)",
		"function isAbort(e)",
		"const ctl = abortable('overview');",
		"const d = await api('overview', { signal: ctl.signal });",
		"if (isAbort(e)) return;",
		"signal: abortable('packages').signal",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("防慢响应覆盖缺失：未找到 %q", want)
		}
	}
	// ② 页面隐藏暂停轮询
	if !strings.Contains(src, "document.addEventListener('visibilitychange'") {
		t.Error("缺 visibilitychange 监听：后台标签页仍在每 5 秒轮询")
	}
	if !strings.Contains(src, "if (!document.hidden) refTimer = setInterval(refreshVisible, 5000);") {
		t.Error("轮询未按 document.hidden 门控")
	}
	// ③ 主题监听收窄到 media query（原来挂在 window 的 change 上，
	//    页面上任何控件改动都会触发 applyTheme 重写图标 innerHTML）
	if strings.Contains(src, "addEventListener('change', applyTheme);") {
		t.Error("主题监听仍挂在 window change 上：任何控件改动都会重写图标")
	}
	if !strings.Contains(src, "matchMedia('(prefers-color-scheme: light)').addEventListener('change'") {
		t.Error("主题监听未收窄到系统配色偏好变化")
	}
}

// TestAppJSRequestPagination 请求记录分页：>50 条时必须分页渲染，避免
// 1000 行 × 10 列全量 DOM 构建卡死面板。
//
// 牙齿：把 slice(startIdx, startIdx + REQ_PAGE_SIZE) 改回渲染全量 list →
// 本测试 FAIL（reqPagerHtml 虽在但表体仍全量）。
func TestAppJSRequestPagination(t *testing.T) {
	src := readAppJS(t)
	for _, want := range []string{
		"const REQ_PAGE_SIZE = 50;",
		"let reqPage = 1;",
		"const totalPages = Math.max(1, Math.ceil(list.length / REQ_PAGE_SIZE));",
		"const pageItems = list.slice(startIdx, startIdx + REQ_PAGE_SIZE);",
		"function reqPagerHtml(total, page, totalPages)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("分页缺失：未找到 %q", want)
		}
	}
	// 渲染必须只画当前页（这是分页的意义所在）
	body := src[strings.Index(src, "function renderRequestTable()"):]
	body = body[:strings.Index(body, "/* reqPagerHtml")]
	if !strings.Contains(body, "pageItems.map(") {
		t.Error("renderRequestTable 未用 pageItems.map（仍全量渲染 list？）")
	}
	if strings.Contains(body, "tb.innerHTML = list.map(") {
		t.Error("renderRequestTable 仍全量渲染 list（分页失效）")
	}
	// 翻页按钮存在
	if !strings.Contains(src, "pagerHost.querySelectorAll('button[data-page]')") {
		t.Error("分页器按钮未绑定点击")
	}
	// HTML 侧有容器
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), `id="reqPager"`) {
		t.Error("index.html 缺 reqPager 容器")
	}
	if !strings.Contains(string(html), ".req-pager {") {
		t.Error("index.html 缺分页器样式")
	}
	// 筛选变化重置到第一页
	if !strings.Contains(src, "reqFilter.q = $('reqQ').value.trim(); reqPage = 1;") {
		t.Error("搜索筛选未重置分页到第一页")
	}
	if !strings.Contains(src, "reqFilter.outcome = $('reqOutcome').value;\n  reqPage = 1;") {
		t.Error("结果筛选未重置分页到第一页")
	}
}

// TestAppJSPlayground API 调试台（Playground）必须完整存在：
// 顶栏入口 + 弹窗表单 + 直连 /v1/chat/completions + 流式/非流式 + 可停止。
//
// 背景：此前面板只能在第三方客户端里验证网关（模型可用吗/鉴权对吗/流式正常吗），
// 竞品（new-api / ARP）都有内置在线测试。CSP connect-src 'self' 只放行同源
// fetch，所以必须直连本机 /v1/chat/completions（与客户端同鉴权口径）。
//
// 牙齿：删掉发送函数 / 去掉停止按钮 / 改调 /panel/api/* → FAIL。
func TestAppJSPlayground(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	h := string(html)
	for _, want := range []string{
		`id="btnPlayground"`, // 顶栏入口
		`id="pgVeil"`,        // 弹窗
		`id="pgModel"`,       // 模型下拉
		`id="pgPrompt"`,      // 用户消息
		`id="pgOut"`,         // 输出区
		`id="btnPgSend"`,
		`id="btnPgStop"`, // 停止（流式中断）
		`id="pgMeta"`,    // 耗时/用量
	} {
		if !strings.Contains(h, want) {
			t.Errorf("Playground HTML 缺失：未找到 %q", want)
		}
	}
	src := readAppJS(t)
	for _, want := range []string{
		"function openPlayground()",
		"async function sendPlayground()",
		"async function pgStreamRead(resp, startedAt)",
		"fetch('/v1/chat/completions', {",
		"new AbortController()",
		"pgAbort.abort()",
		"line.startsWith('data:')",
		"pgVeil: () => { pgStop();", // 纳入统一 Esc/遮罩关闭
	} {
		if !strings.Contains(src, want) {
			t.Errorf("Playground 逻辑缺失：未找到 %q", want)
		}
	}
	// 鉴权必须复用面板 Bearer key（与 /v1/* 同口径，测的就是客户端真实路径）
	if !strings.Contains(src, "headers['Authorization'] = 'Bearer ' + key;") {
		t.Error("Playground 未复用面板 Bearer 鉴权头")
	}
	// 发送中必须禁用按钮（防连点，对齐项目提交锁约定）
	if !strings.Contains(src, "$('btnPgSend').disabled = busy;") {
		t.Error("Playground 发送中未禁用按钮（可连点重复请求）")
	}
}

// TestAppJSCmdPaletteFixAndExpand 命令面板必须引用已定义的 cmdGo（而非此前
// 未定义的 switchView），且覆盖全部 8 个视图 + 高频操作。
//
// 背景：CMD_ITEMS 跳转命令此前直接调 switchView('...')，该函数从未定义——
// 点任何跳转命令都抛 ReferenceError，命令面板等于坏了 5/9。
//
// 牙齿：把 cmdGo 改回 switchView 调用 → FAIL。
func TestAppJSCmdPaletteFixAndExpand(t *testing.T) {
	src := readAppJS(t)
	if !strings.Contains(src, "function cmdGo(v) {") {
		t.Error("缺少 cmdGo 定义（跳转命令仍指向未定义函数）")
	}
	if strings.Contains(src, "switchView(") {
		t.Error("CMD_ITEMS 仍引用未定义的 switchView：点命令会抛 ReferenceError")
	}
	// 8 个视图跳转必须全（此前只覆盖 5 个）
	for _, v := range []string{"accounts", "usage", "taskscenter", "models", "keys", "logs", "config", "packages"} {
		want := "cmdGo('" + v + "')"
		if !strings.Contains(src, want) {
			t.Errorf("命令面板缺少视图跳转 %s", want)
		}
	}
	// 高频操作命令
	for _, want := range []string{
		"openPlayground()",
		"openAdd()",
		"$('btnScanAll') && $('btnScanAll').click()",
		"$('btnUsExport') && $('btnUsExport').click()",
		"$('btnCreditExport') && $('btnCreditExport').click()",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("命令面板缺少操作命令：未找到 %q", want)
		}
	}
}

// TestAppJSRequestRowKeyboard 请求记录行必须键盘可达（tabindex + Enter/Space
// 展开详情），且只有用户主动展开才滚动详情进视口。
//
// 背景：行详情此前只响应 tr.onclick，键盘/读屏用户无法展开；轮询刷新时
// renderReqDetail 也会被调，无条件 scrollIntoView 会打断阅读。
//
// 牙齿：去掉 tabindex / 去掉 keydown 分支 → FAIL。
func TestAppJSRequestRowKeyboard(t *testing.T) {
	src := readAppJS(t)
	for _, want := range []string{
		`tabindex="0"`,
		`aria-expanded="true"`,
		"tr.onkeydown = (ev) => {",
		"ev.key !== 'Enter' && ev.key !== ' '",
		"function toggleReqDetail(rid, scroll)",
		"host.scrollIntoView({ block: 'nearest', behavior: 'smooth' });",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("请求记录键盘可达缺失：未找到 %q", want)
		}
	}
	// 滚动必须由用户展开触发（scroll 参数），轮询刷新路径不滚动
	if !strings.Contains(src, "toggleReqDetail(tr.dataset.rid || '', true);") {
		t.Error("行点击/键盘未用 toggleReqDetail(..., true) 触发滚动")
	}
}

// TestAppJSUsageSkeletonCounts 用量骨架数量必须与实际渲染对齐：
// usStats 4 张（请求/Token/成功率/延迟）、usCreditStats 5 张。
func TestAppJSUsageSkeletonCounts(t *testing.T) {
	src := readAppJS(t)
	if !strings.Contains(src, "$('usStats').innerHTML = skelKpis(4);") {
		t.Error("usStats 骨架仍是旧数量（实际渲染 4 张卡，骨架 6 张会闪跳）")
	}
	if !strings.Contains(src, "$('usCreditStats').innerHTML = skelKpis(5);") {
		t.Error("usCreditStats 骨架仍是旧数量（实际渲染 5 张卡，骨架 4 张）")
	}
}

// TestAppJSNotifReadAll 通知中心必须有「全部已读」按钮且绑定生效。
func TestAppJSNotifReadAll(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), `id="btnNotifReadAll"`) {
		t.Error("index.html 缺少「全部已读」按钮")
	}
	src := readAppJS(t)
	if !strings.Contains(src, "$('btnNotifReadAll').onclick = (e) => {") {
		t.Error("「全部已读」按钮未绑定")
	}
	if !strings.Contains(src, "n.forEach(x => x.read = true);") {
		t.Error("「全部已读」未把通知全部置为已读")
	}
}

// TestAppJSResponsiveAccGrid 账号卡片网格在窄屏必须可收缩（min(340px, 100%)），
// 否则 320px 视口下卡片溢出横向滚动。
func TestAppJSResponsiveAccGrid(t *testing.T) {
	html, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	if !strings.Contains(string(html), `minmax(min(340px, 100%), 1fr)`) {
		t.Error(".acc-grid 未用 min(340px, 100%)：窄屏卡片会溢出")
	}
}

// TestAppJSUnknownsBadge 账号卡片的 unknown 成本徽章与总览提示行必须存在。
//
// 背景：AGENTS.md「Unknown 消解纪律」要求"不知道的事"可被看见——欠什么观测、
// 还差什么，不能只存在后端 registry 里让面板无从得知。这里断言两条展示出口：
//   - 账号卡片状态区：有 unknowns 时输出「N 未知成本」warn 徽章，title 带
//     question + requires（需要什么才能消解）；
//   - 账号池提示行：汇总「N 个 (账号,模型) 成本未知」，给一句话入口。
//
// 牙齿：删掉 renderAccountsUnsafe 里的 unkBadge() 调用或 loadOverview 的 unkNoteText() 调用 → FAIL。
//
// 2026-10-17（F-906）起拼接逻辑抽成纯函数 unkBadge() / unkNoteText()，本测试跟着换断言点，
// 但它仍是**源码文本**级断言；行为级断言见 frontend_test.go 的 TestAppJSUnknownBadgeAndNote
// （真跑 node 调用这两个函数，覆盖空态 / 计数 / question+requires / HTML 转义）。
func TestAppJSUnknownsBadge(t *testing.T) {
	src := readAppJS(t)
	for _, want := range []string{
		"function unkBadge(unknowns) {",
		"未知成本</span>",
		"function unkNoteText(accounts) {",
		"悬停账号卡片查看明细",
		"const unkTag = unkBadge(s.unknowns);",
		"const unkNote = unkNoteText(d.accounts);",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("unknowns 展示缺失：未找到 %q", want)
		}
	}
}

// TestAppJSPgDefaultCheapModel Playground 模型下拉必须默认推荐低成本主力。
//
// 背景：调试台默认选中目录第一个模型，而目录首个未必便宜（hy4 系实测
// 110~133 积分/M vs deepseek-v4.1-flash ~1 积分/M）——试玩一发自选就烧高成本。
// COST-CONTROL.md 已定 deepseek-v4.1-flash 为低成本主力，下拉默认选中它。
//
// 牙齿：删掉 pref 查找/selected 逻辑 → FAIL。
func TestAppJSPgDefaultCheapModel(t *testing.T) {
	src := readAppJS(t)
	for _, want := range []string{
		"pgModels.find(m => /deepseek-v4\\.1-flash$/i.test(m.id)) || pgModels[0]",
		"m.id === prefId ? ' selected' : ''",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("Playground 默认模型推荐缺失：未找到 %q", want)
		}
	}
}
