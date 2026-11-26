// runtime_session.go 运维 agent 的 append-only session 日志（ADR-029 D4）。
//
// 每个事件一行 JSON，追加后 flush+fsync。重开时：
//   - 忽略最后一行不完整 JSON（崩溃只可能损坏尾行，不能让整个会话不可读）；
//   - 已开未闭的 turn 补 turn/end{outcome:"interrupted"}（同 DSH interruptedTurnClosers）。
//
// 这里不做模型/工具，只负责持久化与消息投影。
package opsagent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var runtimeSessionIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RuntimeSession 是一个可读写的 session 文件。
type RuntimeSession struct {
	dir  string
	id   string
	path string

	mu     sync.Mutex
	file   *os.File
	events []RuntimeEvent
	seq    int64
	turns  int
	closed bool
}

// CreateRuntimeSession 新建会话；id 为空时生成时间戳 ID。
func CreateRuntimeSession(dir, id string, now time.Time) (*RuntimeSession, error) {
	if dir == "" {
		return nil, errors.New("opsagent runtime: session dir 为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("opsagent runtime: mkdir session dir: %w", err)
	}
	if id == "" {
		id = fmt.Sprintf("ops-%s-%06x", now.Format("20060102-150405"), now.UnixNano()&0xffffff)
	}
	if !runtimeSessionIDRe.MatchString(id) {
		return nil, fmt.Errorf("opsagent runtime: 非法 session id %q", id)
	}
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opsagent runtime: create session %s: %w", path, err)
	}
	s := &RuntimeSession{dir: dir, id: id, path: path, file: f}
	if err := s.Append(RuntimeEventSessionCreated, 0, 0, runtimeSessionCreated{
		ID: id, StartedAt: now.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		// 忽略：创建 session 失败后的清理关闭，主错误已返回。
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

// OpenRuntimeSession 重开已有会话并修复中断的 turn。
func OpenRuntimeSession(dir, id string) (*RuntimeSession, error) {
	if !runtimeSessionIDRe.MatchString(id) {
		return nil, fmt.Errorf("opsagent runtime: 非法 session id %q", id)
	}
	path := filepath.Join(dir, id+".jsonl")
	events, err := readRuntimeEvents(path)
	if err != nil {
		return nil, err
	}
	if len(events) > 0 && events[len(events)-1].Type == RuntimeEventSessionDisposed {
		return nil, fmt.Errorf("opsagent runtime: session %s 已 disposed，不能重开", id)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opsagent runtime: open session %s: %w", path, err)
	}
	s := &RuntimeSession{dir: dir, id: id, path: path, file: f, events: events}
	if len(events) > 0 {
		s.seq = events[len(events)-1].Seq
	}
	for _, ev := range events {
		if ev.Type == RuntimeEventTurnStart {
			s.turns++
		}
	}
	if err := s.closeInterruptedTurns(); err != nil {
		// 忽略：重开会话失败后的清理关闭，主错误已返回。
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

// readRuntimeEvents 读取 JSONL；只允许最后一行不完整/损坏。
func readRuntimeEvents(path string) ([]RuntimeEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opsagent runtime: read session %s: %w", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			lines = append(lines, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("opsagent runtime: scan session %s: %w", path, err)
	}
	events := make([]RuntimeEvent, 0, len(lines))
	for i, line := range lines {
		var ev RuntimeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			if i == len(lines)-1 {
				break // 崩溃留下的半行：丢弃，不把整个会话判死。
			}
			return nil, fmt.Errorf("opsagent runtime: session %s 第 %d 行损坏: %w", path, i+1, err)
		}
		if ev.Seq <= 0 || ev.Type == "" {
			return nil, fmt.Errorf("opsagent runtime: session %s 第 %d 行缺 seq/type", path, i+1)
		}
		if len(events) > 0 && ev.Seq != events[len(events)-1].Seq+1 {
			return nil, fmt.Errorf("opsagent runtime: session %s seq 不连续：%d -> %d", path, events[len(events)-1].Seq, ev.Seq)
		}
		events = append(events, ev)
	}
	return events, nil
}

// closeInterruptedTurns 为已开未闭的 turn 补终态。
func (s *RuntimeSession) closeInterruptedTurns() error {
	open := map[int]bool{}
	for _, ev := range s.events {
		switch ev.Type {
		case RuntimeEventTurnStart:
			open[ev.Turn] = true
		case RuntimeEventTurnEnd:
			delete(open, ev.Turn)
		}
	}
	turns := make([]int, 0, len(open))
	for t := range open {
		turns = append(turns, t)
	}
	sort.Ints(turns)
	for _, t := range turns {
		if err := s.Append(RuntimeEventTurnEnd, t, 0, runtimeTurnEnd{
			Outcome: RuntimeOutcomeInterrupted,
			Error:   "进程在 turn 完成前退出（重开时补终态）",
		}); err != nil {
			return err
		}
	}
	return nil
}

// Append 追加一个事件。调用方不需要持锁。
func (s *RuntimeSession) Append(typ string, turn, step int, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(typ, turn, step, data)
}

func (s *RuntimeSession) appendLocked(typ string, turn, step int, data any) error {
	if s.closed {
		return errors.New("opsagent runtime: session 已关闭")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("opsagent runtime: marshal %s: %w", typ, err)
	}
	s.seq++
	ev := RuntimeEvent{
		Seq: s.seq, Time: time.Now().UTC().Format(time.RFC3339Nano),
		Type: typ, Turn: turn, Step: step, Data: raw,
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("opsagent runtime: marshal event: %w", err)
	}
	line = append(line, '\n')
	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("opsagent runtime: append %s: %w", typ, err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("opsagent runtime: fsync %s: %w", typ, err)
	}
	s.events = append(s.events, ev)
	return nil
}

// NextTurn 返回下一个 turn 编号。
func (s *RuntimeSession) NextTurn() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.turns + 1
	s.turns = next
	return next
}

// ID 返回会话 ID。
func (s *RuntimeSession) ID() string { return s.id }

// Path 返回会话文件路径。
func (s *RuntimeSession) Path() string { return s.path }

// Events 返回事件副本。
func (s *RuntimeSession) Events() []RuntimeEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RuntimeEvent, len(s.events))
	copy(out, s.events)
	return out
}

// Messages 投影模型可见消息。最近一次 compaction 的 Kept 作为基座，
// 只追加 compaction 之后的消息事件；更早历史被 shadow。
func (s *RuntimeSession) Messages() []RuntimeMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var base []RuntimeMessage
	start := int64(0)
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].Type != RuntimeEventCompaction {
			continue
		}
		var d runtimeCompactionData
		if err := json.Unmarshal(s.events[i].Data, &d); err == nil {
			base = cloneRuntimeMessages(d.Kept)
			start = s.events[i].Seq
		}
		break
	}
	out := cloneRuntimeMessages(base)
	for _, ev := range s.events {
		if ev.Seq <= start {
			continue
		}
		switch ev.Type {
		case RuntimeEventUserMessage:
			var d runtimeUserMessage
			if json.Unmarshal(ev.Data, &d) == nil {
				out = append(out, RuntimeMessage{Role: "user", Content: d.Text})
			}
		case RuntimeEventAssistantMessage:
			var d runtimeAssistantMessage
			if json.Unmarshal(ev.Data, &d) == nil {
				out = append(out, RuntimeMessage{Role: "assistant", Content: d.Content, ToolCalls: d.ToolCalls})
			}
		case RuntimeEventToolResult:
			var d runtimeToolResultData
			if json.Unmarshal(ev.Data, &d) == nil {
				content := d.Content
				if d.IsError {
					content = "Error: " + content
				}
				out = append(out, RuntimeMessage{Role: "tool", Content: content, ToolCallID: d.CallID, Name: d.Name})
			}
		case RuntimeEventToolDenied:
			var d runtimeToolResultData
			if json.Unmarshal(ev.Data, &d) == nil {
				out = append(out, RuntimeMessage{Role: "tool", Content: "Denied: " + d.Reason, ToolCallID: d.CallID, Name: d.Name})
			}
		}
	}
	return out
}

type runtimeUserMessage struct {
	Text string `json:"text"`
}

func cloneRuntimeMessages(in []RuntimeMessage) []RuntimeMessage {
	out := make([]RuntimeMessage, len(in))
	for i := range in {
		out[i] = in[i]
		if len(in[i].ToolCalls) > 0 {
			out[i].ToolCalls = append([]RuntimeToolCall(nil), in[i].ToolCalls...)
		}
	}
	return out
}

// Close 追加 session/disposed 并关闭文件（幂等）。
func (s *RuntimeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	for _, t := range s.openTurnsLocked() {
		// 忽略：Close 补终态是 best-effort，失败也要继续写 disposed 并关文件。
		_ = s.appendLocked(RuntimeEventTurnEnd, t, 0, runtimeTurnEnd{
			Outcome: RuntimeOutcomeInterrupted,
			Error:   "session 关闭时 turn 未完成",
		})
	}
	// 忽略：Close 写 disposed 是 best-effort，主错误是文件关闭。
	_ = s.appendLocked(RuntimeEventSessionDisposed, 0, 0, map[string]string{})
	s.closed = true
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}

func (s *RuntimeSession) openTurnsLocked() []int {
	open := map[int]bool{}
	for _, ev := range s.events {
		switch ev.Type {
		case RuntimeEventTurnStart:
			open[ev.Turn] = true
		case RuntimeEventTurnEnd:
			delete(open, ev.Turn)
		}
	}
	turns := make([]int, 0, len(open))
	for t := range open {
		turns = append(turns, t)
	}
	sort.Ints(turns)
	return turns
}

// ListRuntimeSessions 列出会话文件摘要（按 mtime 倒序，limit<=0 取 20）。
func ListRuntimeSessions(dir string, limit int) ([]RuntimeSessionSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RuntimeSessionSummary
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(ent.Name(), ".jsonl")
		if !runtimeSessionIDRe.MatchString(id) {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		sum := RuntimeSessionSummary{ID: id, Size: info.Size(), UpdatedAt: info.ModTime().UTC().Format(time.RFC3339Nano)}
		if evs, err := readRuntimeEvents(filepath.Join(dir, ent.Name())); err == nil {
			sum.Events = len(evs)
			for _, ev := range evs {
				switch ev.Type {
				case RuntimeEventSessionCreated:
					var d runtimeSessionCreated
					if json.Unmarshal(ev.Data, &d) == nil {
						sum.StartedAt = d.StartedAt
					}
				case RuntimeEventTurnStart:
					sum.Turns++
				case RuntimeEventTurnEnd:
					var d runtimeTurnEnd
					if json.Unmarshal(ev.Data, &d) == nil {
						sum.LastOutcome = d.Outcome
					}
				}
			}
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
