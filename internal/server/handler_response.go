// handler_response.go 响应写入工具：JSON/OpenAI 错误信封、逐块刷新的 writer、WAF 冷却基数。
//
// 从 handler.go 拆出（2026-11-17）：这几个都是无状态 helper，与 Handler 的装配、
// 鉴权、路由无关，混在 900 行的 handler.go 里只会让"这个文件到底管什么"变模糊。
package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// flushWriter 把每次 Write 立即刷给客户端（io.Writer 包装）。
//
// 用途：备用上游的 SSE 透传要「来一块吐一块」——默认的 io.Copy 会攒到缓冲满才
// 写，流式体验被破坏（客户端看到的是一坨延迟到达的数据）。与 StreamHint 的
// 逐帧 flush 同口径。
//
// Flusher 缺失时不报错：退化成缓冲写（比直接失败好，且网关前置无反代时
// ResponseWriter 恒实现 Flusher）。
type flushWriter struct {
	w  http.ResponseWriter
	fl http.Flusher
	n  int64
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	fw.n += int64(n)
	if fw.fl != nil {
		fw.fl.Flush()
	}
	return n, err
}

// flushWriter 构造：从 ResponseWriter 取 Flusher（取不到则只写不刷）。
func newFlushWriter(w http.ResponseWriter) *flushWriter {
	fl, _ := w.(http.Flusher) // 忽略：断言失败时 fl=nil，退化为只写不刷（见上方注释），不影响响应正确性
	return &flushWriter{w: w, fl: fl}
}

// writeJSON 写 JSON 响应。
//
// Marshal 失败按「不可达」处理并显式论证：v 恒为本包构造的 map / 结构体（字段值
// 来自上游 JSON 解码或字符串），不可能出现 chan/func 等不可序列化类型，因此这里
// 忽略 err 不会把「序列化失败」伪装成成功——真失败只可能是调用方传错类型，
// 属于编码期错误，编译与单测即可拦住。
func writeJSON(w http.ResponseWriter, status int, v any) {
	raw, _ := json.Marshal(v) // 忽略：见函数注释的前提（v 恒可序列化），不影响响应内容
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw) // 忽略：写失败只可能是客户端已断开，此处无补救动作可做
}

func writeOpenAIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "api_error",
			"code":    code,
		},
	})
}

// wafCooldownBase WAF 403 软冷却基数（建议 60s 起；抖动 ±25% 后落 [45s,75s]，
// 实际进入 CooldownSoftRate 后再按 softStreak 指数、封顶 soft_rate_max）。
// 与 SoftCooldown 分流的原因：WAF 403 是 IP/指纹维频控，信号比 429「账号级限流」轻
// （账号本身健康），但比 404 重（带粘性会连环）；60s 级的快速避让已足够让频控窗口
// 滑过。抖动复用 backoff.go jitterDur（单一来源）。
const wafCooldownBase = 60 * time.Second

// writeOpenAIErrorHint 同 writeOpenAIError，另在 error 对象上附加
// error.gateway_hint（hint 为空串时不带字段——未覆盖形态不编造）。
// message 仍是上游原文透传（hint 只做并列补充，绝不替换/包装 message）。
func writeOpenAIErrorHint(w http.ResponseWriter, status int, code, msg, hint string) {
	if hint == "" {
		writeOpenAIError(w, status, code, msg)
		return
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message":      msg,
			"type":         "api_error",
			"code":         code,
			"gateway_hint": hint,
		},
	})
}
