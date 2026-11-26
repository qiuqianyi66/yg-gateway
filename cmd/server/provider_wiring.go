package main

import (
	"strings"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/server"
)

// buildProviderRouting 把 config.providers[] 拆成两份（ADR-026）：
//
//  1. providerOfModel：模型 → providerID 白名单路由（注入 pool，选号分派用）
//  2. providerEndpoints：providerID → 出站端点（注入 server，出站分派用）
//
// 模型名经 ResolveModel 归一为裸名（对齐 pick 的 reqModel=bareModel 口径），用户配
// "global:xxx" 也能正确路由。未启用条目跳过（不参与流量，零回归）。从 main.go 拆出
// 是为控制 main.go 体量（check-depth 棘轮，非逻辑变更）。
func buildProviderRouting(cfg *Config, p *pool.Pool) map[string]server.ProviderEndpoint {
	ensureProviderAccounts(cfg, p)
	modelProvider := map[string]string{}
	providerEndpoints := map[string]server.ProviderEndpoint{}
	for _, pc := range cfg.Providers {
		if !pc.Enabled {
			continue
		}
		providerEndpoints[pc.ID] = server.ProviderEndpoint{
			BaseURL: pc.BaseURL,
			APIKey:  pc.APIKey,
			Kind:    pc.Kind,
			Quota:   pc.Quota,
			Models:  pc.Models,
			Enabled: pc.Enabled,
		}
		for _, m := range pc.Models {
			_, bare := server.ResolveModel(m)
			modelProvider[bare] = pc.ID
		}
	}
	if len(providerEndpoints) > 0 {
		p.SetProviderOfModel(func(model string) (string, bool) {
			pid, ok := modelProvider[model]
			return pid, ok
		})
	}
	return providerEndpoints
}

// ensureProviderAccounts 让“provider 级 api_key”无需再手写 auths 文件即可用。
//
// 背景：providers[].api_key 本来就是共享 key 形态（README 已声明回退优先级），但
// 选号仍要求池里存在该 provider 的账号；用户只配一个 provider 时容易得到
// “配置看起来完整、请求却 no_healthy_account”。这里仅在该 provider 尚无任何账号时
// 建一个内存账号（不进 auths、不写盘）：UID 固定 provider:<id>，Provider=id，
// APIKey 来自 provider 配置。已有 auths 账号（可多 key）时一律不建，零回归。
//
// 非 codebuddy provider 的 APIKey 不走 OAuth；scheduler 已按 ProviderID 跳过它们
// （见 internal/scheduler），不会对虚拟账号发 refresh/签到请求。
func ensureProviderAccounts(cfg *Config, p *pool.Pool) {
	if cfg == nil || p == nil {
		return
	}
	existing := map[string]bool{}
	for _, st := range p.List() {
		if st.Provider != "" {
			existing[st.Provider] = true
		}
	}
	for _, pc := range cfg.Providers {
		if !pc.Enabled || strings.TrimSpace(pc.APIKey) == "" || existing[pc.ID] {
			continue
		}
		p.Add(&auth.Auth{
			UID:      "provider:" + pc.ID,
			Provider: pc.ID,
			APIKey:   pc.APIKey,
			Nickname: pc.ID + " (config)",
		})
		existing[pc.ID] = true
	}
}

// providerSummaryView 构造 panel 用的多 provider 汇总（F7）：注册表元数据 + 各
// provider 账号数（实时从 pool 统计）。与 internal/server 的 providerSummary 同
// 形状但独立实现（panel 不依赖 server，依赖方向红线）。未启用条目也列出（展示
// 注册表全貌，前端按 enabled 打标）。providers 空 → nil（overview 省略键）。
func providerSummaryView(providers []ProviderConfig, list []pool.Status) []map[string]any {
	if len(providers) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, a := range list {
		if a.Provider != "" {
			counts[a.Provider]++
		} else {
			counts["codebuddy"]++
		}
	}
	out := make([]map[string]any, 0, len(providers))
	for _, pc := range providers {
		out = append(out, map[string]any{
			"id":            pc.ID,
			"kind":          pc.Kind,
			"quota":         pc.Quota,
			"enabled":       pc.Enabled,
			"models":        pc.Models,
			"account_count": counts[pc.ID],
		})
	}
	return out
}
