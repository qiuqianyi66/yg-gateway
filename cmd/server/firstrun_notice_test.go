package main

import (
	"os"
	"strings"
	"testing"
)

// TestFirstRunNoticeDoesNotEchoKey 锁定首次启动提示：文案必须给出配置文件路径，
// 且不得出现 "api_key=xxx" 的回显形态（值在配置里，不需要也不应该进日志）。
func TestFirstRunNoticeDoesNotEchoKey(t *testing.T) {
	msg := firstRunNotice(`C:\tmp\config.json`)
	if strings.Contains(msg, "api_key=") {
		t.Fatalf("首次启动日志不得回显 api_key 值: %s", msg)
	}
	if !strings.Contains(msg, `C:\tmp\config.json`) {
		t.Fatalf("必须提示配置文件路径（否则用户找不到 key）: %s", msg)
	}
}

// TestMainSourceDoesNotLogRawAPIKey 源码级牙齿：把实现改回
// `log.Printf("... api_key=%s ...", key)` 时本测试立刻 FAIL。
// 只认 %s 回显形态——布尔回显（cfg.APIKey != ""）是安全的，不在拦截范围。
func TestMainSourceDoesNotLogRawAPIKey(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败（测试应在包目录运行）: %v", err)
	}
	if strings.Contains(string(src), "api_key=%s") {
		t.Fatal("main.go 不得回显明文 api_key（R1/R2）：日志会被重定向/采集")
	}
}
