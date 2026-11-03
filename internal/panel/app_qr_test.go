package panel

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppQRScriptServing 走**完整路由**（p.ServeHTTP）而不是直调 handler：
// 拆分成独立文件后，最容易漏的是 routes() 里那行注册——直调 handler 的测试对漏注册
// 完全无感（运行时才是 404，面板二维码变空白）。
func TestAppQRScriptServing(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "test-key"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panel/app_qr.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /panel/app_qr.js code=%d（routes() 里漏注册？）", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("缺 CSP 头")
	}
	if len(rec.Body.Bytes()) == 0 {
		t.Fatal("app_qr.js 响应为空（go:embed 未生效？）")
	}
}

// TestAppQRComponentEmbedded 钉住：QR 组件确实被 go:embed 进了二进制，且不再是空壳。
//
// 为什么需要：拆分后 app_qr.js 成了独立资源，embed 指令漏写/文件名打错时 Go 编译期
// **不会**报错（go:embed 只校验模式匹配），而面板的券码二维码会在运行时静默变空白。
// 与 opsagent_view_test.go 里那条"嵌入为空"的断言同一纪律。
func TestAppQRComponentEmbedded(t *testing.T) {
	if len(appQRJS) == 0 {
		t.Fatal("app_qr.js 嵌入为空（go:embed 未生效？）")
	}
	if !bytes.Contains(appQRJS, []byte("function qrMatrix")) {
		t.Fatal("app_qr.js 里找不到 function qrMatrix —— 拆分搬错文件了？")
	}
	// 反查：QR 实现不许再留在 app.js 里（否则就是两份实现，改一处忘一处）。
	if bytes.Contains(appJS, []byte("function qrMatrix")) {
		t.Fatal("app.js 里仍有 function qrMatrix：QR 编码器已拆到 app_qr.js，不许留副本")
	}
}

// TestAppQRGolden 用金样数字钉住 QR 编码器的**输出**（尺寸 / 深色模块数 / rect 数）。
//
// 为什么钉数字：QR 的位布局、掩码、Reed-Solomon 纠错任一环节改错，扫不出来但
// 矩阵一定变——尺寸或深色模块数会立刻变。原实现"已用 python qrcode 逐像素交叉验证"，
// 本测试把那次数值固化成回归网，改动 QR 代码必须同步更新金样（有意摩擦）。
func TestAppQRGolden(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; QR golden skipped")
	}
	harness := `const fs = require('fs'), vm = require('vm');
const src = fs.readFileSync(process.argv[2], 'utf8');
const sb = { TextEncoder, console, Error, Math, Array, Number, String, Object };
vm.createContext(sb);
vm.runInContext(src, sb, { filename: 'app_qr.js' });
const out = [];
for (const t of ['TEST123', 'A', 'https://example.com/redeem?code=ABC123XYZ']) {
  const M = sb.qrMatrix(t);
  const svg = sb.qrSVG(M, 148);
  out.push({ text: t, n: M.length, dark: M.flat().filter(Boolean).length, rects: (svg.match(/<rect /g) || []).length });
}
console.log(JSON.stringify(out));
`
	hf, err := os.CreateTemp(t.TempDir(), "qr-*.cjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hf.WriteString(harness); err != nil {
		t.Fatal(err)
	}
	hf.Close()

	path, err := filepath.Abs("app_qr.js")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, hf.Name(), path).Output()
	if err != nil {
		t.Fatalf("node 执行 QR 组件失败: %s", nodeDiag(err, out))
	}
	var got []struct {
		Text  string `json:"text"`
		N     int    `json:"n"`
		Dark  int    `json:"dark"`
		Rects int    `json:"rects"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		t.Fatalf("解析 node 输出失败: %v\nraw=%s", err, strings.TrimSpace(string(out)))
	}
	want := []struct {
		text string
		n    int
		dark int
	}{
		{"TEST123", 21, 236},
		{"A", 21, 226},
		{"https://example.com/redeem?code=ABC123XYZ", 29, 436},
	}
	if len(got) != len(want) {
		t.Fatalf("样本数不符: got %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Text != w.text || g.N != w.n || g.Dark != w.dark {
			t.Errorf("QR 金样漂移 text=%q: got n=%d dark=%d, want n=%d dark=%d"+
				"（改了 QR 编码器就要同步更新金样，并重跑面板券码扫码验证）",
				w.text, g.N, g.Dark, w.n, w.dark)
		}
		if g.Rects != g.Dark {
			t.Errorf("QR 金样 text=%q: rect 数 %d 与深色模块数 %d 不一致（SVG 生成错）", w.text, g.Rects, g.Dark)
		}
	}
}
