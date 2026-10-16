// coverage_parse_test.go 解析/目录/纯函数覆盖补强（Task 5）。
//
// 纯增量：不触碰任何生产文件。所有用例只覆盖「无 HTTP 依赖」的纯逻辑（解析、
// 目录查找链、静态兜底表、计量、帧构造、头构造），HTTP 相关路径一律用注入的
// rtFunc / nil client 隔离，不打真网、不依赖真实时间。
//
// 本文件内所有辅助符号统一前缀 upstreamparse，避免与同包其他 agent 的测试文件
// 重名（重名会让整包编译失败）。
package upstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// ---- helpers ----

// upstreamparseTransportErr 注入的传输层失败（不打真网）。
var upstreamparseTransportErr = errors.New("simulated transport failure")

// upstreamparseScheduleShape 是 v3ModelPromotion.Schedule 字段类型的**类型别名**
// （别名 → 与生产代码里的匿名 struct 类型完全同一，可直接赋值）。
type upstreamparseScheduleShape = struct {
	Daily []struct {
		Start string `json:"start"`
		End   string `json:"end"`
	} `json:"daily"`
	Timezone   string `json:"timezone"`
	ValidFrom  string `json:"validFrom"`
	ValidUntil string `json:"validUntil"`
}

// upstreamparseWindow 造一个单日窗口的 schedule。
func upstreamparseWindow(start, end string) *upstreamparseScheduleShape {
	sc := &upstreamparseScheduleShape{}
	var w struct {
		Start string `json:"start"`
		End   string `json:"end"`
	}
	w.Start, w.End = start, end
	sc.Daily = append(sc.Daily, w)
	return sc
}

// upstreamparseValidity 造一个带有效期的 schedule（无 daily 窗口 → 全天生效）。
func upstreamparseValidity(from, until string) *upstreamparseScheduleShape {
	return &upstreamparseScheduleShape{ValidFrom: from, ValidUntil: until}
}

// upstreamparseGlobalAuth 构造 global realm 账号（与生产登录路径同口径：
// domain 推断 + backfill 落 realm 标识）。
func upstreamparseGlobalAuth() *auth.Auth {
	a := &auth.Auth{UID: "uid-global-cov", Domain: "www.workbuddy.ai"}
	a.BackfillRealm()
	return a
}

// upstreamparseCNAuth 构造 CN realm 账号。
func upstreamparseCNAuth() *auth.Auth {
	a := &auth.Auth{UID: "uid-cn-cov", Domain: "copilot.tencent.com"}
	a.BackfillRealm()
	return a
}

// upstreamparseResetCatalog 隔离 model.json 单例状态（前后各清一次，避免用例互相
// 污染，也不把状态留给后续用例）。
func upstreamparseResetCatalog(t *testing.T) {
	t.Helper()
	resetModelCatalog()
	t.Cleanup(resetModelCatalog)
}

// upstreamparseResetModelsDev 隔离 models.dev 单例状态。
func upstreamparseResetModelsDev(t *testing.T) {
	t.Helper()
	resetModelsDev()
	t.Cleanup(resetModelsDev)
}

// upstreamparseWriteCatalog 造一个临时 model.json（内容原样写入），返回路径。
func upstreamparseWriteCatalog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp model.json: %v", err)
	}
	return path
}

// upstreamparseNegKey 负缓存测试的确定性键名（可预测排序用）。
func upstreamparseNegKey(prefix string, i int) string {
	return fmt.Sprintf("%s-%05d", prefix, i)
}

// upstreamparseDay 构造 promoZone 时区的当日时刻（确定性，不读系统时区）。
func upstreamparseDay(h, m int) time.Time {
	return time.Date(2026, 10, 2, h, m, 0, 0, promoZone)
}

// ---- client.go ----

// TestUpstreamParsePromoClock "HH:MM" → 当日分钟数；坏值 -1/false。
func TestUpstreamParsePromoClock(t *testing.T) {
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"23:00", 23*60 + 0, true},
		{"7:50", 7*60 + 50, true},
		{"00:00", 0, true},
		{" 9:05 ", 9*60 + 5, true}, // 前后空白容忍
		{"24:00", 24 * 60, true},   // 上界含 24（上游可下发当日末尾）
		{"25:00", -1, false},       // 上界外
		{"12:60", -1, false},       // 分上界外
		{"-1:00", -1, false},
		{"12", -1, false},       // 缺冒号
		{"12:30:00", -1, false}, // 段数过多
		{"ab:cd", -1, false},    // 非数字
		{"", -1, false},         // 空串
	}
	for _, c := range cases {
		got, ok := promoClock(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("promoClock(%q) = (%d,%v) want (%d,%v)", c.in, got, ok, c.want, c.wantOK)
		}
	}
}

// TestUpstreamParsePromoActive promoActive 的窗口/有效期判定。
// 还原任一分支（跨午夜窗口、end 排他、validFrom/validUntil 边界、坏 clock 跳过、
// enabled 门控）都会让对应子用例 FAIL。
func TestUpstreamParsePromoActive(t *testing.T) {
	cases := []struct {
		name string
		p    *v3ModelPromotion
		now  time.Time
		want bool
	}{
		{"enabled=false 恒不生效", &v3ModelPromotion{Enabled: false}, upstreamparseDay(23, 30), false},
		{"无 schedule 视为全天生效", &v3ModelPromotion{Enabled: true}, upstreamparseDay(12, 0), true},
		{"空 schedule 视为全天生效", &v3ModelPromotion{Enabled: true, Schedule: &upstreamparseScheduleShape{}}, upstreamparseDay(3, 0), true},
		{"跨午夜窗口 23:00→7:50 的 23:30 在内", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("23:00", "7:50")}, upstreamparseDay(23, 30), true},
		{"跨午夜窗口 06:00 仍在内", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("23:00", "7:50")}, upstreamparseDay(6, 0), true},
		{"跨午夜窗口终点 7:50 排他", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("23:00", "7:50")}, upstreamparseDay(7, 50), false},
		{"跨午夜窗口白天不在内", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("23:00", "7:50")}, upstreamparseDay(12, 0), false},
		{"非跨午夜窗口起点含", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("09:00", "18:00")}, upstreamparseDay(9, 0), true},
		{"非跨午夜窗口终点排他", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("09:00", "18:00")}, upstreamparseDay(18, 0), false},
		{"非跨午夜窗口窗口外", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("09:00", "18:00")}, upstreamparseDay(8, 59), false},
		{"坏 clock 跳过该窗口 → 无窗口命中", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseWindow("bad", "18:00")}, upstreamparseDay(10, 0), false},
		{"validFrom 未到不生效", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseValidity("2026-10-03T00:00:00+08:00", "")}, upstreamparseDay(12, 0), false},
		{"validFrom 边界（now == from）生效", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseValidity("2026-10-02T12:00:00+08:00", "")}, upstreamparseDay(12, 0), true},
		{"validUntil 边界（now == until）不生效", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseValidity("", "2026-10-02T12:00:00+08:00")}, upstreamparseDay(12, 0), false},
		{"validUntil 未到生效", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseValidity("", "2026-10-02T23:59:59+08:00")}, upstreamparseDay(12, 0), true},
		{"validFrom 坏值不阻断（忽略该字段）", &v3ModelPromotion{Enabled: true, Schedule: upstreamparseValidity("not-a-rfc3339", "")}, upstreamparseDay(12, 0), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := promoActive(c.p, c.now); got != c.want {
				t.Errorf("promoActive = %v want %v", got, c.want)
			}
		})
	}
}

// TestUpstreamParseIsAllDigits 纯数字快筛（含非 ASCII 数字与小数点拒绝）。
func TestUpstreamParseIsAllDigits(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"0", true},
		{"000123", true},
		{"12a", false},
		{"12.5", false},
		{"-1", false},
		{" 1", false},
		{"１２３", false}, // 全角数字不是 ASCII 数字
	}
	for _, c := range cases {
		if got := isAllDigits(c.in); got != c.want {
			t.Errorf("isAllDigits(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

// TestUpstreamParseRetryNumber parseRetryNumber 的头名口径与量级上限（16 位乘 1e9
// 不得溢出回绕成合法小正数）。
func TestUpstreamParseRetryNumber(t *testing.T) {
	cases := []struct {
		name   string
		v      string
		header string
		want   time.Duration
		wantOK bool
	}{
		{"Retry-After 整数秒", "120", "Retry-After", 120 * time.Second, true},
		{"Retry-After 恰好 2h 上界", "7200", "Retry-After", 2 * time.Hour, true},
		{"Retry-After 超上界丢弃", "7201", "Retry-After", 0, false},
		{"Retry-After 16 位不溢出回绕", "9999999999999999", "Retry-After", 0, false},
		{"17 位直接拒绝", "99999999999999999", "Retry-After", 0, false},
		{"Retry-After-Ms 毫秒", "1500", "Retry-After-Ms", 1500 * time.Millisecond, true},
		{"Retry-After-Ms 恰好 2h", "7200000", "Retry-After-Ms", 2 * time.Hour, true},
		{"Retry-After-Ms 超上界丢弃", "7200001", "Retry-After-Ms", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseRetryNumber(c.v, c.header)
			if got != c.want || ok != c.wantOK {
				t.Errorf("parseRetryNumber(%q,%q) = (%v,%v) want (%v,%v)", c.v, c.header, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestUpstreamParseRetryAfterEpoch X-Ratelimit-Reset 的 epoch 语义（秒/毫秒口径）、
// 已过去时刻与超上限时刻被拒、头优先级与回落。
func TestUpstreamParseRetryAfterEpoch(t *testing.T) {
	// epoch 口径取「now + 30min」：剩余量必须落在 [25min, 30min]（不是写死的 0/负数），
	// 且必须 ≤ retryAfterSanity（2h）才被采信。
	future := time.Now().Add(30 * time.Minute)
	epochSec := fmt.Sprintf("%d", future.Unix())     // 10 位秒口径
	epochMs := fmt.Sprintf("%d", future.UnixMilli()) // 13 位毫秒口径

	h := http.Header{}
	h.Set("X-Ratelimit-Reset", epochSec)
	sec, ok := ParseRetryAfter(h)
	if !ok {
		t.Fatalf("epoch 秒口径应解析为剩余量, got (%v,%v)", sec, ok)
	}
	if sec < 25*time.Minute || sec > 30*time.Minute {
		t.Errorf("epoch 秒口径剩余量 = %v want [25m,30m]", sec)
	}

	hms := http.Header{}
	hms.Set("X-Ratelimit-Reset", epochMs)
	ms, ok := ParseRetryAfter(hms)
	if !ok {
		t.Fatalf("epoch 毫秒口径应解析为剩余量, got (%v,%v)", ms, ok)
	}
	// 两口径指的是同一时刻：剩余量应几乎一致（容忍同一次测试内的执行偏差）。
	if diff := ms - sec; diff > 5*time.Second || diff < -5*time.Second {
		t.Errorf("秒/毫秒口径剩余量不一致: sec=%v ms=%v", sec, ms)
	}

	// 位数不足 12 位按秒解释；8 位（1970 年）意味着早已过去 → 剩余量为负 → 丢弃。
	past := http.Header{}
	past.Set("X-Ratelimit-Reset", "12345678")
	if d, ok := ParseRetryAfter(past); ok {
		t.Errorf("过去时刻应被丢弃, got (%v,%v)", d, ok)
	}

	// epoch 剩余量超上限（2h）同样丢弃（回落本地计算，不臆造等待时长）。
	farSec := fmt.Sprintf("%d", time.Now().Add(6*time.Hour).Unix())
	far := http.Header{}
	far.Set("X-Ratelimit-Reset", farSec)
	if d, ok := ParseRetryAfter(far); ok {
		t.Errorf("超上限的 epoch 应被丢弃, got (%v,%v)", d, ok)
	}

	// 非纯数字（HTTP-Date 形态）不解析。
	date := http.Header{}
	date.Set("Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT")
	if d, ok := ParseRetryAfter(date); ok {
		t.Errorf("HTTP-Date 形态不应解析, got (%v,%v)", d, ok)
	}

	// 头优先级：Retry-After 先于 Retry-After-Ms / X-Ratelimit-Reset。
	multi := http.Header{}
	multi.Set("Retry-After", "30")
	multi.Set("Retry-After-Ms", "9000")
	multi.Set("X-Ratelimit-Reset", epochSec)
	if d, ok := ParseRetryAfter(multi); !ok || d != 30*time.Second {
		t.Errorf("头优先级 = (%v,%v) want (30s,true)", d, ok)
	}

	// 首个头非法（超上限）时回落下一头。
	fallback := http.Header{}
	fallback.Set("Retry-After", "99999")
	fallback.Set("Retry-After-Ms", "2500")
	if d, ok := ParseRetryAfter(fallback); !ok || d != 2500*time.Millisecond {
		t.Errorf("非法头应回落下一头, got (%v,%v) want (2.5s,true)", d, ok)
	}

	// 全空 → false（绝不臆造等待时长）。
	if d, ok := ParseRetryAfter(http.Header{}); ok {
		t.Errorf("无头应返回 false, got (%v,%v)", d, ok)
	}
}

// TestUpstreamParseSoftRateResetLoc 重置时间的固定时区是 UTC+8（与容器时区无关）。
func TestUpstreamParseSoftRateResetLoc(t *testing.T) {
	loc := SoftRateResetLoc()
	if loc == nil {
		t.Fatal("SoftRateResetLoc() = nil")
	}
	_, off := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
	if off != 8*3600 {
		t.Errorf("SoftRateResetLoc offset = %d want %d", off, 8*3600)
	}
	if loc != SoftRateResetLoc() {
		t.Error("SoftRateResetLoc 应返回同一 *time.Location 实例")
	}
}

// TestUpstreamParseGlobalBases 空值回退 defaultGlobalBase，且两域 base 互不串扰。
func TestUpstreamParseGlobalBases(t *testing.T) {
	empty := &Client{}
	if got := empty.globalChatBase(); got != defaultGlobalBase {
		t.Errorf("空 Client.globalChatBase() = %q want %q", got, defaultGlobalBase)
	}
	if got := empty.globalBillingBase(); got != defaultGlobalBase {
		t.Errorf("空 Client.globalBillingBase() = %q want %q", got, defaultGlobalBase)
	}
	if defaultGlobalBase != "https://www.workbuddy.ai" {
		t.Errorf("defaultGlobalBase = %q want https://www.workbuddy.ai", defaultGlobalBase)
	}

	c := &Client{ChatBaseGlobal: "https://chat.g.example", BillingBaseGlobal: "https://bill.g.example"}
	if got := c.globalChatBase(); got != "https://chat.g.example" {
		t.Errorf("globalChatBase() = %q want 覆盖值", got)
	}
	if got := c.globalBillingBase(); got != "https://bill.g.example" {
		t.Errorf("globalBillingBase() = %q want 覆盖值", got)
	}

	// 只覆盖 billing 时 chat 仍回退默认（两字段独立）。
	only := &Client{BillingBaseGlobal: "https://bill.g.example"}
	if got := only.globalChatBase(); got != defaultGlobalBase {
		t.Errorf("仅覆盖 billing 时 globalChatBase() = %q want %q", got, defaultGlobalBase)
	}
}

// TestUpstreamParseErrKindString 错误类别的 String() 口径（reqlog/面板展示依赖）。
func TestUpstreamParseErrKindString(t *testing.T) {
	cases := []struct {
		kind ErrKind
		want string
	}{
		{ErrNone, "none"},
		{ErrHardCredit, "hard_credit"},
		{ErrSoftRate, "soft_rate"},
		{ErrSessionDead, "session_dead"},
		{ErrNotFound, "not_found"},
		{ErrServer, "server"},
		{ErrContentBlocked, "content_blocked"},
		{ErrBadParams, "bad_params"},
		{ErrAccountFault, "account_fault"},
		{ErrModelBlocked, "model_blocked"},
		{ErrWafBlock, "waf_block"},
		{ErrPromptTooLong, "prompt_too_long"},
		{ErrImageInvalid, "image_invalid"},
		{ErrClient, "client"},
		{ErrKind(99), "none"}, // 越界值落 default
	}
	for _, c := range cases {
		if got := c.kind.String(); got != c.want {
			t.Errorf("ErrKind(%d).String() = %q want %q", int(c.kind), got, c.want)
		}
	}
}

// ---- hint.go ----

// TestUpstreamParseGatewayHintKindTable Kind 一对一映射的文案口径（未覆盖形态不编造）。
func TestUpstreamParseGatewayHintKindTable(t *testing.T) {
	cases := []struct {
		kind ErrKind
		want string
	}{
		{ErrPromptTooLong, "request context exceeds the model's limit; reduce history/message size"},
		{ErrImageInvalid, "image request was rejected by upstream; check image_url format and image data"},
		{ErrWafBlock, "upstream WAF blocked the gateway; retry after the block window"},
		{ErrSoftRate, "rate limited by upstream; retry after reset"},
		{ErrAccountFault, "account-level fault at upstream (auth/quota state); the gateway will rotate or disable this account"},
		{ErrSessionDead, "account session expired at upstream; the account is disabled until re-login"},
		{ErrHardCredit, "account credits exhausted at upstream; waiting for daily check-in to restore"},
		{ErrModelBlocked, "upstream has no such model on this backend; switch model or retry on another account"},
		{ErrContentBlocked, "request content was rejected by content policy; adjust the prompt and retry"},
		{ErrNone, ""},
		{ErrNotFound, ""},
		{ErrServer, ""},
		{ErrBadParams, ""},
		{ErrClient, ""},
	}
	for _, c := range cases {
		if got := GatewayHint(c.kind, "neutral upstream body", HintContext{}); got != c.want {
			t.Errorf("GatewayHint(%v) = %q want %q", c.kind, got, c.want)
		}
	}
	// content_blocked 措辞不得含 "upstream"（handler_test 的泄漏守卫口径）。
	if got := GatewayHint(ErrContentBlocked, "", HintContext{}); contains(got, "upstream") {
		t.Errorf("content_blocked hint 不应含 upstream: %q", got)
	}
}

// TestUpstreamParseGatewayHint11133 11133 家族先于 Kind 表判定，且「不支持图片」需
// 带图 + 目录声明不支持两个前提同时成立（目录未收录 → 不点名图片）。
func TestUpstreamParseGatewayHint11133(t *testing.T) {
	body := `{"code":11133,"msg":"invalid request parameters"}`
	const pickModel = "model glm-5.2 does not support images; pick one with supports_images=true from /v1/models"
	const neutral = "request parameters were rejected by the model provider; check message format and model capabilities"

	cases := []struct {
		name string
		kind ErrKind
		msg  string
		ctx  HintContext
		want string
	}{
		{"带图+目录声明不支持 → 点名换模型", ErrSoftRate, body, HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: true, ModelSupportsImages: false}, pickModel},
		{"带图但目录未收录该模型 → 中性参数提示", ErrSoftRate, body, HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: false}, neutral},
		{"带图+目录声明支持 → 中性参数提示", ErrSoftRate, body, HintContext{Model: "gpt-5.5", HasImage: true, ModelInCatalog: true, ModelSupportsImages: true}, neutral},
		{"无图 → 中性参数提示", ErrSoftRate, body, HintContext{Model: "glm-5.2", ModelInCatalog: true}, neutral},
		{"model_param_invalid 文案家族同样命中", ErrSoftRate, `{"extError":{"code":"model_param_invalid"}}`, HintContext{}, neutral},
		{"11133 命中先于 Kind 表（Kind=prompt_too_long）", ErrPromptTooLong, body, HintContext{}, neutral},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := GatewayHint(c.kind, c.msg, c.ctx); got != c.want {
				t.Errorf("GatewayHint = %q want %q", got, c.want)
			}
		})
	}
	// 11133 + Kind=ErrSoftRate：必须走 11133 分支而非限流文案。
	got := GatewayHint(ErrSoftRate, body, HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: true})
	if got == "rate limited by upstream; retry after reset" {
		t.Error("11133 判定必须优先于 Kind 表")
	}
}

// TestUpstreamParseInvalidImageData 11135 家族判定（code/字段名/文案三种形态，大小写不敏感）。
func TestUpstreamParseInvalidImageData(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`{"code":11135}`, true},
		{`{"code": 11135}`, true},
		{`{"code":"11135"}`, true},
		{`{"extError":{"code":"invalid_image_data"}}`, true},
		{"please REPLACE THE IMAGE and retry", true},
		{`{"code":11133}`, false},
		{"plain body", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isInvalidImageData(c.in); got != c.want {
			t.Errorf("isInvalidImageData(%q) = %v want %v", c.in, got, c.want)
		}
	}
	// 命中 11135 时 hint 走图片数据文案，且不被 Kind 表覆盖。
	const want = "image data rejected by upstream; use a real/valid image, may need a new conversation"
	if got := GatewayHint(ErrNone, `{"code":11135}`, HintContext{}); got != want {
		t.Errorf("11135 hint = %q want %q", got, want)
	}
	if got := GatewayHint(ErrSoftRate, `{"code":11135}`, HintContext{}); got != want {
		t.Errorf("11135 应优先于 Kind 表, got %q", got)
	}
}

// TestUpstreamParseNoHealthyAccountHint 本地调度错误 hint（无上游原文时的固定文案）。
func TestUpstreamParseNoHealthyAccountHint(t *testing.T) {
	const want = "no healthy account available in pool; check /status or retry later"
	if got := NoHealthyAccountHint(); got != want {
		t.Errorf("NoHealthyAccountHint() = %q want %q", got, want)
	}
}

// TestUpstreamParseFrameKind SSE error 帧的 Kind 判定：6004 流式形态优先，
// 其余取帧内 error.message 走 Classify（400 请求级），判不出 → ErrNone。
func TestUpstreamParseFrameKind(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    ErrKind
	}{
		{"6004 模型级限流优先", `{"error":{"code":6004,"message":"prompt is too long"}}`, ErrSoftRate},
		{"6004 字符串码", `{"error":{"code":"6004"}}`, ErrSoftRate},
		{"error.message 走 Classify", `{"error":{"message":"prompt is too long for this model"}}`, ErrPromptTooLong},
		{"error 对象无 message → 判不出", `{"error":{"code":123}}`, ErrNone},
		{"非 JSON 帧 → 判不出", `not-json`, ErrNone},
		{"空帧 → 判不出", ``, ErrNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FrameKind(c.payload); got != c.want {
				t.Errorf("FrameKind(%q) = %v want %v", c.payload, got, c.want)
			}
		})
	}
}

// TestUpstreamParseFrameHintFunc FrameHintFunc 的惰性求值（正常帧零开销）与空帧短路。
func TestUpstreamParseFrameHintFunc(t *testing.T) {
	calls := 0
	fn := FrameHintFunc(func() HintContext {
		calls++
		return HintContext{}
	})

	if got := fn(""); got != "" {
		t.Errorf("空 payload hint = %q want \"\"", got)
	}
	if got := fn("[DONE]"); got != "" {
		t.Errorf("[DONE] hint = %q want \"\"", got)
	}
	if calls != 0 {
		t.Errorf("空帧/[DONE] 不应触发 ctxFn（惰性求值），calls=%d", calls)
	}

	if got := fn(`{"error":{"code":6004,"message":"model rate limited"}}`); got != "rate limited by upstream; retry after reset" {
		t.Errorf("6004 帧 hint = %q", got)
	}
	if calls != 1 {
		t.Errorf("命中 error 帧应恰好求值一次 ctxFn，calls=%d", calls)
	}

	// 判不出 Kind 的帧 → 无 hint（不编造）。
	if got := fn(`not-json`); got != "" {
		t.Errorf("不可解析帧 hint = %q want \"\"", got)
	}
}

// ---- truncation.go ----

// TestUpstreamParseIsTruncatedArguments 只把「非空但无法解析」判为截断。
func TestUpstreamParseIsTruncatedArguments(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"   ", false},
		{`{"a":1}`, false},
		{` {"a":1} `, false},
		{"null", false}, // 合法 JSON 标量不算截断（交客户端 schema 校验）
		{"123", false},
		{"[1,2]", false},
		{`{"a":`, true},
		{`{"a":1`, true},
		{"{", true},
	}
	for _, c := range cases {
		if got := isTruncatedArguments(c.in); got != c.want {
			t.Errorf("isTruncatedArguments(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

// TestUpstreamParseDropTruncatedToolCalls 丢弃语义：只丢「非空且解析失败」的调用，
// 无参工具（空串）与合法标量参数照常保留，返回新 slice 不动入参。
func TestUpstreamParseDropTruncatedToolCalls(t *testing.T) {
	calls := []map[string]any{
		{"id": "ok", "function": map[string]any{"name": "a", "arguments": `{"x":1}`}},
		{"id": "cut", "function": map[string]any{"name": "b", "arguments": `{"y":`}},
		{"id": "noargs", "function": map[string]any{"name": "c", "arguments": ""}},
		{"id": "nullargs", "function": map[string]any{"name": "d", "arguments": "null"}},
		{"id": "nofunction"},
		{"function": "not-a-map"},
	}
	kept := dropTruncatedToolCalls(calls)

	if len(calls) != 6 {
		t.Fatalf("输入 slice 被改动: len=%d want 6", len(calls))
	}
	wantIDs := []string{"ok", "noargs", "nullargs", "nofunction", ""}
	if len(kept) != len(wantIDs) {
		t.Fatalf("kept len=%d want %d (kept=%v)", len(kept), len(wantIDs), kept)
	}
	for i, want := range wantIDs {
		got, _ := kept[i]["id"].(string)
		if got != want {
			t.Errorf("kept[%d].id = %q want %q", i, got, want)
		}
	}
	// 截断参数被丢弃（真 bug 还原点：把判据改成「空串才丢」会让 "cut" 留在结果里）。
	for _, c := range kept {
		if c["id"] == "cut" {
			t.Error("截断参数的 tool_call 必须被丢弃")
		}
	}
	// 保留项未被改写（正例零改动）。
	fn, _ := kept[0]["function"].(map[string]any)
	if fn["arguments"] != `{"x":1}` || fn["name"] != "a" {
		t.Errorf("保留项被改写: %v", kept[0])
	}
	// 全部合法 → 全保留；空输入 → 空输出。
	if got := dropTruncatedToolCalls([]map[string]any{calls[0]}); len(got) != 1 {
		t.Errorf("全合法应全保留, got %v", got)
	}
	if got := dropTruncatedToolCalls(nil); len(got) != 0 {
		t.Errorf("nil 输入应返回空, got %v", got)
	}
}

// ---- usage.go ----

// TestUpstreamParseUsageNumber usageNumber 的缺失/类型语义（缺失 → ok=false 而非 0,true）。
func TestUpstreamParseUsageNumber(t *testing.T) {
	cases := []struct {
		name   string
		usage  map[string]any
		key    string
		want   float64
		wantOK bool
	}{
		{"缺失键", map[string]any{}, "prompt_tokens", 0, false},
		{"nil map", nil, "prompt_tokens", 0, false},
		{"float64", map[string]any{"prompt_tokens": 12.5}, "prompt_tokens", 12.5, true},
		{"int", map[string]any{"prompt_tokens": 7}, "prompt_tokens", 7, true},
		{"int64", map[string]any{"prompt_tokens": int64(9)}, "prompt_tokens", 9, true},
		{"json.Number 合法", map[string]any{"prompt_tokens": json.Number("31")}, "prompt_tokens", 31, true},
		{"json.Number 非法", map[string]any{"prompt_tokens": json.Number("abc")}, "prompt_tokens", 0, false},
		{"字符串不解析", map[string]any{"prompt_tokens": "12"}, "prompt_tokens", 0, false},
		{"显式零值存在", map[string]any{"prompt_tokens": float64(0)}, "prompt_tokens", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := usageNumber(c.usage, c.key)
			if got != c.want || ok != c.wantOK {
				t.Errorf("usageNumber = (%v,%v) want (%v,%v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestUpstreamParseUsageCacheHitTokens 多别名取最优 + 别名优先级 + 非正数视为缺失。
func TestUpstreamParseUsageCacheHitTokens(t *testing.T) {
	cases := []struct {
		name   string
		usage  map[string]any
		want   float64
		wantOK bool
	}{
		{"nil", nil, 0, false},
		{"空 map", map[string]any{}, 0, false},
		{"仅全零别名 → 缺失", map[string]any{"cached_tokens": 0, "cache_read_input_tokens": 0}, 0, false},
		{"嵌套 prompt_tokens_details.cached_tokens 优先于顶层别名",
			map[string]any{
				"prompt_tokens_details":   map[string]any{"cached_tokens": 1500},
				"prompt_cache_hit_tokens": 3000,
				"cache_read_input_tokens": 2000,
			}, 1500, true},
		{"无嵌套时 prompt_cache_hit_tokens 优先于 cache_read_input_tokens",
			map[string]any{"prompt_cache_hit_tokens": 5, "cache_read_input_tokens": 7}, 5, true},
		{"退回 cache_read_input_tokens", map[string]any{"cache_read_input_tokens": 7}, 7, true},
		{"退回顶层 cached_tokens", map[string]any{"cached_tokens": 9}, 9, true},
		{"退回 input_tokens_details.cached_tokens", map[string]any{"input_tokens_details": map[string]any{"cached_tokens": 11}}, 11, true},
		{"int64 形态", map[string]any{"prompt_cache_hit_tokens": int64(42)}, 42, true},
		{"int 形态", map[string]any{"prompt_cache_hit_tokens": 43}, 43, true},
		{"int32 形态", map[string]any{"prompt_cache_hit_tokens": int32(44)}, 44, true},
		{"uint 形态", map[string]any{"prompt_cache_hit_tokens": uint(45)}, 45, true},
		{"uint32 形态", map[string]any{"prompt_cache_hit_tokens": uint32(46)}, 46, true},
		{"uint64 形态", map[string]any{"prompt_cache_hit_tokens": uint64(47)}, 47, true},
		{"float32 形态", map[string]any{"prompt_cache_hit_tokens": float32(48)}, 48, true},
		{"负数视为缺失", map[string]any{"prompt_cache_hit_tokens": -3}, 0, false},
		{"非数字类型视为缺失", map[string]any{"prompt_cache_hit_tokens": "100"}, 0, false},
		{"嵌套段非 map 不崩", map[string]any{"prompt_tokens_details": "oops", "cached_tokens": 4}, 4, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := UsageCacheHitTokens(c.usage)
			if got != c.want || ok != c.wantOK {
				t.Errorf("UsageCacheHitTokens = (%v,%v) want (%v,%v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestUpstreamParseUsageCacheMissTokens 显式 miss 优先；缺失时按 prompt-hit 推导，
// 推导为负（命中 > prompt）视为不可信 → ok=false。
func TestUpstreamParseUsageCacheMissTokens(t *testing.T) {
	cases := []struct {
		name   string
		usage  map[string]any
		want   float64
		wantOK bool
	}{
		{"nil", nil, 0, false},
		{"空 map", map[string]any{}, 0, false},
		{"显式 prompt_cache_miss_tokens 优先",
			map[string]any{"prompt_cache_miss_tokens": 42, "prompt_tokens": 100, "cached_tokens": 10}, 42, true},
		{"显式零值仍优先于推导",
			map[string]any{"prompt_cache_miss_tokens": json.Number("0"), "prompt_tokens": 100, "cached_tokens": 10}, 0, true},
		{"推导 prompt - hit", map[string]any{"prompt_tokens": 100, "cached_tokens": 40}, 60, true},
		{"推导正好为 0", map[string]any{"prompt_tokens": 100, "cached_tokens": 100}, 0, true},
		{"命中大于 prompt → 不可信", map[string]any{"prompt_tokens": 100, "cached_tokens": 150}, 0, false},
		{"只有 prompt 无命中 → 无法推导", map[string]any{"prompt_tokens": 100}, 0, false},
		{"只有命中无 prompt → 无法推导", map[string]any{"cached_tokens": 40}, 0, false},
		{"嵌套命中参与推导",
			map[string]any{"prompt_tokens": 500, "prompt_tokens_details": map[string]any{"cached_tokens": 120}}, 380, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := UsageCacheMissTokens(c.usage)
			if got != c.want || ok != c.wantOK {
				t.Errorf("UsageCacheMissTokens = (%v,%v) want (%v,%v)", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// ---- effort_catalog.go / context_catalog.go ----

// TestUpstreamParseStaticEffortCap 按 realm 分表：同 id 在 CN / global 档位不同，
// 绝不混用（deepseek-v4.1-flash：CN 三档 vs global 仅 high）。
func TestUpstreamParseStaticEffortCap(t *testing.T) {
	cn := staticEffortCap("", "deepseek-v4.1-flash") // 空 realm → 归一化 cn
	if len(cn.efforts) != 3 || cn.defaultEffort != "high" {
		t.Errorf("cn deepseek-v4.1-flash = %+v want 3 档 default high", cn)
	}
	if cn.efforts[0] != "low" || cn.efforts[2] != "max" {
		t.Errorf("cn 档位顺序 = %v want [low high max]", cn.efforts)
	}

	gl := staticEffortCap("global", "deepseek-v4.1-flash")
	if len(gl.efforts) != 1 || gl.efforts[0] != "high" {
		t.Errorf("global deepseek-v4.1-flash = %v want [high]（国际版只认 high）", gl.efforts)
	}
	if gl.defaultEffort != "" {
		t.Errorf("global defaultEffort = %q want \"\"", gl.defaultEffort)
	}

	// 固定单档模型（glm-5.1 只有 medium，且无显式默认档）。
	if cap := staticEffortCap("cn", "glm-5.1"); len(cap.efforts) != 1 || cap.efforts[0] != "medium" || cap.defaultEffort != "" {
		t.Errorf("cn glm-5.1 = %+v want [medium]/\"\"", cap)
	}
	// global 独有模型在 CN 表不存在（两表不混用）。
	if cap := staticEffortCap("cn", "primary-model"); len(cap.efforts) != 0 {
		t.Errorf("cn primary-model 不应命中（global 独有）: %+v", cap)
	}
	if cap := staticEffortCap("global", "kimi-k3"); len(cap.efforts) != 1 || cap.efforts[0] != "medium" {
		t.Errorf("global kimi-k3 = %+v want [medium]", cap)
	}
	// 未命中 → zero cap。
	if cap := staticEffortCap("cn", "no-such-model"); len(cap.efforts) != 0 || cap.defaultEffort != "" {
		t.Errorf("未命中应为零值: %+v", cap)
	}
}

// TestUpstreamParseEffortListing 三级查找 + 默认档防御（default ∉ efforts 不返回）。
func TestUpstreamParseEffortListing(t *testing.T) {
	// 远端权威：非空 remoteEfforts 时不回落静态表。
	efforts, def := EffortListing("cn", "glm-5.2", []string{"only-remote"}, "only-remote")
	if len(efforts) != 1 || efforts[0] != "only-remote" || def != "only-remote" {
		t.Errorf("远端权威 = (%v,%q) want ([only-remote],only-remote)", efforts, def)
	}

	// 默认档不在档位表内 → 不宣称该默认档（防御）。
	efforts, def = EffortListing("cn", "glm-5.2", []string{"low", "high"}, "max")
	if def != "" {
		t.Errorf("default 不在 efforts 内应返回空串, got %q", def)
	}
	if len(efforts) != 2 {
		t.Errorf("efforts = %v want 2 档", efforts)
	}

	// 远端有档位但默认档空 → 不回落到静态默认档（默认档随档位表同源）。
	if _, def := EffortListing("cn", "glm-5.2", []string{"low"}, ""); def != "" {
		t.Errorf("远端默认档空时不应回落静态默认档, got %q", def)
	}

	// 远端空 → 静态兜底（glm-5.2 CN: [high xhigh] default high）。
	efforts, def = EffortListing("cn", "glm-5.2", nil, "ignored")
	if len(efforts) != 2 || efforts[0] != "high" || efforts[1] != "xhigh" || def != "high" {
		t.Errorf("静态兜底 = (%v,%q) want ([high xhigh],high)", efforts, def)
	}

	// 静态兜底无默认档（glm-5.1 单档 medium）→ default 空。
	if efforts, def := EffortListing("cn", "glm-5.1", nil, ""); len(efforts) != 1 || def != "" {
		t.Errorf("glm-5.1 静态兜底 = (%v,%q) want ([medium],\"\")", efforts, def)
	}

	// 两源皆无 → nil（调用方省略字段，不输出空数组）。
	if efforts, def := EffortListing("cn", "no-such-model", nil, ""); efforts != nil || def != "" {
		t.Errorf("两源皆无 = (%v,%q) want (nil,\"\")", efforts, def)
	}

	// global 域静态表（WorkBuddy 面）：primary-model 单档 high 无默认档。
	if efforts, def := EffortListing("global", "primary-model", nil, ""); len(efforts) != 1 || efforts[0] != "high" || def != "" {
		t.Errorf("global primary-model = (%v,%q) want ([high],\"\")", efforts, def)
	}

	// 返回副本：改返回值不得污染静态表。
	got, _ := EffortListing("cn", "glm-5.2", nil, "")
	got[0] = "MUTATED"
	if again, _ := EffortListing("cn", "glm-5.2", nil, ""); again[0] != "high" {
		t.Errorf("返回 slice 未做副本，静态表被污染: %v", again)
	}
}

// TestUpstreamParseContainsEffort 档位成员判定。
func TestUpstreamParseContainsEffort(t *testing.T) {
	if !containsEffort([]string{"low", "high"}, "high") {
		t.Error("应命中 high")
	}
	if containsEffort([]string{"low", "high"}, "High") {
		t.Error("精确匹配，大小写不同不应命中")
	}
	if containsEffort(nil, "high") || containsEffort([]string{}, "high") {
		t.Error("空表不应命中")
	}
}

// TestUpstreamParseGlobalEffortMap 静态表为基 + 远端桶覆盖（远端空 slice/空默认档不覆盖）。
func TestUpstreamParseGlobalEffortMap(t *testing.T) {
	// 远端全空：静态兜底照常生效（issue #84：不全程透传）。
	efforts, defs := globalEffortMap(nil, nil)
	if len(efforts["gpt-5.5"]) != 4 || efforts["gpt-5.5"][0] != "low" {
		t.Errorf("静态基 gpt-5.5 = %v want [low medium high xhigh]", efforts["gpt-5.5"])
	}
	if defs["gpt-5.5"] != "high" {
		t.Errorf("静态默认档 gpt-5.5 = %q want high", defs["gpt-5.5"])
	}
	if _, ok := defs["fast-model"]; ok {
		t.Error("静态条目无默认档时不应写入 defs")
	}
	if len(efforts["deepseek-v4.1-flash"]) != 1 || efforts["deepseek-v4.1-flash"][0] != "high" {
		t.Errorf("国际版 deepseek-v4.1-flash 必须只认 high, got %v", efforts["deepseek-v4.1-flash"])
	}

	// 远端权威覆盖；远端空 slice / 空默认档不得覆盖。
	efforts, defs = globalEffortMap(
		map[string][]string{
			"glm-5.2": {"only-remote"},
			"gpt-5.5": {}, // 空：不覆盖静态
			"x-new":   {"low"},
		},
		map[string]string{
			"glm-5.2": "", // 空：不覆盖静态默认档
			"x-new":   "low",
		})
	if len(efforts["glm-5.2"]) != 1 || efforts["glm-5.2"][0] != "only-remote" {
		t.Errorf("远端应覆盖 glm-5.2, got %v", efforts["glm-5.2"])
	}
	if defs["glm-5.2"] != "high" {
		t.Errorf("远端空默认档不应清空静态默认档, got %q", defs["glm-5.2"])
	}
	if len(efforts["gpt-5.5"]) != 4 {
		t.Errorf("远端空 slice 不应覆盖静态, got %v", efforts["gpt-5.5"])
	}
	if len(efforts["x-new"]) != 1 || defs["x-new"] != "low" {
		t.Errorf("远端独有模型应入表: efforts=%v defs=%q", efforts["x-new"], defs["x-new"])
	}

	// 返回的 slice 是副本：改它不污染静态表。
	efforts["gpt-5.5"][0] = "MUTATED"
	againEfforts, _ := globalEffortMap(nil, nil)
	if againEfforts["gpt-5.5"][0] != "low" {
		t.Errorf("静态表被返回值污染: %v", againEfforts["gpt-5.5"])
	}
}

// TestUpstreamParseContextWindowListing 三级查找：remote 权威 > 知识表 > 1M 兜底。
func TestUpstreamParseContextWindowListing(t *testing.T) {
	if DefaultContextWindow != 1000000 {
		t.Errorf("DefaultContextWindow = %d want 1000000", DefaultContextWindow)
	}
	cases := []struct {
		name   string
		model  string
		remote int64
		want   int64
	}{
		{"remote 权威", "glm-5.1", 4242, 4242},
		{"知识表命中", "glm-5.1", 0, 200000},
		{"知识表命中（1M 家族）", "kimi-k3", 0, 1048576},
		{"知识表未收录 → 1M 兜底（宁可高估）", "no-such-model", 0, DefaultContextWindow},
		{"空模型名 → 1M 兜底", "", 0, DefaultContextWindow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ContextWindowListing(c.model, c.remote); got != c.want {
				t.Errorf("ContextWindowListing(%q,%d) = %d want %d", c.model, c.remote, got, c.want)
			}
		})
	}
}

// TestUpstreamParseMaxOutputTokensListing 输出上限未知 → ok=false（无「宁可高估」侧）。
func TestUpstreamParseMaxOutputTokensListing(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		remote int64
		want   int64
		wantOK bool
	}{
		{"remote 权威", "glm-5.1", 999, 999, true},
		{"知识表命中", "kimi-k2.6", 0, 262144, true},
		{"知识表记录输出未知（0）→ 省略", "kimi-k2.8-preview", 0, 0, false},
		{"知识表未收录 → 省略", "no-such-model", 0, 0, false},
		{"空模型名 → 省略", "", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := MaxOutputTokensListing(c.model, c.remote)
			if got != c.want || ok != c.wantOK {
				t.Errorf("MaxOutputTokensListing(%q,%d) = (%d,%v) want (%d,%v)", c.model, c.remote, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// ---- global_models.go 纯解析 ----

// TestUpstreamParseExtractEfforts 抽取 effort 桶 + 有序 names，空 ID 条目跳过。
func TestUpstreamParseExtractEfforts(t *testing.T) {
	names, out, efforts, defaults := extractEfforts([]ModelInfo{
		{ID: "a", Efforts: []string{"low", "high"}, DefaultEffort: "high"},
		{ID: "", Efforts: []string{"max"}}, // 空 ID 跳过（含其 effort）
		{ID: "b", DefaultEffort: "medium"},
		{ID: "c"}, // 无 effort 无默认档
	})
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Errorf("names = %v want [a b c]", names)
	}
	if len(out) != 3 {
		t.Errorf("infos len = %d want 3", len(out))
	}
	if len(efforts) != 1 || efforts["a"][1] != "high" {
		t.Errorf("efforts = %v want {a:[low high]}", efforts)
	}
	if len(defaults) != 2 || defaults["a"] != "high" || defaults["b"] != "medium" {
		t.Errorf("defaults = %v want {a:high b:medium}", defaults)
	}
	if _, ok := efforts[""]; ok {
		t.Error("空 ID 不应进 effort 桶")
	}

	// 空输入 → 空 names，efforts/defaults 为 nil。
	names, out, efforts, defaults = extractEfforts(nil)
	if len(names) != 0 || len(out) != 0 || efforts != nil || defaults != nil {
		t.Errorf("空输入 = (%v,%v,%v,%v) want 全空/nil", names, out, efforts, defaults)
	}
}

// TestUpstreamParseMergeEffortBuckets 主路权威：企业端点只补主路缺失的模型档位。
func TestUpstreamParseMergeEffortBuckets(t *testing.T) {
	primary := map[string][]string{"a": {"low"}}
	secondary := map[string][]string{"a": {"max"}, "b": {"high"}}
	got := mergeEffortBuckets(primary, secondary)
	if len(got) != 2 || got["a"][0] != "low" || got["b"][0] != "high" {
		t.Errorf("merge = %v want {a:[low] b:[high]}", got)
	}

	// secondary 空 → 直接返回 primary。
	if got := mergeEffortBuckets(map[string][]string{"a": {"low"}}, nil); len(got) != 1 || got["a"][0] != "low" {
		t.Errorf("secondary 空 = %v", got)
	}
	// primary nil → 以 secondary 建表。
	if got := mergeEffortBuckets(nil, map[string][]string{"b": {"high"}}); len(got) != 1 || got["b"][0] != "high" {
		t.Errorf("primary nil = %v", got)
	}
	// 两源皆空 → nil。
	if got := mergeEffortBuckets(nil, nil); got != nil {
		t.Errorf("两源皆空 = %v want nil", got)
	}
}

// TestUpstreamParseMergeEffortDefaults 默认档同款合并语义。
func TestUpstreamParseMergeEffortDefaults(t *testing.T) {
	got := mergeEffortDefaults(map[string]string{"a": "low"}, map[string]string{"a": "max", "b": "high"})
	if len(got) != 2 || got["a"] != "low" || got["b"] != "high" {
		t.Errorf("merge = %v want {a:low b:high}", got)
	}
	if got := mergeEffortDefaults(nil, nil); got != nil {
		t.Errorf("两源皆空 = %v want nil", got)
	}
	if got := mergeEffortDefaults(nil, map[string]string{"b": "high"}); got["b"] != "high" {
		t.Errorf("primary nil = %v", got)
	}
	if got := mergeEffortDefaults(map[string]string{"a": "low"}, nil); got["a"] != "low" {
		t.Errorf("secondary nil = %v", got)
	}
}

// TestUpstreamParseResolveGlobalModelsArray 多信封定位模型数组（递归下钻）。
func TestUpstreamParseResolveGlobalModelsArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"顶层数组", `[{"id":"a"}]`, true},
		{"models 键", `{"models":[{"id":"a"}]}`, true},
		{"items 键", `{"items":["a"]}`, true},
		{"list 键", `{"list":["a"]}`, true},
		{"data 键", `{"data":["a"]}`, true},
		{"result 键", `{"result":["a"]}`, true},
		{"嵌套下钻 data.models", `{"data":{"models":["a"]}}`, true},
		{"嵌套下钻 data.result", `{"data":{"result":["a"]}}`, true},
		{"空数组也算定位成功（上层按空名单处理）", `{"models":[]}`, true},
		{"无数组键", `{"foo":"bar"}`, false},
		{"未知键下的数组不识别", `{"a":{"items":["x"]}}`, false},
		{"非法 JSON", `{"a":`, false},
		{"null 载荷", `null`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := resolveGlobalModelsArray(json.RawMessage(c.in))
			if ok != c.want {
				t.Fatalf("ok = %v want %v", ok, c.want)
			}
			if ok {
				var arr []any
				if err := json.Unmarshal(got, &arr); err != nil {
					t.Errorf("定位结果不是数组: %s (%v)", got, err)
				}
			}
		})
	}
}

// TestUpstreamParseGlobalModelNamesObjectShape 主形态：对象数组 → 全字段 + effort 桶，
// id 回退链 id→modelId→model→name，disabled 剔除。
func TestUpstreamParseGlobalModelNamesObjectShape(t *testing.T) {
	raw := []byte(`{"code":0,"data":{"models":[
		{"id":"gpt-5.5","maxInputTokens":900000,"maxOutputTokens":64000,"supportsImages":true,"supportsReasoning":true,
		 "reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}},
		{"name":"alias-model","maxInputTokens":1000},
		{"modelId":"by-modelId"},
		{"model":"by-model"},
		{"id":"dead-model","disabled":true}
	]}}`)
	names, infos, efforts, defaults, err := parseGlobalModelNames(raw)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	wantNames := []string{"gpt-5.5", "alias-model", "by-modelId", "by-model"}
	if len(names) != len(wantNames) {
		t.Fatalf("names = %v want %v", names, wantNames)
	}
	for i, want := range wantNames {
		if names[i] != want {
			t.Errorf("names[%d] = %q want %q (full %v)", i, names[i], want, names)
		}
	}
	if len(infos) != len(wantNames) {
		t.Fatalf("infos len = %d want %d", len(infos), len(wantNames))
	}
	if infos[0].ID != "gpt-5.5" || infos[0].ContextWindow != 900000 || infos[0].MaxTokens != 64000 {
		t.Errorf("infos[0] 窗口字段 = %+v want 900000/64000", infos[0])
	}
	if !infos[0].SupportsImages || !infos[0].SupportsReasoning {
		t.Errorf("infos[0] 能力字段丢失: %+v", infos[0])
	}
	if infos[1].ID != "alias-model" || infos[1].ContextWindow != 1000 {
		t.Errorf("name 回退条目 = %+v want ID=alias-model/1000", infos[1])
	}
	if len(efforts) != 1 || efforts["gpt-5.5"][0] != "low" || efforts["gpt-5.5"][1] != "high" {
		t.Errorf("efforts = %v want {gpt-5.5:[low high]}", efforts)
	}
	if len(defaults) != 1 || defaults["gpt-5.5"] != "high" {
		t.Errorf("defaults = %v want {gpt-5.5:high}", defaults)
	}
	for _, n := range names {
		if n == "dead-model" {
			t.Error("disabled 条目必须剔除")
		}
	}
}

// TestUpstreamParseGlobalModelNamesNarrowShape 窄表：字符串数组 → 仅 ID，元数据不编造。
func TestUpstreamParseGlobalModelNamesNarrowShape(t *testing.T) {
	names, infos, efforts, defaults, err := parseGlobalModelNames([]byte(`{"code":0,"data":["a"," b ","",  "c"]}`))
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Errorf("names = %v want [a b c]（空白 trim、空串剔除）", names)
	}
	if infos != nil {
		t.Errorf("窄表不应产出对象字段 infos, got %v", infos)
	}
	if efforts != nil || defaults != nil {
		t.Errorf("窄表无 effort 元数据, got %v/%v", efforts, defaults)
	}
}

// TestUpstreamParseGlobalModelNamesLooseShape 动态兜底：键名全对不上时逐对象宽松解析
// （混合数组里的裸字符串按窄表条目处理）。
func TestUpstreamParseGlobalModelNamesLooseShape(t *testing.T) {
	raw := []byte(`{"data":{"result":[
		{"modelId":"m1","contextWindow":200000,"maxTokens":4096,
		 "reasoning":{"effort":"low","supportedEfforts":["low","high"]}},
		"m2",
		{"name":"m3","supportsImages":true}
	]}}`)
	names, infos, efforts, defaults, err := parseGlobalModelNames(raw)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	wantNames := []string{"m1", "m2", "m3"}
	if len(names) != len(wantNames) {
		t.Fatalf("names = %v want %v", names, wantNames)
	}
	for i, want := range wantNames {
		if names[i] != want {
			t.Errorf("names[%d] = %q want %q", i, names[i], want)
		}
	}
	if infos[0].ContextWindow != 200000 || infos[0].MaxTokens != 4096 {
		t.Errorf("宽松解析窗口 = %d/%d want 200000/4096", infos[0].ContextWindow, infos[0].MaxTokens)
	}
	if infos[1].ID != "m2" || infos[1].ContextWindow != 0 {
		t.Errorf("裸 ID 条目应只有 ID: %+v", infos[1])
	}
	if !infos[2].SupportsImages {
		t.Errorf("宽松解析应保留 supportsImages: %+v", infos[2])
	}
	if len(efforts["m1"]) != 2 || defaults["m1"] != "low" {
		t.Errorf("宽松路径 effort 桶 = %v/%v want m1:[low high]/low", efforts, defaults)
	}
}

// TestUpstreamParseGlobalModelNamesErrors 信封拒码 / 空名单 / 全 disabled 的报空语义。
func TestUpstreamParseGlobalModelNamesErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"业务码非 0", `{"code":11102,"msg":"no such model","data":["a"]}`},
		{"非 JSON", `nope`},
		{"顶层裸数组被信封层拒绝（信封必须是对象）", `["a","b"]`},
		{"data 为 null 且无数组键", `{"code":0,"data":null}`},
		{"窄表全空串", `{"data":["", "  "]}`},
		{"全 disabled 的对象数组走宽松路径后仍空", `{"models":[{"id":"x","disabled":true}]}`},
		{"数组内元素无可解析 id", `{"data":[{"foo":"bar"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			names, infos, efforts, defaults, err := parseGlobalModelNames([]byte(c.in))
			if err == nil {
				t.Fatalf("应报错, got names=%v infos=%v efforts=%v defaults=%v", names, infos, efforts, defaults)
			}
			if names != nil || infos != nil || efforts != nil || defaults != nil {
				t.Errorf("报错时应返回全 nil, got %v/%v/%v/%v", names, infos, efforts, defaults)
			}
		})
	}

	// 无 code 字段的信封放行（兼容无信封直出的目录端点）。
	names, _, _, _, err := parseGlobalModelNames([]byte(`{"data":["a"]}`))
	if err != nil || len(names) != 1 || names[0] != "a" {
		t.Errorf("无 code 字段应放行, got names=%v err=%v", names, err)
	}
	// code=0 + data 为数组（payload 取 data）。
	names, _, _, _, err = parseGlobalModelNames([]byte(`{"code":0,"data":["only"]}`))
	if err != nil || len(names) != 1 || names[0] != "only" {
		t.Errorf("code=0 直出数组应解析, got names=%v err=%v", names, err)
	}
}

// TestUpstreamParseGlobalModelLoose parseGlobalModelLoose 的键回退与值语义。
func TestUpstreamParseGlobalModelLoose(t *testing.T) {
	// 全字段 + reasoning 新旧键：defaultEffort 优先、effort 兜底、supportedEfforts 过滤脏项。
	mi, ok := parseGlobalModelLoose(map[string]any{
		"modelId":           "  m1  ",
		"maxInputTokens":    float64(100),
		"maxOutputTokens":   float64(50),
		"maxAllowedSize":    float64(200),
		"supportsReasoning": true,
		"supportsImages":    true,
		"reasoning": map[string]any{
			"defaultEffort":      "low",
			"effort":             "high",
			"canDisableThinking": true,
			"supportedEfforts":   []any{"low", "", " high ", 7},
		},
	})
	if !ok {
		t.Fatal("应解析成功")
	}
	if mi.ID != "m1" {
		t.Errorf("ID = %q want m1（trim + modelId 回退）", mi.ID)
	}
	if mi.ContextWindow != 100 || mi.MaxTokens != 50 || mi.MaxAllowedSize != 200 {
		t.Errorf("窗口字段 = %d/%d/%d want 100/50/200", mi.ContextWindow, mi.MaxTokens, mi.MaxAllowedSize)
	}
	if !mi.SupportsReasoning || !mi.SupportsImages || !mi.CanDisableThinking {
		t.Errorf("能力字段丢失: %+v", mi)
	}
	if mi.DefaultEffort != "low" {
		t.Errorf("DefaultEffort = %q want low（新键优先）", mi.DefaultEffort)
	}
	if len(mi.Efforts) != 2 || mi.Efforts[0] != "low" || mi.Efforts[1] != "high" {
		t.Errorf("Efforts = %v want [low high]（空白 trim + 非字符串剔除 + 空串剔除）", mi.Efforts)
	}

	// effort 老键兜底。
	mi, ok = parseGlobalModelLoose(map[string]any{"id": "m2", "reasoning": map[string]any{"effort": "high"}})
	if !ok || mi.DefaultEffort != "high" {
		t.Errorf("老键兜底 = %+v/%v want high", mi, ok)
	}

	// id 回退链：model → name。
	if mi, ok := parseGlobalModelLoose(map[string]any{"model": "by-model"}); !ok || mi.ID != "by-model" {
		t.Errorf("model 键回退失败: %+v/%v", mi, ok)
	}
	if mi, ok := parseGlobalModelLoose(map[string]any{"name": "by-name"}); !ok || mi.ID != "by-name" {
		t.Errorf("name 键回退失败: %+v/%v", mi, ok)
	}

	// 无 id（含全空白 id）→ 判不出。
	if _, ok := parseGlobalModelLoose(map[string]any{"foo": "bar"}); ok {
		t.Error("无 id 应判不出")
	}
	if _, ok := parseGlobalModelLoose(map[string]any{"id": "   "}); ok {
		t.Error("空白 id 应判不出")
	}
	// disabled 剔除；disabled 非 bool 不触发剔除（宽松容错）。
	if _, ok := parseGlobalModelLoose(map[string]any{"id": "x", "disabled": true}); ok {
		t.Error("disabled 应剔除")
	}
	if _, ok := parseGlobalModelLoose(map[string]any{"id": "x", "disabled": "yes"}); !ok {
		t.Error("disabled 非 bool 不应剔除")
	}

	// contextWindow / maxTokens 键回退；非正数与非数字不采信。
	mi, _ = parseGlobalModelLoose(map[string]any{"id": "m3", "contextWindow": float64(300), "maxTokens": float64(60)})
	if mi.ContextWindow != 300 || mi.MaxTokens != 60 {
		t.Errorf("键回退 = %d/%d want 300/60", mi.ContextWindow, mi.MaxTokens)
	}
	mi, _ = parseGlobalModelLoose(map[string]any{"id": "m4", "maxInputTokens": float64(0), "contextWindow": float64(-5)})
	if mi.ContextWindow != 0 {
		t.Errorf("非正数不得采信, got %d", mi.ContextWindow)
	}
	mi, _ = parseGlobalModelLoose(map[string]any{"id": "m5", "maxInputTokens": "1000"})
	if mi.ContextWindow != 0 {
		t.Errorf("字符串数字不得强转, got %d", mi.ContextWindow)
	}
}

// TestUpstreamParseGlobalModelsOnce 单端点探测：2xx 解析 / 非 2xx / 传输层失败，
// 且 URL 按 realm 切 global base。
func TestUpstreamParseGlobalModelsOnce(t *testing.T) {
	a := upstreamparseGlobalAuth()
	const wantURL = "https://global.example/v2/enterprises/personal/models"

	newClient := func(fn rtFunc) *Client {
		return &Client{
			HTTP:           &http.Client{Transport: fn},
			ChatBaseCN:     "https://cn.example",
			ChatBaseGlobal: "https://global.example",
			GlobalEnabled:  true,
		}
	}

	t.Run("2xx 窄表 → names + nil infos", func(t *testing.T) {
		var gotURL string
		c := newClient(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return jsonResp(200, `{"code":0,"data":["m1","m2"]}`), nil
		})
		names, infos, err := c.globalModelsOnce(a, "/v2/enterprises/personal/models")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if gotURL != wantURL {
			t.Errorf("URL = %q want %q（global 账号必须切 global base）", gotURL, wantURL)
		}
		if len(names) != 2 || names[0] != "m1" || names[1] != "m2" || infos != nil {
			t.Errorf("names/infos = %v/%v want [m1 m2]/nil", names, infos)
		}
	})

	t.Run("2xx 对象数组 → 全字段 infos", func(t *testing.T) {
		c := newClient(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, `{"models":[{"id":"gpt-5.5","maxInputTokens":900000}]}`), nil
		})
		names, infos, err := c.globalModelsOnce(a, "/v2/enterprises/personal/models")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if len(names) != 1 || len(infos) != 1 || infos[0].ContextWindow != 900000 {
			t.Errorf("names/infos = %v/%v want 1 条 900000", names, infos)
		}
	})

	t.Run("非 2xx → err", func(t *testing.T) {
		c := newClient(func(r *http.Request) (*http.Response, error) {
			return jsonResp(500, `{"msg":"boom"}`), nil
		})
		names, infos, err := c.globalModelsOnce(a, "/v2/enterprises/personal/models")
		if err == nil || names != nil || infos != nil {
			t.Errorf("非 2xx 应报错并返回 nil, got %v/%v/%v", names, infos, err)
		}
	})

	t.Run("传输层失败 → err 且不解析半截 body", func(t *testing.T) {
		c := newClient(func(r *http.Request) (*http.Response, error) {
			return nil, upstreamparseTransportErr
		})
		names, infos, err := c.globalModelsOnce(a, "/v2/enterprises/personal/models")
		if err == nil || names != nil || infos != nil {
			t.Errorf("传输失败应报错并返回 nil, got %v/%v/%v", names, infos, err)
		}
	})
}

// ---- modelsdev.go ----

// TestUpstreamParseModelsDevDocVendorPriority 同名多 provider 采值：官方 vendor 源优先。
func TestUpstreamParseModelsDevDocVendorPriority(t *testing.T) {
	raw := []byte(`{
		"agg-gateway": {"models": {"shared": {"limit": {"context": 100, "output": 10}}}},
		"openai":      {"models": {"shared": {"limit": {"context": 200, "output": 20}}}}
	}`)
	doc, err := parseModelsDevDoc(raw)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	got, ok := doc["shared"]
	if !ok {
		t.Fatalf("shared 未入索引: %v", doc)
	}
	if got.Context != 200 || got.Output != 20 {
		t.Errorf("vendor 源未优先: %+v want 200/20", got)
	}
}

// TestUpstreamParseModelsDevDocVotesAndTieBreak 无 vendor 时取众数；
// 票数并列时按 provider 字典序（确定性 tie-break，消灭 map 迭代序抖动）。
func TestUpstreamParseModelsDevDocVotesAndTieBreak(t *testing.T) {
	// 众数：值 A 有 2 票（agg-a / agg-b），值 B 有 1 票。
	modeDoc := []byte(`{
		"agg-a":  {"models": {"m": {"limit": {"context": 100, "output": 10}}}},
		"agg-b":  {"models": {"m": {"limit": {"context": 100, "output": 10}}}},
		"agg-c":  {"models": {"m": {"limit": {"context": 900, "output": 90}}}}
	}`)
	doc, err := parseModelsDevDoc(modeDoc)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if got := doc["m"]; got.Context != 100 || got.Output != 10 {
		t.Errorf("众数采值失败: %+v want 100/10", got)
	}

	// tie-break：1 票 vs 1 票，取 provider 字典序较小者（"aaa" < "zzz"）。
	tieDoc := []byte(`{
		"zzz": {"models": {"m": {"limit": {"context": 200, "output": 20}}}},
		"aaa": {"models": {"m": {"limit": {"context": 100, "output": 10}}}}
	}`)
	// 重复解析同一文档 20 次：结果必须完全一致（map 迭代序随机化不得影响取值）。
	for i := 0; i < 20; i++ {
		doc, err := parseModelsDevDoc(tieDoc)
		if err != nil {
			t.Fatalf("parse err: %v", err)
		}
		if got := doc["m"]; got.Context != 100 || got.Output != 10 {
			t.Fatalf("第 %d 次 tie-break 结果 = %+v want 100/10（须按 provider 字典序确定性选择）", i, got)
		}
	}
}

// TestUpstreamParseModelsDevDocNamespaceAndFilters 命名空间取尾段 + 脏值/畸形条目剔除。
func TestUpstreamParseModelsDevDocNamespaceAndFilters(t *testing.T) {
	raw := []byte(`{
		"openai": {"models": {
			"openai/gpt-5.5":   {"limit": {"context": 1000, "output": 100}},
			"openai/":          {"limit": {"context": 1111, "output": 111}},
			"no-limit":         {},
			"zero-context":     {"limit": {"context": 0, "output": 10}},
			"negative-context": {"limit": {"context": -5, "output": 10}},
			"huge-context":     {"limit": {"context": 2000000000, "output": 10}},
			"negative-output":  {"limit": {"context": 100, "output": -1}},
			"huge-output":      {"limit": {"context": 100, "output": 2000000000}},
			"zero-output":      {"limit": {"context": 512, "output": 0}}
		}}
	}`)
	doc, err := parseModelsDevDoc(raw)
	if err != nil {
		t.Fatalf("parse err: %v", err)
	}
	if got := doc["gpt-5.5"]; got.Context != 1000 || got.Output != 100 {
		t.Errorf("命名空间尾段索引失败: %+v（doc=%v）", got, doc)
	}
	if _, ok := doc["no-limit"]; ok {
		t.Error("无 limit 条目应跳过")
	}
	for _, bad := range []string{"zero-context", "negative-context", "huge-context", "negative-output", "huge-output"} {
		if _, ok := doc[bad]; ok {
			t.Errorf("脏值条目 %s 不应入索引", bad)
		}
	}
	// output 为 0 是合法值（输出上限未知 → 保留条目，调用方按 0 省略）。
	if got, ok := doc["zero-output"]; !ok || got.Context != 512 || got.Output != 0 {
		t.Errorf("output=0 条目应保留: %+v ok=%v", got, ok)
	}
	if len(doc) != 2 {
		t.Errorf("索引规模 = %d want 2（doc=%v）", len(doc), doc)
	}

	// 非法 JSON → 带上下文的错误。
	if _, err := parseModelsDevDoc([]byte("{")); err == nil {
		t.Error("非法 JSON 应报错")
	}
}

// TestUpstreamParseModelsDevLookupAndNegatives lookup 命中/未命中的负缓存语义：
// 重复查询不刷新记录时刻（TTL 只从首次未命中起算）。
func TestUpstreamParseModelsDevLookupAndNegatives(t *testing.T) {
	upstreamparseResetModelsDev(t)

	// 索引空（doc=nil）→ miss + 记负缓存。
	if e, ok := modelsDev.lookup("unknown-a"); ok {
		t.Fatalf("索引未就绪不应命中: %+v", e)
	}
	modelsDev.mu.Lock()
	if _, seen := modelsDev.negatives["unknown-a"]; !seen {
		modelsDev.mu.Unlock()
		t.Fatal("miss 应记入 negatives")
	}
	// 首次未命中的时刻被「盖章」后不再更新（否则 TTL 永远续期）。
	stamped := time.Now().Add(-time.Hour)
	modelsDev.negatives["unknown-a"] = stamped
	modelsDev.mu.Unlock()

	if _, ok := modelsDev.lookup("unknown-a"); ok {
		t.Fatal("负缓存条目不改变 miss 语义")
	}
	modelsDev.mu.Lock()
	got := modelsDev.negatives["unknown-a"]
	modelsDev.mu.Unlock()
	if !got.Equal(stamped) {
		t.Errorf("重复未命中不得刷新记录时刻: got %v want %v", got, stamped)
	}

	// 索引命中 → 不记负缓存，值原样返回。
	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{"known": {Context: 123, Output: 45}}
	modelsDev.mu.Unlock()
	e, ok := modelsDev.lookup("known")
	if !ok || e.Context != 123 || e.Output != 45 {
		t.Errorf("命中值 = %+v ok=%v want 123/45", e, ok)
	}
	modelsDev.mu.Lock()
	_, seen := modelsDev.negatives["known"]
	modelsDev.mu.Unlock()
	if seen {
		t.Error("命中不应记负缓存")
	}
}

// TestUpstreamParseModelsDevNegativeFresh 负缓存有效期判定（24h）。
func TestUpstreamParseModelsDevNegativeFresh(t *testing.T) {
	upstreamparseResetModelsDev(t)

	if modelsDev.negativeFresh("nope") {
		t.Error("无记录 → false")
	}
	modelsDev.mu.Lock()
	modelsDev.negatives = map[string]time.Time{
		"fresh": time.Now().Add(-time.Minute),
		"stale": time.Now().Add(-25 * time.Hour),
	}
	modelsDev.mu.Unlock()

	if !modelsDev.negativeFresh("fresh") {
		t.Error("TTL 内应为 true")
	}
	if modelsDev.negativeFresh("stale") {
		t.Error("超过 24h 应为 false（等效不存在）")
	}
}

// TestUpstreamParseModelsDevNegativesSoftCapEviction 超软上限时惰性淘汰已过期条目
// （还原「lookup 里删掉淘汰扫描」会让负缓存无界增长 → 下面的规模断言 FAIL）。
func TestUpstreamParseModelsDevNegativesSoftCapEviction(t *testing.T) {
	upstreamparseResetModelsDev(t)

	now := time.Now()
	modelsDev.mu.Lock()
	modelsDev.negatives = make(map[string]time.Time, modelsDevNegativesSoftCap)
	for i := 0; i < modelsDevNegativesSoftCap; i++ {
		modelsDev.negatives[upstreamparseNegKey("expired", i)] = now.Add(-25 * time.Hour)
	}
	modelsDev.negatives["keep-fresh"] = now
	modelsDev.mu.Unlock()

	if _, ok := modelsDev.lookup("trigger"); ok {
		t.Fatal("索引空应 miss")
	}
	modelsDev.mu.Lock()
	size := len(modelsDev.negatives)
	_, keptFresh := modelsDev.negatives["keep-fresh"]
	_, keptNew := modelsDev.negatives["trigger"]
	_, keptExpired := modelsDev.negatives[upstreamparseNegKey("expired", 0)]
	modelsDev.mu.Unlock()

	if size != 2 || !keptFresh || !keptNew || keptExpired {
		t.Errorf("过期条目应被淘汰: size=%d keepFresh=%v new=%v expired0=%v", size, keptFresh, keptNew, keptExpired)
	}
}

// TestUpstreamParseModelsDevNegativesHardCapEviction 超硬上限时按时间序丢弃最旧条目，
// 规模削回软上限（纯性能兜底，语义不变）。
func TestUpstreamParseModelsDevNegativesHardCapEviction(t *testing.T) {
	upstreamparseResetModelsDev(t)

	now := time.Now()
	n := modelsDevNegativesHardCap + 1
	modelsDev.mu.Lock()
	modelsDev.negatives = make(map[string]time.Time, n+1)
	for i := 0; i < n; i++ {
		// 时间严格递增且都在 TTL 内（不被软上限那轮的过期扫描删掉）。
		modelsDev.negatives[upstreamparseNegKey("m", i)] = now.Add(-time.Hour).Add(time.Duration(i) * time.Second)
	}
	modelsDev.mu.Unlock()

	modelsDev.lookup("zz-new")

	modelsDev.mu.Lock()
	size := len(modelsDev.negatives)
	_, hasOldest := modelsDev.negatives[upstreamparseNegKey("m", 0)]
	_, hasNewest := modelsDev.negatives[upstreamparseNegKey("m", n-1)]
	_, hasFresh := modelsDev.negatives["zz-new"]
	modelsDev.mu.Unlock()

	if size != modelsDevNegativesSoftCap {
		t.Errorf("硬上限后规模 = %d want %d", size, modelsDevNegativesSoftCap)
	}
	if hasOldest {
		t.Error("最旧条目应被丢弃")
	}
	if !hasNewest || !hasFresh {
		t.Errorf("时间较新的条目应保留: newest=%v fresh=%v", hasNewest, hasFresh)
	}
}

// TestUpstreamParseResetModelsDev resetModelsDev 清空全部单例状态。
func TestUpstreamParseResetModelsDev(t *testing.T) {
	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{"x": {Context: 1}}
	modelsDev.fetched = true
	modelsDev.lastFetch = time.Now()
	modelsDev.negatives = map[string]time.Time{"y": time.Now()}
	modelsDev.mu.Unlock()
	t.Cleanup(resetModelsDev)

	resetModelsDev()

	modelsDev.mu.Lock()
	defer modelsDev.mu.Unlock()
	if modelsDev.doc != nil || modelsDev.fetched || modelsDev.negatives != nil || !modelsDev.lastFetch.IsZero() {
		t.Errorf("reset 未清空: doc=%v fetched=%v negatives=%v lastFetch=%v",
			modelsDev.doc, modelsDev.fetched, modelsDev.negatives, modelsDev.lastFetch)
	}
}

// TestUpstreamParseEnsureDocAsync 触发条件：已有索引 / 冷却期内 / nil client 不回落
// DefaultClient（不打真网）——全部靠注入与状态断言，零网络。
func TestUpstreamParseEnsureDocAsync(t *testing.T) {
	upstreamparseResetModelsDev(t)

	// 1) doc 已就绪 → 直接返回，不刷新 fetched/lastFetch。
	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{}
	modelsDev.mu.Unlock()
	modelsDev.ensureDocAsync(nil, "")
	modelsDev.mu.Lock()
	if modelsDev.fetched || !modelsDev.lastFetch.IsZero() {
		modelsDev.mu.Unlock()
		t.Fatal("doc 就绪时不应触发拉取")
	}
	modelsDev.mu.Unlock()

	// 2) 拉取过且在冷却期内 → 不重拉（lastFetch 不动）。
	modelsDev.mu.Lock()
	modelsDev.doc = nil
	modelsDev.fetched = true
	before := time.Now()
	modelsDev.lastFetch = before
	modelsDev.mu.Unlock()
	modelsDev.ensureDocAsync(nil, "")
	modelsDev.mu.Lock()
	gotLast := modelsDev.lastFetch
	modelsDev.mu.Unlock()
	if !gotLast.Equal(before) {
		t.Errorf("冷却期内不应重拉: lastFetch=%v want %v", gotLast, before)
	}

	// 3) 未拉取 → 置 in-flight 标记并起 goroutine；nil client 被 fetchDoc 拒绝
	//    （不回落 http.DefaultClient，静默降级 1M，不打真网）。
	resetModelsDev()
	modelsDev.ensureDocAsync(nil, "")
	modelsDev.mu.Lock()
	fetched := modelsDev.fetched
	if modelsDev.lastFetch.IsZero() {
		modelsDev.mu.Unlock()
		t.Fatal("触发拉取应置 lastFetch")
	}
	first := modelsDev.lastFetch
	modelsDev.mu.Unlock()
	if !fetched {
		t.Error("触发拉取应置 fetched（in-flight 去重标记）")
	}

	// 第二次调用处于冷却窗口内 → 不重复起拉取。
	modelsDev.ensureDocAsync(nil, "")
	modelsDev.mu.Lock()
	second := modelsDev.lastFetch
	doc := modelsDev.doc
	modelsDev.mu.Unlock()
	if !second.Equal(first) || doc != nil {
		t.Errorf("并发去重失效: first=%v second=%v doc=%v", first, second, doc)
	}
}

// TestUpstreamParseFetchDoc fetchDoc 的拉取/解析/回流全路径（注入 rtFunc，零真网）。
func TestUpstreamParseFetchDoc(t *testing.T) {
	docRaw := `{"zai":{"models":{"glm-5.2":{"limit":{"context":1000000,"output":131072}},"m1":{"limit":{"context":1000,"output":100}}}}}`

	t.Run("nil client 被拒（不回落 DefaultClient）", func(t *testing.T) {
		f := &modelsDevFetcher{}
		f.fetchDoc(nil, "")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.doc != nil {
			t.Error("nil client 不应产出索引")
		}
	})

	t.Run("成功：建索引 + 回流 model.json + 清负缓存", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		f := &modelsDevFetcher{negatives: map[string]time.Time{
			"m1":         time.Now(), // 文档里有 → 应回流并清除
			"still-miss": time.Now(), // 文档里没有 → 保持负缓存
		}}
		var gotURL string
		client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return jsonResp(200, docRaw), nil
		})}
		f.fetchDoc(client, "https://models.example/api.json")

		if gotURL != "https://models.example/api.json" {
			t.Errorf("baseOverride 未生效: URL=%q", gotURL)
		}
		f.mu.Lock()
		entry := f.doc["m1"]
		_, stillNegative := f.negatives["still-miss"]
		_, reflowedNegative := f.negatives["m1"]
		f.mu.Unlock()
		if entry.Context != 1000 || entry.Output != 100 {
			t.Errorf("索引值 = %+v want 1000/100", entry)
		}
		if !stillNegative {
			t.Error("未收录模型应保持负缓存")
		}
		if reflowedNegative {
			t.Error("回流成功的模型应清除负缓存")
		}

		// 回流写入 model.json 缓存（第 4 级 → 第 3 级）。
		e, ok := modelCatalogGet("m1")
		if !ok || e.ContextLength != 1000 || e.MaxOutputTokens != 100 {
			t.Errorf("回流后 model.json 命中 = %+v ok=%v want 1000/100", e, ok)
		}
		if e.Source != "modelsdev" {
			t.Errorf("回流条目 source = %q want modelsdev", e.Source)
		}
		if e.FetchedAt == "" {
			t.Error("回流条目应带 fetched_at")
		}
	})

	t.Run("baseOverride 空串 → 用 ModelsDevURL", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		f := &modelsDevFetcher{}
		var gotURL string
		client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			gotURL = r.URL.String()
			return jsonResp(200, docRaw), nil
		})}
		f.fetchDoc(client, "")
		if gotURL != ModelsDevURL {
			t.Errorf("URL = %q want %q", gotURL, ModelsDevURL)
		}
	})

	t.Run("非 200 → 静默降级不落索引", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		f := &modelsDevFetcher{}
		client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(503, "nope"), nil
		})}
		f.fetchDoc(client, "https://models.example/api.json")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.doc != nil {
			t.Error("非 200 不应落索引")
		}
	})

	t.Run("解析失败 → 静默降级不落索引", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		f := &modelsDevFetcher{}
		client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, "{not json"), nil
		})}
		f.fetchDoc(client, "https://models.example/api.json")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.doc != nil {
			t.Error("解析失败不应落索引")
		}
	})

	t.Run("传输层失败 → 静默降级", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		f := &modelsDevFetcher{}
		client := &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return nil, upstreamparseTransportErr
		})}
		f.fetchDoc(client, "https://models.example/api.json")
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.doc != nil {
			t.Error("传输失败不应落索引")
		}
	})
}

// TestUpstreamParseBackfillMisses 回流清单语义：doc 未就绪不动；只回流文档里查到的
// 模型并清其负缓存，查不到的保持负缓存。
func TestUpstreamParseBackfillMisses(t *testing.T) {
	t.Run("doc 未就绪 → 保持负缓存", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		f := &modelsDevFetcher{negatives: map[string]time.Time{"m": time.Now()}}
		f.backfillMisses()
		f.mu.Lock()
		_, kept := f.negatives["m"]
		f.mu.Unlock()
		if !kept {
			t.Error("doc 未就绪不应清负缓存")
		}
		if _, ok := modelCatalogGet("m"); ok {
			t.Error("doc 未就绪不应写 model.json")
		}
	})

	t.Run("doc 就绪 → 命中回流 + 清负缓存，未命中保留", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		f := &modelsDevFetcher{
			doc:       map[string]modelsDevEntry{"hit": {Context: 777, Output: 12}},
			negatives: map[string]time.Time{"hit": time.Now(), "miss": time.Now()},
		}
		f.backfillMisses()

		f.mu.Lock()
		_, hitNegative := f.negatives["hit"]
		_, missNegative := f.negatives["miss"]
		f.mu.Unlock()
		if hitNegative {
			t.Error("回流成功的模型应清负缓存")
		}
		if !missNegative {
			t.Error("未命中模型应保留负缓存")
		}
		e, ok := modelCatalogGet("hit")
		if !ok || e.ContextLength != 777 || e.MaxOutputTokens != 12 {
			t.Errorf("回流值 = %+v ok=%v want 777/12", e, ok)
		}
	})
}

// ---- model_catalog.go ----

// TestUpstreamParseValidCapEntry 条目校验：context 正数 + 输出非负。
func TestUpstreamParseValidCapEntry(t *testing.T) {
	cases := []struct {
		name string
		e    ModelCapEntry
		want bool
	}{
		{"正常", ModelCapEntry{ContextLength: 1, MaxOutputTokens: 1}, true},
		{"输出未知（0）合法", ModelCapEntry{ContextLength: 1000, MaxOutputTokens: 0}, true},
		{"context 为 0 非法", ModelCapEntry{ContextLength: 0, MaxOutputTokens: 10}, false},
		{"context 负非法", ModelCapEntry{ContextLength: -1}, false},
		{"输出负非法", ModelCapEntry{ContextLength: 10, MaxOutputTokens: -1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validCapEntry(c.e); got != c.want {
				t.Errorf("validCapEntry(%+v) = %v want %v", c.e, got, c.want)
			}
		})
	}
}

// TestUpstreamParseCatalogSeedAndCorrupt 文件缺失/损坏 → 降级内置种子（不崩）；
// 非法条目剔除。还原「损坏时直接空表/panic」会让种子断言 FAIL。
func TestUpstreamParseCatalogSeedAndCorrupt(t *testing.T) {
	t.Run("文件缺失 → 种子初值", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		loadModelCatalogAt(filepath.Join(t.TempDir(), "absent.json"))
		e, ok := modelCatalogGet("glm-5.2")
		if !ok || e.ContextLength != 1000000 || e.MaxOutputTokens != 131072 || e.Source != "seed" {
			t.Errorf("种子条目 = %+v ok=%v want 1000000/131072/seed", e, ok)
		}
		if _, ok := modelCatalogGet("no-such-model"); ok {
			t.Error("未收录模型不应命中")
		}
	})

	t.Run("整体损坏 JSON → 降级种子不崩", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		path := upstreamparseWriteCatalog(t, `{"glm-5.2": {`)
		loadModelCatalogAt(path)
		e, ok := modelCatalogGet("glm-5.2")
		if !ok || e.ContextLength != 1000000 {
			t.Errorf("损坏降级后种子未生效: %+v ok=%v", e, ok)
		}
	})

	t.Run("条目级非法剔除，合法条目保留", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		path := upstreamparseWriteCatalog(t, `{
			"good": {"context_length": 555, "max_output_tokens": 55, "source": "manual"},
			"bad-context": {"context_length": 0, "source": "manual"},
			"bad-output": {"context_length": 10, "max_output_tokens": -3, "source": "manual"}
		}`)
		loadModelCatalogAt(path)
		e, ok := modelCatalogGet("good")
		if !ok || e.ContextLength != 555 || e.MaxOutputTokens != 55 || e.Source != "manual" {
			t.Errorf("合法条目 = %+v ok=%v want 555/55/manual", e, ok)
		}
		if _, ok := modelCatalogGet("bad-context"); ok {
			t.Error("context=0 条目应被剔除")
		}
		if _, ok := modelCatalogGet("bad-output"); ok {
			t.Error("输出为负条目应被剔除")
		}
	})

	t.Run("path 为空 → 纯内存 + 种子，不落盘", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		modelCatalogPut("mem-only", 123, 45)
		e, ok := modelCatalogGet("mem-only")
		if !ok || e.ContextLength != 123 {
			t.Errorf("内存缓存 = %+v ok=%v want 123", e, ok)
		}
		// 种子仍在（空 path 走 loadSeedLocked）。
		if _, ok := modelCatalogGet("glm-5.2"); !ok {
			t.Error("空 path 应加载种子")
		}
	})
}

// TestUpstreamParseCatalogPersistence 落盘（原子 tmp+rename）后重读一致 +
// 非法写入被拒 + 无 .tmp 残留。
func TestUpstreamParseCatalogPersistence(t *testing.T) {
	upstreamparseResetCatalog(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "model.json")
	SetModelCatalogPath(path)

	modelCatalogPutSourced("fresh-model", 123456, 7890, "modelsdev")
	before, ok := modelCatalogGet("fresh-model")
	if !ok {
		t.Fatal("put 后应命中内存缓存")
	}
	if before.ContextLength != 123456 || before.MaxOutputTokens != 7890 || before.Source != "modelsdev" || before.FetchedAt == "" {
		t.Fatalf("put 条目 = %+v want 123456/7890/modelsdev/非空 fetched_at", before)
	}

	// 非法写入被拒（不覆盖、不落盘）。
	modelCatalogPut("fresh-model", 0, 5) // context 非法
	if got, _ := modelCatalogGet("fresh-model"); got.ContextLength != 123456 {
		t.Errorf("非法 put 不应覆盖既有条目: %+v", got)
	}
	modelCatalogPut("", 100, 10) // 空模型名
	if _, ok := modelCatalogGet(""); ok {
		t.Error("空模型名不应写入")
	}

	// 落盘形态：目录里恰好一个 model.json，无 .tmp 残留。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "model.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("落盘文件 = %v want [model.json]（不得留 .tmp）", names)
	}

	// 清空内存后从文件重读 → 值一致（持久化生效）。
	resetModelCatalog()
	loadModelCatalogAt(path)
	after, ok := modelCatalogGet("fresh-model")
	if !ok {
		t.Fatal("重读后应命中")
	}
	if after.ContextLength != before.ContextLength || after.MaxOutputTokens != before.MaxOutputTokens ||
		after.Source != before.Source || after.FetchedAt != before.FetchedAt {
		t.Errorf("重读不一致:\nbefore=%+v\nafter =%+v", before, after)
	}
	// 新写一条后再重读：两条都在。
	modelCatalogPutSourced("second", 1, 2, "manual")
	resetModelCatalog()
	loadModelCatalogAt(path)
	if _, ok := modelCatalogGet("fresh-model"); !ok {
		t.Error("整体落盘应保留既有条目")
	}
	if e, ok := modelCatalogGet("second"); !ok || e.ContextLength != 1 || e.MaxOutputTokens != 2 {
		t.Errorf("新条目未落盘: %+v ok=%v", e, ok)
	}
}

// TestUpstreamParseCatalogLazyLoadAndCache SetModelCatalogPath 惰性首次 get 触发加载；
// 加载后内存为准（不再回读文件）。
func TestUpstreamParseCatalogLazyLoadAndCache(t *testing.T) {
	upstreamparseResetCatalog(t)

	path := upstreamparseWriteCatalog(t, `{"lazy": {"context_length": 111, "max_output_tokens": 11, "source": "manual"}}`)
	SetModelCatalogPath(path)

	// 首次 get 触发加载（未显式调 loadModelCatalogAt）。
	e, ok := modelCatalogGet("lazy")
	if !ok || e.ContextLength != 111 {
		t.Fatalf("惰性加载失败: %+v ok=%v", e, ok)
	}

	// 覆盖文件后内存值不变（loaded=true 短路）。
	if err := os.WriteFile(path, []byte(`{"lazy": {"context_length": 999, "source": "manual"}}`), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, _ := modelCatalogGet("lazy"); got.ContextLength != 111 {
		t.Errorf("加载后应以内存为准, got %d want 111", got.ContextLength)
	}
}

// TestUpstreamParseResetLookupChainForTest 跨包测试钩子清空两级单例。
func TestUpstreamParseResetLookupChainForTest(t *testing.T) {
	upstreamparseResetCatalog(t)
	upstreamparseResetModelsDev(t)

	modelCatalogPut("x", 1, 2)
	modelsDev.mu.Lock()
	modelsDev.doc = map[string]modelsDevEntry{"x": {Context: 9}}
	modelsDev.fetched = true
	modelsDev.mu.Unlock()

	ResetLookupChainForTest()

	if _, ok := modelCatalogGet("x"); ok {
		t.Error("model.json 缓存未清空")
	}
	modelsDev.mu.Lock()
	doc, fetched := modelsDev.doc, modelsDev.fetched
	modelsDev.mu.Unlock()
	if doc != nil || fetched {
		t.Errorf("models.dev 状态未清空: doc=%v fetched=%v", doc, fetched)
	}
}

// TestUpstreamParseContextWindowListingV4 四级查找链优先级：
// remote > 静态种子表 > model.json > models.dev 进程内索引 > 1M 兜底。
func TestUpstreamParseContextWindowListingV4(t *testing.T) {
	upstreamparseResetModelsDev(t)

	t.Run("remote 权威", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		if got := ContextWindowListingV4("glm-5.1", 4242, nil); got != 4242 {
			t.Errorf("＝ %d want 4242", got)
		}
	})

	t.Run("空模型名 → 1M", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		if got := ContextWindowListingV4("", 0, nil); got != DefaultContextWindow {
			t.Errorf("＝ %d want %d", got, DefaultContextWindow)
		}
	})

	t.Run("静态种子表压过 model.json（第 2 级 > 第 3 级）", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		path := upstreamparseWriteCatalog(t, `{
			"glm-5.1": {"context_length": 123456, "source": "manual"},
			"catalog-only": {"context_length": 333333, "max_output_tokens": 44444, "source": "manual"},
			"out-zero": {"context_length": 555555, "source": "manual"}
		}`)
		loadModelCatalogAt(path)

		if got := ContextWindowListingV4("glm-5.1", 0, nil); got != 200000 {
			t.Errorf("glm-5.1 = %d want 200000（静态表权威，不取 model.json 的 123456）", got)
		}
		if got := ContextWindowListingV4("catalog-only", 0, nil); got != 333333 {
			t.Errorf("catalog-only = %d want 333333（第 3 级）", got)
		}
		if got := ContextWindowListingV4("out-zero", 0, nil); got != 555555 {
			t.Errorf("out-zero = %d want 555555", got)
		}
	})

	t.Run("进程内索引命中 → 写回 model.json 并返回", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		modelsDev.mu.Lock()
		modelsDev.doc = map[string]modelsDevEntry{"doc-only": {Context: 777777, Output: 12345}}
		modelsDev.mu.Unlock()

		if got := ContextWindowListingV4("doc-only", 0, nil); got != 777777 {
			t.Errorf("＝ %d want 777777", got)
		}
		e, ok := modelCatalogGet("doc-only")
		if !ok || e.ContextLength != 777777 || e.MaxOutputTokens != 12345 {
			t.Errorf("未写回 model.json: %+v ok=%v", e, ok)
		}
	})

	t.Run("全链 miss → 1M 并触发异步拉取（不阻塞本次）", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		resetModelsDev()

		if got := ContextWindowListingV4("never-heard-of", 0, nil); got != DefaultContextWindow {
			t.Errorf("＝ %d want %d", got, DefaultContextWindow)
		}
		modelsDev.mu.Lock()
		fetched := modelsDev.fetched
		modelsDev.mu.Unlock()
		if !fetched {
			t.Error("全链 miss 应触发 models.dev 拉取")
		}
		if e, ok := modelCatalogGet("never-heard-of"); ok {
			t.Errorf("拉取前不应有缓存: %+v", e)
		}
	})

	t.Run("负缓存有效期内不重复触发第 4 级", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		resetModelsDev()

		ContextWindowListingV4("neg-cached", 0, nil)
		modelsDev.mu.Lock()
		fetchedAt := modelsDev.lastFetch
		modelsDev.mu.Unlock()
		if fetchedAt.IsZero() {
			t.Fatal("首次应触发")
		}

		// 第二次：关掉冷却窗口，只剩负缓存能挡住第 4 级（lastFetch 不得变化）。
		modelsDev.mu.Lock()
		modelsDev.fetched = false
		modelsDev.mu.Unlock()
		ContextWindowListingV4("neg-cached", 0, nil)
		modelsDev.mu.Lock()
		after := modelsDev.lastFetch
		modelsDev.mu.Unlock()
		if !after.Equal(fetchedAt) {
			t.Errorf("负缓存应短路第 4 级触发: before=%v after=%v", fetchedAt, after)
		}
	})
}

// TestUpstreamParseMaxOutputTokensListingV4 输出上限四级链：未知 → 省略（不编造）。
func TestUpstreamParseMaxOutputTokensListingV4(t *testing.T) {
	upstreamparseResetModelsDev(t)

	t.Run("remote 权威", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		if got, ok := MaxOutputTokensListingV4("glm-5.1", 4242, nil); !ok || got != 4242 {
			t.Errorf("＝ (%d,%v) want (4242,true)", got, ok)
		}
	})

	t.Run("空模型名 → 省略", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		if got, ok := MaxOutputTokensListingV4("", 0, nil); ok || got != 0 {
			t.Errorf("＝ (%d,%v) want (0,false)", got, ok)
		}
	})

	t.Run("静态表命中", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		if got, ok := MaxOutputTokensListingV4("kimi-k2.6", 0, nil); !ok || got != 262144 {
			t.Errorf("＝ (%d,%v) want (262144,true)", got, ok)
		}
	})

	t.Run("静态表记录输出未知 → 不采信", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		if got, ok := MaxOutputTokensListingV4("kimi-k2.8-preview", 0, nil); ok || got != 0 {
			t.Errorf("＝ (%d,%v) want (0,false)", got, ok)
		}
	})

	t.Run("model.json 命中（输出 >0）/ 输出为 0 视作未知", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		path := upstreamparseWriteCatalog(t, `{
			"catalog-only": {"context_length": 333333, "max_output_tokens": 44444, "source": "manual"},
			"out-zero": {"context_length": 555555, "source": "manual"}
		}`)
		loadModelCatalogAt(path)
		if got, ok := MaxOutputTokensListingV4("catalog-only", 0, nil); !ok || got != 44444 {
			t.Errorf("＝ (%d,%v) want (44444,true)", got, ok)
		}
		if got, ok := MaxOutputTokensListingV4("out-zero", 0, nil); ok || got != 0 {
			t.Errorf("输出 0 条目＝ (%d,%v) want (0,false)", got, ok)
		}
	})

	t.Run("进程内索引命中（输出 0 时 ok=false 但 context 仍回写）", func(t *testing.T) {
		upstreamparseResetCatalog(t)
		modelsDev.mu.Lock()
		modelsDev.doc = map[string]modelsDevEntry{
			"doc-out":    {Context: 888888, Output: 999},
			"doc-no-out": {Context: 111111, Output: 0},
		}
		modelsDev.mu.Unlock()

		if got, ok := MaxOutputTokensListingV4("doc-out", 0, nil); !ok || got != 999 {
			t.Errorf("＝ (%d,%v) want (999,true)", got, ok)
		}
		if got, ok := MaxOutputTokensListingV4("doc-no-out", 0, nil); ok || got != 0 {
			t.Errorf("＝ (%d,%v) want (0,false)", got, ok)
		}
		if e, ok := modelCatalogGet("doc-no-out"); !ok || e.ContextLength != 111111 {
			t.Errorf("context 应回写 model.json: %+v ok=%v", e, ok)
		}
	})
}

// ---- headers.go ----

// TestUpstreamParseExtractClientIP XFF 首段提取（多值/空白/非法/端口/IPv6）与
// X-Real-IP 回落。
func TestUpstreamParseExtractClientIP(t *testing.T) {
	cases := []struct {
		name string
		xff  string
		real string
		want string
	}{
		{"无头", "", "", ""},
		{"单值", "1.2.3.4", "", "1.2.3.4"},
		{"多值取首段", "1.2.3.4, 5.6.7.8, 9.10.11.12", "", "1.2.3.4"},
		{"首段带空白 trim", "  1.2.3.4  , 5.6.7.8", "", "1.2.3.4"},
		{"IPv6 首段", "2001:db8::1, 1.2.3.4", "", "2001:db8::1"},
		{"带端口原样返回", "1.2.3.4:5678", "", "1.2.3.4:5678"},
		{"非法值不做校验", "not-an-ip", "", "not-an-ip"},
		{"XFF 为空串时回落 X-Real-IP", "", "9.9.9.9", "9.9.9.9"},
		{"X-Real-IP 带空白 trim", "", "  9.9.9.9  ", "9.9.9.9"},
		{"XFF 优先于 X-Real-IP", "1.2.3.4", "9.9.9.9", "1.2.3.4"},
		{"XFF 仅逗号 → 空首段", ",", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{Header: http.Header{}}
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			if c.real != "" {
				r.Header.Set("X-Real-IP", c.real)
			}
			if got := ExtractClientIP(r); got != c.want {
				t.Errorf("ExtractClientIP = %q want %q", got, c.want)
			}
		})
	}

	if got := ExtractClientIP(nil); got != "" {
		t.Errorf("nil 请求 = %q want \"\"", got)
	}
}

// TestUpstreamParseInjectGlobalChatHeaders 仅 global 账号注入国际版声明头。
func TestUpstreamParseInjectGlobalChatHeaders(t *testing.T) {
	c := &Client{}

	global := &http.Request{Header: http.Header{}}
	c.injectGlobalChatHeaders(global, upstreamparseGlobalAuth())
	if got := global.Header.Get("X-No-Enterprise-Id"); got != "1" {
		t.Errorf("global X-No-Enterprise-Id = %q want 1", got)
	}
	if got := global.Header.Get("X-Domain"); got != "www.workbuddy.ai" {
		t.Errorf("global X-Domain = %q want www.workbuddy.ai", got)
	}

	cn := &http.Request{Header: http.Header{}}
	c.injectGlobalChatHeaders(cn, upstreamparseCNAuth())
	if len(cn.Header) != 0 {
		t.Errorf("CN 账号不应注入 global 头: %v", cn.Header)
	}

	anon := &http.Request{Header: http.Header{}}
	c.injectGlobalChatHeaders(anon, nil)
	if len(anon.Header) != 0 {
		t.Errorf("nil 账号不应注入: %v", anon.Header)
	}
}

// TestUpstreamParseDefaultWorkBuddyUA 默认 CN 三段式 UA（R8 报文指纹：不得改名）。
func TestUpstreamParseDefaultWorkBuddyUA(t *testing.T) {
	c := &Client{}
	const want = "WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1"
	if got := c.defaultWorkBuddyUA(); got != want {
		t.Errorf("defaultWorkBuddyUA() = %q want %q", got, want)
	}
	// 版本覆盖同时作用于 WorkBuddy 两段与 CLI 段。
	custom := &Client{ClientVersion: "9.9.9", CliVersion: "1.2.3"}
	if got := custom.defaultWorkBuddyUA(); got != "WorkBuddy/9.9.9 WorkBuddy/9.9.9 CLI/1.2.3" {
		t.Errorf("版本覆盖 = %q", got)
	}
	// nil Client 走内置默认（不 panic）。
	if got := (*Client)(nil).defaultWorkBuddyUA(); got != want {
		t.Errorf("nil Client = %q want %q", got, want)
	}
}

// TestUpstreamParseInjectClientIP 客户端 IP 透传开关（关闭时不注入任何 IP 头）。
func TestUpstreamParseInjectClientIP(t *testing.T) {
	off := &Client{PassthroughIP: false}
	req := &http.Request{Header: http.Header{}}
	off.injectClientIP(req, "1.2.3.4")
	if len(req.Header) != 0 {
		t.Errorf("PassthroughIP=false 不应注入: %v", req.Header)
	}

	on := &Client{PassthroughIP: true}
	req = &http.Request{Header: http.Header{}}
	on.injectClientIP(req, "1.2.3.4")
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "X-Client-IP"} {
		if got := req.Header.Get(h); got != "1.2.3.4" {
			t.Errorf("%s = %q want 1.2.3.4", h, got)
		}
	}

	// 开启但 IP 为空 → 不注入。
	req = &http.Request{Header: http.Header{}}
	on.injectClientIP(req, "")
	if len(req.Header) != 0 {
		t.Errorf("IP 为空不应注入: %v", req.Header)
	}
	// nil Client 不 panic 且不注入。
	req = &http.Request{Header: http.Header{}}
	(*Client)(nil).injectClientIP(req, "1.2.3.4")
	if len(req.Header) != 0 {
		t.Errorf("nil Client 不应注入: %v", req.Header)
	}
}

// ---- transport.go ----

// upstreamparseCloseCounter 记录 CloseIdleConnections 调用的 RoundTripper。
type upstreamparseCloseCounter struct {
	calls int
}

func (c *upstreamparseCloseCounter) RoundTrip(*http.Request) (*http.Response, error) {
	return jsonResp(200, "{}"), nil
}

func (c *upstreamparseCloseCounter) CloseIdleConnections() { c.calls++ }

// TestUpstreamParseRoundTripCloseIdle 失败后清空闲连接池：nil / 不实现 closeIdler
// 静默跳过，实现者被调用恰好一次。
func TestUpstreamParseRoundTripCloseIdle(t *testing.T) {
	roundTripCloseIdle(nil) // 不得 panic

	plain := rtFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	roundTripCloseIdle(plain) // 未实现 closeIdler：静默跳过

	counter := &upstreamparseCloseCounter{}
	roundTripCloseIdle(counter)
	if counter.calls != 1 {
		t.Errorf("CloseIdleConnections calls = %d want 1", counter.calls)
	}

	// 真 *http.Transport 满足 closeIdler（不 panic、无网络动作）。
	roundTripCloseIdle(newTransport())
}

// ---- blackcat.go ----

// TestUpstreamParseInNightWindow 夜猫子窗口 23:00–08:00（跨午夜）。
func TestUpstreamParseInNightWindow(t *testing.T) {
	cases := []struct {
		h    int
		m    int
		want bool
	}{
		{22, 59, false},
		{23, 0, true},
		{23, 59, true},
		{0, 0, true},
		{7, 59, true},
		{8, 0, false},
		{12, 0, false},
	}
	for _, c := range cases {
		now := time.Date(2026, 10, 2, c.h, c.m, 0, 0, time.UTC)
		if got := InNightWindow(now); got != c.want {
			t.Errorf("InNightWindow(%02d:%02d) = %v want %v", c.h, c.m, got, c.want)
		}
	}
}

// TestUpstreamParseBlackcatNeed black_cat 剩余差额：已领/已达标/无任务 → 0，拉取失败 → err。
func TestUpstreamParseBlackcatNeed(t *testing.T) {
	a := &auth.Auth{UID: "u1", AccessToken: "at"}

	newClient := func(tasksJSON string) (*Client, *string) {
		var path string
		c := testClient(func(r *http.Request) (*http.Response, error) {
			path = r.URL.Path
			return jsonResp(200, `{"code":0,"data":{"tasks":`+tasksJSON+`}}`), nil
		})
		return c, &path
	}

	cases := []struct {
		name  string
		tasks string
		want  int64
	}{
		{"进度未达标 → 差额",
			`[{"task_code":"black_cat","target":3,"current":1}]`, 2},
		{"进度为 0 → 全额",
			`[{"task_code":"black_cat","target":3,"current":0}]`, 3},
		{"已达标 → 0",
			`[{"task_code":"black_cat","target":3,"current":3}]`, 0},
		{"已领取 → 0",
			`[{"task_code":"black_cat","target":3,"current":1,"accept_status":"claimed"}]`, 0},
		{"progress 对象形态覆盖平铺值",
			`[{"task_code":"black_cat","target":9,"current":0,"progress":{"current":2,"target":5}}]`, 3},
		{"无 black_cat 任务 → 0",
			`[{"task_code":"other","target":3,"current":0}]`, 0},
		{"任务列表为空 → 0", `[]`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client, path := newClient(c.tasks)
			got, err := client.BlackcatNeed(a)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("BlackcatNeed = %d want %d", got, c.want)
			}
			if *path != tasksListPath {
				t.Errorf("请求路径 = %q want %q", *path, tasksListPath)
			}
		})
	}

	t.Run("拉取失败 → err", func(t *testing.T) {
		c := testClient(func(r *http.Request) (*http.Response, error) {
			return jsonResp(500, `{"msg":"boom"}`), nil
		})
		got, err := c.BlackcatNeed(a)
		if err == nil || got != 0 {
			t.Errorf("应报错并返回 0, got (%d,%v)", got, err)
		}
	})
}

// ---- sse.go ----

// TestUpstreamParseNum64 JSON number 归一（非数字只跳过，不报错）。
func TestUpstreamParseNum64(t *testing.T) {
	cases := []struct {
		name   string
		in     any
		want   float64
		wantOK bool
	}{
		{"float64", 1.5, 1.5, true},
		{"int64", int64(7), 7, true},
		{"int", 8, 8, true},
		{"json.Number 不认", json.Number("9"), 0, false},
		{"字符串不认", "10", 0, false},
		{"nil 不认", nil, 0, false},
		{"bool 不认", true, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := num64(c.in)
			if got != c.want || ok != c.wantOK {
				t.Errorf("num64(%v) = (%v,%v) want (%v,%v)", c.in, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestUpstreamParseFinishOnlyFrame 收尾帧构造：空 delta + finish_reason，
// 只透传 created/model（不得把 id 等键一起抄进新帧）。
func TestUpstreamParseFinishOnlyFrame(t *testing.T) {
	src := map[string]any{"id": "chatcmpl-1", "created": float64(1700000000), "model": "glm-5.2", "object": "chat.completion.chunk"}
	frame := finishOnlyFrame(src, "stop")

	if frame["object"] != "chat.completion.chunk" {
		t.Errorf("object = %v want chat.completion.chunk", frame["object"])
	}
	if frame["created"] != src["created"] {
		t.Errorf("created 未透传: %v", frame["created"])
	}
	if frame["model"] != "glm-5.2" {
		t.Errorf("model 未透传: %v", frame["model"])
	}
	if _, has := frame["id"]; has {
		t.Error("id 不应被抄进收尾帧（由 writeChunk 的 id 续传逻辑负责）")
	}
	chs, ok := frame["choices"].([]any)
	if !ok || len(chs) != 1 {
		t.Fatalf("choices 形状异常: %v", frame["choices"])
	}
	ch, _ := chs[0].(map[string]any)
	if ch["index"] != 0 || ch["finish_reason"] != "stop" {
		t.Errorf("choice 字段 = %v want index=0/finish_reason=stop", ch)
	}
	delta, ok := ch["delta"].(map[string]any)
	if !ok || len(delta) != 0 {
		t.Errorf("delta 应为空对象, got %v", ch["delta"])
	}

	// src 缺 created/model → 新帧不带这两个键（不编造）。
	bare := finishOnlyFrame(map[string]any{}, "length")
	if _, has := bare["created"]; has {
		t.Error("src 无 created 时不应带该键")
	}
	if _, has := bare["model"]; has {
		t.Error("src 无 model 时不应带该键")
	}
	if bare["choices"].([]any)[0].(map[string]any)["finish_reason"] != "length" {
		t.Error("finish 值未透传")
	}
}

// TestUpstreamParseAttachHintToErrorFrame 只加不改：非 JSON / 无 error 对象原样返回，
// error 对象上追加 gateway_hint 且既有键全部保留。
func TestUpstreamParseAttachHintToErrorFrame(t *testing.T) {
	// 非 JSON：原样返回（不破坏原文透传）。
	const bad = `not-json`
	if got := attachHintToErrorFrame(bad, "H"); got != bad {
		t.Errorf("非 JSON 应原样返回, got %q", got)
	}
	// 无 error 对象：原样返回。
	const noErr = `{"foo":"bar"}`
	if got := attachHintToErrorFrame(noErr, "H"); got != noErr {
		t.Errorf("无 error 对象应原样返回, got %q", got)
	}
	// error 是字符串而非对象：原样返回。
	const strErr = `{"error":"boom"}`
	if got := attachHintToErrorFrame(strErr, "H"); got != strErr {
		t.Errorf("error 非对象应原样返回, got %q", got)
	}

	const payload = `{"error":{"message":"upstream said no","code":"6004","requestId":"r-1"},"id":"chatcmpl-x"}`
	out := attachHintToErrorFrame(payload, "rate limited by upstream; retry after reset")
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("结果不是合法 JSON: %v (%s)", err, out)
	}
	if obj["id"] != "chatcmpl-x" {
		t.Errorf("顶层键被改动: %v", obj)
	}
	e, ok := obj["error"].(map[string]any)
	if !ok {
		t.Fatalf("error 对象丢失: %v", obj)
	}
	if e["gateway_hint"] != "rate limited by upstream; retry after reset" {
		t.Errorf("gateway_hint = %v", e["gateway_hint"])
	}
	if e["message"] != "upstream said no" || e["code"] != "6004" || e["requestId"] != "r-1" {
		t.Errorf("既有键被改写: %v", e)
	}
}

// TestUpstreamParseFrameGatewayHint hintFn 缺失/异常/空白处理（panic 隔离）。
func TestUpstreamParseFrameGatewayHint(t *testing.T) {
	if got := frameGatewayHint(nil, `{"error":{"message":"x"}}`); got != "" {
		t.Errorf("nil hintFn = %q want \"\"", got)
	}
	if got := frameGatewayHint(func(string) string { return "  spaced  " }, "p"); got != "spaced" {
		t.Errorf("应 trim 返回值, got %q", got)
	}
	if got := frameGatewayHint(func(string) string { return "   " }, "p"); got != "" {
		t.Errorf("全空白应归零, got %q", got)
	}
	// panic 隔离：实现缺陷不得击穿流透传主路径。
	if got := frameGatewayHint(func(string) string { panic("hint bug") }, "p"); got != "" {
		t.Errorf("panic 应被隔离为 \"\", got %q", got)
	}
	// 入参透传：payload 原样交给 hintFn。
	var seen string
	frameGatewayHint(func(p string) string { seen = p; return "" }, `{"error":{"message":"m"}}`)
	if seen != `{"error":{"message":"m"}}` {
		t.Errorf("payload 未透传: %q", seen)
	}
}

// TestUpstreamParseSSEKeepAlive SetSSEKeepAlive / SSEKeepAlive 的取值与负值归零。
func TestUpstreamParseSSEKeepAlive(t *testing.T) {
	orig := SSEKeepAlive()
	t.Cleanup(func() { SetSSEKeepAlive(orig) })

	SetSSEKeepAlive(0)
	if got := SSEKeepAlive(); got != 0 {
		t.Errorf("关闭态 = %v want 0", got)
	}
	SetSSEKeepAlive(5 * time.Second)
	if got := SSEKeepAlive(); got != 5*time.Second {
		t.Errorf("＝ %v want 5s", got)
	}
	// 负值归零（<=0 = 关闭，绝不出现负间隔）。
	SetSSEKeepAlive(-3 * time.Second)
	if got := SSEKeepAlive(); got != 0 {
		t.Errorf("负值应归零, got %v", got)
	}
}
