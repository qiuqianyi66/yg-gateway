// config_guardrail.go 上下文护栏配置（REQ-context-guardrail-2026-10-31 / ADR-019）。
//
// 结构体定义独立成文件——config.go 处于 maxFileLines 棘轮上限（998/1000），
// 独立配置域按 config_opsagent.go 先例拆文件（同包方法可跨文件）。
package main

import "fmt"

// GuardrailsConfig 上下文护栏配置。空配置/未配置 = 全部 0 = 关闭（零回归）。
type GuardrailsConfig struct {
	// ContextWarnTokens 估算 prompt token 软阈值：超限打标（reqlog
	// guardrail_hit=warn）+ X-Gateway-Warning 头，不阻断。0 = 关（默认）。
	// 负值无意义（fail fast，同 max_body_bytes 口径）。
	ContextWarnTokens int64 `json:"context_warn_tokens"`
	// ContextMaxTokens 估算 prompt token 硬阈值：超限 413 context_length_exceeded
	// + Retry-After: 60，不读上游不选号。0 = 关（默认）。负值无意义。
	ContextMaxTokens int64 `json:"context_max_tokens"`
	// SessionRPM 单会话（sessKey）1 分钟窗口请求上限：超限 429
	// session_rate_limit + Retry-After: 60。散客（无会话键）不计。
	// 0 = 关（默认）。负值无意义。
	SessionRPM int `json:"session_rpm"`
}

// validate 校验护栏配置：0 = 关（零回归）；负值无意义 fail fast（写 -1 想「关」
// 的人应写 0，静默归一会让配错的人以为关了实际没关）。由 main 装配时调用——
// 同 opsagent 先例（config_opsagent.go：独立配置域兜底由 main 侧统一做，
// config.go 处于 maxFileLines 棘轮上限，Normalize 不展开）。
func (g GuardrailsConfig) validate() error {
	if g.ContextWarnTokens < 0 {
		return fmt.Errorf("guardrails.context_warn_tokens: 负值 %d 无意义（0=关闭，正数=软阈值）", g.ContextWarnTokens)
	}
	if g.ContextMaxTokens < 0 {
		return fmt.Errorf("guardrails.context_max_tokens: 负值 %d 无意义（0=关闭，正数=硬阈值）", g.ContextMaxTokens)
	}
	if g.SessionRPM < 0 {
		return fmt.Errorf("guardrails.session_rpm: 负值 %d 无意义（0=关闭，正数=会话每分钟上限）", g.SessionRPM)
	}
	return nil
}
