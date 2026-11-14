package server

// 本文件承载非 codebuddy provider 的 OpenAI 兼容出站 + 透传（ADR-026）。
// 与 handler_fallback.go（全池失败兜底）区分：本路径走账号池选号租约——请求先
// 命中某 provider 的 models[] 白名单，选号只在该 provider 的账号里选，选中后
// 才走这里出站；fallback 是选号全失败后的最后兜底。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/logfmt"
	"yg-gateway/internal/reqlog"
	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// providerCompatTimeout 非 codebuddy 出站的总时长上限。与 fallback 的 120s 默认
// 对齐；后续可在 ProviderEndpoint 上扩展为可配置字段。
const providerCompatTimeout = 120 * time.Second

// serveProviderCompat 非 codebuddy 账号的 OpenAI 兼容出站 + 透传（ADR-026）。
//
// 为什么不复用 serveStream/serveNonStream：它们内嵌的 SSE 解析（积分统计、工具
// 标记修复、WorkBuddy 错误帧分类）是 codebuddy 协议专属，对标准 OpenAI 兼容流
// 不适用。本函数用 upstream.OpenAICompatEndpoint 直接转发（R6 出站收口），成功/
// 失败判定对齐 codebuddy 路径的语义（NoteSuccess / 绑粘性 / usage 记录）。
//
// 返回 true = 已写完响应（调用方终结请求）；false = 出站失败（调用方 NoteError +
// 释放租约 + 换号重试）。
func (run *chatRun) serveProviderCompat(w http.ResponseWriter, r *http.Request, acct *auth.Auth, ep ProviderEndpoint, attemptStarted time.Time) bool {
	h := run.h
	st := run.st
	bareModel := run.bareModel
	sessKey := run.sessKey
	pid := acct.ProviderID()

	// 凭证优先级（消歧双真相源）：账号级 api_key（auths/*.json 的 api_key）优先于
	// provider 级（config.providers[].api_key）。一个火山引擎账号一个 key 的常见形态
	// 用账号级（多账号各自独立计费）；共享一个 key 的简化形态用 provider 级（账号只写
	// provider 标记、不写 api_key）。两者都空 normalizeProviders 拦不下（它看不到
	// auths/ 里的账号），出站会拿到空 Bearer 并由上游 401 暴露——不在此静默兜底（R10）。
	key := ep.APIKey
	if acct.APIKey != "" {
		key = acct.APIKey
	}
	compat := upstream.OpenAICompatEndpoint{BaseURL: ep.BaseURL, APIKey: key}
	ctx, cancel := context.WithTimeout(r.Context(), providerCompatTimeout)
	defer cancel()

	resp, err := compat.Do(ctx, bytes.NewReader(run.body), r.Header.Get("Accept"))
	if err != nil {
		log.Printf("[provider] 出站失败 provider=%s acct=%s model=%s err=%v",
			pid, logfmt.Label(acct.UID, acct.Nickname), bareModel, err)
		return false
	}
	defer resp.Body.Close()

	// 非 2xx：读 body 截断入日志供排障，返回 false（调用方 NoteError + 换号）。
	if resp.StatusCode >= 400 {
		// 忽略：错误响应 body 只用于日志截断，读失败不影响「状态码已表达失败」的判定。
		b, _ := io.ReadAll(resp.Body)
		log.Printf("[provider] 上游错误 provider=%s acct=%s model=%s status=%d body=%s",
			pid, logfmt.Label(acct.UID, acct.Nickname), bareModel, resp.StatusCode,
			logfmt.Truncate(string(b), 200))
		return false
	}

	// 2xx：写响应头（此后不可改状态码）。
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	st.status = resp.StatusCode

	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		fw := newFlushWriter(w)
		if _, cerr := io.Copy(fw, resp.Body); cerr != nil {
			// 已写头，状态码不可改；但转发没走完 = 不是成功，返回 false 让调用方
			// 按失败处置（不 NoteSuccess、不绑粘性）。
			log.Printf("[provider] 流式转发中断 provider=%s acct=%s model=%s err=%v",
				pid, logfmt.Label(acct.UID, acct.Nickname), bareModel, cerr)
			return false
		}
		run.noteProviderSuccess(acct, sessKey)
		st.outcome = reqlog.OutcomeSuccess
		return true
	}

	// 非流式：读 body → 解析 usage → 写回。
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[provider] 响应读取失败 provider=%s acct=%s model=%s err=%v",
			pid, logfmt.Label(acct.UID, acct.Nickname), bareModel, err)
		return false
	}
	if resp.StatusCode == http.StatusOK && h.cfg.Usage != nil {
		var u struct {
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(respBody, &u) == nil && u.Usage.TotalTokens > 0 {
			h.cfg.Usage.Add(time.Now(), pid, acct.UID, bareModel, usage.Delta{
				PromptTokens:     u.Usage.PromptTokens,
				HasPromptTokens:  u.Usage.PromptTokens > 0,
				CompletionTokens: u.Usage.CompletionTokens,
				HasCompletion:    u.Usage.CompletionTokens > 0,
				TotalTokens:      u.Usage.TotalTokens,
				HasTotal:         true,
				KeyID:            pid,
			}, true)
		}
	}
	// 写回客户端。
	// 忽略：响应已提交、客户端可能已断连，写失败无法补救或重试，不影响已提交状态
	//（判据同 handler_fallback.forwardToOne）。
	_, _ = w.Write(respBody)
	run.noteProviderSuccess(acct, sessKey)
	st.outcome = reqlog.OutcomeSuccess
	return true
}

// noteProviderSuccess 非 codebuddy 账号出站成功的统一收尾：NoteSuccess + 绑粘性。
// 与 serveStream/serveNonStream 的真成功分支同语义（覆盖旧绑定）。
func (run *chatRun) noteProviderSuccess(acct *auth.Auth, sessKey string) {
	run.h.cfg.Pool.NoteSuccess(acct.UID)
	if sessKey != "" && run.h.cfg.Session != nil {
		run.h.cfg.Session.Bind(sessKey, acct.UID)
	}
}
