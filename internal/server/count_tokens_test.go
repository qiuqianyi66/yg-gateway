// count_tokens_test.go V3-1：估算数值锚定（ASCII/CJK/混合/工具段）、畸形体、
// body 上限、诚实标注（estimated=true）。启发式权重是 derivation——数值锚定
// 防止有人无意改权重而无人知晓。
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doCountTokens(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.countTokens(rec, httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body)))
	return rec
}

func TestCountTokensASCII(t *testing.T) {
	h := NewHandler(Config{})
	ascii := strings.Repeat("abcd", 100) // 400 字符 → 152 token（0.38/字符，2026-11-26 真机校准）
	rec := doCountTokens(t, h, `{"model":"m","messages":[{"role":"user","content":"`+ascii+`"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		InputTokens int  `json:"input_tokens"`
		Estimated   bool `json:"estimated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.InputTokens != 157 {
		t.Fatalf("input_tokens = %d, want 157（400 ASCII → 152 + role \"user\" 2 + 信封/结构字节 3，结构按 0.1/字节计）", out.InputTokens)
	}
	if !out.Estimated {
		t.Fatal("必须带 estimated=true（诚实口径：这是启发式不是上游精确值）")
	}
}

func TestCountTokensCJK(t *testing.T) {
	h := NewHandler(Config{})
	cjk := strings.Repeat("测", 10) // 10 个 CJK 全角 → 10 token（计 1）
	rec := doCountTokens(t, h, `{"model":"m","messages":[{"role":"user","content":"`+cjk+`"}]}`)
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.InputTokens != 18 {
		t.Fatalf("input_tokens = %d, want 18（护栏口径取两套估算的较大者：此形状影子=18 > 主口径 15）", out.InputTokens)
	}
}

func TestCountTokensToolsCounted(t *testing.T) {
	h := NewHandler(Config{})
	base := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	withTool := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"` +
		strings.Repeat("t", 200) + `","description":"` + strings.Repeat("d", 400) + `"}]}`
	nBase := doCountTokens(t, h, base)
	nTool := doCountTokens(t, h, withTool)
	var a, b struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(nBase.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(nTool.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.InputTokens <= a.InputTokens {
		t.Fatalf("tools 段应计入（base=%d withTool=%d）", a.InputTokens, b.InputTokens)
	}
}

func TestCountTokensMalformedAndEmpty(t *testing.T) {
	h := NewHandler(Config{})
	if rec := doCountTokens(t, h, `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，got %d", rec.Code)
	}
	// 局部畸形子段宽容（段按空处理，不拒整体）；空对象 → 0 token。
	rec := doCountTokens(t, h, `{"system":123,"messages":[{"content":{"weird":[1,2]}}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("宽容形态应 200，got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.InputTokens < 0 {
		t.Fatalf("估算不得为负：%d", out.InputTokens)
	}
}

// TestEstimateBreakdownParts 估算构成必须可分解：宽字符/其余字符/图片块各自计数，
// 总数由三者重建（2026-11-26 起归档同行落构成，用于按组成拟合权重）。
func TestEstimateBreakdownParts(t *testing.T) {
	raw := []byte(`{"messages":[{"content":"中文ab"},{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}]}`)
	var b estBreakdown
	b = estimateBreakdownJSON(raw)
	// 字符串叶子：内容 "中文ab"（wide 2 / other 2）+ 块类型 "image_url"（other 9）；图片 1 块。
	if b.Wide != 2 || b.Other != 11 || b.Images != 1 {
		t.Fatalf("构成错误: %+v（want wide=2 other=11 images=1）", b)
	}
	if got, want := b.tokens(), 2+5+8+imageTokenCost; got != want {
		t.Fatalf("tokens=%d want=%d（总数必须由三者 + 结构字节重建）", got, want)
	}
	// 结构字节 = 原始 JSON 长度 - 已计字符串字节（图片 base64 不计入，避免虚高）。
	if b.StructBytes != len(raw)-b.Bytes || b.StructBytes <= 0 {
		t.Fatalf("struct_bytes=%d bytes=%d raw=%d（want raw-bytes>0，用于归因常数级偏差）", b.StructBytes, b.Bytes, len(raw))
	}
	// Anthropic base64 源整块计一张图，块内字符串（含 media_type）不再按字符计；
	// 外层 content 块类型 "image" 仍按文本计（other 5）。
	raw2 := []byte(`{"messages":[{"content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJDQUJD"}}]}]}`)
	b2 := estimateBreakdownJSON(raw2)
	if b2.Images != 1 || b2.Other != 5 || b2.Wide != 0 {
		t.Fatalf("anthropic 源构成错误: %+v（want images=1 other=5）", b2)
	}
}

// TestEstimateShadowTokens 影子口径只做落档：总字节/3.4 + 350/图，必须与主口径分开算。
// 退化场景（纯 ASCII 400 字符、0 图）应≈主口径量级（0.38 vs 0.294/字符，不要求相等）。
func TestEstimateShadowTokens(t *testing.T) {
	var b estBreakdown
	b.Wide = 2   // 6 字节
	b.Other = 11 // 11 字节
	b.StructBytes = 7
	b.Images = 1
	// 24 字节 → ceil(24*10/34)=8，+ 350 = 358
	if got, want := b.shadowTokens(), 358; got != want {
		t.Fatalf("shadowTokens=%d want=%d（字节口径 + 350/图）", got, want)
	}
	if b.tokens() == b.shadowTokens() {
		t.Fatal("主口径与影子口径不应恒等（否则并行落档没有裁决价值）")
	}
}

// TestGuardTokensTakesMax 护栏口径必须取两套估算的较大者：
// 结构字节多、图片少 → 影子更大（主口径会低估）；图片多 → 主口径更大。
func TestGuardTokensTakesMax(t *testing.T) {
	var b estBreakdown
	b.Wide, b.Other, b.StructBytes = 100, 1000, 50000
	if b.guardTokens() != b.shadowTokens() {
		t.Fatalf("结构重、无图时应取影子：guard=%d shadow=%d main=%d", b.guardTokens(), b.shadowTokens(), b.tokens())
	}
	b.Images = 50
	if b.guardTokens() != b.tokens() {
		t.Fatalf("50 张图时应取主口径：guard=%d shadow=%d main=%d", b.guardTokens(), b.shadowTokens(), b.tokens())
	}
	if b.guardTokens() < b.tokens() || b.guardTokens() < b.shadowTokens() {
		t.Fatal("guardTokens 不可能低于任一单口径")
	}
}

func TestCountTokensBodyLimit(t *testing.T) {
	h := NewHandler(Config{MaxBodyBytes: 16})
	rec := doCountTokens(t, h, `{"messages":["`+strings.Repeat("x", 64)+`"]}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，got %d", rec.Code)
	}
}

// 图片载荷按固定视觉成本计，不按 base64 字符（2026-11-26 实弹回归：
// DSH 考试会话 6-7 张截图被估成 107 万 token 假性击穿 1M 护栏，
// 上游实际 usage 仅 5-8 万）。

func countInput(t *testing.T, h *Handler, body string) int {
	t.Helper()
	rec := doCountTokens(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.InputTokens
}

func TestCountTokensOpenAIImageDataURL(t *testing.T) {
	h := NewHandler(Config{})
	// 60 万 base64 字符 ≈ 450KB 截图；按字符估会是 15 万 token，按图片应 ~2000。
	b64 := strings.Repeat("QUJD", 150000)
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + b64 + `"}}]}]}`
	n := countInput(t, h, body)
	if n > imageTokenCost+100 {
		t.Fatalf("data:image/ 载荷应按固定成本计，got %d（want ≤ %d）", n, imageTokenCost+100)
	}
	if n < imageTokenCost {
		t.Fatalf("图片成本不应低于固定值：%d", n)
	}
}

func TestCountTokensAnthropicBase64Source(t *testing.T) {
	h := NewHandler(Config{})
	b64 := strings.Repeat("QUJD", 150000)
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}`
	n := countInput(t, h, body)
	if n > imageTokenCost+100 {
		t.Fatalf("base64 source 应按固定成本计，got %d", n)
	}
}

func TestCountTokensSevenScreenshotsUnderGuardrail(t *testing.T) {
	h := NewHandler(Config{})
	// 事故形状：7 张 474KB 级截图 + 少量文本。修复前估算 >1M，修复后应远低于护栏。
	var blocks []string
	for i := 0; i < 7; i++ {
		blocks = append(blocks, `{"type":"image_url","image_url":{"url":"data:image/png;base64,`+strings.Repeat("QUJD", 158000)+`"}}`)
	}
	body := `{"model":"m","messages":[{"role":"user","content":[` + strings.Join(blocks, ",") + `]}]}`
	n := countInput(t, h, body)
	if n >= 1000000 {
		t.Fatalf("7 张截图估算 %d 仍击穿 1M 护栏（事故回归）", n)
	}
	if n > 7*imageTokenCost+1000 {
		t.Fatalf("估算 %d 超出 7×固定成本+余量", n)
	}
}

func TestCountTokensHTTPImageURLStillText(t *testing.T) {
	h := NewHandler(Config{})
	// 非内联图片（http URL）是短字符串，照旧按文本计，不受图片规则影响。
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`
	n := countInput(t, h, body)
	if n >= imageTokenCost {
		t.Fatalf("http URL 不应按整图成本计：%d", n)
	}
}

// imageTokenCost 是校准值（2026-11-26 拟合 1248 + 16% 余量），不是随手写的常数。
// 这条守卫防止后续调参把护栏调回「拍脑袋」区间：
//   - 低于 1200 会跌破拟合值，图多请求转为系统性低估（漏拦）；
//   - 高于 1700 会让图多请求回到 1.4 倍以上的保守偏差（假性 413）。
func TestImageTokenCostInsideCalibratedBand(t *testing.T) {
	if imageTokenCost < 1200 || imageTokenCost > 1700 {
		t.Fatalf("imageTokenCost = %d，超出校准区间 [1200,1700]（拟合值 1248，见 count_tokens.go 注释）", imageTokenCost)
	}
}
