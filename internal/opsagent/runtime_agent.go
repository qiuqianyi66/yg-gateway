// runtime_agent.go 调度与生命周期：定时巡检 + followup inbox + 取消。
//
// DSH 的 handle 语义这里只落 MVP 需要的部分：followup / cancel / whenIdle /
// 周期运行。steer/inject 的完整语义留给后续阶段，不在这里假装已实现。
package opsagent

import (
	"context"
	"time"
)

// Run 周期巡检；ctx 取消即退出（R9 回收路径）。
func (r *Runtime) Run(ctx context.Context) {
	interval := r.cfg.Interval
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	logRuntime("已启动：interval=%s model=%s session=%s", interval, r.cfg.Model, r.session.ID())
	for {
		select {
		case <-ctx.Done():
			logRuntime("停止：%v", ctx.Err())
			return
		case <-ticker.C:
			r.runOne(ctx, defaultRuntimePrompt)
		case prompt := <-r.inbox:
			if !r.runOne(ctx, prompt) {
				// 忙时刚消费的 followup 必须回队，否则用户输入被静默丢弃；
				// 队列满只能丢弃并留日志（不阻塞调度器）。
				select {
				case r.inbox <- prompt:
				default:
					logRuntime("丢弃 followup：inbox 已满")
				}
			}
		}
	}
}

// runOne 执行一轮；返回 false 表示因上一轮在跑而跳过（调用方决定是否回队）。
func (r *Runtime) runOne(ctx context.Context, prompt string) bool {
	r.mu.Lock()
	busy := r.busy
	r.mu.Unlock()
	if busy {
		logRuntime("跳过本轮：上一轮仍在跑")
		return false
	}
	res, err := r.Turn(ctx, prompt)
	if err != nil {
		logRuntime("turn=%d outcome=%s err=%v", resTurn(res), resOutcome(res), err)
		return true
	}
	logRuntime("turn=%d outcome=%s steps=%d tools=%d", res.Turn, res.Outcome, res.Steps, res.ToolCalls)
	return true
}

func resTurn(res *TurnResult) int {
	if res == nil {
		return 0
	}
	return res.Turn
}

func resOutcome(res *TurnResult) string {
	if res == nil {
		return "nil"
	}
	return res.Outcome
}
