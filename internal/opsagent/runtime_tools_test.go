// runtime_tools_test.go 工具策略/执行器的牙齿（ADR-029 D3）。
package opsagent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func testTool(name string, readOnly bool, fn func(context.Context, json.RawMessage) (string, error)) RuntimeToolSpec {
	return RuntimeToolSpec{
		Name: name, Description: name, Parameters: []byte(`{"type":"object"}`),
		ReadOnly: readOnly, Execute: fn,
	}
}

func TestRuntimeToolPolicyDeniesWriteWithoutExecute(t *testing.T) {
	var calls atomic.Int32
	reg, err := NewRuntimeToolRegistry(testTool("set_config", false, func(context.Context, json.RawMessage) (string, error) {
		calls.Add(1)
		return "should not run", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	ex := NewRuntimeToolExecutor(time.Second, 1024)
	res := ex.Execute(context.Background(), RuntimeToolCall{ID: "c1", Name: "set_config"}, reg, RuntimeReadOnlyPolicy{})
	if !res.Denied || !res.IsError {
		t.Fatalf("res=%#v, want denied+is_error", res)
	}
	if calls.Load() != 0 {
		t.Fatalf("工具被调用了 %d 次；拒绝路径必须零副作用", calls.Load())
	}
}

func TestRuntimeToolUnknown(t *testing.T) {
	reg, err := NewRuntimeToolRegistry(testTool("ok", true, func(context.Context, json.RawMessage) (string, error) { return "x", nil }))
	if err != nil {
		t.Fatal(err)
	}
	res := NewRuntimeToolExecutor(time.Second, 1024).Execute(context.Background(), RuntimeToolCall{ID: "c", Name: "nope"}, reg, nil)
	if !res.IsError || res.Denied {
		t.Fatalf("res=%#v", res)
	}
}

func TestRuntimeToolPanicAndTimeout(t *testing.T) {
	reg, err := NewRuntimeToolRegistry(
		testTool("panic", true, func(context.Context, json.RawMessage) (string, error) { panic("boom") }),
		testTool("slow", true, func(ctx context.Context, _ json.RawMessage) (string, error) {
			select {
			case <-time.After(200 * time.Millisecond):
				return "late", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ex := NewRuntimeToolExecutor(50*time.Millisecond, 1024)
	if res := ex.Execute(context.Background(), RuntimeToolCall{ID: "p", Name: "panic"}, reg, nil); !res.IsError || res.Content == "" {
		t.Fatalf("panic res=%#v", res)
	}
	if res := ex.Execute(context.Background(), RuntimeToolCall{ID: "s", Name: "slow"}, reg, nil); !res.IsError {
		t.Fatalf("timeout res=%#v", res)
	}
}

func TestRuntimeToolResultTruncated(t *testing.T) {
	reg, err := NewRuntimeToolRegistry(testTool("big", true, func(context.Context, json.RawMessage) (string, error) {
		return "0123456789", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	res := NewRuntimeToolExecutor(time.Second, 5).Execute(context.Background(), RuntimeToolCall{ID: "b", Name: "big"}, reg, nil)
	if len(res.Content) != 5+len("\n...[truncated]") {
		t.Fatalf("content=%q len=%d", res.Content, len(res.Content))
	}
}

func TestRuntimeToolRegistryErrorsAndNil(t *testing.T) {
	if _, err := NewRuntimeToolRegistry(RuntimeToolSpec{}); err == nil {
		t.Fatal("空 name 应报错")
	}
	if _, err := NewRuntimeToolRegistry(RuntimeToolSpec{Name: "x"}); err == nil {
		t.Fatal("nil Execute 应报错")
	}
	dup := testTool("x", true, func(context.Context, json.RawMessage) (string, error) { return "x", nil })
	if _, err := NewRuntimeToolRegistry(dup, dup); err == nil {
		t.Fatal("重复 name 应报错")
	}
	var nilReg *RuntimeToolRegistry
	if nilReg.Count() != 0 || len(nilReg.Schemas()) != 0 {
		t.Fatal("nil registry 应零值")
	}
	if _, ok := nilReg.Get("x"); ok {
		t.Fatal("nil registry Get 应 false")
	}
}

func TestRuntimeToolPolicyAllowsReadOnly(t *testing.T) {
	reg, _ := NewRuntimeToolRegistry(testTool("ok", true, func(context.Context, json.RawMessage) (string, error) { return "x", nil }))
	ex := NewRuntimeToolExecutor(0, 0)
	res := ex.Execute(context.Background(), RuntimeToolCall{ID: "c", Name: "ok"}, reg, RuntimeReadOnlyPolicy{})
	if res.IsError || res.Denied || res.Content != "x" {
		t.Fatalf("res=%#v", res)
	}
}

func TestRuntimeToolSchemaStable(t *testing.T) {
	reg, err := NewRuntimeToolRegistry(
		testTool("zeta", true, func(context.Context, json.RawMessage) (string, error) { return "z", nil }),
		testTool("alpha", true, func(context.Context, json.RawMessage) (string, error) { return "a", nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	schemas := reg.Schemas()
	if len(schemas) != 2 || schemas[0].Name != "alpha" || schemas[1].Name != "zeta" {
		t.Fatalf("schemas=%#v", schemas)
	}
}

// 编译期保证：默认策略仍实现接口。
var _ RuntimePolicy = RuntimeReadOnlyPolicy{}
