package panel

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestViewsComponentEmbedded 钉住各视图组件的嵌入与"不留副本"。
//
// 为什么需要：拆出去的函数若在 app.js 里还留一份，JS 的 `function` 重复声明**不报错**
// （后者覆盖前者），两份实现会静默漂移——这正是组件拆分最容易发生的退化。
// 用表驱动覆盖所有视图组件：新增视图只要往 moved 里加函数名即可。
func TestViewsComponentEmbedded(t *testing.T) {
	cases := []struct {
		name  string
		src   []byte
		funcs []string
	}{
		{
			name:  "app_views_models.js",
			src:   appViewsModelsJS,
			funcs: []string{"fmtK", "probeDays", "outCell", "fmtCostPpm", "costLine", "rateCell", "loadModels"},
		},
		{
			name: "app_views_packages.js",
			src:  appViewsPackagesJS,
			funcs: []string{"pkDetailGroups", "pkCreditOpacity", "pkExpiryText", "pkExpiryDateTime",
				"pkAccountSegments", "summarizeCreditDays", "renderExpiryDistribution", "renderPackages",
				"loadPackages", "expBatches", "expDaysLeft", "renderExpiry"},
		},
		{
			name: "app_views_keys.js",
			src:  appViewsKeysJS,
			funcs: []string{"buildKeyImportConfigs", "showKeyPlaintext", "writeClipboardText",
				"copyKeyPlaintext", "copyCfgText", "openKeyCfg", "resetKeySecret",
				"buildEnvFile", "downloadEnvFile", "showVeil", "hideVeil"},
		},
		{
			name: "app_views_usage.js",
			src:  appViewsUsageJS,
			funcs: []string{"usStat", "usKpi", "usMixBar", "usPct", "usRow", "fmtUSD", "usSortRows",
				"usDimHead", "usTabsHtml", "renderUsageDim", "usCreditHead", "cacheRateCell",
				"renderCreditDim", "renderUsage", "parsePointTime", "fmtTokTimeLabel",
				"renderUsageChart", "fmtTokTip", "warmUsageModelRates", "loadUsage", "bindRetry"},
		},
		{
			name: "app_views_accounts.js",
			src:  appViewsAccountsJS,
			funcs: []string{"accountsSig", "unkBadge", "unkNoteText", "renderAccounts",
				"renderAccountsUnsafe", "renderModelLocks", "renderModelLocksUnsafe", "loadOverview",
				"mdRateValue", "mdSearchText", "mdMatch", "mdSortList", "mdRowHtml", "renderModels",
				"resetModelFilter"},
		},
		{
			name:  "app_views_config.js",
			src:   appViewsConfigJS,
			funcs: []string{"dig", "put", "loadConfig", "collectConfig", "durationBad", "markDurationFields"},
		},
		{
			name: "app_views_tasks.js",
			src:  appViewsTasksJS,
			funcs: []string{"openTasks", "closeTasks", "loadTasks", "staskHTML", "copyText", "vcCard",
				"loadSchoolVouchers", "groupItems", "qrowHTML", "renderQueue", "updateProgress",
				"groupsFromQueue", "startQueuePolling", "reattachQueueView"},
		},
		{
			name: "app_views_logs.js",
			src:  appViewsLogsJS,
			funcs: []string{"loadLogs", "renderRequestMetrics", "reqMatch", "reqOutcomeTag", "reqTokenCell",
				"reqCreditCell", "updateSelButtons", "syncSelectAll", "reqSortValue", "renderRequestTable",
				"renderReqDetail", "reqPagerHtml", "requestLogText", "cacheRateText", "fmtBytes",
				"renderKeyList", "rpmLiveHTML", "refreshRpmLive", "keyCardHTML", "attachKeyCardEvents",
				"openKeyDialog", "closeKeyDialog", "getCurrentRealm", "genApiKey", "collectKeyFromDialog",
				"buildSavePayload", "saveKeyFromDialog", "toggleKeyEnabled", "deleteKey", "saveKeys"},
		},
	}
	for _, c := range cases {
		if len(c.src) == 0 {
			t.Errorf("%s 嵌入为空（go:embed 未生效？）", c.name)
			continue
		}
		for _, fn := range c.funcs {
			needle := []byte("function " + fn + "(")
			if !bytes.Contains(c.src, needle) {
				t.Errorf("%s 里找不到 function %s（拆分回退了？）", c.name, fn)
			}
			if bytes.Contains(appJS, needle) {
				t.Errorf("%s 在 app.js 里仍有定义：视图函数只许在 %s 有一份实现", fn, c.name)
			}
		}
	}
}

// TestModelsViewScriptServing 走完整路由：200 + JS Content-Type + 非空 + CSP 头。
func TestModelsViewScriptServing(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/app_views_models.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /panel/app_views_models.js code=%d（routes() 里漏注册？）", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺 CSP 头")
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("app_views_models.js 响应为空")
	}
}

// TestViewsComponentsAreFunctionOnly 视图组件里**只许有声明**（函数 / const / let / var）。
//
// 依据：这些文件排在 app.js **之前**加载。任何其他顶层语句——事件绑定 `$('x').onclick=…`、
// 顶层 `for` 循环、顶层调用 `bindRetry($('x'), …)`——都会在 app.js 的 `$`/state 就绪前执行，
// 浏览器里直接 `ReferenceError: Cannot access '$' before initialization` → 面板白屏。
//
// 判据从"黑名单关键字"改成"白名单声明形态"（2026-11-02 实测教训：黑名单漏掉了顶层
// `for (…) { const el = $(id) }` 与顶层 `bindRetry($(…))` 两种形态，它们照样白屏）。
func TestViewsComponentsAreFunctionOnly(t *testing.T) {
	for _, name := range scriptFiles(t) {
		if !strings.HasPrefix(name, "app_views_") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if line == "" || line[0] == ' ' || line[0] == '\t' {
				continue // 缩进行属于函数体 / 块体
			}
			ok := strings.HasPrefix(line, "function ") || strings.HasPrefix(line, "async function ") ||
				strings.HasPrefix(line, "const ") || strings.HasPrefix(line, "let ") ||
				strings.HasPrefix(line, "var ") || strings.HasPrefix(line, "/*") ||
				strings.HasPrefix(line, "*") || strings.HasPrefix(line, "//") ||
				strings.HasPrefix(line, "}") || strings.HasPrefix(line, ")")
			if !ok {
				t.Errorf("%s:%d 出现非声明顶层语句：视图组件只许放声明（函数/const/let/var），"+
					"否则会在 app.js 的 $ 初始化前执行 → 面板白屏\n  %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
