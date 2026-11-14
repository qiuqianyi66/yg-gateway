// OneLine 的牙齿测试：没有它，客户端可用 model 里的换行伪造整行日志。
//
// 还原 bug（把 OneLine 换回恒等函数 / 去掉控制字符分支）→ 本文件的
// TestOneLinePinsNewline 必 FAIL。
package logfmt

import (
	"strings"
	"testing"
)

func TestOneLinePinsNewline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lf", "a\nb", "a b"},
		{"crlf", "a\r\nb", "a  b"}, // \r 与 \n 各压成一个空格
		{"tab", "a\tb", "a b"},
		{"nul", "a\x00b", "a b"},
		{"del", "a\x7fb", "a b"},
		{"trailing lf kept as space", "model\n", "model "},
		{"leading lf kept as space", "\nmodel", " model"},
		{"chinese untouched", "深度优化\n完成", "深度优化 完成"},
		{"clean fast path", "deepseek-v4.1-flash", "deepseek-v4.1-flash"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := OneLine(tc.in); got != tc.want {
				t.Fatalf("OneLine(%q) = %q want %q", tc.in, got, tc.want)
			}
			if strings.ContainsAny(OneLine(tc.in), "\r\n\t\x00\x7f") {
				t.Fatalf("OneLine(%q) 仍含控制字符: %q", tc.in, OneLine(tc.in))
			}
		})
	}
}

// 压平后的字符串必须仍是单行可打印形态：任何输入下都不出现 \n。
func TestOneLineNeverEmitsNewline(t *testing.T) {
	inputs := []string{
		"a\nb\nc", "\r\n\r\n", "\t\t", "伪造\r\n2026-11-14 INFO: 账号已禁用",
		"GCU\x00\x01\x02", "正常模型名",
	}
	for _, in := range inputs {
		out := OneLine(in)
		if strings.Contains(out, "\n") || strings.Contains(out, "\r") {
			t.Fatalf("OneLine(%q) = %q 仍含换行", in, out)
		}
	}
}

func BenchmarkOneLineClean(b *testing.B) {
	s := "deepseek-v4.1-flash"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = OneLine(s)
	}
}

func BenchmarkOneLineDirty(b *testing.B) {
	s := "model\nwith\r\ncontrols"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = OneLine(s)
	}
}
