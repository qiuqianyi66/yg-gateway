package server

// 本文件承载备用 OpenAI 兼容端点的级联转发（fallbackForward / forwardToOne），
// 2026-10-23 从 handler.go 拆分而来：handler.go 非空行被批次 1 的 X-3/F-4 改动
// 推过 1000 行棘轮阈值（1014 行），按 check-depth 的修复方向拆分超长文件
// 而非放宽阈值（-Update 被脚本明令禁止用于掩盖回退）。逻辑零改动，纯文件搬移。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"time"

	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// endpointHost 把备用源 BaseURL 收成只含 host 的展示名：响应头要对客户端
// 说明「实际是谁服务的」，但不外泄路径、查询串与凭证。
func endpointHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return "fallback-endpoint"
}

// setFallbackHeaders 标记「本次响应由备用源服务」。必须在真正写响应头之前调用：
// 若在拿到上游响应后就设置，非流式读体失败会返回 false，级联转下一端点或最终
// 走网关错误，但 w.Header 里残留的 Served-By 会把「网关报错」伪装成
// 「备用源已服务」——客户端据此排障会走错方向（2026-11-25 复审发现）。
func setFallbackHeaders(w http.ResponseWriter, baseURL string) {
	w.Header().Set("X-YG-Route", "fallback")
	w.Header().Set("X-YG-Degraded", "1")
	w.Header().Set("X-YG-Served-By", endpointHost(baseURL))
}

// fallbackForward 把请求体原样转发到备用 OpenAI 兼容端点（P2）。
// 返回 true = 转发成功（已写响应）；false = 转发失败（调用方继续走错误路径）。
// fallbackForward 备用上游按序级联转发（R-107）：按 Endpoints 配置顺序逐个尝试，
// 第一个「白名单覆盖该模型 + 转发成功」的端点终结请求；全部失败返回 false 走末端错误。
// 顺序即优先级——列表靠前的端点优先消耗。
func (h *Handler) fallbackForward(w http.ResponseWriter, r *http.Request, body []byte, model string) bool {
	fc := h.cfg.Fallback
	if fc == nil {
		return false
	}
	// committed = 已对客户端写出响应头。一旦提交，本端点失败后禁止续写
	// 下一端点——A 已写头/半截流，B 再 WriteHeader + 写体会把两个响应
	// 拼成 A/B 混合的损坏流。连接失败（未写任何字节）时 committed 仍为
	// false，级联语义保留。
	committed := false
	for i := range fc.Endpoints {
		ep := fc.Endpoints[i]
		if !ep.Models[model] {
			continue
		}
		if h.forwardToOne(w, r, body, model, ep, &committed) {
			return true
		}
		if committed {
			// 已提交响应头后失败：终止级联（见上注释）。
			break
		}
		// 本端点没走完且未写头：继续下一个端点（级联语义）。
	}
	return false
}

// forwardToOne 把请求转发到单个备用端点并透传响应。
// 返回 true = 已写出完整响应（调用方可终结请求）；false = 未成功（连接失败/读不完整，
// 由调用方继续级联或走末端错误——R10 不静默吞）。
// committed 非空时：入口若已提交则直接放弃；WriteHeader 后置位，告知调用方
// 级联终止（不允许续写另一端点）。
func (h *Handler) forwardToOne(w http.ResponseWriter, r *http.Request, body []byte, model string, ep FallbackEndpoint, committed *bool) bool {
	if committed != nil && *committed {
		return false
	}
	compat := upstream.OpenAICompatEndpoint{BaseURL: ep.BaseURL, APIKey: ep.APIKey}
	timeout := time.Duration(ep.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// 出站请求收口在 upstream 适配器（R6）：组装 + 发出都在 OpenAICompatEndpoint。
	resp, err := compat.Do(ctx, bytes.NewReader(body), r.Header.Get("Accept"))
	if err != nil {
		log.Printf("[fallback] 转发到备用源失败 endpoint=%s model=%s err=%v", compat.BaseURL, model, err)
		return false
	}
	defer resp.Body.Close()

	// 降级必须让客户端看得见（2026-11-21 蒸馏 K-Dense「声明+验证」后补）：
	// 此前客户端只拿到备用源的 200，与主池成功无法区分。三个头在写状态码前
	// 落定，流式/非流式都带；实际模型名只有非流式能解析响应体拿到。
	// 但**必须延后到真正写响应头的那一刻**：拿到 resp 就设置的话，读体失败返回
	// false 会把 Served-By 留给末端错误响应（见 setFallbackHeaders 注释）。
	ct := resp.Header.Get("Content-Type")
	// SSE stream 逐块 flush 透传（缓冲会破坏流式体验）；**不再静默吞掉 io.Copy
	// 的错误**——R10 红线。此前 `_, _ = io.Copy(...)` 把「客户端断连 / 备用源半截流」
	// 一律记成成功：调用方据此 return true，于是把一次实际上没走完的转发当成
	// 已完成的成功响应（观测里看不到备用源的传输故障）。
	if strings.Contains(ct, "text/event-stream") {
		w.Header().Set("Content-Type", ct)
		setFallbackHeaders(w, ep.BaseURL)
		w.WriteHeader(resp.StatusCode)
		if committed != nil {
			*committed = true
		}
		fw := newFlushWriter(w)
		n, cerr := io.Copy(fw, resp.Body)
		if cerr != nil {
			// 已写 header，不能再改状态码；但必须让调用方知道这次转发没走完。
			log.Printf("[fallback] 备用源流式转发中断 endpoint=%s model=%s status=%d bytes=%d err=%v",
				compat.BaseURL, model, resp.StatusCode, n, cerr)
			return false
		}
		log.Printf("[fallback] 已转发(stream) endpoint=%s model=%s status=%d bytes=%d", compat.BaseURL, model, resp.StatusCode, n)
		return true
	}
	// 非 stream：先读全部 body → 解析 usage / 实际模型名 → 再写头。
	// 读失败时尚未提交响应头，级联语义保留（可继续试下一端点），不会拼出半截响应。
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		// 同上：读不全不是成功。返回 false 让调用方走既有错误路径
		// （返回值语义：true = 已写出完整响应）。
		log.Printf("[fallback] 备用源响应读取失败 endpoint=%s model=%s err=%v", compat.BaseURL, model, err)
		return false
	}
	// 200 才解析 usage（错误响应没有 usage 字段）。
	if resp.StatusCode == http.StatusOK && h.cfg.Usage != nil {
		var u struct {
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(respBody, &u) == nil && u.Usage.TotalTokens > 0 {
			h.cfg.Usage.Add(time.Now(), "fallback", "fallback", model, usage.Delta{
				PromptTokens:     u.Usage.PromptTokens,
				HasPromptTokens:  u.Usage.PromptTokens > 0,
				CompletionTokens: u.Usage.CompletionTokens,
				HasCompletion:    u.Usage.CompletionTokens > 0,
				TotalTokens:      u.Usage.TotalTokens,
				HasTotal:         true,
				KeyID:            "fallback",
			}, true)
		}
	}
	// 响应体的 model 才是备用源实际服务的模型：与请求 model 不一致时客户端
	// 必须能看到差异（别名/降档都在这一层暴露），不许把「请求的主模型」当结果。
	var meta struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(respBody, &meta) == nil && meta.Model != "" {
		w.Header().Set("X-YG-Upstream-Model", meta.Model)
	}
	w.Header().Set("Content-Type", ct)
	setFallbackHeaders(w, ep.BaseURL)
	w.WriteHeader(resp.StatusCode)
	if committed != nil {
		*committed = true
	}
	// 忽略：响应可能已部分写出，客户端断连后写失败无法补救或重试，不影响已提交状态（判据①）。
	_, _ = w.Write(respBody)
	log.Printf("[fallback] 已转发 endpoint=%s model=%s status=%d", compat.BaseURL, model, resp.StatusCode)
	return true
}
