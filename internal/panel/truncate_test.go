package panel

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateStrUTF8Boundary（C-5）：切点落在多字节字符中间时必须回退 rune 边界。
// 上游错误响应多为中文，按字节切会出半截序列非法 UTF-8（经 json.Marshal 变 U+FFFD 乱码）。
func TestTruncateStrUTF8Boundary(t *testing.T) {
	long := strings.Repeat("测试错误响应内容很长", 20) // 24 字节 ×20 = 480 字节
	got := truncateStr(long, 160)
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果含非法 UTF-8: %q", got)
	}
	if len(got) > 160 {
		t.Fatalf("截断超上限: len=%d want <=160", len(got))
	}
	// 短于上限原样返回。
	if got := truncateStr("短错误", 160); got != "短错误" {
		t.Fatalf("短串 = %q", got)
	}
}
