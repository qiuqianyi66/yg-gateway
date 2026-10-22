package panel

// R-101（F-101 面板显式 ACL 提示）与 R-106（OpenRouter 费率 inference 标注）
// 的面板展示断言。判定口径：serve index.html 的实际输出里必须含对应文案与挂载点；
// 两个新 JS 必须经路由可下载且语法可被 node 解析。
import (
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIndexHTMLFeatureCopy 页面骨架里必须带 R-101 静态说明、R-106 说明行容器、
// 以及两个新脚本的外链（CSP script-src 'self' 只放行同源外链）。
func TestIndexHTMLFeatureCopy(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	want := []string{
		// R-101：账号池区静态说明 + 添加账号弹窗底部说明
		"凭证文件（auths/*.json）由服务写回后自动收紧 ACL（仅当前用户 + SYSTEM）",
		"新增/导入的凭证落盘后由服务自动收紧 ACL（仅当前用户 + SYSTEM）",
		"lock-auths.ps1",
		// R-106：用量区说明行容器（savings_note.js 渲染目标）
		`id="savingsLine"`,
		"美元折算为估算值（inference），非上游账单实测",
		// 两个新脚本必须被 index.html 引用，否则白挂
		`src="acl_notice.js"`,
		`src="savings_note.js"`,
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("index.html 缺 %q", w)
		}
	}
}

// TestFeatureJSServed 两个新 JS 必须经同源路由可取（与 appScript 同口径，免鉴权），
// 且正文含各自的核心文案（防 embed 挂错文件）。
func TestFeatureJSServed(t *testing.T) {
	p := newTestPanel()
	cases := []struct {
		path string
		want string
	}{
		{"/panel/acl_notice.js", "凭证已落盘：写回后服务会自动收紧 ACL"},
		{"/panel/savings_note.js", "本窗口未估算：无模型定价或窗口内无计费用量"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != 200 {
			t.Errorf("GET %s = %d, want 200", c.path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("GET %s 正文缺 %q", c.path, c.want)
		}
	}
}

// TestFeatureJSSyntax 两个新 JS 必须过 node --check（同 TestAppJSSyntax 口径）。
func TestFeatureJSSyntax(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping JS syntax check")
	}
	for _, f := range []string{"acl_notice.js", "savings_note.js"} {
		path, err := filepath.Abs(f)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(node, "--check", path).Output()
		if err != nil {
			t.Fatalf("%s syntax error:\n%s", f, nodeDiag(err, out))
		}
	}
}
