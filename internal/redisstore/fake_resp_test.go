package redisstore

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// 本文件用「最小 RESP2 假服务器」验证 redisstore 的出站命令构造与关停语义：
// 断言的是服务器**实际收到的字节帧**（命令名 + 参数 + TTL 单位），不是本地状态。
//
// 为什么不用 miniredis / 真实 Redis：零新依赖（硬约束 2），且本机无 gcc。
// 假服务器只做两件事：解析 *N/$len 多批命令帧、按脚本回原始 RESP 回复。
//
// 确定性关键：Upstash 的写是 fire-and-forget，但 Close 会 wg.Wait 排空全部
// 已提交的写才返回；而服务器「先记录命令、后回回复」，所以
// 「SetBind → Close → 读服务器记录」是确定性的（无 sleep、无轮询）。

// ---------------------------------------------------------------------------
// 假 RESP2 服务器
// ---------------------------------------------------------------------------

// frsServer 最小 RESP2 假服务器。
type frsServer struct {
	ln     net.Listener
	script func(args []string) string // 命令 → 原始 RESP 回复

	mu      sync.Mutex
	got     [][]string
	conns   map[net.Conn]struct{}
	stopped bool

	wg sync.WaitGroup
}

// frsStart 起一个监听 127.0.0.1:0 的假服务器，script 为 nil 时用默认脚本。
// 清理在 t.Cleanup 注册（先关监听与所有连接，再等 goroutine 退出）。
func frsStart(t *testing.T, script func(args []string) string) *frsServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("frs: listen: %v", err)
	}
	if script == nil {
		script = frsDefaultScript
	}
	s := &frsServer{ln: ln, script: script, conns: map[net.Conn]struct{}{}}
	s.wg.Add(1)
	go s.acceptLoop()
	t.Cleanup(s.stop)
	return s
}

func (s *frsServer) addr() string { return s.ln.Addr().String() }

// url 返回可直接喂给 New 的 redis:// 连接串（触发 normalizeURL 的「原样返回」分支）。
func (s *frsServer) url() string { return "redis://" + s.addr() }

func (s *frsServer) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(c)
		}()
	}
}

func (s *frsServer) serveConn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	for {
		args, err := frsReadCommand(r)
		if err != nil {
			return
		}
		s.mu.Lock()
		s.got = append(s.got, args)
		s.mu.Unlock()
		if _, err := w.WriteString(s.script(args)); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func (s *frsServer) stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	_ = s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
}

// commands 返回服务器实际收到的全部命令（深拷贝，顺序 = 到达顺序）。
func (s *frsServer) commands() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.got))
	for i, c := range s.got {
		out[i] = append([]string(nil), c...)
	}
	return out
}

// named 返回指定命令名（不区分大小写的首个参数）的全部命令。
func (s *frsServer) named(name string) [][]string {
	name = strings.ToUpper(name)
	var out [][]string
	for _, c := range s.commands() {
		if len(c) > 0 && strings.ToUpper(c[0]) == name {
			out = append(out, c)
		}
	}
	return out
}

// frsReadCommand 解析一个 RESP2 多批命令帧（*N\r\n 后跟 N 个 $len\r\n<bytes>\r\n）。
func frsReadCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) < 2 || line[0] != '*' {
		return nil, fmt.Errorf("frs: want multibulk header, got %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, fmt.Errorf("frs: bad multibulk len %q: %w", line, err)
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		hdr = strings.TrimRight(hdr, "\r\n")
		if len(hdr) < 2 || hdr[0] != '$' {
			return nil, fmt.Errorf("frs: want bulk header, got %q", hdr)
		}
		blen, err := strconv.Atoi(hdr[1:])
		if err != nil {
			return nil, fmt.Errorf("frs: bad bulk len %q: %w", hdr, err)
		}
		buf := make([]byte, blen+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:blen]))
	}
	return args, nil
}

// frsDefaultScript 默认回复：够 go-redis 完成连接握手 + 常见命令。
func frsDefaultScript(args []string) string {
	if len(args) == 0 {
		return frsErr("empty command")
	}
	switch strings.ToUpper(args[0]) {
	case "PING":
		return "+PONG\r\n"
	case "AUTH", "SELECT", "SET":
		return "+OK\r\n"
	case "DEL":
		return ":1\r\n"
	case "GET":
		return "$-1\r\n" // 未命中
	case "HELLO", "CLIENT":
		// 连接握手命令：回 Redis 错误让 go-redis 降级（HELLO 失败即退到 RESP2，
		// CLIENT MAINT_NOTIFICATIONS 失败在 auto 模式下自动禁用），不影响后续命令。
		return frsErr("unknown command '" + args[0] + "'")
	}
	return frsErr("unknown command")
}

func frsErr(msg string) string { return "-ERR " + msg + "\r\n" }

func frsBulk(s string) string { return "$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n" }

// frsArray 拼一个 RESP2 数组（元素为已编码的原始回复片段）。
func frsArray(items ...string) string {
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(items)) + "\r\n")
	for _, it := range items {
		b.WriteString(it)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 测试夹具
// ---------------------------------------------------------------------------

// frsClient 直连假服务器的 go-redis 客户端：显式 Protocol=2、无重试、超时收紧，
// 避免依赖默认握手与真实重试时序。
func frsClient(t *testing.T, s *frsServer) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{
		Addr:         s.addr(),
		Protocol:     2,
		DialTimeout:  200 * time.Millisecond,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		MaxRetries:   0,
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// frsUpstash 构造一个连到假服务器、sem/done 与 New 同规格的 Upstash。
func frsUpstash(t *testing.T, s *frsServer) *Upstash {
	t.Helper()
	return &Upstash{
		client: frsClient(t, s),
		sem:    make(chan struct{}, writeConcurrencyLimit),
		done:   make(chan struct{}),
	}
}

// frsEq 逐参数比较一条命令帧（期望值为手写字面量，不由被测代码算出）。
func frsEq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 参数个数 got %d %q want %d %q", what, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: 第 %d 个参数 got %q want %q（整条 got %q）", what, i, got[i], want[i], got)
		}
	}
}

func frsSingle(t *testing.T, s *frsServer, name string) []string {
	t.Helper()
	got := s.named(name)
	if len(got) != 1 {
		t.Fatalf("期望恰好 1 条 %s 命令，实际 %d 条（全部命令：%q）", name, len(got), s.commands())
	}
	return got[0]
}

func frsNoCmd(t *testing.T, s *frsServer, name string) {
	t.Helper()
	if got := s.named(name); len(got) != 0 {
		t.Fatalf("期望 0 条 %s 命令，实际 %d 条：%q", name, len(got), got)
	}
}

// frsCheckRepeatClose 校验重复 Close 的返回值：允许 nil，或 redis.ErrClosed
// （go-redis 的 ConnPool.Close 第二次返回 ErrClosed），但不允许其它错误——
// 即重复关停既不 panic，也不产生新的失败原因。
func frsCheckRepeatClose(t *testing.T, err error) {
	t.Helper()
	if err == nil || errors.Is(err, redis.ErrClosed) {
		return
	}
	t.Fatalf("重复 Close 只允许返回 nil 或 redis.ErrClosed，实际 %v", err)
}

// ---------------------------------------------------------------------------
// 出站命令构造：SetBind / DelBind / SaveState
// ---------------------------------------------------------------------------

// TestFRSSetBindSendsSetWithKeyTTLWhenTTLNonPositive 钉住「SetBind 的 ttl<=0 兜底」：
// 调用方传 0 时 Redis 侧必须拿到 keyTTL=7d 的显式 EX，而不是无过期时间的永久键。
func TestFRSSetBindSendsSetWithKeyTTLWhenTTLNonPositive(t *testing.T) {
	s := frsStart(t, nil)
	u := frsUpstash(t, s)

	u.SetBind("sess-1", "uid-42", 0) // ttl<=0 → 必须兜底为 keyTTL
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	frsEq(t, "SET 粘性绑定", frsSingle(t, s, "SET"),
		[]string{"set", "yg-gateway:bind:sess-1", "uid-42", "ex", "604800"})
}

// TestFRSSetBindHonoursExplicitTTL 钉住显式 TTL 原样透传（不被兜底覆盖）。
func TestFRSSetBindHonoursExplicitTTL(t *testing.T) {
	s := frsStart(t, nil)
	u := frsUpstash(t, s)

	u.SetBind("sess-2", "uid-7", 90*time.Minute)
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	frsEq(t, "SET 显式 TTL", frsSingle(t, s, "SET"),
		[]string{"set", "yg-gateway:bind:sess-2", "uid-7", "ex", "5400"})
}

// TestFRSSetBindSubSecondTTLUsesPX 钉住「秒级以下 TTL 走 PX」，防止实现改成一律 EX
// 导致短 TTL 被截断成 1s（go-redis 从 1s 截断会静默放大过期时间）。
func TestFRSSetBindSubSecondTTLUsesPX(t *testing.T) {
	s := frsStart(t, nil)
	u := frsUpstash(t, s)

	u.SetBind("sess-3", "uid-8", 300*time.Millisecond)
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	frsEq(t, "SET 亚秒 TTL", frsSingle(t, s, "SET"),
		[]string{"set", "yg-gateway:bind:sess-3", "uid-8", "px", "300"})
}

// TestFRSDelBindSendsDelOnPrefixedKey 钉住 DelBind 删的是带前缀的 key 而不是裸 key。
func TestFRSDelBindSendsDelOnPrefixedKey(t *testing.T) {
	s := frsStart(t, nil)
	u := frsUpstash(t, s)

	u.DelBind("sess-4")
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	frsEq(t, "DEL 粘性绑定", frsSingle(t, s, "DEL"), []string{"del", "yg-gateway:bind:sess-4"})
}

// TestFRSSaveStateSendsRawJSONWithKeyTTL 钉住状态快照的 key、原始字节与 7d TTL 三者。
func TestFRSSaveStateSendsRawJSONWithKeyTTL(t *testing.T) {
	s := frsStart(t, nil)
	u := frsUpstash(t, s)

	payload := []byte(`{"rev":11,"cooldown":{"a":5}}`)
	u.SaveState(payload)
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	frsEq(t, "SET 状态快照", frsSingle(t, s, "SET"),
		[]string{"set", "yg-gateway:state", string(payload), "ex", "604800"})
}

// TestFRSWriteErrorsDoNotBreakClose 钉住 fire-and-forget 契约：Redis 回错只是 debug 日志，
// 既不 panic 也不阻塞关停（Close 仍返回 nil、后续连接照样关）。
func TestFRSWriteErrorsDoNotBreakClose(t *testing.T) {
	s := frsStart(t, func(args []string) string {
		if len(args) > 0 {
			switch strings.ToUpper(args[0]) {
			case "SET", "DEL":
				return frsErr("READONLY you can't write against a read only replica")
			}
		}
		return frsDefaultScript(args)
	})
	u := frsUpstash(t, s)

	u.SetBind("sess-5", "uid-5", time.Minute)
	u.DelBind("sess-5")
	u.SaveState([]byte("{}"))
	if err := u.Close(); err != nil {
		t.Fatalf("写失败不得影响 Close: %v", err)
	}

	if got := s.named("SET"); len(got) != 2 {
		t.Fatalf("期望 2 条 SET 尝试（绑定 + 快照），实际 %d 条：%q", len(got), got)
	}
	frsEq(t, "DEL 仍发出", frsSingle(t, s, "DEL"), []string{"del", "yg-gateway:bind:sess-5"})
}

// ---------------------------------------------------------------------------
// LoadState（读路径 + 未命中 + 服务端错误）
// ---------------------------------------------------------------------------

func TestFRSLoadStateHit(t *testing.T) {
	body := `{"rev":7}`
	s := frsStart(t, func(args []string) string {
		if len(args) > 0 && strings.ToUpper(args[0]) == "GET" {
			return frsBulk(body)
		}
		return frsDefaultScript(args)
	})
	u := frsUpstash(t, s)

	got, ok := u.LoadState()
	if !ok {
		t.Fatalf("命中时 LoadState 应报告 found")
	}
	if string(got) != body {
		t.Fatalf("LoadState 内容 got %q want %q", got, body)
	}
	frsEq(t, "GET 状态快照", frsSingle(t, s, "GET"), []string{"get", "yg-gateway:state"})
}

func TestFRSLoadStateMissAndServerError(t *testing.T) {
	cases := []struct {
		name string
		istr string
	}{
		{"未命中 nil bulk", "$-1\r\n"},
		{"服务端错误", frsErr("LOADING Redis is loading the dataset in memory")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := frsStart(t, func(args []string) string {
				if len(args) > 0 && strings.ToUpper(args[0]) == "GET" {
					return tc.istr
				}
				return frsDefaultScript(args)
			})
			u := frsUpstash(t, s)

			got, ok := u.LoadState()
			if ok {
				t.Fatalf("redis.Nil / 服务端错误都必须报告 not-found，实际 ok=true data=%q", got)
			}
			if got != nil {
				t.Fatalf("not-found 时数据必须为 nil，实际 %q", got)
			}
			frsEq(t, "GET 状态快照", frsSingle(t, s, "GET"), []string{"get", "yg-gateway:state"})
		})
	}
}

// ---------------------------------------------------------------------------
// LoadBinds（SCAN 分页 + match/count 契约 + 前缀剥离 + 单键失败跳过）
// ---------------------------------------------------------------------------

// TestFRSLoadBindsScansPagesAndStripsPrefix 是本包最重的一条：钉住
// ①SCAN 的 match 前缀与 count=200、②cursor 分页确实推进、③返回 key 剥掉前缀、
// ④单个 key 的 GET 失败只跳过该键（continue 而非 break）——**失败键之后**的键
// （同页的 aaa、次页的 bbb）都必须还在结果映射里。
//
// 用例前提（变异测试教训）：失败键 ccc 必须是**页面中部**而非迭代序最后一个。
// 若失败键是最后一个键，continue→break 的输出完全相同（前面已写入的键不受影响），
// 变异存活。这里首页返回 [ccc, aaa]、次页 [bbb]，break 会直接掐断整轮扫描，
// 于是 aaa/bbb 双双缺失。
func TestFRSLoadBindsScansPagesAndStripsPrefix(t *testing.T) {
	s := frsStart(t, func(args []string) string {
		if len(args) == 0 {
			return frsErr("empty command")
		}
		switch strings.ToUpper(args[0]) {
		case "SCAN":
			if args[1] == "0" {
				// 首页：读失败的 ccc 排在同页 aaa 之前，且后面还有一页。
				return frsArray(frsBulk("5"), frsArray(
					frsBulk("yg-gateway:bind:ccc"),
					frsBulk("yg-gateway:bind:aaa"),
				))
			}
			return frsArray(frsBulk("0"), frsArray(frsBulk("yg-gateway:bind:bbb")))
		case "GET":
			switch args[1] {
			case "yg-gateway:bind:aaa":
				return frsBulk("u1")
			case "yg-gateway:bind:bbb":
				return frsBulk("u2")
			}
			// ccc：读取失败 → 必须只跳过该键（continue），而不是终止整轮扫描（break）
			return frsErr("WRONGTYPE key holds the wrong kind of value")
		}
		return frsDefaultScript(args)
	})
	u := frsUpstash(t, s)

	got := u.LoadBinds()

	// 先钉住用例前提：GET 顺序必须是 ccc → aaa → bbb（期望值为手写字面量）。
	// ccc 先于 aaa 是"失败键在页面中部"的机器化证据；若有人把脚本改成 ccc 收尾，
	// 这条会红，提醒他 continue/break 已不可区分。
	var getKeys []string
	for _, g := range s.named("GET") {
		getKeys = append(getKeys, g[1])
	}
	wantGetOrder := []string{
		"yg-gateway:bind:ccc", // 失败键：必须排在 aaa 之前
		"yg-gateway:bind:aaa", // 失败键之后、同页 → 只有 continue 才会进入结果
		"yg-gateway:bind:bbb", // 失败键之后、次页 → 只有 continue 才会被读到
	}
	if strings.Join(getKeys, ",") != strings.Join(wantGetOrder, ",") {
		t.Fatalf("GET 顺序 got %q want %q（用例前提：失败键必须排在同页其它键之前）", getKeys, wantGetOrder)
	}

	// 逐键断言（期望值独立手写）：失败键被跳过，其后的两个键必须健在。
	if len(got) != 2 {
		t.Fatalf("LoadBinds 条数 got %d %v want 2（aaa、bbb）", len(got), got)
	}
	for k, want := range map[string]string{"aaa": "u1", "bbb": "u2"} {
		if got[k] != want {
			t.Fatalf("LoadBinds[%q] got %q want %q（全部：%v）", k, got[k], want, got)
		}
	}
	if v, ok := got["ccc"]; ok {
		t.Fatalf("GET 失败的 ccc 不得进入结果映射，实际 ccc=%q（全部：%v）", v, got)
	}

	scans := s.named("SCAN")
	if len(scans) != 2 {
		t.Fatalf("期望 2 次 SCAN（两页），实际 %d 次：%q", len(scans), scans)
	}
	frsEq(t, "SCAN 首页", scans[0],
		[]string{"scan", "0", "match", "yg-gateway:bind:*", "count", "200"})
	frsEq(t, "SCAN 次页", scans[1],
		[]string{"scan", "5", "match", "yg-gateway:bind:*", "count", "200"})
}

// TestFRSLoadBindsEmptyScanReturnsEmptyMap 钉住「无绑定」返回非 nil 空映射
// （调用方 range 安全，且不会误判为 nil）。
func TestFRSLoadBindsEmptyScanReturnsEmptyMap(t *testing.T) {
	s := frsStart(t, func(args []string) string {
		if len(args) > 0 && strings.ToUpper(args[0]) == "SCAN" {
			return frsArray(frsBulk("0"), frsArray())
		}
		return frsDefaultScript(args)
	})
	u := frsUpstash(t, s)

	got := u.LoadBinds()
	if got == nil {
		t.Fatalf("无绑定时应返回非 nil 空映射")
	}
	if len(got) != 0 {
		t.Fatalf("无绑定时映射应为空，实际 %v", got)
	}
	frsSingle(t, s, "SCAN")
	frsNoCmd(t, s, "GET")
}

// ---------------------------------------------------------------------------
// 关停语义：goWrite / Close / closeClient
// ---------------------------------------------------------------------------

// TestFRSCloseFlushesQueuedWrites 钉住停机契约「Close 前提交的写（含排队中）必然执行」。
// sem 故意设为 cap=1（串行化），让 4 笔写确定性地卡在抢槽点；Close 必须把它们全部排空，
// 并在第二次调用时幂等且不再发新命令。
func TestFRSCloseFlushesQueuedWrites(t *testing.T) {
	s := frsStart(t, nil)
	u := &Upstash{client: frsClient(t, s), sem: make(chan struct{}, 1), done: make(chan struct{})}

	const n = 5
	for i := 0; i < n; i++ {
		u.SetBind(fmt.Sprintf("k%d", i), fmt.Sprintf("u%d", i), time.Minute)
	}
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sets := s.named("SET")
	if len(sets) != n {
		t.Fatalf("Close 必须排空全部已提交写（含排队中），got %d want %d：%q", len(sets), n, sets)
	}
	seen := map[string]bool{}
	for _, c := range sets {
		seen[c[1]] = true
	}
	for i := 0; i < n; i++ {
		if k := fmt.Sprintf("yg-gateway:bind:k%d", i); !seen[k] {
			t.Fatalf("丢失写 %s（实际收到 %q）", k, sets)
		}
	}

	// 幂等：第二次 Close 不 panic（closeOnce 只关一次 done）、不再产生命令。
	// 只有底层连接重复关闭会被 go-redis 报成 ErrClosed，这不是本层的新失败原因。
	frsCheckRepeatClose(t, u.Close())
	if got := len(s.named("SET")); got != n {
		t.Fatalf("幂等 Close 后 SET 数量变化：%d → %d", n, got)
	}
}

// TestFRSWritesAfterCloseAreDiscarded 钉住「Close 后新提交的写直接丢弃」：
// 关停后再提交的写既不得登记进 wg，也不得执行 fn。
//
// 探针说明：**不能**用「服务器没收到 SET/DEL」来断言丢弃语义——Close 已经关掉底层
// 连接，迟到的写根本到不了服务器，这条断言对 done 检查失效的变异是免疫的（原版注释
// 「这些写必然落到服务器上」的假设不成立）。改用不触网的 fn：goWrite 的 done 分支若
// 失效，fn 必然执行（第二次 Close 的 wg.Wait 会把它的执行等到，断言因此确定性）。
func TestFRSWritesAfterCloseAreDiscarded(t *testing.T) {
	s := frsStart(t, nil)
	u := &Upstash{client: frsClient(t, s), sem: make(chan struct{}, 1), done: make(chan struct{})}

	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 不触网探针：关停后提交的写必须**连 fn 都不执行**（done 分支的唯一决定性证据）。
	var ran atomic.Bool
	u.goWrite(func() { ran.Store(true) })
	// 三个公开写入口各提交 4 轮：它们同样走 goWrite 的 done 分支（顺带覆盖调用链，
	// 但"没在服务器上看到 SET/DEL"不构成证据——连接已关，它们根本到不了服务器）。
	for i := 0; i < 4; i++ {
		u.SetBind("late", "uid", 0)
		u.DelBind("late")
		u.SaveState([]byte("{}"))
	}
	// 第二次 Close 会 wg.Wait：若关停检查失效（写被登记进 wg 并在 goroutine 里跑），
	// 它必须等 fn 执行完才返回；检查点放在其后即为确定性断言（无需 sleep）。
	frsCheckRepeatClose(t, u.Close())
	if ran.Load() {
		t.Fatalf("Close 后提交的写必须直接丢弃，但 goWrite 的 fn 被执行了（done 分支失效）")
	}

	// 顺带（弱）钉住不发命令：这不构成丢弃语义的证据，只防"连接被重新建起来真发出去"。
	frsNoCmd(t, s, "SET")
	frsNoCmd(t, s, "DEL")
}

// TestFRSZeroValueUpstashIsNilSafe 钉住零值 Upstash（未经 New 构造）的 nil 安全：
// closeOnceGuard 补建 sem/done、closeClient 跳过 nil client、关停后的写不执行 fn。
func TestFRSZeroValueUpstashIsNilSafe(t *testing.T) {
	var u Upstash // client/sem/done 全 nil

	if err := u.Close(); err != nil {
		t.Fatalf("零值 Close 应返回 nil，实际 %v", err)
	}
	if u.sem == nil || u.done == nil {
		t.Fatalf("closeOnceGuard 应补建 sem/done（sem=%v done=%v）", u.sem, u.done)
	}

	u.SetBind("k", "u", time.Minute)
	u.DelBind("k")
	u.SaveState([]byte("{}"))
	// 关停后提交的写必须被丢弃：fn 一旦执行即说明 done 检查失效。
	u.goWrite(func() { t.Error("关停后提交的写不得执行 fn") })
	if err := u.Close(); err != nil {
		t.Fatalf("第二次 Close 应返回 nil（client=nil 时跳过关闭），实际 %v", err)
	}
}

// TestFRSConcurrentWritesAllFlushedBeforeClose 钉住并发边界：多 goroutine 提交的写在
// Close 返回前必须全部送达服务器（一条都不能丢）。
func TestFRSConcurrentWritesAllFlushedBeforeClose(t *testing.T) {
	s := frsStart(t, nil)
	u := frsUpstash(t, s)

	const n = 24
	var submit sync.WaitGroup
	for i := 0; i < n; i++ {
		submit.Add(1)
		go func(i int) {
			defer submit.Done()
			u.SetBind(fmt.Sprintf("c%d", i), fmt.Sprintf("uid%d", i), time.Minute)
		}(i)
	}
	submit.Wait()
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	sets := s.named("SET")
	if len(sets) != n {
		t.Fatalf("Close 返回时仍有写未送达：got %d want %d", len(sets), n)
	}
	seen := map[string]bool{}
	for _, c := range sets {
		if seen[c[1]] {
			t.Fatalf("命令重复送达：%s", c[1])
		}
		seen[c[1]] = true
	}
	for i := 0; i < n; i++ {
		if k := fmt.Sprintf("yg-gateway:bind:c%d", i); !seen[k] {
			t.Fatalf("丢失写 %s", k)
		}
	}
}

// ---------------------------------------------------------------------------
// 写并发上限（sem）：服务器侧观测「同时在途命令数」
// ---------------------------------------------------------------------------
//
// 变异测试教训：删掉 goWrite 里的 `u.sem <- struct{}{}` 与 `defer func(){ <-u.sem }()`
// 后原测试 4 次连跑全绿——没有任何断言钉住写并发上限。这里让假服务器**观测同时在途
// 命令数**（script 回调里维护在途计数器），并用阻塞 channel 扣住每条命令、由测试决定
// 何时放行。全程无 sleep、无轮询，同步只靠 channel。

// frsGate 在服务器侧统计同时在途的 SET 命令数（进入 script 回调 +1，放行后 -1）。
type frsGate struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int

	arrived chan struct{} // 一条命令到达并在途
	drain   chan struct{} // 放行所有被扣住的命令（正常收尾与清理期都靠它）

	drainOnce sync.Once
}

// newFRSGate 建 gate（清理由 frsStartGated 统一注册，见那里的清理顺序说明）。
func newFRSGate(total int) *frsGate {
	return &frsGate{
		arrived: make(chan struct{}, total),
		drain:   make(chan struct{}),
	}
}

// frsStartGated 起假服务器 + gate，并钉死清理顺序：先放行被扣住的命令（releaseAll），
// 再关连接（frsStart 注册的 stop）。顺序反了的话，卡在 drain 上的 serveConn 会让
// stop 的 wg.Wait 挂住——用例失败时整个测试二进制要等超时才退出。
func frsStartGated(t *testing.T, total int) (*frsServer, *frsGate) {
	t.Helper()
	g := newFRSGate(total)
	s := frsStart(t, frsGateScript(g))
	t.Cleanup(g.releaseAll) // LIFO：晚于 frsStart 注册 → 早于 s.stop 执行
	return s, g
}

// releaseAll 放行所有被扣住的命令（正常收尾用，也防测试中途失败时清理期挂死）。
func (g *frsGate) releaseAll() {
	g.drainOnce.Do(func() { close(g.drain) })
}

// enter 记录一条命令进入在途，并扣住它直到 releaseAll 放行。
func (g *frsGate) enter() {
	g.mu.Lock()
	g.inFlight++
	if g.inFlight > g.maxSeen {
		g.maxSeen = g.inFlight
	}
	g.mu.Unlock()

	g.arrived <- struct{}{}
	<-g.drain

	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
}

// max 返回观测到的最大同时在途命令数。
func (g *frsGate) max() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxSeen
}

// frsGateScript SET 走 gate（观测在途 + 扣住回复），其余命令走默认脚本。
func frsGateScript(g *frsGate) func(args []string) string {
	return func(args []string) string {
		if len(args) > 0 && strings.ToUpper(args[0]) == "SET" {
			g.enter()
			return "+OK\r\n"
		}
		return frsDefaultScript(args)
	}
}

// frsGateClient 握手用例专用客户端：读超时放宽（命令被测试故意扣住不回复），
// 不重试（重试会多发命令，污染在途计数）。
func frsGateClient(t *testing.T, s *frsServer) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{
		Addr:         s.addr(),
		Protocol:     2,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Second,
		MaxRetries:   0,
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// frsAwaitRecv 带超时的接收：超时即失败（只用于失败路径兜底；正常路径的同步完全靠
// 令牌握手，不依赖这个超时）。
func frsAwaitRecv(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatalf("等待 %s 超时（写槽提交/释放路径可能不自洽）", what)
	}
}

// frsSemHoldAndCheck 是写槽用例的共用流程：
//  1. 提交 total 笔写，服务器把每条 SET 都**扣住**不回复（命令一直「在途」）；
//  2. 等 capN 条命令同时在途——写槽容量是 capN 时，这已是上限；
//  3. 见证写槽被这 capN 条在途写占满：写槽申请/释放一旦失效，占用必然不是 capN
//     （删掉 `u.sem <- struct{}{}` 时恒为 0）。这条见证同时**证明其余 total-capN 笔
//     写仍卡在写槽上**，因此「不会再有命令到达服务器」，服务器侧峰值此刻已定论，
//     无需任何等待/超时即可断言；
//  4. 放行全部、Close 排空，返回（扣住阶段的服务器峰值, 排空后的服务器峰值）。
//
// 为什么不用「放行一批、再看下一批」的轮转握手：放行出去的 permit 会被**后续迟到到达**
// 的命令顺手消费掉，服务器侧在途数就永远超不过 capN——变异实测正是如此存活。
func frsSemHoldAndCheck(t *testing.T, u *Upstash, s *frsServer, g *frsGate, capN, total int) (held, peak int) {
	t.Helper()
	for i := 0; i < total; i++ {
		u.SetBind(fmt.Sprintf("g%d", i), "uid", time.Minute)
	}
	for i := 0; i < capN; i++ {
		frsAwaitRecv(t, g.arrived, fmt.Sprintf("第 %d 条在途命令到达服务器", i+1))
	}

	held = g.max()
	if got := len(u.sem); got != capN {
		t.Fatalf("在途写占用的写槽数 got %d want %d（写槽失效：%d 笔写不受上限约束）",
			got, capN, total)
	}

	g.releaseAll()
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := len(s.named("SET")); got != total {
		t.Fatalf("Close 返回后必须送达全部 %d 笔写，实际 %d 笔", total, got)
	}
	return held, g.max()
}

// TestFRSWriteSemaphoreCapsInFlightWrites 钉住 goWrite 的写槽语义：提交 3×8=24 笔写
// （远超上限），服务器观测到的最大同时在途命令数必须**恰好等于 8**，且严格小于提交数。
// 删掉 `u.sem <- struct{}{}` 与释放后：①8 条命令在途时写槽占用为 0（应等于 8）→ 先红；
// ②排空阶段 24 笔写不受约束地并发打到服务器，峰值也会超过 8。
func TestFRSWriteSemaphoreCapsInFlightWrites(t *testing.T) {
	const wantInFlight = 8 // 手写字面量，必须与生产常量 writeConcurrencyLimit 同值
	const total = 24       // 3 × 8

	s, g := frsStartGated(t, total)
	u := frsUpstash(t, s) // sem 容量取自 writeConcurrencyLimit

	// 前置条件：夹具的写槽容量必须就是期望值（生产常量被误改时在此红，而不是静默跑偏）。
	if got := cap(u.sem); got != wantInFlight {
		t.Fatalf("前置条件：写槽容量 got %d want %d", got, wantInFlight)
	}

	held, peak := frsSemHoldAndCheck(t, u, s, g, wantInFlight, total)

	if held != wantInFlight {
		t.Fatalf("扣住阶段服务器观测到的同时在途写 got %d want %d", held, wantInFlight)
	}
	if peak != wantInFlight {
		t.Fatalf("服务器观测到的最大同时在途写 got %d want %d（写槽未生效：写并发打到服务器）", peak, wantInFlight)
	}
	if peak >= total {
		t.Fatalf("最大同时在途写 got %d，必须严格小于提交数 %d", peak, total)
	}
}

// TestFRSWriteSemaphoreSerializesWhenCapOne 钉住写槽 cap=1 的串行语义：同一时刻服务器
// 只被一条写命令占用（前一条未放行，后一条不得到达）。写槽失效时 4 笔会同时在途 → 红。
func TestFRSWriteSemaphoreSerializesWhenCapOne(t *testing.T) {
	const wantInFlight = 1 // 手写字面量，不引用生产常量
	const total = 4

	s, g := frsStartGated(t, total)
	u := &Upstash{
		client: frsGateClient(t, s),
		sem:    make(chan struct{}, wantInFlight),
		done:   make(chan struct{}),
	}

	held, peak := frsSemHoldAndCheck(t, u, s, g, wantInFlight, total)

	if held != wantInFlight {
		t.Fatalf("cap=1 扣住阶段服务器观测到的同时在途写 got %d want 1", held)
	}
	if peak != wantInFlight {
		t.Fatalf("cap=1 时最大同时在途写 got %d want 1（串行语义失效）", peak)
	}
	if peak >= total {
		t.Fatalf("最大同时在途写 got %d，必须严格小于提交数 %d", peak, total)
	}
}

// ---------------------------------------------------------------------------
// New 的探活成功 / 失败降级
// ---------------------------------------------------------------------------

// TestFRSNewPingsAndBuildsUpstashWithWiredSemaphore 钉住 New 成功路径：
// 必须真的 Ping 探活、返回 *Upstash 且 sem/done 已按 writeConcurrencyLimit 装配
// （不是靠 closeOnceGuard 事后兜底 cap=1）。
func TestFRSNewPingsAndBuildsUpstashWithWiredSemaphore(t *testing.T) {
	s := frsStart(t, nil)

	st := New(s.url(), "")
	u, ok := st.(*Upstash)
	if !ok {
		t.Fatalf("可达的 redis 应返回 *Upstash，实际 %T", st)
	}
	t.Cleanup(func() { _ = u.Close() })

	frsEq(t, "PING 探活", frsSingle(t, s, "PING"), []string{"ping"})
	if u.client == nil {
		t.Fatalf("New 必须装配 client")
	}
	if u.sem == nil || u.done == nil {
		t.Fatalf("New 必须初始化 sem/done（sem=%v done=%v）", u.sem, u.done)
	}
	// 期望值是**手写字面量 8**，故意不写成 writeConcurrencyLimit：拿被测常量当期望
	// 等于断言恒真，常量被误改成 3 也照样通过（变异测试实测存活）。改常量必须在此红。
	if got := cap(u.sem); got != 8 {
		t.Fatalf("sem 容量 got %d want 8（写并发上限的字面期望值；生产常量被改动时这里必须红）", got)
	}

	// 端到端：New 出来的实例写一条绑定，服务器必须收到带 7d TTL 的 SET。
	u.SetBind("from-new", "uid-9", 0)
	if err := u.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	frsEq(t, "SET 经 New 的实例", frsSingle(t, s, "SET"),
		[]string{"set", "yg-gateway:bind:from-new", "uid-9", "ex", "604800"})
}

// TestFRSNewPingFailureDegradesToNoop 钉住硬性降级要求：Redis 不可达时 New 不得返回
// 真实现（否则调用方会在每次写时打到死连接），必须降级 Noop。
func TestFRSNewPingFailureDegradesToNoop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // 握手即断：探活必然失败，端口始终被本测试持有
		}
	}()

	st := New("redis://"+ln.Addr().String(), "")
	_ = ln.Close()
	wg.Wait()

	if _, ok := st.(Noop); !ok {
		t.Fatalf("探活失败必须降级 Noop，实际 %T", st)
	}
}

// TestFRSNoopContract 钉住 Noop 降级契约：全空实现，且 LoadBinds 返回 nil
// （与 Upstash 的「非 nil 空映射」明确区分，调用方据此可判断是否真接了 Redis）。
func TestFRSNoopContract(t *testing.T) {
	var st Store = Noop{}

	st.SetBind("k", "u", time.Minute)
	st.DelBind("k")
	st.SaveState([]byte("{}"))
	if got := st.LoadBinds(); got != nil {
		t.Fatalf("Noop.LoadBinds 应为 nil，实际 %v", got)
	}
	if data, ok := st.LoadState(); ok || data != nil {
		t.Fatalf("Noop.LoadState 应为 (nil,false)，实际 (%q,%v)", data, ok)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Noop.Close 应为 nil，实际 %v", err)
	}
}
