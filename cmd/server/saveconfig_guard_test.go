package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// saveConfig 此前 66.0%：缺的是**错误分支**。它们决定「面板提交坏配置会不会落盘」——
// 面板保存是不可逆的用户动作，半成品配置顶掉好配置是最难恢复的一类事故。
//
// 三条子用例都断言「失败时原文件一字未改」，不只是断言返回了 err。
func TestSaveConfigRejectsBadInputWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "config.json")
	if err := os.WriteFile(good, []byte(`{"api_key":"k","listen":":7863"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("读不到现有配置 → 报错（不在无基础上覆盖写）", func(t *testing.T) {
		missing := filepath.Join(dir, "nope.json")
		if _, err := saveConfig([]byte(`{"listen":":1"}`), missing, nil, nil, nil, nil); err == nil {
			t.Fatal("读不到现有 config 时必须报错")
		} else if !strings.Contains(err.Error(), "read current config") {
			t.Errorf("错误信息应指明 read current config，got %v", err)
		}
		if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
			t.Error("失败时不得生成配置文件")
		}
	})

	t.Run("提交的 JSON 非法 → 报错且原文件不变", func(t *testing.T) {
		before, err := os.ReadFile(good)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := saveConfig([]byte(`{not json`), good, nil, nil, nil, nil); err == nil {
			t.Fatal("非法 JSON 必须报错")
		} else if !strings.Contains(err.Error(), "parse submitted config") {
			t.Errorf("错误信息应指明 parse submitted config，got %v", err)
		}
		after, err := os.ReadFile(good)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Error("校验失败不得改动原配置文件")
		}
	})

	t.Run("配置校验不通过（显式空 api_keys）→ 报错且不落盘", func(t *testing.T) {
		before, err := os.ReadFile(good)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := saveConfig([]byte(`{"api_key":"k","api_keys":[]}`), good, nil, nil, nil, nil); err == nil {
			t.Fatal("显式空 api_keys 必须被 ParseConfig 拒绝")
		}
		after, err := os.ReadFile(good)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Error("校验失败不得改动原配置文件")
		}
	})
}
