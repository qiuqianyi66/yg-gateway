package logfmt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 本文件为 Task 5 覆盖率补强（纯增量）。期望值全部手写字面量或按契约独立推算，
// 不使用被测函数自身的输出当期望。helper 统一加 logfmtCov 前缀，避免与并行
// agent 新建的测试文件重名。

// ---- Truncate ------------------------------------------------------------

func TestLogfmtCovTruncate(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{"n zero", "abc", 0, ""},
		{"n negative", "abc", -5, ""},
		{"n zero empty input", "", 0, ""},
		{"trim then short", "  abc  ", 10, "abc"},
		{"trim then cut", "  abcdef  ", 3, "abc"},
		{"exact length kept", "abcdef", 6, "abcdef"},
		{"one longer than n", "abcdef", 5, "abcde"},
		{"only whitespace", "   ", 4, ""},
		{"ascii cut at rune start", "abcdef", 4, "abcd"},
		// é = C3 A9：n=2 落在字节 2（'1'）属 rune 起始，不触发回退 → "é"。
		{"two byte rune kept whole", "\u00e91", 2, "\u00e9"},
		{"two byte rune plus one", "\u00e91", 3, "\u00e91"},
		// 猫 = E7 8C AB：n=4 落在第 2 个猫的第 2 字节 → 回退到边界，整字让出。
		{"multibyte rollback", "\u732b\u732b", 4, "\u732b"},
		// n=1 落在第 1 个猫内部 → 回退到 0。
		{"multibyte full rollback", "\u732b", 1, ""},
		// n=3 恰好是第 1 个猫的完整边界，不该多让。
		{"multibyte exact boundary", "\u732b\u732b", 3, "\u732b"},
		// emoji（4 字节 F0 9F 98 80）n=5 落在第 2 个 emoji 第 2 字节。
		{"emoji rollback", "\U0001f600\U0001f600", 5, "\U0001f600"},
		{"n larger than len", "abc", 100, "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Truncate(tc.s, tc.n); got != tc.want {
				t.Fatalf("Truncate(%q, %d) = %q want %q", tc.s, tc.n, got, tc.want)
			}
		})
	}
}

// Truncate 的契约：结果永远是「原串 TrimSpace 后」的前缀、字节数 <= n、且是合法
// UTF-8。去掉 rune 边界回退循环会让非法截断（半截中文）出现 → 本测试 FAIL。
func TestLogfmtCovTruncateRuneSafe(t *testing.T) {
	inputs := []string{
		"\u5c06\u5728 3600 \u79d2\u540e\u91cd\u7f6e\uff0c\u8bf7\u7a0d\u540e\u91cd\u8bd5", // 中文错误 body
		"a\u732b\u5b57\u7b26\u4e32",
		"\U0001f600\U0001f601\U0001f602\U0001f603",
		"ascii-only-string",
	}
	for _, s := range inputs {
		trimmed := strings.TrimSpace(s)
		for n := 1; n <= len(trimmed)+2; n++ {
			got := Truncate(s, n)
			if !utf8.ValidString(got) {
				t.Fatalf("Truncate(%q, %d) = %q not valid UTF-8", s, n, got)
			}
			if len(got) > n {
				t.Fatalf("Truncate(%q, %d) = %q len %d > n", s, n, got, len(got))
			}
			if !strings.HasPrefix(trimmed, got) {
				t.Fatalf("Truncate(%q, %d) = %q is not a prefix of %q", s, n, got, trimmed)
			}
		}
	}
}

// ---- UID8 ----------------------------------------------------------------

func TestLogfmtCovUID8(t *testing.T) {
	cases := []struct {
		name string
		uid  string
		want string
	}{
		{"empty", "", "-"},
		{"single char", "a", "a"},
		{"seven chars", "1234567", "1234567"},
		{"exactly eight", "12345678", "12345678"},
		{"nine chars cut", "123456789", "12345678"},
		{"long uid cut", "1234567890abcdef", "12345678"},
		{"full dash placeholder passthrough", "-", "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UID8(tc.uid); got != tc.want {
				t.Fatalf("UID8(%q) = %q want %q", tc.uid, got, tc.want)
			}
		})
	}
}

// 契约不变量：非空 uid 的返回值必须是原 uid 前缀且长度 <= 8。
func TestLogfmtCovUID8PrefixInvariant(t *testing.T) {
	uids := []string{"a", "abcdefgh", "abcdefghijklmnop", "0f1e2d3c4b5a6978"}
	for _, uid := range uids {
		got := UID8(uid)
		if len(got) > 8 {
			t.Fatalf("UID8(%q) = %q len %d > 8", uid, got, len(got))
		}
		if !strings.HasPrefix(uid, got) {
			t.Fatalf("UID8(%q) = %q is not a prefix", uid, got)
		}
	}
}

// ---- Label ---------------------------------------------------------------

func TestLogfmtCovLabel(t *testing.T) {
	cases := []struct {
		name string
		uid  string
		nick string
		want string
	}{
		{"both empty", "", "", "-"},
		{"nick empty falls back to uid8", "1234567890abcdef", "", "12345678"},
		{"nick whitespace only falls back", "1234567890abcdef", "   ", "12345678"},
		{"both present", "1234567890abcdef", "\u793a\u4f8b\u6635\u79f0\u7532", "\u793a\u4f8b\u6635\u79f0\u7532(12345678)"},
		{"nick trimmed", "1234567890abcdef", "  nick  ", "nick(12345678)"},
		{"short uid kept whole", "abc", "nick", "nick(abc)"},
		{"empty uid with nick keeps dash", "", "nick", "nick(-)"},
		{"nick with parens already", "12345678", "a(b)", "a(b)(12345678)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Label(tc.uid, tc.nick); got != tc.want {
				t.Fatalf("Label(%q, %q) = %q want %q", tc.uid, tc.nick, got, tc.want)
			}
		})
	}
}

// ---- DisplayWidth --------------------------------------------------------

func TestLogfmtCovDisplayWidth(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"empty", "", 0},
		{"ascii", "abc", 3},
		{"space", " ", 1},
		{"nbsp is one column", "\u00a0", 1},
		{"cjk two columns", "\u732b", 2},
		{"three cjk", "\u732b\u732b\u732b", 6},
		{"cjk mixed with ascii", "a\u732bb", 4},
		{"nul zero", "\x00", 0},
		{"tab zero", "\t", 0},
		{"cr lf zero", "\r\n", 0},
		{"del zero", "\x7f", 0},
		{"c1 control zero", "\u0080", 0},
		{"us 0x1f zero", "\x1f", 0},
		{"latein small one col", "\u0100", 1},
		{"hangul jamo initial two cols", "\u1100", 2},
		{"hangul jamo last initial two cols", "\u115f", 2},
		{"hangul jamo medial one col", "\u1160", 1},
		{"angle bracket wide", "\u2329", 2},
		{"angle bracket right wide", "\u232a", 2},
		{"cjk radical supplement start wide", "\u2e80", 2},
		{"just below cjk radical narrow", "\u2e7f", 1},
		{"ideographic half fill is narrow", "\u303f", 1},
		{"cjk range end wide", "\ua4cf", 2},
		{"just past cjk range narrow", "\ua4d0", 1},
		{"hangul syllable start wide", "\uac00", 2},
		{"hangul syllable end wide", "\ud7a3", 2},
		{"past hangul syllable narrow", "\ud7a4", 1},
		{"cjk compat start wide", "\uf900", 2},
		{"cjk compat end wide", "\ufaff", 2},
		{"past cjk compat narrow", "\ufb00", 1},
		{"cjk compat forms start wide", "\ufe30", 2},
		{"cjk compat forms end wide", "\ufe6f", 2},
		{"halfwidth katakana narrow", "\uff61", 1},
		{"fullwidth ascii start wide", "\uff01", 2},
		{"fullwidth ascii end wide", "\uff5e", 2},
		{"halfwidth hangul narrow", "\uffa0", 1},
		{"fullwidth sign start wide", "\uffe0", 2},
		{"fullwidth sign end wide", "\uffe6", 2},
		{"past fullwidth signs narrow", "\uffe7", 1},
		{"emoji start wide", "\U0001f300", 2},
		{"emoji end wide", "\U0001f9ff", 2},
		{"emoji grinning wide", "\U0001f600", 2},
		{"cjk ext b start wide", "\U00020000", 2},
		{"mixed emoji ascii cjk", "a\U0001f600\u732b", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayWidth(tc.s); got != tc.want {
				t.Fatalf("DisplayWidth(%q) = %d want %d", tc.s, got, tc.want)
			}
		})
	}
}

// 显示宽度与字节数必须解耦：中文昵称 3 字节但只占 2 列。
// 若把 DisplayWidth 换成 len()（字节数），本测试 FAIL。
func TestLogfmtCovDisplayWidthNotBytes(t *testing.T) {
	nick := "\u732b\u732b\u732b" // 3 个中文 = 9 字节 / 6 列
	if len(nick) != 9 {
		t.Fatalf("test fixture broken: len = %d want 9", len(nick))
	}
	if got := DisplayWidth(nick); got != 6 {
		t.Fatalf("DisplayWidth(%q) = %d want 6", nick, got)
	}
}

// 窄字符「下沿」端点：宽字符区段从 U+1100 开始，其下必须全窄。
// U+1100 是 Hangul Jamo 初声（宽）的起点，它前面的 U+1000（缅甸文）、U+10FF
// （格鲁吉亚文）都只占 1 列。把 runeWidth 的 `r < 0x1100` 上限挪到 0x1000
// （或更小）会让 U+1000..U+10FF 掉进 `r <= 0x115f` 分支被算成 2 列 → 本测试 FAIL。
func TestLogfmtCovDisplayWidthNarrowLowerBound(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"just below myanmar narrow", "\u0fff", 1},
		{"myanmar start narrow", "\u1000", 1},
		{"myanmar letter narrow", "\u1001", 1},
		{"georgian upper narrow", "\u10fb", 1},
		{"georgian last before jamo narrow", "\u10ff", 1},
		{"hangul jamo initial is the wide start", "\u1100", 2},
		{"three narrow non-ascii runes", "\u1000\u10ff\u1001", 3},
		{"narrow non-ascii mixed with ascii", "\u1000ab\u10ff", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayWidth(tc.s); got != tc.want {
				t.Fatalf("DisplayWidth(%q) = %d want %d", tc.s, got, tc.want)
			}
		})
	}
}

// 同一批窄字符在 Truncate 下的字节边界行为：U+1000 / U+10FF 都是 3 字节
// （E1 80 80 / E1 83 BF），切点落在字符中间时必须整体让出。
// 去掉 rune 回退循环会让 n=4 切出 "\u1000" + 半截 \u10ff → 非合法 UTF-8，本测试 FAIL。
func TestLogfmtCovTruncateNarrowMultibyteBoundary(t *testing.T) {
	const s = "\u1000\u10ff" // 手写：3 + 3 = 6 字节
	if len(s) != 6 {
		t.Fatalf("test fixture broken: len = %d want 6", len(s))
	}
	cases := []struct {
		name string
		n    int
		want string
	}{
		{"cut in first rune", 1, ""},
		{"cut inside first rune second byte", 2, ""},
		{"first rune exact boundary", 3, "\u1000"},
		{"cut inside second rune first byte", 4, "\u1000"},
		{"cut inside second rune second byte", 5, "\u1000"},
		{"exact whole length", 6, "\u1000\u10ff"},
		{"beyond length", 9, "\u1000\u10ff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(s, tc.n)
			if got != tc.want {
				t.Fatalf("Truncate(%q, %d) = %q want %q", s, tc.n, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("Truncate(%q, %d) = %q not valid UTF-8", s, tc.n, got)
			}
		})
	}
}

// CJK 扩展区段「出口」端点：runeWidth 的判定是 0x20000 <= r <= 0x3fffd。
// 旧测试只钉了入口 0x20000，出口没人管：把上界改成 0x2ffff（U+30000 变窄）、
// 改成排他 0x3fffd 或放大到 0x4fffd 都能让测试全绿。这里四个端点都用手写字面量钉死。
func TestLogfmtCovDisplayWidthCJKExtExit(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"ext b entry wide", "\U00020000", 2},
		{"ext c wide", "\U0002a700", 2},
		{"ext g assigned wide", "\U00030000", 2},
		{"ext g mid wide", "\U00031234", 2},
		{"range last code point wide", "\U0003fffd", 2},
		{"just past range exit narrow", "\U0003fffe", 1},
		{"plane 3 far end narrow", "\U0003ffff", 1},
		{"two ext chars sum", "\U00030000\U0003fffd", 4},
		{"ext char plus ascii", "\U0003fffda", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayWidth(tc.s); got != tc.want {
				t.Fatalf("DisplayWidth(%q) = %d want %d", tc.s, got, tc.want)
			}
		})
	}
}

// CJK 扩展区的 4 字节补充平面字符在 Truncate 下同样必须落在 rune 边界：
// U+30000 / U+3FFFD 都是 4 字节（F0 B0 80 80 / F0 BF BF BD），n=5 落在第二个
// 字符的第 2 字节 → 回退到 4，整个第二个字符让出。
func TestLogfmtCovTruncateCJKExtBoundary(t *testing.T) {
	const s = "\U00030000\U0003fffd" // 手写：4 + 4 = 8 字节
	if len(s) != 8 {
		t.Fatalf("test fixture broken: len = %d want 8", len(s))
	}
	cases := []struct {
		name string
		n    int
		want string
	}{
		{"inside first char", 2, ""},
		{"first char exact boundary", 4, "\U00030000"},
		{"inside second char", 5, "\U00030000"},
		{"second char last byte", 7, "\U00030000"},
		{"whole both", 8, "\U00030000\U0003fffd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(s, tc.n)
			if got != tc.want {
				t.Fatalf("Truncate(%q, %d) = %q want %q", s, tc.n, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("Truncate(%q, %d) = %q not valid UTF-8", s, tc.n, got)
			}
		})
	}
}

// ---- Pad -----------------------------------------------------------------

func TestLogfmtCovPad(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"pad ascii", "abc", 5, "abc  "},
		{"already exact", "abc", 3, "abc"},
		{"overwide untouched", "abcdef", 3, "abcdef"},
		{"width zero untouched", "abc", 0, "abc"},
		{"width negative untouched", "abc", -7, "abc"},
		{"empty gets spaces", "", 3, "   "},
		{"empty width zero", "", 0, ""},
		// 中文占 2 列：width=6 只补 2 个空格（不是 3）。
		{"cjk pads by display width", "\u732b\u732b", 6, "\u732b\u732b  "},
		{"cjk overwide untouched", "\u732b\u732b\u732b", 5, "\u732b\u732b\u732b"},
		// 控制字符不占列：'\t' 宽度 0，故 "a\tb" 宽 2，补 1 空格。
		{"control char contributes nothing", "a\tb", 3, "a\tb "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Pad(tc.s, tc.width); got != tc.want {
				t.Fatalf("Pad(%q, %d) = %q want %q", tc.s, tc.width, got, tc.want)
			}
		})
	}
}

// Pad 契约：补完后的显示列宽恰好达到 width（未超宽时），且不截断超宽内容。
func TestLogfmtCovPadReachesTargetWidth(t *testing.T) {
	cases := []struct {
		s     string
		width int
	}{
		{"a", 4},
		{"\u732b", 5},
		{"", 2},
		{"abc", 4},
	}
	for _, tc := range cases {
		got := Pad(tc.s, tc.width)
		if w := DisplayWidth(got); w != tc.width {
			t.Fatalf("DisplayWidth(Pad(%q, %d)) = %d want %d", tc.s, tc.width, w, tc.width)
		}
		if !strings.HasPrefix(got, tc.s) {
			t.Fatalf("Pad(%q, %d) = %q dropped content", tc.s, tc.width, got)
		}
	}
}

// ---- ShortUA（截断 / 字段解析分支） --------------------------------------

func TestLogfmtCovShortUABranches(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want string
	}{
		{"whitespace only incl newline", " \t\n ", ""},
		{"first token wins", "WorkBuddy/5.5.6 curl/8.4.0", "WorkBuddy/5.5.6"},
		{"token without slash skipped", "hello curl/8.4.0", "curl/8.4.0"},
		{"empty name skipped", "/5.0 curl/8.4.0", "curl/8.4.0"},
		{"engine name case insensitive", "MOZILLA/5.0 CURL/8.4.0", "CURL/8.4.0"},
		{"engine then no-version token falls back to whole", "Mozilla/5.0 node", "Mozilla/5.0 node"},
		// 括号/分号/逗号包裹的 token 先剥壳再判定。
		{"comma wrapped token", "curl/8.4.0,", "curl/8.4.0"},
		{"paren wrapped token", "(WorkBuddy/5.5.6)", "WorkBuddy/5.5.6"},
		{"semicolon wrapped token", ";curl/8.4.0;", "curl/8.4.0"},
		// 三段式 UA 指纹：保留客户端段。
		{
			"three segment fingerprint",
			"WorkBuddy/5.5.6 (Windows NT 10.0; Win64) CLI/2.137.1",
			"WorkBuddy/5.5.6",
		},
		{"version with parens after slash", "PostmanRuntime/7.36.0", "PostmanRuntime/7.36.0"},
		{"leading whitespace trimmed", "   curl/8.4.0", "curl/8.4.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShortUA(tc.ua); got != tc.want {
				t.Fatalf("ShortUA(%q) = %q want %q", tc.ua, got, tc.want)
			}
		})
	}
}

// 超过 maxShortUALen 的单 token 必须截断到恰好 40 字节（不是 41，也不是 0）。
// 去掉 Truncate 调用或把上限改错会让本测试 FAIL。
func TestLogfmtCovShortUATruncatesLongToken(t *testing.T) {
	// 10 + 1 + 30 = 41 字节 → 应截到 10 + 1 + 29 = 40 字节。
	ua41 := strings.Repeat("a", 10) + "/" + strings.Repeat("b", 30)
	want40 := strings.Repeat("a", 10) + "/" + strings.Repeat("b", 29)
	if len(ua41) != 41 || len(want40) != 40 {
		t.Fatalf("test fixture broken: %d / %d", len(ua41), len(want40))
	}
	if got := ShortUA(ua41); got != want40 {
		t.Fatalf("ShortUA(41-byte token) = %q (len %d) want %q (len %d)",
			got, len(got), want40, len(want40))
	}

	// 恰好 40 字节 → 原样返回，不截断。
	ua40 := strings.Repeat("a", 10) + "/" + strings.Repeat("b", 29)
	if got := ShortUA(ua40); got != ua40 {
		t.Fatalf("ShortUA(40-byte token) = %q want identity", got)
	}
}

// 多字节 token 的截断必须落在 rune 边界：40 字节处是第 14 个中文的第 2 字节，
// 必须整体让出到 13 个中文（39 字节）。去掉 rune 回退会产生半截中文 → FAIL。
func TestLogfmtCovShortUAMultibyteTruncation(t *testing.T) {
	ua := strings.Repeat("\u732b", 20) + "/1.2.3" // 60 + 6 = 66 字节
	got := ShortUA(ua)
	want := strings.Repeat("\u732b", 13) // 13 * 3 = 39 <= 40
	if got != want {
		t.Fatalf("ShortUA(multibyte 66-byte) = %q (len %d) want %q (len %d)",
			got, len(got), want, len(want))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("ShortUA returned invalid UTF-8: %q", got)
	}
}

// 没有 name/version token 时回落整串，且同样受 40 字节上限约束并保持 rune 边界。
func TestLogfmtCovShortUAFallbackTruncation(t *testing.T) {
	// 纯产品名（无 slash）超长。
	long := strings.Repeat("x", 60)
	if got := ShortUA(long); got != strings.Repeat("x", 40) {
		t.Fatalf("ShortUA(60 x) = %q (len %d) want 40 x", got, len(got))
	}

	// 回落分支上的多字节串：30 个中文 = 90 字节 → 13 个中文（39 字节）。
	multi := strings.Repeat("\u5ba2\u6237\u7aef", 10)
	got := ShortUA(multi)
	want := strings.Repeat("\u5ba2\u6237\u7aef", 4) + "\u5ba2" // 4*9=36 +3 = 39
	if got != want {
		t.Fatalf("ShortUA(fallback multibyte) = %q (len %d) want %q (len %d)",
			got, len(got), want, len(want))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("ShortUA fallback returned invalid UTF-8: %q", got)
	}
}

// 输出恒有界：任意输入下字节数与显示列宽都不超过 40/40（列宽 <= 字节数）。
// 同时确保结果永远是 trim 后原串的子串，不做伪造拼装。
func TestLogfmtCovShortUABoundsAndSubstring(t *testing.T) {
	inputs := []string{
		"",
		"   ",
		"curl/8.4.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		strings.Repeat("\u732b", 100),
		"\U0001f600\U0001f601" + strings.Repeat("/v", 50),
		strings.Repeat("a/", 60),
	}
	for _, ua := range inputs {
		got := ShortUA(ua)
		if len(got) > maxShortUALen {
			t.Fatalf("ShortUA(%q) = %q len %d > %d", ua, got, len(got), maxShortUALen)
		}
		if w := DisplayWidth(got); w > maxShortUALen {
			t.Fatalf("ShortUA(%q) = %q display width %d > %d", ua, got, w, maxShortUALen)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("ShortUA(%q) invalid UTF-8 %q", ua, got)
		}
		if trimmed := strings.TrimSpace(ua); got != "" && !strings.Contains(trimmed, got) {
			t.Fatalf("ShortUA(%q) = %q is not a substring of trimmed input", ua, got)
		}
	}
}

// IE11 真实 UA：按 shortUAEngines 的规则逐步走一遍（期望值独立手写给字面量，不引用实现）：
//   - "Mozilla/5.0"          → name=mozilla 在白名单 → 跳过
//   - "(Windows" / "NT" / "6.1;" / "rv:11.0)" / "like" / "Gecko" 全都无 "/" → 跳过
//   - "Trident/7.0;"         → 剥壳后 name=trident 在白名单 → 跳过（这是 IE11 的关键项：
//     若把 trident 从白名单删掉，这里会直接返回 "Trident/7.0"）
//
// 于是走回落分支：整串 trim 后 61 字节，截到 40 字节。第 41 个字节（下标 40）是 ';'，
// 属 rune 起始 → 不回退，结果就是 "…Trident/7.0"（恰好 40 字节，以 Trident/7.0 结尾）。
// 注意这里钉的是「回落串」，不是把 Trident/7.0 当客户端标签——那正是当前实现的语义。
func TestLogfmtCovShortUAIE11RealUA(t *testing.T) {
	const ua = "Mozilla/5.0 (Windows NT 6.1; Trident/7.0; rv:11.0) like Gecko"
	const want = "Mozilla/5.0 (Windows NT 6.1; Trident/7.0" // 手写：独立数出来的 40 字节前缀
	if len(ua) != 61 {
		t.Fatalf("test fixture broken: len(ua) = %d want 61", len(ua))
	}
	if len(want) != 40 {
		t.Fatalf("test fixture broken: len(want) = %d want 40", len(want))
	}
	got := ShortUA(ua)
	if got != want {
		t.Fatalf("ShortUA(IE11 UA) = %q (len %d) want %q (len %d)", got, len(got), want, len(want))
	}
	if !strings.HasSuffix(got, "Trident/7.0") {
		t.Fatalf("ShortUA(IE11 UA) = %q want suffix %q", got, "Trident/7.0")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("ShortUA(IE11 UA) = %q not valid UTF-8", got)
	}
}

// IE11 的另一个真实变体（WOW64）走同一条回落路径，同样是手写字面量：
// 前 40 字节切在 "Trident/7.0" 的 'd' 之后 → "…; Trid"（第 41 字节 'd' 是 rune 起始，不回退）。
func TestLogfmtCovShortUAIE11WOW64(t *testing.T) {
	const ua = "Mozilla/5.0 (Windows NT 6.1; WOW64; Trident/7.0; rv:11.0) like Gecko"
	const want = "Mozilla/5.0 (Windows NT 6.1; WOW64; Trid"
	if len(ua) != 68 {
		t.Fatalf("test fixture broken: len(ua) = %d want 68", len(ua))
	}
	if len(want) != 40 {
		t.Fatalf("test fixture broken: len(want) = %d want 40", len(want))
	}
	if got := ShortUA(ua); got != want {
		t.Fatalf("ShortUA(IE11 WOW64 UA) = %q (len %d) want %q (len %d)", got, len(got), want, len(want))
	}
}
