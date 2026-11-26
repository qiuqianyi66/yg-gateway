// count_tokens.go Anthropic /v1/messages/count_tokens（ROADMAP v3 V3-1）。
//
// 为什么需要：Claude Code 原生调用它做上下文核算；未实现时客户端退化估算。
// 实现口径：不引分词依赖——三段解析为通用值后**只对字符串叶子估**：CJK 全角
// （≥0x2E80，覆盖 Han/假名/谚文）计 1、其余字符计 0.38（向上取整，2026-11-26
// 真机校准前为 1/4）。结构与数值
// 字段不计——它们不是模型看到的内容主体（第一版「整串估」实测被 role/model
// 信封高估 8%，弃）。图片载荷（data:image/ URL、base64 source）按固定视觉
// 成本计，不按字符（2026-11-26，防截图会话假性击穿护栏）。权重是 derivation
// （真机 prompt_tokens 校准后可调）。
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
	"strings"
)

// imageTokenCost 单张图片的固定视觉成本估算（derivation，非上游精确值）。
// 为什么需要：base64 图片载荷不是模型按字符计费的文本——上游按视觉块计
// （千级 token/图），而字符串叶子启发式会把 474KB 截图的 ~63 万 base64 字符
// 估成 ~16 万 token，6-7 张图即假性击穿 1M 护栏（2026-11-26 DSH 考试会话
// 实弹：估算 107 万 vs 上游实际 usage 5-8 万）。图片一律按固定成本计，
// 真实偏差由 /status calibration 样本持续校准。
//
// 取值来源（2026-11-26 校准）：两个真实会话重放 552 组「估算 vs 上游 usage」配对
// 样本、3482 次图片出现，最小二乘拟合单图成本 = 1248；原拍 2000 使图多步骤
// 估算/实际 p90 = 1.48（偏保守 48%）。取 1450 = 拟合值 + 16% 余量（护栏宁保守
// 勿漏拦），校准后 p90 ≈ 1.39、中位 ≈ 0.99。样本不足或换模型时应重拟合。
const imageTokenCost = 1450

// isImageDataURL 识别 OpenAI 形状的内联图片（data:image/...;base64,...）。
func isImageDataURL(s string) bool {
	return strings.HasPrefix(s, "data:image/")
}

// textTokenPermille 非 CJK 字符的千字符 token 成本（0.38 token/字符）。
// 取值来源（2026-11-26 真实流量校准）：DSH 长会话 6 条配对样本，估算/上游
// 比值稳定在 0.655–0.660（1/4 字符口径在 JSON/代码/路径为主的载荷上系统性
// 低估 34%）。按「全靠 ASCII」反解，上游真实密度 ≈ 0.382 token/字符；取 0.38
// 使比值回到 1.0 附近。低估方向危险（实际 100 万时估算只报 66 万，护栏会晚
// 一步），宁可在纯英文散文上略保守。CJK 计 1 不变。
const textTokenPermille = 380

// estimateTokens 启发式：CJK 全角计 1、其余按 0.38/字符向上取整。
func estimateTokens(s string) int {
	wide, other := 0, 0
	for _, r := range s {
		if r >= 0x2E80 {
			wide++
		} else {
			other++
		}
	}
	return wide + (other*textTokenPermille+999)/1000
}

// estBreakdown 估算构成（校准用）：宽字符/其他字符/图片张数。
// 为什么需要：只有总数时，估算偏高偏低只能看比率猜来源（2026-11-26 实弹：
// 小请求 p90 1.86、大请求 0.92，无法判断是图片固定成本还是文本密度）。
// 归档按请求落这三项，才能按构成分组拟合权重，而不是拿一个平均数硬调。
type estBreakdown struct {
	Wide        int // CJK 全角字符数（≥0x2E80）
	Other       int // 其余字符数
	Images      int // 图片块数（按固定视觉成本计）
	Bytes       int // 已计入的字符串字节数（用于反推结构字节）
	StructBytes int // JSON 结构字节（键名/括号/逗号/数字等未按字符计的部分）
}

func (b *estBreakdown) addString(s string) {
	if isImageDataURL(s) {
		b.Images++
		// 图片 data URL 也计入「已计字节」，否则它的 base64 会全部落进
		// StructBytes，让结构字节被几 MB 的假数据污染（2026-11-26 实测踩到）。
		b.Bytes += len(s)
		return
	}
	b.Bytes += len(s)
	for _, r := range s {
		if r >= 0x2E80 {
			b.Wide++
		} else {
			b.Other++
		}
	}
}

func (b *estBreakdown) merge(o estBreakdown) {
	b.Wide += o.Wide
	b.Other += o.Other
	b.Images += o.Images
	b.Bytes += o.Bytes
	b.StructBytes += o.StructBytes
}

func (b estBreakdown) tokens() int {
	return b.Wide + (b.Other*textTokenPermille+999)/1000 + b.Images*imageTokenCost
}

func walkEst(v any, b *estBreakdown) {
	switch t := v.(type) {
	case string:
		b.addString(t)
	case []any:
		for _, e := range t {
			walkEst(e, b)
		}
	case map[string]any:
		// Anthropic 图片源 {"type":"base64","media_type":"image/...","data":"<raw b64>"}：
		// 载荷是无 data: 前缀的裸 base64，字符串层认不出，这里按整块固定成本计。
		if t["type"] == "base64" {
			if data, ok := t["data"].(string); ok && len(data) > 0 {
				b.Images++
				return
			}
		}
		for _, e := range t {
			walkEst(e, b)
		}
	}
}

// estimateTokensValue 递归累加通用值里全部字符串叶子的估算；数值/布尔不计。
// 图片载荷例外：OpenAI 的 data:image/ URL 与 Anthropic 的 base64 source 都按
// 固定 imageTokenCost 计，不把 base64 字符当文本（见 imageTokenCost 注释）。
func estimateTokensValue(v any) int {
	var b estBreakdown
	walkEst(v, &b)
	return b.tokens()
}

// estimateBreakdownJSON 解析一段 JSON 并返回构成；空段/不可解析（畸形子段）
// 返回零值（宽容语义，不因局部畸形拒整体）。
func estimateBreakdownJSON(raw json.RawMessage) estBreakdown {
	var b estBreakdown
	if len(raw) == 0 {
		return b
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return b
	}
	walkEst(v, &b)
	if rest := len(raw) - b.Bytes; rest > 0 {
		b.StructBytes = rest // 键名 + 标点 + 数字 + 空白，当前不计 token，只做记录
	}
	return b
}

// estimateTokensJSON 解析一段 JSON 并估算其字符串叶子；空段/不可解析（畸形子段）
// 返回 0（宽容语义，不因局部畸形拒整体）。
func estimateTokensJSON(raw json.RawMessage) int {
	return estimateBreakdownJSON(raw).tokens()
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
	// 入口形状守卫：非 JSON 对象就地 400。此前 `null` 会被解成零值结构体、
	// 静默返回 0 token，看起来像成功（见 body_shape.go）。
	if rejectIfNotJSONObject(w, body) {
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
