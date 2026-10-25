// count_tokens.go Anthropic /v1/messages/count_tokens（ROADMAP v3 V3-1）。
//
// 为什么需要：Claude Code 原生调用它做上下文核算；未实现时客户端退化估算。
// 实现口径：不引分词依赖——三段解析为通用值后**只对字符串叶子估**：CJK 全角
// （≥0x2E80，覆盖 Han/假名/谚文）计 1、其余字符计 1/4（向上取整）。结构与数值
// 字段不计——它们不是模型看到的内容主体（第一版「整串估」实测被 role/model
// 信封高估 8%，弃）。权重是 derivation（真机 prompt_tokens 校准后可调）。
//
// 诚实口径：响应在 Anthropic 原形状（input_tokens）之上加 "estimated": true——
// 多余字段对客户端无害，且不伪装成上游精确值（四态纪律）。
// 段落解析宽容：某段缺失/形态异常按空处理（count_tokens 的宽容语义），仅整体
// JSON 非法才 400。
package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// estimateTokens 启发式：CJK 全角计 1、其余计 1/4 向上取整。
func estimateTokens(s string) int {
	wide, other := 0, 0
	for _, r := range s {
		if r >= 0x2E80 {
			wide++
		} else {
			other++
		}
	}
	return wide + (other+3)/4
}

// estimateTokensValue 递归累加通用值里全部字符串叶子的估算；数值/布尔不计。
func estimateTokensValue(v any) int {
	switch t := v.(type) {
	case string:
		return estimateTokens(t)
	case []any:
		n := 0
		for _, e := range t {
			n += estimateTokensValue(e)
		}
		return n
	case map[string]any:
		n := 0
		for _, e := range t {
			n += estimateTokensValue(e)
		}
		return n
	}
	return 0
}

// estimateTokensJSON 解析一段 JSON 并估算其字符串叶子；空段/不可解析（畸形子段）
// 返回 0（宽容语义，不因局部畸形拒整体）。
func estimateTokensJSON(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0
	}
	return estimateTokensValue(v)
}

// countTokens POST /v1/messages/count_tokens。入站 body 上限与聊天同源
// （MaxBodyBytes，F-001 同口径）。
func (h *Handler) countTokens(w http.ResponseWriter, r *http.Request) {
	var body []byte
	var err error
	if h.cfg.MaxBodyBytes > 0 {
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	} else {
		body, err = io.ReadAll(r.Body)
	}
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body too large")
			return
		}
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "read body: "+err.Error())
		return
	}
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages json.RawMessage `json:"messages"`
		Tools    json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body: "+err.Error())
		return
	}
	n := estimateTokensJSON(req.System) + estimateTokensJSON(req.Messages) + estimateTokensJSON(req.Tools)
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": n, "estimated": true})
}
