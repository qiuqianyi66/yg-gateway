package upstream

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamHintHeadersBeforeFirstFrame 首帧前就把 200 + SSE 响应头刷给客户端
// （F-704）。
//
// 牙齿：把 WriteHeader(http.StatusOK) + Flush 那两行删掉 → 本测试 FAIL
// （此前响应头是惰性的，只有第一次 Write 才连同 200 刷出去）。
func TestStreamHintHeadersBeforeFirstFrame(t *testing.T) {
	// 上游「迟迟不吐首帧」：用一个阻塞 reader，验证在没有任何 data 帧的情况下
	// 响应头已经提交。httptest.ResponseRecorder 的 Flush 会把 Code 定格，
	// 因此这里能直接断言 Code/Header。
	rec := httptest.NewRecorder()
	gate := make(chan struct{})
	r := &blockingReader{gate: gate}
	done := make(chan error, 1)
	go func() { done <- StreamHint(rec, r, nil) }()
	// 等一小会儿让 StreamHint 走到 header 提交点（不该依赖首帧）。
	time.Sleep(50 * time.Millisecond)
	if rec.Code != 200 {
		t.Errorf("Code=%d want 200 before first frame", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type=%q want text/event-stream", ct)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body=%q want empty before first frame", rec.Body.String())
	}
	// 放行后流应能收尾（空流 → errEmptyStream，这是既有约定，不是失败）。
	close(gate)
	if err := <-done; !IsEmptyStreamError(err) {
		t.Fatalf("StreamHint err=%v want errEmptyStream", err)
	}
	if !strings.Contains(rec.Body.String(), "data: [DONE]") {
		t.Errorf("missing [DONE]: %q", rec.Body.String())
	}
}

// blockingReader 阻塞到 gate 关闭，然后返回 EOF（模拟「上游迟迟不吐首帧、
// 最后干净收流」）。返回错误会让调用方把流判成失败，测不到 header 先行路径。
type blockingReader struct {
	gate chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	<-b.gate
	return 0, io.EOF
}

// TestStreamHintFrameTooLarge 单帧超上限 → errFrameTooLarge（F-705）。
//
// 牙齿：把 readSSELine 的 cap 判据去掉（或改回 ReadString）→ 本测试 FAIL
// （此前会把整行读进内存，不报错）。
func TestStreamHintFrameTooLarge(t *testing.T) {
	// 构造一个超过 1MiB 的单行 data 帧。
	huge := strings.Repeat("a", maxSSEFrameBytes+1024)
	rec := httptest.NewRecorder()
	err := StreamHint(rec, strings.NewReader("data: {\"content\":\""+huge+"\"}\n\n"), nil)
	if !IsFrameTooLarge(err) {
		t.Fatalf("err=%v want errFrameTooLarge", err)
	}
}

// TestAggregateFrameTooLarge 非流式聚合同样受单帧上限约束。
func TestAggregateFrameTooLarge(t *testing.T) {
	huge := strings.Repeat("a", maxSSEFrameBytes+1024)
	_, err := Aggregate(strings.NewReader("data: {\"content\":\"" + huge + "\"}\n\n"))
	if !IsFrameTooLarge(err) {
		t.Fatalf("err=%v want errFrameTooLarge", err)
	}
}

// TestAggregateTotalTooLarge 聚合总量超上限 → errFrameTooLarge。
// 用多个合法小帧累计超过 maxAggregateBytes。
func TestAggregateTotalTooLarge(t *testing.T) {
	const per = 1024
	chunk := strings.Repeat("b", per-64)
	frames := maxAggregateBytes/(per-64) + 16
	var sb strings.Builder
	for i := 0; i < frames; i++ {
		sb.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"" + chunk + "\"}}]}\n\n")
	}
	_, err := Aggregate(strings.NewReader(sb.String()))
	if !IsFrameTooLarge(err) {
		t.Fatalf("err=%v want errFrameTooLarge", err)
	}
}

// TestStreamHintKeepAlivePings 开启心跳后，静默流中会收到 `: ping` 注释帧（F-706）。
//
// 牙齿：把心跳 goroutine 去掉 → 本测试 FAIL（等不到 ping）。
func TestStreamHintKeepAlivePings(t *testing.T) {
	rec := httptest.NewRecorder()
	gate := make(chan struct{})
	r := &blockingReader{gate: gate}
	done := make(chan error, 1)
	go func() { done <- StreamHint(rec, r, nil, WithKeepAlive(20*time.Millisecond)) }()
	// 等到出现 ping（最多等 1s）。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rec.Body.String(), ": ping") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(gate)
	if err := <-done; !IsEmptyStreamError(err) {
		t.Fatalf("StreamHint err=%v want errEmptyStream", err)
	}
	if !strings.Contains(rec.Body.String(), ": ping") {
		t.Fatalf("no ping frame in body: %q", rec.Body.String())
	}
	// ping 必须是 SSE 注释行（冒号开头），标准客户端会忽略它。
	for _, ln := range strings.Split(strings.TrimRight(rec.Body.String(), "\n"), "\n") {
		if ln == ": ping" || ln == "data: [DONE]" || strings.HasPrefix(ln, "data: ") {
			continue
		}
		if ln != "" {
			t.Errorf("unexpected line: %q", ln)
		}
	}
}

// TestStreamHintDefaultNoKeepAlive 默认（不传 WithKeepAlive）不产生任何注释帧——
// 既有字节流行为零回归。
func TestStreamHintDefaultNoKeepAlive(t *testing.T) {
	rec := httptest.NewRecorder()
	raw := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("err=%v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, ": ping") {
		t.Errorf("default path must not emit keepalive frames: %q", body)
	}
	for _, ln := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if ln != "" && !strings.HasPrefix(ln, "data: ") {
			t.Errorf("bad line: %q", ln)
		}
	}
}
