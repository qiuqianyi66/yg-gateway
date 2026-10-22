package upstream

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 文件说明（2026-10-16 修订）：两个「流式进行中就想检查输出」的测试改用**真实 httptest.Server**。
//
// 起因：CI 的 -race 首次跑出 data race——旧写法把 httptest.ResponseRecorder 直接喂给 StreamHint，
// 然后在测试 goroutine 里读 `rec.Code` / `rec.Header()` / `rec.Body.String()`，而 StreamHint
// 仍在另一个 goroutine 里写同一份状态（`h.Set(...)` 在 sse.go:625、`WriteHeader` 在 sse.go:640）。
// `httptest.ResponseRecorder` 只对**自己的方法**加锁，外部直读字段不受保护，
// 所以这是**测试写法**的问题（生产路径上 http.ResponseWriter 只被处理该请求的 goroutine 写）。
//
// 改走真 HTTP 之后：
//   - 服务端只有 handler goroutine 写 ResponseWriter，客户端只有测试 goroutine 读 body，
//     两侧天然不共享内存 → 无竞态；
//   - 顺带把 F-704 要保证的事验得更实：**Flusher 真把首帧前的响应头刷给了客户端**
//     （旧写法只验了 recorder 里的字段，那不代表字节真的出网了）。
//
// 读取一律带超时：行为回归时测试**失败**而不是挂死（挂死会被 go test 的 10 分钟 panic 兜住，
// 但定位价值远低于一条明确的断言消息）。

// newStreamHintServer 起一个真实 HTTP 服务，把 StreamHint 放在 handler 里跑，并把它的返回值
// 通过带缓冲的 channel 交回测试（缓冲 1 → 即使测试不读也不会泄漏 goroutine）。
func newStreamHintServer(t *testing.T, r io.Reader, opts ...StreamOption) (*httptest.Server, <-chan error) {
	t.Helper()
	errCh := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		errCh <- StreamHint(w, r, nil, opts...)
	}))
	t.Cleanup(srv.Close)
	return srv, errCh
}

// getWithDeadline 发一个 GET 并在超时前取回响应（= 响应头已到客户端）。
// 超时说明「首帧前提交响应头」的行为回归了：此时 GET 会一直阻塞到第一段 body 才返回。
func getWithDeadline(t *testing.T, url string, d time.Duration) *http.Response {
	t.Helper()
	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := http.Get(url)
		ch <- result{resp, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("GET %s: %v", url, r.err)
		}
		return r.resp
	case <-time.After(d):
		t.Fatalf("GET %s 在 %v 内没拿到响应头 —— 「首帧前提交 200 + SSE 头」的行为回归了", url, d)
		return nil
	}
}

// releaseGate 返回一个「只关一次」的放行函数。
//
// 为什么必须保证一定放行：blockingReader 不放行 → StreamHint 永不返回 → handler 卡住 →
// `httptest.Server.Close()`（经 t.Cleanup）会一直等这个未完成的请求 → **测试挂死**（10 分钟 panic 兜底）
// 而不是快速失败。行为回归时我们要的是后者，所以放行放在 defer 里、并用 Once 防重复关闭。
func releaseGate(gate chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

// TestStreamHintHeadersBeforeFirstFrame 首帧前就把 200 + SSE 响应头刷给客户端（F-704）。
//
// 牙齿：把 `WriteHeader(http.StatusOK)` + `Flush` 那两行删掉 → 本测试 FAIL
// （`getWithDeadline` 会超时，因为响应头要等第一段 body 才发出）。
func TestStreamHintHeadersBeforeFirstFrame(t *testing.T) {
	gate := make(chan struct{})
	release := releaseGate(gate)
	defer release() // 失败路径也必须放行，否则 srv.Close() 挂死（见 releaseGate 注释）

	srv, errCh := newStreamHintServer(t, &blockingReader{gate: gate})

	resp := getWithDeadline(t, srv.URL, 2*time.Second)
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status=%d want 200 before first frame", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type=%q want text/event-stream", ct)
	}

	// 放行后流应能收尾（空流 → errEmptyStream，这是既有约定，不是失败）。
	release()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if e := <-errCh; !IsEmptyStreamError(e) {
		t.Fatalf("StreamHint err=%v want errEmptyStream", e)
	}
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Errorf("missing [DONE]: %q", body)
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

// TestAggregateReasoningAndArgsCountedInBudget P2-2 补丁：聚合上限必须同时覆盖
// reasoning_content 与 tool_calls arguments——此前只量 content.Len()，一个不吐
// content 的纯推理畸形流可以把网关内存吃到 16MiB 以上不报警。
//
// 牙齿：把 trackedAggregateBytes 改回只量 content → 本测试 FAIL（流被读完不报错）。
func TestAggregateReasoningAndArgsCountedInBudget(t *testing.T) {
	const per = 4096
	chunk := strings.Repeat("r", per-96) // 每帧 reasoning_content 约 4KB
	frames := maxAggregateBytes/(per-96) + 16
	var sb strings.Builder
	for i := 0; i < frames; i++ {
		sb.WriteString("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"" + chunk + "\"}}]}\n\n")
	}
	_, err := Aggregate(strings.NewReader(sb.String()))
	if !IsFrameTooLarge(err) {
		t.Fatalf("纯 reasoning 流超上限应报 errFrameTooLarge, got %v", err)
	}
}

// TestStreamHintKeepAlivePings 开启心跳后，静默流中会收到 `: ping` 注释帧（F-706）。
//
// 牙齿：把心跳 goroutine 去掉 → 本测试 FAIL（等不到 ping）。
func TestStreamHintKeepAlivePings(t *testing.T) {
	gate := make(chan struct{})
	release := releaseGate(gate)
	defer release() // 失败路径也必须放行，否则 srv.Close() 挂死（见 releaseGate 注释）

	srv, errCh := newStreamHintServer(t, &blockingReader{gate: gate}, WithKeepAlive(20*time.Millisecond))

	resp := getWithDeadline(t, srv.URL, 2*time.Second)
	defer resp.Body.Close()

	// 读线程只做「逐行读并投递」，判定在主 goroutine；缓冲给足，避免服务端写被读端拖住。
	lines := make(chan string, 256)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			ln, err := br.ReadString('\n')
			if ln != "" {
				lines <- strings.TrimRight(ln, "\r\n")
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	sawPing := false
	deadline := time.After(time.Second)
waitPing:
	for {
		select {
		case ln, ok := <-lines:
			if !ok {
				break waitPing
			}
			if ln == ": ping" {
				sawPing = true
				break waitPing
			}
		case <-deadline:
			break waitPing
		}
	}
	if !sawPing {
		t.Fatalf("1 秒内没有收到 `: ping` 注释帧（心跳行为回归）")
	}

	// 放行并收尾：读到 EOF，收集全部行。
	release()
	var all []string
	for ln := range lines {
		all = append(all, ln)
	}
	if e := <-errCh; !IsEmptyStreamError(e) {
		t.Fatalf("StreamHint err=%v want errEmptyStream", e)
	}

	var hasDone bool
	for _, ln := range all {
		switch {
		case ln == ": ping":
			// 等待阶段之后可能还有 ping（本循环只是收尾，不再重复判定心跳存在性）。
		case ln == "data: [DONE]":
			hasDone = true
		case ln == "" || strings.HasPrefix(ln, "data: "):
			// 正常帧
		default:
			t.Errorf("unexpected line: %q", ln)
		}
	}
	// 心跳存在性由**等待阶段**的 sawPing 判定：那条 `: ping` 已被等待循环消费，
	// 收尾集合里自然不再有它（第一版实现重复判定了一次，导致假失败）。
	if !hasDone {
		t.Errorf("流里没有收尾帧 [DONE]：%v", all)
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
