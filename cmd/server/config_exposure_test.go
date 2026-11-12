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

// TestSaveConfigRejectsClearingKeyOnWildcard 锁定热更新路径：面板清空 api_key
// 属热生效字段，若只挡冷启动，实例会当场变成无鉴权开放（本闸必须同口径复检）。
func TestSaveConfigRejectsClearingKeyOnWildcard(t *testing.T) {
	env := cmdserverNewEnv(t, `{"listen":":7863","api_key":"sk-live"}`)

	if _, err := env.save(t, `{"api_key":""}`); err == nil {
		t.Fatal("全网卡监听下清空 api_key 必须被拒绝")
	} else if !strings.Contains(err.Error(), "拒绝启动") {
		t.Fatalf("错误信息应带拒绝理由，got: %v", err)
	}

	// 失败不落盘：磁盘上原密钥必须还在。
	disk := cmdserverFileMap(t, env.path)
	if disk["api_key"] != "sk-live" {
		t.Fatalf("被拒绝的保存不得改写磁盘，got api_key=%v", disk["api_key"])
	}

	// 保留密钥的保存照常通过（不误伤正常路径）。
	if _, err := env.save(t, `{"api_key":"sk-new"}`); err != nil {
		t.Fatalf("带密钥的保存应通过，got: %v", err)
	}
}

// TestCheckExposureNoOptIn 开关已移除：空密钥 + 全网卡一律拒绝，不存在放行配置。
func TestCheckExposureNoOptIn(t *testing.T) {
	off, err := ParseConfig([]byte(`{"listen":":7863"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckUnauthenticatedExposure(off); err == nil {
		t.Fatal("空密钥 + 全网卡必须拒绝")
	}

	// 旧配置里残留的 allow_unauthenticated 字段不得再成为放行依据。
	legacy, err := ParseConfig([]byte(`{"listen":":7863","security":{"allow_unauthenticated":true}}`))
	if err != nil {
		t.Fatalf("旧字段应被忽略而不是解析失败，got: %v", err)
	}
	if err := CheckUnauthenticatedExposure(legacy); err == nil {
		t.Fatal("旧的 allow_unauthenticated 字段不得放行")
	}
}
