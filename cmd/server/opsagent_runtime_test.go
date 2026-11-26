// opsagent_runtime_test.go 装配层默认关闭与配置映射。
package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildOpsRuntimeGatesAndSuccess(t *testing.T) {
	cfg := &Config{StateFile: filepath.Join(t.TempDir(), "state.json")}
	cfg.OpsAgent.Enabled = true
	cfg.OpsAgent.Agent.Enabled = true
	cfg.OpsAgent.Agent.Model = "free"
	if rt, err := buildOpsRuntime(cfg, nil, nil); err != nil || rt != nil {
		t.Fatalf("pool 为空应跳过: rt=%v err=%v", rt, err)
	}
	p, err := newPool(cfg.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg.OpsAgent.Agent.Model = ""
	if rt, err := buildOpsRuntime(cfg, p, nil); err != nil || rt != nil {
		t.Fatalf("model 为空应跳过: rt=%v err=%v", rt, err)
	}

	cfg.OpsAgent.Agent.Model = "free"
	rt, err := buildOpsRuntime(cfg, p, nil)
	if err != nil {
		t.Fatalf("buildOpsRuntime: %v", err)
	}
	if rt == nil {
		t.Fatal("配置齐全应构建 runtime")
	}
	defer rt.Close()
	startOpsRuntime(rt, context.Background())
	rt.Cancel()
}

func TestStartOpsRuntimeNil(t *testing.T) {
	startOpsRuntime(nil, context.Background())
}

func TestBuildOpsRuntimeDisabled(t *testing.T) {
	rt, err := buildOpsRuntime(&Config{}, nil, nil)
	if err != nil || rt != nil {
		t.Fatalf("rt=%v err=%v, want nil/nil", rt, err)
	}
}

func TestOpsAgentRuntimeConfigMapping(t *testing.T) {
	c := OpsAgentConfig{
		Enabled: true, IntervalSec: 120,
		Agent: RuntimeAgentConfig{
			Enabled: true, Model: "free", MaxSteps: 5, MaxTokens: 1024,
			ContextMaxTokens: 64000, TurnTimeoutSec: 90, SessionDir: "D:/x",
		},
	}
	rc := c.runtimeConfig()
	if !rc.Enabled || rc.Model != "free" || rc.MaxSteps != 5 || rc.MaxTokens != 1024 ||
		rc.ContextMaxTokens != 64000 || rc.SessionDir != "D:/x" {
		t.Fatalf("rc=%#v", rc)
	}
	if rc.Interval != 2*time.Minute || rc.TurnTimeout != 90*time.Second {
		t.Fatalf("durations=%s / %s", rc.Interval, rc.TurnTimeout)
	}
}
