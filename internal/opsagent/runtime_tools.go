// runtime_tools.go 工具注册、策略与受控执行器（ADR-029 D3）。
//
// 执行顺序固定：lookup -> policy.Authorize -> Execute。策略拒绝时工具函数
// 零调用；panic/超时/结果过大都被收敛为 tool/result{is_error}，不把进程带走。
package opsagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"
)

// RuntimeToolRegistry 保存已注册工具；名称重复直接报错（装配期 fail fast）。
type RuntimeToolRegistry struct {
	specs map[string]RuntimeToolSpec
	order []string
}

// NewRuntimeToolRegistry 构造注册表。Execute 为 nil 的工具也拒绝注册。
func NewRuntimeToolRegistry(specs ...RuntimeToolSpec) (*RuntimeToolRegistry, error) {
	r := &RuntimeToolRegistry{specs: make(map[string]RuntimeToolSpec, len(specs))}
	for _, spec := range specs {
		if spec.Name == "" {
			return nil, fmt.Errorf("opsagent runtime: tool 缺 name")
		}
		if spec.Execute == nil {
			return nil, fmt.Errorf("opsagent runtime: tool %s 缺 Execute", spec.Name)
		}
		if _, ok := r.specs[spec.Name]; ok {
			return nil, fmt.Errorf("opsagent runtime: tool %s 重复注册", spec.Name)
		}
		r.specs[spec.Name] = spec
		r.order = append(r.order, spec.Name)
	}
	sort.Strings(r.order)
	return r, nil
}

// Schemas 返回稳定排序的工具声明。
func (r *RuntimeToolRegistry) Schemas() []RuntimeToolSchema {
	if r == nil {
		return nil
	}
	out := make([]RuntimeToolSchema, 0, len(r.order))
	for _, name := range r.order {
		spec := r.specs[name]
		out = append(out, RuntimeToolSchema{
			Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters,
		})
	}
	return out
}

// Get 查询工具。
func (r *RuntimeToolRegistry) Get(name string) (RuntimeToolSpec, bool) {
	if r == nil {
		return RuntimeToolSpec{}, false
	}
	spec, ok := r.specs[name]
	return spec, ok
}

// Count 返回工具数量。
func (r *RuntimeToolRegistry) Count() int {
	if r == nil {
		return 0
	}
	return len(r.order)
}

// RuntimeReadOnlyPolicy 只放行 ReadOnly 工具；写工具没有审批回调，默认拒绝。
type RuntimeReadOnlyPolicy struct{}

// Authorize 实现 RuntimePolicy。
func (RuntimeReadOnlyPolicy) Authorize(_ context.Context, spec RuntimeToolSpec, _ json.RawMessage) (bool, string) {
	if spec.ReadOnly {
		return true, ""
	}
	return false, "MVP 只允许只读工具；写动作没有审批回调"
}

// RuntimeToolExecutor 单次执行一个工具调用。
type RuntimeToolExecutor struct {
	timeout   time.Duration
	maxResult int
}

// NewRuntimeToolExecutor 构造执行器；timeout<=0 取 30s，maxResult<=0 取 8KB。
func NewRuntimeToolExecutor(timeout time.Duration, maxResult int) *RuntimeToolExecutor {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if maxResult <= 0 {
		maxResult = 8 * 1024
	}
	return &RuntimeToolExecutor{timeout: timeout, maxResult: maxResult}
}

// Execute 执行一次调用。策略拒绝/工具缺失/panic/超时都返回结构化结果。
func (e *RuntimeToolExecutor) Execute(ctx context.Context, call RuntimeToolCall, reg *RuntimeToolRegistry, pol RuntimePolicy) (res RuntimeToolResult) {
	res = RuntimeToolResult{CallID: call.ID, Name: call.Name}
	spec, ok := reg.Get(call.Name)
	if !ok {
		res.IsError = true
		res.Content = "unknown tool: " + call.Name
		return res
	}
	if pol == nil {
		pol = RuntimeReadOnlyPolicy{}
	}
	allowed, reason := safeAuthorize(ctx, pol, spec, call.Arguments)
	if !allowed {
		res.Denied = true
		res.IsError = true
		res.Reason = reason
		if res.Reason == "" {
			res.Reason = "policy denied"
		}
		res.Content = res.Reason
		return res
	}
	runCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	content, err := safeToolExecute(runCtx, spec, call.Arguments)
	if err != nil {
		res.IsError = true
		res.Content = err.Error()
		return res
	}
	res.Content = truncateRuntimeResult(content, e.maxResult)
	return res
}

func safeAuthorize(ctx context.Context, pol RuntimePolicy, spec RuntimeToolSpec, args json.RawMessage) (allowed bool, reason string) {
	defer func() {
		if r := recover(); r != nil {
			allowed, reason = false, fmt.Sprintf("policy panic: %v", r)
		}
	}()
	return pol.Authorize(ctx, spec, args)
}

func safeToolExecute(ctx context.Context, spec RuntimeToolSpec, args json.RawMessage) (content string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s panic: %v", spec.Name, r)
		}
	}()
	return spec.Execute(ctx, args)
}

func truncateRuntimeResult(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n...[truncated]"
}
