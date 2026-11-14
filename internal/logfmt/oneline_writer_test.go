package logfmt

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestOneLineWriterFlattensMessageBody(t *testing.T) {
	var buf bytes.Buffer
	w := OneLineWriter{W: &buf}
	// 一次 Write = 一条日志（log 包的行为），末尾 \n 是分隔符。
	msg := "| model=evil\n2026-11-14 INFO: 账号已禁用 |\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("应只剩末尾一个换行，实际 %d 个:\n%s", n, out)
	}
	if !strings.HasPrefix(out, "| model=evil 2026-11-14 INFO: 账号已禁用 |") {
		t.Fatalf("压平后内容不符: %q", out)
	}
}

func TestOneLineWriterPassesCleanThrough(t *testing.T) {
	var buf bytes.Buffer
	w := OneLineWriter{W: &buf}
	msg := "INFO: [server] 正常日志行\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	if buf.String() != msg {
		t.Fatalf("干净消息应原样透传，got %q want %q", buf.String(), msg)
	}
}

func TestOneLineWriterNeverEmitsInnerNewline(t *testing.T) {
	var buf bytes.Buffer
	w := OneLineWriter{W: &buf}
	for _, in := range []string{
		"a\nb\nc\n", "payload={\n\"x\":1\n}\n", "\r\n\r\n伪造\n", "无换行",
	} {
		buf.Reset()
		if _, err := w.Write([]byte(in)); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(buf.String(), "\n"); n > 1 {
			t.Fatalf("输入 %q 产生 %d 个换行（应 ≤1）: %q", in, n, buf.String())
		}
	}
}

func TestOneLineWriterNilWriter(t *testing.T) {
	var w OneLineWriter
	if n, err := w.Write([]byte("x\n")); n != 2 || err != nil {
		t.Fatalf("nil writer 应静默消费，got n=%d err=%v", n, err)
	}
}

var _ io.Writer = OneLineWriter{}
