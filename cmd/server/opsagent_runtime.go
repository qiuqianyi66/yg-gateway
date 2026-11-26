// opsagent_runtime.go 运维 agent runtime 装配（ADR-029）。
//
// 默认关闭：opsagent.agent.enabled=false 或 model 为空时返回 nil，不建目录、
// 不启动 goroutine、不调模型（零回归）。模型只走本机网关，工具只读。
package main

import (
	"context"
	"log"

	"yg-gateway/internal/opsagent"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
)

func buildOpsRuntime(cfg *Config, p *pool.Pool, rl *reqlog.Recorder) (*opsagent.Runtime, error) {
	if cfg == nil || !cfg.OpsAgent.Enabled || !cfg.OpsAgent.Agent.Enabled {
		return nil, nil
	}
	if p == nil {
		log.Printf("[opsagent-runtime] agent.enabled=true 但 pool 为空，跳过")
		return nil, nil
	}
	if cfg.OpsAgent.Agent.Model == "" {
		log.Printf("[opsagent-runtime] agent.enabled=true 但 model 为空，跳过（避免误调付费模型）")
		return nil, nil
	}
	tools, err := buildOpsRuntimeTools(cfg, p, rl)
	if err != nil {
		return nil, err
	}
	rc := cfg.OpsAgent.runtimeConfig()
	if rc.SessionDir == "" {
		rc.SessionDir = stateSibling(cfg.StateFile, "opsagent-sessions")
	}
	rt, err := opsagent.NewRuntime(rc, newGatewayRuntimeModel(cfg, cfg.OpsAgent.Agent.Model), tools,
		opsagent.RuntimeReadOnlyPolicy{}, opsRuntimePrompt)
	if err != nil {
		return nil, err
	}
	return rt, nil
}

func startOpsRuntime(rt *opsagent.Runtime, ctx context.Context) {
	if rt == nil {
		return
	}
	sts := rt.Status()
	log.Printf("[opsagent-runtime] 已启用：model=%s session=%s", sts.Model, sts.SessionID)
	go rt.Run(ctx)
}

const opsRuntimePrompt = `你是 yg-gateway 的运维 agent。只读诊断，不自动改配置。
先调用 gateway_status 获取池/健康面；再按异常选择 request_stats、recent_errors、deploy_reports、unknowns。
输出四段：结论、证据（工具返回的关键数字）、风险、建议的下一步（只读验证优先）。
禁止建议或执行改配置、换号、重启、部署；这些只能作为人工建议。`
