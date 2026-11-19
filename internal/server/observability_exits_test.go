package server

import (
	"strings"
	"testing"
)

// TestKeyRPMStatsNilGuard 零值 Handler（keyRPM 未初始化）返回 nil，不 panic。
//
// keyRPM 只在 NewHandler 里构造（handler.go:262）。注释把它标成「理论不可达」，
// 但零值 Handler 在测试与某些构造路径里真实存在 —— 一个 nil 解引用会让
// /panel/api/overview 直接 500。所以这个守卫不是形式主义。
//
// nil 与空数组的区别是有意义的：nil 表示「没有观测器」，空数组表示「观测器在、
// 观测到 0 个 key」。调用方据此决定面板显示「无数据」还是「0 请求」。
func TestKeyRPMStatsNilGuard(t *testing.T) {
	h := &Handler{}
	if got := h.KeyRPMStats(); got != nil {
		t.Errorf("未初始化 keyRPM 时应返回 nil，实际 %v", got)
	}
}

// TestKeyRPMStatsExposesSnapshot 正常构造的 Handler 能拿到观测快照。
//
// 这是 panel overview 的唯一数据来源：如果这里返回 nil 而不是快照，面板的
// per-key RPM 区块会一直显示空，而底层观测器其实在正常记账 —— 静默的功能缺失。
func TestKeyRPMStatsExposesSnapshot(t *testing.T) {
	h := NewHandler(Config{Pool: testPoolWith(), Upstream: newFakeUpstream(t,
		func(string) (int, string, bool) { return 200, sseOK, true })})

	got := h.KeyRPMStats()
	if got == nil {
		t.Fatal("NewHandler 构造后 keyRPM 应已初始化，快照不应为 nil")
	}
	// 尚未有任何请求通过 → 观测到 0 个 key。必须是「非 nil 的空数组」，
	// 与上面 nil 的语义区分开。
	if len(got) != 0 {
		t.Errorf("尚无请求时应为空快照，实际 %d 条", len(got))
	}
}

// TestPromptTooLongMessage 覆盖 11115 透传文案的两条分支。
//
// 上游 body 原文必须原样透传：里面有真实 token 数、上限值和 requestId，
// 客户端据此排查「为什么超限」。网关**不加工**这段文本，只在其为空时兜一句
// 可读短文案 —— 兜底也不能编造原文（那是伪造上游证据）。
func TestPromptTooLongMessage(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "上游原文原样透传",
			body: `{"error":{"message":"prompt is too long: 2100000 tokens > 2000000 maximum","code":"11115"}}`,
		},
		{
			name: "纯空白 body 兜底为可读短文案",
			body: " \n\t ",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := promptTooLongMessage(c.body)
			if c.name == "上游原文原样透传" {
				if got != c.body {
					t.Errorf("原文必须原样透传\n got: %s\nwant: %s", got, c.body)
				}
				// 关键信息不能被吞掉：token 数与上限是客户端唯一线索。
				for _, need := range []string{"2100000", "2000000", "11115"} {
					if !strings.Contains(got, need) {
						t.Errorf("透传文案丢了关键信息 %q", need)
					}
				}
				return
			}
			if got != "prompt is too long" {
				t.Errorf("空 body 应兜底为固定短文案，实际 %q", got)
			}
			// 兜底文案不得伪装成上游原文（不能含 JSON 信封等伪造痕迹）。
			if strings.Contains(got, "{") || strings.Contains(got, "code") {
				t.Errorf("兜底文案不得伪造上游原文，实际 %q", got)
			}
		})
	}
}
