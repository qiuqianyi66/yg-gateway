// stream.go 上游 SSE → 客户端的流式透传（Stream / StreamHint 家族）。
//
// 拆分来由（2026-11-15）：`StreamHint` 原是一个 260 代码行的函数，7 个跨帧状态
// （toolCallSeen / firstID / synthIdx / maxToolIdx / toolFinishSent / lastBeat /
// writeMu）与 7 个闭包共处一个作用域。闭包之间靠「变量在不在作用域里」隐式耦合：
// 读循环直接读写修复计数，心跳 goroutine 绕过 writeThrough 自己内联写。上一轮正是在
// 这里查出 `[DONE]` 兜底写没加锁的真竞态——那是「状态所有权不可见」的直接后果。
//
// 现在状态是结构体字段，"谁可以改什么"由方法边界表达；`StreamHint` 退化成
// 「装配 → 起心跳 → 读循环 → 收尾」四步编排。
//
// 报文内容零改动（R8）：本文件只是同一套逻辑的所有权重排，出站字节流逐字节不变
// （sse_test.go / coverage_parse_test.go / r8 金样全绿即证据）。
package upstream

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stream 透传上游 SSE 到 w（逐帧规范化后 flush），保证至少写一个 [DONE]。
// 调用方必须先设置过 status 200；本函数自设 SSE headers。
// 流式策略：逐帧透传（规范化已剥空 content 噪声），恢复与上游一致的平滑流式。
//
// StreamHint 变体（gateway_hint 任务）：上游 error 帧透传时附加
// error.gateway_hint 字段——message 原文不动，hint 并列补充；hintFn 返回空串
// 或 nil 时与 Stream 行为逐字节一致。
func Stream(w http.ResponseWriter, r io.Reader) error {
	return StreamHint(w, r, nil)
}

// StreamOption StreamHint 的可选行为开关。
// WithErrorFrameObserver 只做旁路观测、不改透传字节；WithMarkupRepair 会改写出站
// 帧（把正文里的原生工具调用标记还原成 delta.tool_calls，见 dsml.go）；
// WithKeepAlive 会在流静默超阈值时写 SSE 注释帧保活（默认关，零回归）。
type StreamOption func(*streamOptions)

type streamOptions struct {
	onErrorFrame func(payload string)
	markup       *MarkupRepair
	keepAlive    time.Duration
}

// WithMarkupRepair 注册「模型原生工具调用标记」修复器（见 dsml.go）。
//
// 上游在没有 tools 的请求里会把工具调用吐成标记文本，Codex 这类只执行结构化
// function_call 的客户端于是把它当普通正文落进会话历史，回合直接结束、工具一次
// 都没跑。挂上修复器后，这类标记在透传前被还原成 delta.tool_calls。
//
// 传 nil、或传入未启用的修复器（客户端没声明过 tools）→ 与 Stream 行为逐字节一致。
func WithMarkupRepair(r *MarkupRepair) StreamOption {
	return func(o *streamOptions) { o.markup = r }
}

// WithErrorFrameObserver 注册"上游以 error 帧报错"的观察者：透传该帧之前先用
// payload（原始 data 内容）回调一次。给调用方一个**账号处置**的挂载点——
// 上游「200 + error 帧」是真实形态（6004 限流、内容拦截、审核），此前网关在读
// 第一帧之前就把账号记成功，限流号被当成健康号；有观察者后可在流尾按帧分类处置。
func WithErrorFrameObserver(fn func(payload string)) StreamOption {
	return func(o *streamOptions) { o.onErrorFrame = fn }
}

// WithKeepAlive 开启 SSE 心跳：流中静默超过 d 时向客户端写 `: ping\n\n` 注释帧
// （SSE 规范的注释行，标准客户端按规范忽略），防止中间层（nginx / LB / 代理）
// 按空闲掐断长静默流——典型场景是长思考链模型首字节前要等几十秒。
//
// 默认关闭（不传本选项）：注释帧会改变出站字节流，测试与既有客户端行为一并
// 零回归。开启后：
//   - 写路径加互斥锁（心跳 goroutine 与主循环并发写同一 ResponseWriter）；
//   - 心跳 goroutine 由 stop 通道 + WaitGroup 保证在函数返回前退出（R9：
//     不留无人回收的 goroutine）。
func WithKeepAlive(d time.Duration) StreamOption {
	return func(o *streamOptions) {
		if d > 0 {
			o.keepAlive = d
		}
	}
}

// sseKeepAlive 聊天 SSE 心跳间隔（F-706）。包级变量而非 Client 字段：ChatStream
// 的调用方（handler）持有 *Client，但 StreamHint 在 upstream 包内被直接调用，
// 传参要穿过三层；而该值**只在装配期设置一次**（main 启动时按 config），运行期
// 无人改写，包级 atomic 足够且不触碰 Client 的既有构造面。
//
// <=0 = 关闭（默认）→ 不传 WithKeepAlive，出站字节流与旧版逐位一致（零回归）。
var sseKeepAlive atomic.Int64

// SetSSEKeepAlive 设置聊天 SSE 心跳间隔（仅装配期调用一次；<=0 关闭）。
func SetSSEKeepAlive(d time.Duration) {
	if d < 0 {
		d = 0
	}
	sseKeepAlive.Store(int64(d))
}

// SSEKeepAlive 返回当前心跳间隔（<=0 = 关闭）。供 handler 决定是否传
// WithKeepAlive，避免为关闭状态付选项构造与 lastBeat 原子写的开销。
func SSEKeepAlive() time.Duration { return time.Duration(sseKeepAlive.Load()) }

// streamRun 一次流式透传的全部可变状态。
//
// 分三组，边界即约定：
//   - 装配期只读：w / fl / o / hintFn / repair；
//   - 心跳组（仅 WithKeepAlive 启用时非空）：writeMu / lastBeat / stopKa / kaDone；
//   - 跨帧状态（读循环写、收尾读）：toolCallSeen / firstID / synthIdx /
//     maxToolIdx / toolFinishSent / validFrames。
type streamRun struct {
	w      http.ResponseWriter
	fl     http.Flusher
	o      streamOptions
	hintFn func(string) string
	repair *MarkupRepair

	// 心跳：默认关闭 → writeMu 为 nil，writeThrough 走无锁路径（零回归）。
	writeMu  *sync.Mutex
	lastBeat atomic.Int64
	stopKa   chan struct{}
	kaDone   sync.WaitGroup

	// 跨帧状态
	//
	// toolCallSeen 记录 delta.tool_calls 里已发过首片的 index，供逐 chunk 透传时
	// 收敛 name 为「每 index 一次」（对齐 OpenAI 官方流）。
	toolCallSeen map[int]bool

	// firstID 透传流的消息级 id 基准：缓存首个非空上游 id，后续帧缺失/空串时复用
	// （issue #35：同一条 SSE 消息所有帧共用一个真实 id，后台按 id 归并；此前中间帧
	// 一律补 chatcmpl-wb2api 哨兵，造成同流 id 分裂）。全流无真实 id → 才出现哨兵。
	firstID string

	// 标记修复（见 dsml.go）的跨帧记账：
	//   - synthIdx：本流已还原的调用数（合成的 index 从上游最大 index 之后接续）；
	//   - maxToolIdx：上游已用过的最大 tool_call index，避免合成的 index 与真实调用撞车；
	//   - toolFinishSent：是否已发出过 finish_reason: tool_calls，供流尾兜底。
	synthIdx       int
	maxToolIdx     int
	toolFinishSent bool

	// validFrames 已转发的有效帧数（0 = 空流，收尾时补 error 帧并返回 errEmptyStream）。
	validFrames int
}

// StreamHint 同 Stream，但上游 error 帧透出前把 hintFn(payload) 的返回值写入
// error.gateway_hint。hintFn 为 nil 或返回空串 → 原样透传（零改写）。
// 空流兜底 error 帧（"empty upstream stream"）不带 hint（网关本地故障形态
// 未覆盖，不编造）。
// opts：旁路观测 / 标记修复 / 保活开关（见各 With*）。
func StreamHint(w http.ResponseWriter, r io.Reader, hintFn func(string) string, opts ...StreamOption) error {
	s := &streamRun{
		w:            w,
		hintFn:       hintFn,
		toolCallSeen: map[int]bool{},
		maxToolIdx:   -1,
	}
	for _, f := range opts {
		if f != nil {
			f(&s.o)
		}
	}
	s.repair = s.o.markup
	s.fl, _ = w.(http.Flusher)

	s.setSSEHeaders()
	s.commitStreamStart()
	s.startKeepAlive()
	// 无论哪条返回路径，先停心跳并等它退出，再交还 ResponseWriter
	// （否则函数返回后 handler 仍可能往同一 w 写，与心跳竞争）。
	defer s.stopKeepAlive()

	if err := s.readLoop(r); err != nil {
		return err
	}
	return s.finish()
}

// setSSEHeaders 写入 SSE 响应头。调用方必须先写过 status 200。
func (s *streamRun) setSSEHeaders() {
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

// commitStreamStart 首帧前先提交 200 + SSE 响应头（F-704）。
//
// 为什么要显式做：此前响应头是**惰性**发出的——net/http 在第一次 Write
// 时才连同 200 一起刷出去。于是「上游已接受请求但迟迟不吐首 token」这段
// 时间里，客户端什么都看不到（没有状态码、没有 Content-Type），只能干等；
// 中间层也因看不到字节而可能按自己的空闲策略掐连。
// 提前 WriteHeader(200) + Flush 后，客户端立刻知道「流已建立」，TTFB 的
// 感知与首 token 解耦；真实错误仍按既有约定在流里以 error 帧下发
// （HTTP 状态码一旦发出就不可改，这是 SSE 的既有约束，不是本改动引入的）。
func (s *streamRun) commitStreamStart() {
	s.w.WriteHeader(http.StatusOK)
	if s.fl != nil {
		s.fl.Flush()
	}
}

// startKeepAlive 启动心跳 goroutine（默认关闭时只记一次时刻就返回，见 WithKeepAlive）。
func (s *streamRun) startKeepAlive() {
	s.lastBeat.Store(time.Now().UnixNano())
	if s.o.keepAlive <= 0 {
		return
	}
	s.writeMu = &sync.Mutex{}
	s.stopKa = make(chan struct{})
	s.kaDone.Add(1)
	go s.keepAliveLoop()
}

// keepAliveLoop 按 o.keepAlive 周期在流静默时写 `: ping\n\n`。
//
// 注意：本函数不能调 writeThrough（它自己持 writeMu，再调会自锁），故内联「加锁 →
// 写 → flush」三步；那三步与 writeThrough 的唯一差别是不更新 lastBeat（心跳不是数据）。
func (s *streamRun) keepAliveLoop() {
	defer s.kaDone.Done()
	t := time.NewTicker(s.o.keepAlive)
	defer t.Stop() // R12：定时器必须 Stop
	for {
		select {
		case <-s.stopKa:
			return
		case <-t.C:
			if time.Since(time.Unix(0, s.lastBeat.Load())) < s.o.keepAlive {
				continue // 刚写过数据：不需要心跳
			}
			s.writeMu.Lock()
			_, werr := io.WriteString(s.w, ": ping\n\n")
			if s.fl != nil {
				s.fl.Flush()
			}
			s.writeMu.Unlock()
			if werr != nil {
				return // 客户端已断：写失败即退（主循环随后也会感知）
			}
		}
	}
}

// stopKeepAlive 停心跳并等它退出（未启用时是空操作）。
func (s *streamRun) stopKeepAlive() {
	if s.stopKa == nil {
		return
	}
	close(s.stopKa)
	s.kaDone.Wait()
}

// writeThrough 所有写路径的唯一实现：加锁（仅心跳开启时）→ 写 → flush → 记时。
// 原先 writeSSE 与 writeLine 各写一份同样的「锁 - 写 - flush - 记时」序列，靠注释
// 「与 writeSSE 同锁口径」约束——本仓已被证明这种注释约束会漂移（见 guards.go 同因）。
// 注意：心跳 goroutine 不能调本函数（它持有 writeMu，再调会自锁）；心跳自己内联写。
func (s *streamRun) writeThrough(payload string) error {
	if s.writeMu != nil {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	_, werr := io.WriteString(s.w, payload)
	if werr == nil && s.fl != nil {
		s.fl.Flush()
	}
	if werr == nil {
		s.lastBeat.Store(time.Now().UnixNano()) // 心跳判据：刚写过数据
	}
	return werr
}

// writeSSE 写出一帧 data 行（writeThrough 的语义命名包装；调用点较多故保留名字）。
func (s *streamRun) writeSSE(payload string) error {
	return s.writeThrough("data: " + payload + "\n\n")
}

// writeRaw 原样写出一帧（绕过 normalizeFrame）。上游 error 帧（error-passthrough）
// 与空流错误帧需保留 error 字段，不能被白名单剥掉，故经此写出。
// gateway_hint：上游 error 帧透出前按 hintFn 附加 error.gateway_hint 字段
// （error 对象上加一个键，message/code/requestId 等原文不动；hintFn 为
// nil / 空串 / 非 JSON 帧 → 原样写出，零改写）。
func (s *streamRun) writeRaw(payload string) error {
	if hint := frameGatewayHint(s.hintFn, payload); hint != "" {
		payload = attachHintToErrorFrame(payload, hint)
	}
	return s.writeSSE(payload)
}

// writeChunk 把一个 chunk 对象按规范白名单重建后写出并 flush，同时维护三件跨帧
// 状态：tool_calls name 收敛（每 index 仅首片带 name）、消息 id 续传、以及上游
// 已用过的最大 tool_call index（供标记修复接续编号）。
// fallback 是规范化失败时的兜底原文（与修复前「marshal 失败即原样透传」一致）。
func (s *streamRun) writeChunk(obj map[string]any, fallback string) error {
	// 先按 index 收敛 tool_calls name（每 index 仅首片保留，后续分片删 name 键），再规范化透传。
	stripToolCallNames(obj, s.toolCallSeen)
	// id 续传：首帧非空真实 id 缓存；后续帧缺 id / 空 id 一律用缓存值，
	// 有自己 id 的帧保持原样（不同流分裂的帧允许各自 id）。
	if s.firstID == "" {
		if v, ok := obj["id"].(string); ok && v != "" {
			s.firstID = v
		}
	} else if v, ok := obj["id"].(string); !ok || v == "" {
		obj["id"] = s.firstID
	}
	if chs, ok := obj["choices"].([]any); ok {
		for _, ci := range chs {
			c, _ := ci.(map[string]any)
			if c == nil {
				continue
			}
			d, _ := c["delta"].(map[string]any)
			if d == nil {
				continue
			}
			tcs, _ := d["tool_calls"].([]any)
			for _, tci := range tcs {
				tc, _ := tci.(map[string]any)
				if tc == nil {
					continue
				}
				if v, ok := tc["index"].(float64); ok && int(v) > s.maxToolIdx {
					s.maxToolIdx = int(v)
				}
			}
		}
	}
	raw, err := json.Marshal(normalizeFrame(obj))
	if err != nil {
		return s.writeSSE(fallback)
	}
	return s.writeSSE(string(raw))
}

// repairFrame 在透传前跑标记修复，并把一帧展开成「可能多帧」：正文帧（标记被摘掉
// 后的可见文本）+ N 个 delta.tool_calls 帧 + 收尾 finish_reason 帧。没有命中标记
// 时原样返回入参帧（单元素），与修复前逐字节一致。
func (s *streamRun) repairFrame(obj map[string]any) []map[string]any {
	if !s.repair.Enabled() {
		return []map[string]any{obj}
	}
	chs, ok := obj["choices"].([]any)
	if !ok {
		return []map[string]any{obj}
	}
	var calls []MarkupCall
	for _, ci := range chs {
		c, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		d, ok := c["delta"].(map[string]any)
		if !ok {
			continue
		}
		txt, ok := d["content"].(string)
		if !ok || txt == "" {
			continue
		}
		emit, got := s.repair.Feed(txt)
		calls = append(calls, got...)
		if emit == "" {
			delete(d, "content")
		} else {
			d["content"] = emit
		}
	}
	if len(calls) == 0 {
		// 本帧没命中标记；但本流此前还原过调用 → 把上游的 stop 收尾改写成
		// tool_calls，客户端据此知道「回合以工具调用结束」而不是「模型只说了话」。
		if s.synthIdx > 0 && rewriteFinishReason(obj, "tool_calls") {
			s.toolFinishSent = true
		}
		return []map[string]any{obj}
	}
	// 命中：finish_reason 必须挪到工具调用帧之后，否则客户端在读到调用之前就
	// 认为回合已经结束。注意「本帧本来没有 finish_reason」时**不得**补一帧收尾
	// ——上游的收尾帧随后就到，补了会变成两个 finish_reason（后续那个由
	// rewriteFinishReason 改写）。
	fr := takeFinishReason(obj)
	out := []map[string]any{obj}
	for _, call := range calls {
		idx := s.maxToolIdx + 1 + s.synthIdx
		s.synthIdx++
		out = append(out, markupCallFrame(obj, idx, call))
	}
	if fr != "" {
		if fr == "stop" {
			fr = "tool_calls"
		}
		out = append(out, finishOnlyFrame(obj, fr))
		s.toolFinishSent = fr == "tool_calls"
	}
	return out
}

// writeFrame 把 payload 按规范白名单重建后以 data: 帧写出并 flush。
// 仅 JSON 解析成功时计数记为一次有效转发（JSON 解析失败照常降级原样写出，但不计数）。
func (s *streamRun) writeFrame(payload string) (int, error) {
	var obj map[string]any
	if json.Unmarshal([]byte(payload), &obj) != nil {
		// 非 JSON 帧：原样降级写出，不计数（与修复前一致）。
		if werr := s.writeSSE(payload); werr != nil {
			return 0, werr
		}
		return 0, nil
	}
	// 上游错误帧透传（error-passthrough）：带 error 键的帧**原样写出**，不走
	// normalizeFrame 白名单——白名单会剥掉 error 字段，客户端就看不到上游
	// code/msg/requestId。error.message 即上游原文（如 6004 限流、审核拦截），
	// 计入有效帧（避免误判空流补写 "empty upstream stream"）。
	if _, hasErr := obj["error"]; hasErr {
		// 旁路通知（账号处置用）：原始 payload 交给观察者，透传字节不变。
		if s.o.onErrorFrame != nil {
			s.o.onErrorFrame(payload)
		}
		if werr := s.writeRaw(payload); werr != nil {
			return 0, werr
		}
		return 1, nil
	}
	for _, ch := range s.repairFrame(obj) {
		if werr := s.writeChunk(ch, payload); werr != nil {
			return 0, werr
		}
	}
	return 1, nil
}

// readLoop 逐行读上游 SSE 并写出，返回 nil 表示「读完了」（EOF 或上游显式 [DONE]），
// 返回非 nil 表示写失败/上游畸形，调用方直接上抛（跳过收尾，与拆分前一致）。
func (s *streamRun) readLoop(r io.Reader) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		// 单行上限（F-705）：不吐换行的超长行不再无限吃内存，超限即判上游畸形。
		line, err := readSSELine(br)
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "data: [DONE]"):
			// 上游显式结束：停止读取，DONE 之后的任何数据（含垃圾帧）一律不再透传。
			// [DONE] 统一在 finish 里写出，保证恰好一个。
			return nil
		case strings.HasPrefix(trimmed, "data: "):
			n, werr := s.writeFrame(strings.TrimPrefix(trimmed, "data: "))
			s.validFrames += n
			if werr != nil {
				return werr
			}
		case trimmed != "":
			// 注释/其他行：原样透传
			if werr := s.writeThrough(line); werr != nil {
				return werr
			}
		}
		// 空行（帧分隔）吞掉：本函数自产 "\n\n"
		if err != nil {
			if errors.Is(err, errFrameTooLarge) {
				return err
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// finish 流尾收尾：标记修复尾部回吐 → 空流兜底 error 帧 → 恰好一个 [DONE]。
func (s *streamRun) finish() error {
	// 标记修复的尾部收尾：流结束时仍未判定的字节一律原文回吐（绝不吞字节，未闭合
	// 的块连起始标记一起交还）；若本流还原过调用、但上游从未给出收尾帧，补一帧
	// finish_reason: tool_calls，保证客户端不会把「工具调用回合」读成「模型只说了话」。
	if s.repair.Enabled() {
		if tail := s.repair.Flush(); tail != "" {
			tailObj := map[string]any{
				"object": "chat.completion.chunk",
				"choices": []any{map[string]any{
					"index": 0,
					"delta": map[string]any{"content": tail},
				}},
			}
			if werr := s.writeChunk(tailObj, ""); werr != nil {
				return werr
			}
			s.validFrames++
		}
		if s.synthIdx > 0 && !s.toolFinishSent {
			if werr := s.writeChunk(finishOnlyFrame(map[string]any{}, "tool_calls"), ""); werr != nil {
				return werr
			}
		}
	}
	// 空流（0 有效帧）：先写一帧 error（绕过 normalizeFrame 原样保留 error 字段），
	// 再补 [DONE] 保证客户端能正常收尾，并返回非 nil error 供调用方记录。
	//
	// 注释修正（2026-11-15 独立复审）：此处原写「走 hintFn=nil 的直写路径」，与实现不符——
	// 它经 writeRaw，而 writeRaw 一定会咨询 s.hintFn（本包没有 hintFn=nil 的写路径）。
	// 真实行为：该兜底帧是网关自造的 payload，hintFn 对它判不出上游 ErrKind（推断，
	// 无独立断言兜住），于是 hint 为空、帧原样写出。若将来要**保证**它绝不带
	// gateway_hint，必须改用不经 frameGatewayHint 的写路径，而不是依赖 hintFn 返回空串。
	if s.validFrames == 0 {
		// 忽略：写响应路径客户端可能已断开无法补救；紧邻的 [DONE] 写入错误会被检查并返回（判据①）。
		_ = s.writeRaw(`{"error":{"message":"empty upstream stream","type":"upstream_error","code":"upstream_parse"}}`)
	}
	// 保证恰好写一个 [DONE]（上游漏发时兜底补上）。
	// 必须经 writeThrough：本段在 readLoop 之后、心跳的 defer 之前执行，心跳 goroutine
	// 仍可能正在写 ping——直写会与它交错出半帧；此前这里是全函数唯一绕过加锁的写路径。
	if werr := s.writeThrough("data: [DONE]\n\n"); werr != nil {
		return werr
	}
	if s.validFrames == 0 {
		return errEmptyStream
	}
	return nil
}
