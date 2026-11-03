package panel

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 前端脚本加载顺序的**单一来源**：读面板实际下发的 <script src>，不手抄。
//
// 为什么（2026-11-02 前端组件库拆分第 1 步）：此前所有前端测试都硬读 "app.js"，
// 全包共 26 处引用 + 28 处 `src.indexOf('function x')` 切片。一旦把 app.js 拆成多个
// 组件文件，这些测试要么读不到源码、要么切片落空——而且**不报错**（空切片仍能跑，
// 断言变成空转）。所以先把"取源码"这件事收成一个入口，拆分时改这一处即可。
//
// 2026-11-03 二次调整：标签改由 index.go 的 panelScripts 生成（index.html 只留
// `<!--panel-scripts-->` 占位符），因此本入口必须读 renderIndexHTML() 的产物。
// 若仍读原始 index.html，拿到的会是空清单——所有依赖本入口的测试静默空转。
var scriptSrcRe = regexp.MustCompile(`<script\s+src="([^"]+)"`)

// scriptFiles 返回面板**实际下发**的脚本加载顺序（相对路径，如 "app.js"）。
//
// 只取 `<script src="...">`：下发页面被 TestIndexHTMLNoInlineScript /
// TestIndexReferencesExternalScript 保证无内联脚本，所以这个列表就是浏览器实际
// 执行的全部脚本，顺序即依赖顺序。
func scriptFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, m := range scriptSrcRe.FindAllStringSubmatch(string(renderIndexHTML()), -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatal("下发页面里没有 <script src>：脚本清单为空（占位符没展开 / 清单被清空，拆分会静默失效）")
	}
	return out
}

// TestIndexHTMLHasNoHandWrittenAssetTags 骨架里不许再手写 <script src> / <link>。
//
// 为什么单列：资源清单必须只在 index.go 的 panelScripts / panelStyles 一处维护。
// 手写标签会立刻复活「加一个组件吃一行 index.html 棘轮额度」的结构性增长——
// 2026-11-03 加一个 gamify 组件就把 maxFrontendLines 1842 顶成 1844、门禁判红。
//
// 牙齿：把任意一条 `<script src="x.js"></script>` 抄回 index.html → 本用例立刻红。
func TestIndexHTMLHasNoHandWrittenAssetTags(t *testing.T) {
	raw, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatalf("读 index.html 失败: %v", err)
	}
	body := string(raw)
	if m := scriptSrcRe.FindStringSubmatch(body); m != nil {
		t.Errorf("index.html 不应手写 <script src>（发现 %q）：清单只在 index.go panelScripts 维护", m[1])
	}
	if strings.Contains(body, `<link rel="stylesheet"`) {
		t.Error("index.html 不应手写 <link rel=stylesheet>：清单只在 index.go panelStyles 维护")
	}
}

// TestIndexHTMLPlaceholdersReplaced 占位符必须被展开，且展开结果覆盖清单每一项。
//
// 占位符改名而 renderIndexHTML 没同步时，strings.Replace 会**静默不替换** →
// 页面没有任何脚本 → 白屏，而 Go 侧编译与测试全绿（正是本仓最怕的「不报错的失效」）。
// 所以做双向断言：占位符残留要红，清单项在产物里缺失也要红。
func TestIndexHTMLPlaceholdersReplaced(t *testing.T) {
	rendered := string(renderIndexHTML())

	for _, ph := range []string{panelStylesPlaceholder, panelScriptsPlaceholder} {
		if strings.Contains(rendered, ph) {
			t.Errorf("下发页面仍残留占位符 %q —— renderIndexHTML 的 Replace 没命中（占位符改名未同步？）", ph)
		}
	}
	for _, name := range panelScripts {
		want := `<script src="` + name + `"></script>`
		if !strings.Contains(rendered, want) {
			t.Errorf("下发页面缺 %s（panelScripts 里有、展开后没有）", want)
		}
	}
	for _, name := range panelStyles {
		want := `<link rel="stylesheet" href="` + name + `">`
		if !strings.Contains(rendered, want) {
			t.Errorf("下发页面缺 %s（panelStyles 里有、展开后没有）", want)
		}
	}
}

// scriptBundle 按加载顺序拼接全部前端脚本源码，供测试做文本检索与切片。
//
// 每段前加一行文件标记注释：拆分后若某个测试的 indexOf 切片跨了文件边界，
// 报错信息里能直接看出是哪两个文件之间断的（否则只能看到一串空字符串）。
func scriptBundle(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	for _, name := range scriptFiles(t) {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读前端脚本 %s 失败（index.html 引用了它，但文件不存在？）: %v", name, err)
		}
		b.WriteString("/* ==== " + name + " ==== */\n")
		b.Write(src)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// scriptBundlePath 把拼接结果写入临时文件并返回路径（node harness 用 argv[2] 读）。
//
// 用拼接文件而不是逐个跑 node：这些 harness 依赖"全局作用域共享"（经典脚本、无模块），
// 逐个执行会丢跨文件符号。顺序与浏览器一致，语义等价。
func scriptBundlePath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bundle.js")
	if err := os.WriteFile(p, scriptBundle(t), 0o644); err != nil {
		t.Fatalf("写脚本拼接文件失败: %v", err)
	}
	return p
}

// funcsOnlyWithConstsPath 在 funcsOnly 基础上补入指定的顶层 `const NAME = …;` 声明。
//
// 为什么需要：有些切片类测试的起点是一个**数据常量**（如 PK_DEFAULT_DETAIL_LIMIT、
// PK_ACCOUNT_COLORS），而终点是拆到视图文件里的函数。跨文件切片会把中间的顶层语句
// 卷进来（见 funcsOnly 注释）。这里只补"纯数据常量"，不补任何顶层执行语句。
func funcsOnlyWithConstsPath(t *testing.T, constNames ...string) string {
	t.Helper()
	bundle := string(scriptBundle(t))
	out := strings.Builder{}
	// 常量必须写在函数**之前**：切片类测试用 `indexOf(常量)` 作起点、`indexOf(函数)` 作终点，
	// 顺序反了 slice(start,end) 会得到空串（症状是 "xxx is not defined"）。
	for _, name := range constNames {
		block, ok := topLevelConst(bundle, name)
		if !ok {
			t.Fatalf("bundle 里找不到顶层 const %s（改名了？）", name)
		}
		out.WriteString(block)
		out.WriteString("\n")
	}
	out.Write(funcsOnly(t, []byte(bundle)))
	p := filepath.Join(t.TempDir(), "funcs-consts.js")
	if err := os.WriteFile(p, []byte(out.String()), 0o644); err != nil {
		t.Fatalf("写函数+常量拼接文件失败: %v", err)
	}
	return p
}

// topLevelConst 取出顶层 `const NAME = …;` 的完整文本（支持跨行数组/对象字面量）。
func topLevelConst(src, name string) (string, bool) {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "const "+name+" ") {
			continue
		}
		depth := 0
		var b strings.Builder
		for j := i; j < len(lines); j++ {
			b.WriteString(lines[j])
			b.WriteString("\n")
			for _, r := range lines[j] {
				switch r {
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
			}
			if depth == 0 && strings.HasSuffix(strings.TrimSpace(lines[j]), ";") {
				return b.String(), true
			}
		}
		return b.String(), true
	}
	return "", false
}

// funcsOnlyBundlePath 生成"只含顶层函数声明"的脚本拼接文件，供**切片类**测试使用。
//
// 为什么需要（2026-11-02 组件库拆分第 3 步实测）：这批测试的取源方式是
// `src.indexOf('function A')` … `src.indexOf('function B')` 后切片执行。拆分前 A、B 同在
// app.js，两者之间只有纯函数；拆分后 A 可能在 app_core.js、B 在 app.js，切片会**跨越文件**
// 把 app.js 的顶层语句（`let theme = localStorage.getItem(...)`）一起卷进来 ——
// 在裸 node 上下文里直接 ReferenceError，症状是"面板代码坏了"，实际是取源方式坏了。
//
// 解法：喂给它们一份只含顶层 `function` 声明的源码（按 bundle 顺序），切片再跨文件也不会
// 带进任何顶层副作用语句。
func funcsOnlyBundlePath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "funcs-only.js")
	if err := os.WriteFile(p, funcsOnly(t, scriptBundle(t)), 0o644); err != nil {
		t.Fatalf("写函数拼接文件失败: %v", err)
	}
	return p
}

// funcsOnly 从 JS 源码里抽出所有**顶层** `function name(...) {...}` 声明（按出现顺序）。
//
// 判定：行首 `function `（或 `async function `）→ 从该行起按花括号配平吃到闭合。
// 只处理本仓的统一格式（顶层函数一律顶格声明），不追求通用 JS 解析器。
func funcsOnly(t *testing.T, src []byte) []byte {
	t.Helper()
	lines := strings.Split(string(src), "\n")
	var out strings.Builder
	for i := 0; i < len(lines); i++ {
		if !(strings.HasPrefix(lines[i], "function ") || strings.HasPrefix(lines[i], "async function ")) {
			continue
		}
		depth := 0
		started := false
		for ; i < len(lines); i++ {
			out.WriteString(lines[i])
			out.WriteString("\n")
			for _, r := range lines[i] {
				switch r {
				case '{':
					depth++
					started = true
				case '}':
					depth--
				}
			}
			if started && depth == 0 {
				break
			}
		}
	}
	if out.Len() == 0 {
		t.Fatal("没有抽到任何顶层函数声明（源码格式变了？）")
	}
	return []byte(out.String())
}
