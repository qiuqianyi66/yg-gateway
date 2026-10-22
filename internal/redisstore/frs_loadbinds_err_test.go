package redisstore

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestFRSLoadBindsScanErrorIsWarned 钉住 F-01：SCAN 中途出错（服务器返回错误）时
// iter.Err() 必须被检查——LoadBinds 不得静默返回部分结果，要打 WARN 日志如实标注；
// 同时不阻断启动恢复（不 panic、返回映射）。
func TestFRSLoadBindsScanErrorIsWarned(t *testing.T) {
	s := frsStart(t, func(args []string) string {
		if len(args) > 0 && strings.ToUpper(args[0]) == "SCAN" {
			return frsErr("LOADING Redis is loading the dataset in memory")
		}
		return frsDefaultScript(args)
	})
	u := frsUpstash(t, s)

	// 捕获标准日志输出，断言 WARN 确实打出。
	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(oldOut)

	got := u.LoadBinds() // 不 panic

	if got == nil {
		t.Fatalf("扫描出错也应返回非 nil 映射（调用方 range 安全）")
	}
	if len(got) != 0 {
		t.Fatalf("扫描出错时无任何键可读，映射应为空，实际 %v", got)
	}
	if !strings.Contains(buf.String(), "[redisstore] LoadBinds scan error") {
		t.Fatalf("扫描出错必须打 WARN 日志（R10 不静默吞），实际日志：%q", buf.String())
	}
	frsSingle(t, s, "SCAN")
}
