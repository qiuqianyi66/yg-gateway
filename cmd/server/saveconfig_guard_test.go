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

	// D-03：磁盘上的配置文件本身已是坏 JSON 时，此前降级成空 map 继续合并——
	// 一次保存就把用户手写的未知键整体洗掉。必须报错拒绝，且原文件不得被触碰。
	t.Run("磁盘配置文件已损坏 → 报错且原样保留（不降级空 map 洗掉手写键）", func(t *testing.T) {
		dir := t.TempDir()
		broken := filepath.Join(dir, "config.json")
		if err := os.WriteFile(broken, []byte(`{"api_key":"k", not-a-key`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := saveConfig([]byte(`{"listen":":1"}`), broken, nil, nil, nil, nil); err == nil {
			t.Fatal("磁盘配置已损坏时必须报错（不得降级空 map 继续合并落盘）")
		} else if !strings.Contains(err.Error(), "已损坏") {
			t.Errorf("错误信息应提示配置已损坏，请先修复，got %v", err)
		}
		after, err := os.ReadFile(broken)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(after), "not-a-key") {
			t.Error("损坏的原文件必须原样保留，不得被半截新配置覆盖")
		}
		if _, statErr := os.Stat(broken + ".tmp"); !os.IsNotExist(statErr) {
			t.Error("失败路径不得残留 .tmp 文件")
		}
	})
}

// D-01：alerting.* 是装配期字段（Notifier 启动时构造后不再读取），必须出现在
// restartRequiredFields 里——否则面板改告警地址「保存成功」却静默发旧地址，
// 告警是积分打穿的安全网，这与 issue #102 附带发现 2 同类。
func TestRestartRequiredFieldsIncludesAlerting(t *testing.T) {
	cfg, err := ParseConfig([]byte(`{"api_key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := restartRequiredFields(cfg)
	for _, want := range []string{
		"alerting.webhook_url",
		"alerting.min_credit_threshold",
		"alerting.consecutive_failures",
		"alerting.dedup_minutes",
	} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("restartRequiredFields 缺 %q（面板改它将无重启提示）；清单=%v", want, got)
		}
	}
}
