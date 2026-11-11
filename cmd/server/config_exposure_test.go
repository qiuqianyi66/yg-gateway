package main

import (
	"strings"
	"testing"
)

func TestClassifyListen(t *testing.T) {
	cases := []struct {
		listen string
		want   listenScope
	}{
		{":7863", listenWildcard},
		{"0.0.0.0:7863", listenWildcard},
		{"[::]:7863", listenWildcard},
		{"127.0.0.1:7863", listenLoopback},
		{"[::1]:7863", listenLoopback},
		{"localhost:7863", listenLoopback},
		{"LOCALHOST:7863", listenLoopback},
		{"192.168.1.10:7863", listenConcrete},
		{"example.com:7863", listenConcrete},
		// 异常输入（没有端口）：整串当主机判严，不因解析失败放行。
		{"garbage", listenConcrete},
	}
	for _, c := range cases {
		if got := classifyListen(c.listen); got != c.want {
			t.Errorf("classifyListen(%q)=%v want %v", c.listen, got, c.want)
		}
	}
}

func TestCheckUnauthenticatedExposure(t *testing.T) {
	disabled := false
	enabled := true
	cases := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name:    "全网卡 + 无密钥 → 拒绝",
			cfg:     &Config{Listen: ":7863"},
			wantErr: true,
		},
		{
			name: "全网卡 + 单 api_key → 放行",
			cfg:  &Config{Listen: ":7863", APIKey: "sk-live"},
		},
		{
			name: "全网卡 + 多 Key（启用）→ 放行",
			cfg: &Config{Listen: ":7863", APIKeys: []APIKeyConfig{
				{ID: "k1", Key: "sk-a", Enabled: &enabled},
			}},
		},
		{
			name: "全网卡 + 多 Key 全部 disabled → 拒绝",
			cfg: &Config{Listen: ":7863", APIKeys: []APIKeyConfig{
				{ID: "k1", Key: "sk-a", Enabled: &disabled},
			}},
			wantErr: true,
		},
		{
			name: "回环 + 空密钥 → 放行（本机调试合法用法）",
			cfg:  &Config{Listen: "127.0.0.1:7863"},
		},
		{
			name:    "具体内网 IP + 空密钥 → 拒绝",
			cfg:     &Config{Listen: "192.168.1.10:7863"},
			wantErr: true,
		},
		{
			name: "nil 配置 → 放行（不 panic）",
			cfg:  nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckUnauthenticatedExposure(c.cfg)
			if c.wantErr && err == nil {
				t.Fatalf("want error, got nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if err != nil {
				// 错误必须是可操作提示：带 listen 与至少一条解法，否则运维只能去翻代码。
				msg := err.Error()
				for _, want := range []string{"拒绝启动", "api_key", c.cfg.Listen} {
					if !strings.Contains(msg, want) {
						t.Errorf("error message missing %q: %s", want, msg)
					}
				}
			}
		})
	}
}
