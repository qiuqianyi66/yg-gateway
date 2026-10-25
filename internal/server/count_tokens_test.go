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
	ascii := strings.Repeat("abcd", 100) // 400 字符 → 100 token（1/4 向上取整）
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
	if out.InputTokens != 101 {
		t.Fatalf("input_tokens = %d, want 101（内容 400 ASCII → 100，加 role 值 \"user\" 4 字符 → 1；role 计入=计费口径）", out.InputTokens)
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
	if out.InputTokens != 11 {
		t.Fatalf("input_tokens = %d, want 11（CJK 计 1 ×10，加 role 值 \"user\" → 1）", out.InputTokens)
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

func TestCountTokensBodyLimit(t *testing.T) {
	h := NewHandler(Config{MaxBodyBytes: 16})
	rec := doCountTokens(t, h, `{"messages":["`+strings.Repeat("x", 64)+`"]}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，got %d", rec.Code)
	}
}
