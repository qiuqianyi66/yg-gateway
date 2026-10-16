package upstream

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"yg-gateway/internal/auth"
)

// ---------------------------------------------------------------------------
// R8 协议数据 golden：把「上游报文里不得改名」的字面量、顺序与形状**一次性钉死**。
//
// 为什么需要：R8（`wb2api-<ms>` conversationId、`chatcmpl-wb2api` 哨兵、三段式 UA
// `WorkBuddy/<ver> <platform>/<ver> CLI/<cliVer>`）此前只靠「人记得」+ 散落在各测试里的
// 逐字断言。改名有风控风险，但没有任何**单点**能让人在同一次改动里看到「我弄坏了 R8 的哪一条」。
// 本 golden 就是那个单点：任何结构变化都会在这一处报出精确的 diff。
//
// 与「钉死版本号」的本质区别（也是本文件唯一的设计取舍）：
//   - **钉**：段数、段序、分隔符、大小写、字面量、按 realm 的候选序列与顺序、头族是否发出。
//   - **不钉**：会随上游版本变动的**值**——版本号、派生 ID、时间戳、token。
//     它们在建 golden 时就归一成 `<ver>`/`<32hex>`/`<36hex>`/`<digits>`/`<uuid>`/`<token>`。
//   于是「升级 WorkBuddy 客户端版本常量」不会红（那是正常维护），
//   而「去掉 `CLI/` 段」「把 `chatcmpl-wb2api` 改成别的」「调换 global 的 billing 候选顺序」会红。
//
// 重写方式（**唯一**）：
//
//	go test ./internal/upstream/ -run TestR8ProtocolGolden -count=1 -update-r8-golden
//
// 重写前必须回答：这次变化是**评审通过的行为变更**，还是一次 R8 事故？
// ---------------------------------------------------------------------------

const r8GoldenPath = "testdata/r8_protocol.golden"

var updateR8Golden = flag.Bool("update-r8-golden", false,
	"重写 testdata/r8_protocol.golden（默认只比对；重写等于承认报文结构变化并对其负责）")

var (
	r8BearerRe  = regexp.MustCompile(`(?i)Bearer\s+\S+`)
	r8UUIDRe    = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	r8Hex36Re   = regexp.MustCompile(`\b[0-9a-fA-F]{36}\b`)
	r8Hex32Re   = regexp.MustCompile(`\b[0-9a-fA-F]{32}\b`)
	r8Hex16Re   = regexp.MustCompile(`\b[0-9a-fA-F]{16}\b`)
	r8MsgIDRe   = regexp.MustCompile(`\bmsg-[0-9a-fA-F]+\b`)
	r8VersionRe = regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)
	r8DigitsRe  = regexp.MustCompile(`\b\d{10,}\b`)
)

// r8Norm 把「会变的值」换成占位符，只留「不许变的结构」。顺序敏感：
// token → uuid → 36hex → 32hex → 16hex → 版本 → 长数字。
//
// 16hex 必须先于 32hex 之外单独处理：B3 规范只认 16/32 hex，SpanId 恒为 messageID[:16]
// （每次调用都不同），漏了它 golden 会**每次都变**（正是「golden 必须确定性」的反例）。
func r8Norm(s string) string {
	if s == "" {
		return "(empty)"
	}
	s = r8BearerRe.ReplaceAllString(s, "Bearer <token>")
	s = r8UUIDRe.ReplaceAllString(s, "<uuid>")
	s = r8Hex36Re.ReplaceAllString(s, "<36hex>")
	s = r8Hex32Re.ReplaceAllString(s, "<32hex>")
	s = r8Hex16Re.ReplaceAllString(s, "<16hex>")
	s = r8MsgIDRe.ReplaceAllString(s, "msg-<hex>")
	s = r8VersionRe.ReplaceAllString(s, "<ver>")
	s = r8DigitsRe.ReplaceAllString(s, "<digits>")
	return s
}

// r8HeaderLines 把请求头族按名字排序后逐行输出（顺序稳定 = 可比对）。
func r8HeaderLines(h http.Header) []string {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, k := range names {
		out = append(out, fmt.Sprintf("  %s: %s", k, r8Norm(strings.Join(h.Values(k), ", "))))
	}
	return out
}

// r8ChatHeaders 用真实代码路径产出一次 chat 出站的头族（含会话头族 / 归属头 / 风控头）。
func r8ChatHeaders(a *auth.Auth) http.Header {
	req, _ := http.NewRequest(http.MethodPost, "https://chat.example/v2/chat/completions", nil)
	c := &Client{GlobalEnabled: true}
	c.ChatHeaders(req, a, "", ChatMeta{
		ConversationID:        "wb2api-conv-1753600000000",
		ConversationRequestID: "0123456789abcdef0123456789abcdef",
	})
	return req.Header
}

// r8BuildGolden 生成 golden 全文（确定性，无时间/随机来源——全部经 r8Norm 归一）。
func r8BuildGolden(t *testing.T) string {
	t.Helper()

	cn := upstreamhttpCNAuth()
	global := upstreamhttpGlobalAuth()
	c := &Client{GlobalEnabled: true}

	var b strings.Builder
	sec := func(title string, why string) {
		fmt.Fprintf(&b, "\n[%s]\n# %s\n", title, why)
	}
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, format+"\n", args...)
	}

	b.WriteString("" +
		"# R8 协议数据 golden —— 上游报文里**不得改名 / 不得改序**的内容。\n" +
		"# 生成与比对：internal/upstream/r8_golden_test.go（默认只读比对，任何字节差异即 FAIL）。\n" +
		"# 归一化：<ver> 版本 · <32hex>/<36hex> 派生 ID · <uuid> · <digits> 时间戳 · <token> 凭证。\n" +
		"# 即：改版本常量不红；改段数/段序/分隔符/字面量/候选顺序/头族会红。\n")

	sec("ua", "三段式 WorkBuddy UA：段数、段序、分隔符、平台段按 realm 的品牌 —— 改名会触发上游风控 11140")
	line("  userAgent(cn)                     = %s", r8Norm(c.userAgent(cn)))
	line("  userAgent(global)                 = %s", r8Norm(c.userAgent(global)))
	line("  defaultWorkBuddyUA()              = %s", r8Norm(c.defaultWorkBuddyUA()))
	line("  billingUA()                       = %s", r8Norm(c.billingUA()))
	custom := &Client{ClientVersion: "9.9.9", CliVersion: "1.2.3"}
	line("  userAgent(custom 版本,custom 平台) = %s", r8Norm(custom.userAgent(global)))
	line("  billingUA(custom 版本)             = %s", r8Norm(custom.billingUA()))
	saas := &Client{ClientName: "SaaS"}
	line("  userAgent(SaaS)                   = %s", r8Norm(saas.userAgent(cn)))
	line("  billingUA(SaaS)                   = %s", r8Norm(saas.billingUA()))
	override := &Client{UserAgent: "custom-agent/1.0"}
	line("  userAgent(显式覆盖)                = %s", r8Norm(override.userAgent(cn)))

	sec("paths", "出站路径与按 realm 的候选序列（顺序即行为：只在首个候选 404 时回落）")
	line("  chatCompletionsPath               = %s", chatCompletionsPath)
	line("  chatPaths(cn)                     = %s", r8Norm(strings.Join(c.chatPaths(cn), " , ")))
	line("  chatPaths(global)                 = %s", r8Norm(strings.Join(c.chatPaths(global), " , ")))
	line("  billingMeterPaths(cn)             = %s", r8Norm(strings.Join(c.billingMeterPaths(cn), " , ")))
	line("  billingMeterPaths(global)         = %s", r8Norm(strings.Join(c.billingMeterPaths(global), " , ")))
	line("  checkinMeterPaths(cn)             = %s", r8Norm(strings.Join(c.checkinMeterPaths(cn), " , ")))
	line("  checkinMeterPaths(global)         = %s", r8Norm(strings.Join(c.checkinMeterPaths(global), " , ")))
	line("  reportPath                        = %s", reportPath)
	line("  desktopReportPath                 = %s", desktopReportPath)
	line("  mpReportPath                      = %s", mpReportPath)

	sec("origin-language", "Origin/Referer 基础域与 Accept-Language 按 realm 切换（对齐官方客户端，避免风控误判）")
	line("  originRefererFor(cn)              = %s", originRefererFor(cn))
	line("  originRefererFor(global)          = %s", originRefererFor(global))
	line("  acceptLanguageFor(cn)             = %s", acceptLanguageFor(cn))
	line("  acceptLanguageFor(global)         = %s", acceptLanguageFor(global))

	sec("headers.chat.cn", "CN chat 出站头族全集（含会话头族 X-Conversation-* / X-Request-ID / B3）")
	for _, l := range r8HeaderLines(r8ChatHeaders(cn)) {
		b.WriteString(l + "\n")
	}

	sec("headers.chat.global", "global chat 出站头族：平台段与域声明换成国际版形态")
	for _, l := range r8HeaderLines(r8ChatHeaders(global)) {
		b.WriteString(l + "\n")
	}

	sec("headers.billing.cn", "billing 域头族（不走 CommonHeaders，单独注入；UA 为单段 WorkBuddy/<ver>）")
	breq, _ := http.NewRequest(http.MethodPost, "https://billing.example/v2/billing/meter/get-user-resource", nil)
	c.BillingHeaders(breq, cn)
	for _, l := range r8HeaderLines(breq.Header) {
		b.WriteString(l + "\n")
	}

	sec("headers.refresh.cn", "refresh 端点专属头：X-Refresh-Token 只允许出现在这里")
	rreq, _ := http.NewRequest(http.MethodPost, "https://billing.example/v2/plugin/auth/token/refresh", nil)
	refAuth := &auth.Auth{UID: "u-refresh", AccessToken: "at-refresh", RefreshToken: "rt-refresh"}
	c.RefreshHeaders(rreq, refAuth)
	for _, l := range r8HeaderLines(rreq.Header) {
		b.WriteString(l + "\n")
	}

	sec("stream", "流式帧 id：无真实 id 时的哨兵，以及有真实 id 时不得覆盖")
	empty := normalizeFrame(map[string]any{})
	line("  normalizeFrame({}).id            = %s", r8Norm(fmt.Sprint(empty["id"])))
	line("  normalizeFrame({}).object        = %s", r8Norm(fmt.Sprint(empty["object"])))
	kept := normalizeFrame(map[string]any{"id": "chatcmpl-real-1"})
	line("  normalizeFrame(真实 id).id       = %s", r8Norm(fmt.Sprint(kept["id"])))

	sec("conversation-id", "对话 ID 的**形状**（值随调用变，形状不变）：wb2api 前缀 + 派生 ID 的稳定性")
	line("  clientToken() 形状                = %s", r8Norm(clientToken()))
	machine := deriveAccountStableID("u-cn", "machine")
	session := deriveAccountStableID("u-cn", "session")
	line("  deriveAccountStableID(u-cn,machine) = %s", r8Norm(machine))
	line("  同 uid 同用途幂等                  = %t", machine == deriveAccountStableID("u-cn", "machine"))
	line("  同 uid 不同用途互异                = %t", machine != session)
	line("  不同 uid 互异                      = %t", machine != deriveAccountStableID("u-other", "machine"))

	sec("conversation-id-generators", "各生成点的 conversationId 形态：真跑生成代码（不写字面量），只归一掉随机部分")
	drec := &upstreamhttpRec{}
	dc := testClient(upstreamhttpStreamStub(drec, "data: {\"id\":\"0123456789abcdef0123456789abcdef\"}\n\n"))
	conv, rid, err := dc.DesktopChatWithExpert(upstreamhttpCNAuth(), "")
	if err != nil {
		t.Fatalf("DesktopChatWithExpert 失败，golden 无法完整生成：%v", err)
	}
	line("  DesktopChatWithExpert conversationId      = %s", r8Norm(conv))
	line("  DesktopChatWithExpert requestId           = %s", r8Norm(rid))
	dreq := drec.one(t, "/v2/chat/completions")
	line("  DesktopChatWithExpert Accept              = %s", r8Norm(dreq.Header.Get("Accept")))
	line("  DesktopChatWithExpert X-Conversation-ID 与返回值一致 = %t", dreq.Header.Get("X-Conversation-ID") == conv)

	mpEv := SchoolChatTimesEvents("wb2api-conv-1753600000000")
	line("  SchoolChatTimesEvents traceId             = %s", r8Norm(fmt.Sprint(mpEv["traceId"])))
	line("  SchoolChatTimesEvents rootRequestId       = %s", r8Norm(fmt.Sprint(mpEv["rootRequestId"])))
	line("  SchoolChatTimesEvents traceId==rootReqId  = %t", mpEv["traceId"] == mpEv["rootRequestId"])
	line("  SchoolChatTimesEvents messageId           = %s", r8Norm(fmt.Sprint(mpEv["messageId"])))
	line("  SchoolChatTimesEvents conversationId      = %s", r8Norm(fmt.Sprint(mpEv["conversationId"])))
	line("  SchoolChatTimesEvents agentName/Type      = %s / %s", mpEv["agentName"], mpEv["agentType"])

	// blackcat.go：夜猫子上报的 conversationId（`wb2api-night-<ms>-<i>`）。
	// 这条此前**没有任何断言**——改名不会让任何测试变红（与 T3 发现的 M2 同类缺口）。
	nrec := &upstreamhttpRec{}
	nc := testClient(upstreamhttpStub(nrec, func(q upstreamhttpReq) (*http.Response, error) {
		switch q.Path {
		case "/v2/chat/completions":
			return jsonResp(200, "data: {\"id\":\"wb2api-1\",\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\ndata: [DONE]\n\n"), nil
		case "/v2/report":
			return jsonResp(200, `{"code":0,"msg":"OK"}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", q.Path)
		}
	}))
	// N4：golden 只关心报文内容，不关心真实节奏——注入空等待，免掉 4s。
	// 「生产默认间隔是 4s」由 blackcat_test.go 的 TestNightChatsGapIsInjectable 单独钉住，
	// 因此这里跳过等待不会让「节奏被改小」失去覆盖。
	nc.SetNightChatSleep(func(time.Duration) {})
	if _, err := nc.RunNightChats(upstreamhttpCNAuth(), 1); err != nil {
		t.Fatalf("RunNightChats 失败，golden 无法完整生成：%v", err)
	}
	narr := upstreamhttpArray(t, nrec.one(t, "/v2/report").Body)
	line("  RunNightChats 上报 conversationId         = %s", r8Norm(fmt.Sprint(narr[0]["conversationId"])))

	sec("report-body", "上报体里 conversationId 为**原样透传**的 wb2api-<ms> 形态（网关不重写它）")
	rec := &upstreamhttpRec{}
	rc := testClient(upstreamhttpFixed(rec, 200, `{"code":0}`))
	if err := rc.ReportChatActivity(upstreamhttpCNAuth(), "wb2api-123", ""); err != nil {
		t.Fatalf("report 上报失败，golden 无法完整生成：%v", err)
	}
	var events []map[string]any
	if err := json.Unmarshal(rec.one(t, "/v2/report").Body, &events); err != nil {
		t.Fatalf("上报体不是 JSON 数组：%v", err)
	}
	if len(events) != 1 {
		t.Fatalf("上报体事件数 = %d，want 1", len(events))
	}
	ev := events[0]
	line("  eventCode                         = %s", r8Norm(fmt.Sprint(ev["eventCode"])))
	line("  mode                              = %s", r8Norm(fmt.Sprint(ev["mode"])))
	line("  conversationId                    = %s", r8Norm(fmt.Sprint(ev["conversationId"])))
	line("  userId                            = %s", r8Norm(fmt.Sprint(ev["userId"])))
	line("  conversationId 原样透传(未加后缀)  = %t", !strings.Contains(fmt.Sprint(ev["conversationId"]), "conv"))

	return strings.ReplaceAll(b.String(), "\r\n", "\n")
}

// r8Compare 逐行比对，返回**第一处**差异 + 差异行数。错误信息要能直接定位到 golden 的哪一行。
func r8Compare(want, got string) error {
	if want == got {
		return nil
	}
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	first := -1
	diff := 0
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			diff++
			if first < 0 {
				first = i + 1
			}
		}
	}
	show := func(lines []string, idx int) string {
		if idx < 0 || idx >= len(lines) {
			return "(该行不存在)"
		}
		return lines[idx]
	}
	return fmt.Errorf("R8 golden 与当前代码不一致：第 %d 行起有差异，共 %d 行不同\n"+
		"  golden: %s\n  当前  : %s\n"+
		"  → 若这是**评审通过的报文变更**，用 -update-r8-golden 重写；若是 R8 事故，改回代码。",
		first, diff, show(wl, first-1), show(gl, first-1))
}

// r8ReadGolden 读 golden；文件缺失视为致命（不能靠「没有 golden」绕过 R8 锁）。
func r8ReadGolden(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(r8GoldenPath)
	if err != nil {
		t.Fatalf("读不到 %s：%v（首次生成用 -update-r8-golden）", r8GoldenPath, err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// TestR8ProtocolGolden R8 锁本体：当前代码产出的协议数据必须与 golden 逐字节一致。
//
// 「还原什么行为会让它 FAIL」：删/改三段式 UA 的任一 `CLI/` 段、把平台段品牌换掉、
// 改名 `chatcmpl-wb2api` 哨兵、调换 global 的 billing/checkin 候选顺序、
// 把 `wb2api-<ms>` 上报形态改成别的、去掉会话头族里的任一头、改 Origin/Referer 基础域或
// Accept-Language —— 任意一条都会让本测试红并指出 golden 的第几行。
func TestR8ProtocolGolden(t *testing.T) {
	got := r8BuildGolden(t)

	if *updateR8Golden {
		if err := os.WriteFile(r8GoldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("写 golden 失败：%v", err)
		}
		t.Logf("已重写 %s（%d 字节 / %d 行）", r8GoldenPath, len(got), strings.Count(got, "\n")+1)
		return
	}

	if err := r8Compare(r8ReadGolden(t), got); err != nil {
		t.Fatal(err)
	}
}

// TestR8GoldenHoldsCriticalLiterals 防止 golden 被「重写成空/被清空」后锁形同虚设：
// R8 的三类字面量必须真的出现在 golden 里。
func TestR8GoldenHoldsCriticalLiterals(t *testing.T) {
	want := r8ReadGolden(t)
	for _, lit := range []string{
		"WorkBuddy/<ver>",           // 三段式 UA 的 WorkBuddy 段
		"CLI/<ver>",                 // 三段式 UA 的 CLI 段（改名高危）
		"WorkBuddy AI/<ver>",        // global 平台段品牌
		"chatcmpl-wb2api",           // 流式无 id 哨兵
		"wb2api-",                   // conversationId 前缀族
		"wb2api-conv-<digits>",      // 入站 conversationId 的透传形态
		"wb2api-<uuid>",             // mp/school 域的 wb2api-<token> 生成形态
		"wb2api-night-",             // 夜猫子对话 ID 前缀族（补的缺口）
		"msg-<hex>",                 // 消息级 ID 形态
		"/v2/chat/completions",      // chat 出站路径
		"/v2/billing/meter/",        // billing meter 候选族
		"X-Conversation-Request-Id", // 会话聚合主键头（必发；注意 Go 把头名规范成 -Id）
		"X-Codebuddy-Request",       // 官方风控闸门头（所有出站必带）
		"X-Auth-Refresh-Source",     // refresh 渠道标识
	} {
		if !strings.Contains(want, lit) {
			t.Errorf("golden 里缺少 R8 关键内容 %q —— golden 被削弱了（要么重写错了，要么这些内容被真的删掉了）", lit)
		}
	}
}

// TestR8GoldenComparatorDetectsEveryByteEdit 证明比对器**不空转**：
// 对 golden 的**每一个字节**做一次篡改，比对器都必须报出差异。
// 没有这条，「改 golden 里任一字节 → 测试红」只是口头承诺（比对器写错了也一样是绿的）。
func TestR8GoldenComparatorDetectsEveryByteEdit(t *testing.T) {
	want := r8ReadGolden(t)
	if len(want) == 0 {
		t.Fatal("golden 为空")
	}
	for i := 0; i < len(want); i++ {
		repl := byte('X')
		if want[i] == 'X' {
			repl = 'Y'
		}
		corrupted := want[:i] + string(repl) + want[i+1:]
		if err := r8Compare(want, corrupted); err == nil {
			t.Fatalf("第 %d 字节被篡改（%q）后比对器仍判定通过 —— 比对器有洞，golden 保护是假的", i, want[i])
		}
	}
}
