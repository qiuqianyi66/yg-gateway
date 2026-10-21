package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// taskMetrics 是 F5 度量基线的会话级采集器（REQ-f5-metric-counter-2026-10-20）。
//
// 职责：把上游响应里的 usage（prompt_tokens / total_tokens）按会话 ID 聚合成
// 「首调用上下文」与「一次任务的总 token」，把原本记成 unknown 的两项成本字段
// 变成 observation，供 /status 透出、供 agent-eval 账本回填。
//
// 设计约束（沿用 AGENTS.md 红线）：
//   - 采集点只挂在响应透传处（R6：出站逻辑集中 internal/upstream），不散点拼。
//   - 纯内存、按会话 ID 做 key、带 TTL 防泄漏；不落库、不做告警（REQ §三/§五）。
//   - 同步累加，无每请求 goroutine；仅一个后台 TTL 清理 goroutine，且提供 Stop()
//     回收路径（R9：goroutine 必须有回收路径）。
//   - provider 不返回 usage 时本采集器不被调用 → /status 对应字段省略（unknown），
//     不记 0、不报错退出（R10：缺失是观测缺口，不是业务错误）。
type taskMetrics struct {
	mu       sync.Mutex
	entries  map[string]*taskMetricEntry
	lastConv string
	ttl      time.Duration
	stop     chan struct{}
	once     sync.Once
}

type taskMetricEntry struct {
	firstCallPrompt float64   // 该会话首轮响应的 prompt_tokens（首调用上下文）
	firstCallSet    bool      // 是否已观测到首轮 prompt_tokens（未观测=unknown，不输出）
	totalTokens     float64   // 该会话各轮 total_tokens 之和
	turns           int64     // 观测到 usage 的轮次数
	lastSeen        time.Time // 末次观测时刻（TTL 清理用）
}

// TaskMetricsView 是 /status 消费的快照视图（字段省略语义由调用方按 Known 决定）。
type TaskMetricsView struct {
	HasData               bool
	Tasks                 int     // 观测到的会话数
	FirstCallKnown        bool    // 最近会话的首调用上下文是否已观测（false=省略该字段）
	FirstCallPromptTokens float64 // 最近会话的首轮 prompt_tokens（observation）
	TaskTotalTokens       float64 // 最近会话各轮 total_tokens 之和（observation）
	TaskTurns             int64   // 最近会话的轮次数
}

// TaskMetrics 是进程级单例；测试可调用 Reset 清空，Snapshot/Record 为导出方法。
var TaskMetrics = newTaskMetrics()

func newTaskMetrics() *taskMetrics {
	tm := &taskMetrics{
		entries: make(map[string]*taskMetricEntry),
		ttl:     1 * time.Hour, // REQ §六 待决议：TTL 取 1h，覆盖一次任务 + 余量
	}
	tm.start()
	return tm
}

// start 启动后台 TTL 清理 goroutine（仅一次）。回收路径见 Stop。
func (tm *taskMetrics) start() {
	tm.once.Do(func() {
		tm.stop = make(chan struct{})
		go tm.cleanupLoop()
	})
}

// Stop 回收后台清理 goroutine（R9 回收路径）。运维关停时调用；不调用也不泄漏——
// 只是进程级常驻单例，随进程退出。
func (tm *taskMetrics) Stop() {
	tm.mu.Lock()
	stop := tm.stop
	tm.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func (tm *taskMetrics) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-tm.stop:
			return
		case <-ticker.C:
			tm.purgeExpired()
		}
	}
}

func (tm *taskMetrics) purgeExpired() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	now := time.Now()
	for id, e := range tm.entries {
		if now.Sub(e.lastSeen) > tm.ttl {
			delete(tm.entries, id)
		}
	}
	if tm.lastConv != "" {
		if e, ok := tm.entries[tm.lastConv]; !ok || now.Sub(e.lastSeen) > tm.ttl {
			tm.lastConv = ""
		}
	}
}

// Record 在观察到一次响应 usage 时调用（每轮至多一次，取该轮最终 usage）。
// 首轮设置首调用上下文；后续轮累加到 totalTokens；turns 计观测轮次。
// convID 为空时调用方应跳过（无法归属会话）；此处再做一次防御。
func (tm *taskMetrics) Record(convID string, usage map[string]any) {
	if convID == "" || usage == nil {
		return
	}
	pt, hasPT := usageNumber(usage, "prompt_tokens")
	tt, hasTT := usageNumber(usage, "total_tokens")

	tm.mu.Lock()
	defer tm.mu.Unlock()
	e, ok := tm.entries[convID]
	if !ok {
		e = &taskMetricEntry{}
		tm.entries[convID] = e
	}
	if !e.firstCallSet && hasPT {
		e.firstCallPrompt = pt
		e.firstCallSet = true
	}
	if hasTT {
		e.totalTokens += tt
	}
	e.turns++
	e.lastSeen = time.Now()
	tm.lastConv = convID
}

// Snapshot 返回最近一次观测会话的聚合视图。HasData=false 时调用方应省略整个
// task_metrics 段（unknown，不输出 0、不输出 false）。
func (tm *taskMetrics) Snapshot() (TaskMetricsView, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	v := TaskMetricsView{HasData: len(tm.entries) > 0, Tasks: len(tm.entries)}
	if tm.lastConv != "" {
		if e, ok := tm.entries[tm.lastConv]; ok {
			v.FirstCallKnown = e.firstCallSet
			if e.firstCallSet {
				v.FirstCallPromptTokens = e.firstCallPrompt
			}
			v.TaskTotalTokens = e.totalTokens
			v.TaskTurns = e.turns
		}
	}
	return v, v.HasData
}

// Reset 清空所有会话数据（测试隔离用；不影响后台清理 goroutine）。
func (tm *taskMetrics) Reset() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.entries = make(map[string]*taskMetricEntry)
	tm.lastConv = ""
}

// observeUsage 包装上游返回的流：透传字节的同时，扫描 SSE data: 行（或单条 JSON
// 响应）中的 usage，在流关闭时把最终 usage 记进 TaskMetrics。
//
// 只观测、不改写流；convID 为空时直接透传（记 unknown，不归属）。每流仅记录一次
// （取末次 usage），避免增量 usage 多帧导致 total_tokens 重复累加。
func observeUsage(rc io.ReadCloser, convID string) io.ReadCloser {
	if convID == "" {
		return rc
	}
	return &usageObservingReader{inner: rc, convID: convID}
}

type usageObservingReader struct {
	inner     io.ReadCloser
	convID    string
	buf       []byte
	lastUsage map[string]any
}

func (r *usageObservingReader) Read(p []byte) (int, error) {
	n, err := r.inner.Read(p)
	if n > 0 {
		r.scan(p[:n])
	}
	return n, err
}

func (r *usageObservingReader) scan(chunk []byte) {
	r.buf = append(r.buf, chunk...)
	for {
		idx := bytes.IndexByte(r.buf, '\n')
		if idx < 0 {
			break
		}
		line := r.buf[:idx]
		r.buf = r.buf[idx+1:]
		r.processLine(line)
	}
}

func (r *usageObservingReader) processLine(line []byte) {
	s := strings.TrimSpace(string(line))
	if s == "" {
		return
	}
	if strings.HasPrefix(s, "data:") {
		payload := strings.TrimSpace(s[len("data:"):])
		if payload == "[DONE]" {
			return
		}
		if u := extractUsage(payload); u != nil {
			r.lastUsage = u
		}
		return
	}
	// 非 SSE 单条 JSON 响应（上游未走 SSE 时）的兜底：整行即一个 JSON 对象。
	if len(s) > 0 && s[0] == '{' {
		if u := extractUsage(s); u != nil {
			r.lastUsage = u
		}
	}
}

func (r *usageObservingReader) Close() error {
	if len(r.buf) > 0 {
		r.processLine(r.buf)
		r.buf = nil
	}
	if r.lastUsage != nil {
		TaskMetrics.Record(r.convID, r.lastUsage)
		r.lastUsage = nil // 幂等：代理/传输层可能重复 Close，重复计数会让 total_tokens 翻倍
	}
	return r.inner.Close()
}

// extractUsage 从一条 JSON 文本里取出 usage 对象；非 JSON 或不含 usage 时返回 nil。
func extractUsage(payload string) map[string]any {
	if payload == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return nil
	}
	u, ok := m["usage"].(map[string]any)
	if !ok {
		return nil
	}
	return u
}
