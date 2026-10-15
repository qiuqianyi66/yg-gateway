// chatflow.go chatCompletions 阶段函数的落点。
//
// 背景：chatCompletions 曾是一个 847 行的单函数，同时承担准入、解析、会话派生、
// 出站改写、轮转重试、末端错误构造六件事。拆分按**原函数注释里已有的边界**进行
// （不是新设计的划分），每个阶段函数提取一段，主函数退化为编排。
//
// 拆分纪律：
//   - 每次只抽一个阶段，抽完立即跑 TestChatCompletionsBehaviorSnapshot（7 场景
//     行为快照），全绿才提交；
//   - 跨阶段共享状态用显式参数或结构体承载，**禁止**提升为包级变量（那是把局部
//     复杂度转成全局复杂度，属于假重构）；
//   - 纯计算与副作用分离：能做成纯函数的优先做成纯函数（可单独测试、无隐式耦合）。
package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"yg-gateway/internal/httpauth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/upstream"
)

// finalErrorOf 把末端错误映射成 (status, code, msg, gateway_hint)。
//
// 这是 chatCompletions 阶段 ⑤「末端错误构造」的**纯计算部分**：不写响应、不改
// 统计，只做「错误 → 展示三元组」的映射。写回由调用方负责，因此本函数可以脱离
// HTTP 上下文单独测试。
//
// 语义（与原内联实现逐位一致）：
//   - 默认：503 + no_healthy_account（本地调度错误，无上游原文可透传，不编造）；
//   - 上游超时：区分文案 upstream_timeout（轮转已在传输层止损，排查方向与"没有
//     可用账号"完全不同）；
//   - 上游 *Error：按 Kind 映射状态码/业务码，且**上游原文优先**（透传
//     code/msg/requestId，不拼本地前缀）；
//   - 模型级阻塞：优先级最高，覆盖为 400 + model_unavailable（"你选的模型当前
//     不可用"不是"服务端过载"，客户端不该按可重试错误处理）；
//   - key 绑定账号组却选不到号：追加运维提示。
//
// 参数说明：hasImage 是 reqHasImage() 的**求值结果**（原闭包是惰性的，调用方在
// 需要时才求值；这里显式传入，避免本函数隐式持有请求态）。
func (h *Handler) finalErrorOf(
	lastErr error,
	modelBlock pool.ModelBlockStatus,
	groupFilteredOut bool,
	authKey *httpauth.Key,
	bareModel string,
	hasImage bool,
) (status int, code, msg, hint string) {
	status = http.StatusServiceUnavailable
	code = "no_healthy_account"
	msg = "all accounts are temporarily unavailable, please retry later"

	// 上游超时：轮转已在传输层分支止损（见 isUpstreamTimeout），这里给一条**能区分**
	// 的文案，别混进"没有可用账号"——两者的排查方向完全不同。
	if errors.Is(lastErr, errUpstreamTimeout) {
		code = "upstream_timeout"
		msg = "upstream timed out: rotation stopped (another account would hit the same slow upstream), please retry later"
	}

	// gateway_hint（末端透传）：上游错误按 Kind + 原文 + 请求形态判定；本地调度类
	// 错误（无上游原文）固定 no_healthy_account hint。
	hint = upstream.NoHealthyAccountHint()

	// upstreamMsgPassed 记录 msg 是否已被上游原文占据：模型级阻塞分支据此决定要不要
	// 覆盖 msg（上游原文优先，含 requestId）。
	upstreamMsgPassed := false
	var ue *upstream.Error
	if errors.As(lastErr, &ue) {
		hint = h.hintOf(ue.Kind, ue.Msg, bareModel, hasImage, ue)
		switch ue.Kind {
		case upstream.ErrSoftRate:
			status = http.StatusTooManyRequests
			code = "rate_limit_exceeded"
			msg = "rate limited: all accounts are cooling down, please wait a moment and try again"
		case upstream.ErrWafBlock:
			if h.wafIP.active() {
				// IP 级拦截措辞（fail-fast 终止路径）：网关出口 IP 被 WAF 拦截、
				// 轮转已止损、窗口过后自动解除。客户端提前重试无意义（换号不换 IP）；
				// 有上游原文时原文优先（下方统一）。
				code = "waf_ip_blocked"
				msg = "waf ip-level block: upstream firewall is blocking the gateway IP, rotation stopped; retry after the block window expires"
			}
		case upstream.ErrModelBlocked:
			// 11102「该后端无此模型」：上游原文（下方统一透传）已经写清了原因，但
			// code 此前停在默认的 no_healthy_account —— 那是"服务端过载"的语义，
			// 客户端据此会不断重试（#81 里就是 503 → 客户端自动重试 5 次），而真正
			// 该做的是换个模型。用 400 + 明确的 code 把不可重试的性质讲清楚。
			code = "model_unavailable"
			status = http.StatusBadRequest
		}
		if s := strings.TrimSpace(ue.Msg); s != "" {
			// 上游原文优先：透传 code/msg/requestId，不拼接本地前缀。
			msg = s
			upstreamMsgPassed = true
		}
	}

	// 模型级阻塞覆盖上面的通用文案（放在最后 = 优先级最高）。
	//
	// 为什么能覆盖 code 而不丢信息：modelBlock.Reason 就是 BlockModelBackoff 存下的
	// 上游原因，已在 hint 里原样带出，所以覆盖 message 不会丢掉上游信息，反而补上
	// 了上游不会告诉客户端的两件事——「有几个号被挡」和「最早什么时候解封」。
	//
	// 用 400 而非 503：这是"你选的这个模型当前不可用"，不是"服务端暂时过载"。
	// 换号重试必然同样失败，客户端不该按可重试错误处理。
	if modelBlock.Blocked {
		code = "model_unavailable"
		// 有上游原文时保留它（含 requestId，用户要拿去向上游反馈），只把"有几个号
		// 被挡、最早何时解封"这类本地调度信息放进 gateway_hint。
		if !upstreamMsgPassed {
			msg = "model is unavailable on every account (per-model cooldown), try another model"
		}
		hint = fmt.Sprintf("model_blocked: %d account(s) cooling down this model", modelBlock.Count)
		if !modelBlock.Until.IsZero() {
			hint += "; earliest unblock at " + modelBlock.Until.Format(time.RFC3339)
		}
		if s := strings.TrimSpace(modelBlock.Reason); s != "" {
			hint += "; upstream: " + s
		}
		status = http.StatusBadRequest
	}

	// P1-B：key 绑定了账号组但组里没选到号 → 在 hint 里给运维明确提示。
	// 只在「不是模型级阻塞」时加（modelBlock 已覆盖那一类，避免信息重复）。
	if groupFilteredOut && !modelBlock.Blocked && authKey != nil {
		hint += fmt.Sprintf("; note: API key is bound to account group %q, no healthy account in this group — check auth files group tag or the group name typo", authKey.Group)
	}

	return status, code, msg, hint
}
