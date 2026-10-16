// coverage_test.go 补齐 internal/session 的覆盖率（Task 5）。
//
// 约定（本文件独立于既有测试，helper 一律 csess 前缀，避免与既有文件冲突）：
//   - 只新增本文件；不改生产代码、不改既有测试、不动 go.mod/go.sum；
//   - 期望值全部是**独立手写字面量**（不调用被测函数反推期望值），
//     需要复算派生键时用 csessSha256Hex16 独立复述算法（sha256(sep) 前 16 hex）；
//   - 时间确定性：需要"已过期"时把 lastActive 写成固定过去时刻（csessPast），
//     需要"注入时钟"时用 gcOnce(now)；绝不 sleep 等待、不联网、不依赖 map 迭代顺序
//     （需要顺序处显式 sort，或断言"数量 + 成员"而非顺序）；
//   - 并发用例用 channel 屏障（close(start)）同步，断言的是设计不变量。
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"yg-gateway/internal/redisstore"
)

// ── 固定时刻（与真实时钟无关，供"必定过期"与序列化断言使用）─────────────────

// csessPast 固定过去时刻：lastActive 设成它即与真实时钟无关地判定为过期。
var csessPast = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// csessTimeA / csessTimeB 固定时刻，快照序列化断言用（RFC3339 字面量见下）。
var (
	csessTimeA = time.Date(2030, time.May, 4, 3, 2, 1, 0, time.UTC)
	csessTimeB = time.Date(2031, time.January, 2, 3, 4, 5, 0, time.UTC)
)

// csessSha256Hex16 独立复述"派生键"算法：sha256(s) 前 16 字节的 hex。
// 与被测实现零共享代码——算法（分隔符/取字节数）一旦被改动，期望字面量立即失配。
func csessSha256Hex16(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}

// ── 假 Store（记录镜像调用，不联网）───────────────────────────────────────

type csessStore struct {
	redisstore.Noop
	mu     sync.Mutex
	setMap map[string]string
	setN   int
	delKey []string
}

func newCSSessStore() *csessStore {
	return &csessStore{setMap: map[string]string{}}
}

func (c *csessStore) SetBind(key, uid string, ttl time.Duration) {
	c.mu.Lock()
	c.setN++
	c.setMap[key] = uid
	c.mu.Unlock()
}

func (c *csessStore) DelBind(key string) {
	c.mu.Lock()
	c.delKey = append(c.delKey, key)
	delete(c.setMap, key)
	c.mu.Unlock()
}

func (c *csessStore) LoadBinds() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for k, v := range c.setMap {
		out[k] = v
	}
	return out
}

// set 直接写入镜像（不计数），用于构造"Redis 里有、内存里没有/冲突"的场景。
func (c *csessStore) set(key, uid string) {
	c.mu.Lock()
	c.setMap[key] = uid
	c.mu.Unlock()
}

func (c *csessStore) delKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.delKey))
	copy(out, c.delKey)
	return out
}

func (c *csessStore) uidOf(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	u, ok := c.setMap[key]
	return u, ok
}

// csessSignalStore 把 DelBind 变成事件信号（后台 GC 用例用事件驱动同步，非 sleep）。
type csessSignalStore struct {
	redisstore.Noop
	del chan string
}

func (c *csessSignalStore) DelBind(key string) {
	select {
	case c.del <- key:
	default: // 已有一条待读信号：丢弃重复（用例只关心"发生过"）
	}
}

// ── 包内断言 helper ────────────────────────────────────────────────────────

func csessEntry(t *testing.T, r *Router, key string) (entry, bool) {
	t.Helper()
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[key]
	return e, ok
}

func csessSetEntry(r *Router, key string, e entry) {
	r.mu.Lock()
	r.entries[key] = e
	r.mu.Unlock()
}

func csessStopChan(r *Router) chan struct{} {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stop
}

// csessBody / csessMsg 构造 deriveKey 直接入参（map[string]any 形态）。
func csessBody(msgs []any) map[string]any {
	return map[string]any{"messages": msgs}
}

func csessMsg(role string, content any) map[string]any {
	return map[string]any{"role": role, "content": content}
}

// ── 1. 构造与兜底 ──────────────────────────────────────────────────────────

// TestCSSessNewDefaultsAndNilStoreFallback 零值 Config 的兜底：TTL 30m、GCInterval 5m、
// nil Store → Noop（否则首次 Bind 直接空指针 panic）。
func TestCSSessNewDefaultsAndNilStoreFallback(t *testing.T) {
	r := New(Config{})
	if r == nil {
		t.Fatal("New 不得返回 nil")
	}
	if got, want := r.cfg.TTL, time.Duration(30)*time.Minute; got != want {
		t.Errorf("零值 Config 的 TTL = %v，want %v（兜底缺失 → 绑定永不因默认 TTL 过期）", got, want)
	}
	if got, want := r.cfg.GCInterval, time.Duration(5)*time.Minute; got != want {
		t.Errorf("零值 Config 的 GCInterval = %v，want %v", got, want)
	}
	if r.cfg.Store == nil {
		t.Fatal("New 必须把 nil Store 兜底为 Noop（否则 Bind 立即 panic）")
	}
	if r.Count() != 0 {
		t.Errorf("新建 router 的 Count = %d，want 0", r.Count())
	}
	// 兜底生效：nil Store 下 Bind/Unbind 不得 panic，且内存绑定照常建立
	r.Bind("sess-x", "uid-x")
	if e, ok := csessEntry(t, r, "sess-x"); !ok || e.uid != "uid-x" {
		t.Fatalf("nil Store 兜底后 Bind 失效：uid=%q ok=%v", e.uid, ok)
	}
	r.Unbind("sess-x")
	if r.Count() != 0 {
		t.Errorf("Unbind 后 Count = %d，want 0", r.Count())
	}
	// 显式值必须原样保留（不得被兜底覆盖）
	r2 := New(Config{TTL: time.Duration(7) * time.Second, GCInterval: time.Duration(3) * time.Second})
	if r2.cfg.TTL != time.Duration(7)*time.Second || r2.cfg.GCInterval != time.Duration(3)*time.Second {
		t.Errorf("显式 TTL/GCInterval 被兜底覆盖：%v / %v", r2.cfg.TTL, r2.cfg.GCInterval)
	}
}

// TestCSSessStickyLifecycleBindHitExpireUnbind 键派生与粘性绑定全生命周期：
// 绑定 → 命中 → 过期重分配 → 解绑 → 再分配。
func TestCSSessStickyLifecycleBindHitExpireUnbind(t *testing.T) {
	st := newCSSessStore()
	avail := []string{"uid-a1"}
	r := New(Config{
		TTL:       time.Duration(1) * time.Hour,
		Store:     st,
		Available: func() []string { return avail },
	})

	// ① 显式绑定 → 命中同一号（并滚动 lastActive + 镜像）
	r.Bind("sess-l", "uid-a1")
	if u, ok := r.Resolve("sess-l"); !ok || u != "uid-a1" {
		t.Fatalf("① 绑定后解析 = (%q,%v)，want (\"uid-a1\",true)", u, ok)
	}
	if u, _ := st.uidOf("sess-l"); u != "uid-a1" {
		t.Fatalf("① 绑定镜像 = %q，want uid-a1", u)
	}

	// ② 把 lastActive 改成固定过去时刻（与真实时钟无关地过期）；池里只剩另一个号 →
	//    必须换号并镜像删除旧绑定。
	csessSetEntry(r, "sess-l", entry{uid: "uid-a1", lastActive: csessPast})
	avail = []string{"uid-b2"}
	if u, ok := r.Resolve("sess-l"); !ok || u != "uid-b2" {
		t.Fatalf("② 过期绑定 = (%q,%v)，want (\"uid-b2\",true)——仍返回 uid-a1 说明快路径漏了 TTL 检查", u, ok)
	}
	if r.Count() != 1 {
		t.Errorf("② 过期重分配后 Count = %d，want 1（失效条目不得残留）", r.Count())
	}
	if got := st.delKeys(); len(got) != 1 || got[0] != "sess-l" {
		t.Errorf("② 过期重分配必须镜像 DelBind：%v，want [sess-l]", got)
	}

	// ③ 解绑 → 条目与镜像都要消失，下次解析重新分配（不得沿用旧号）
	if !r.Unbind("sess-l") {
		t.Fatal("③ Unbind 存在的绑定应返回 true")
	}
	if _, ok := csessEntry(t, r, "sess-l"); ok {
		t.Error("③ Unbind 必须删除内存条目")
	}
	if _, ok := st.uidOf("sess-l"); ok {
		t.Error("③ Unbind 必须删除镜像绑定")
	}
	avail = []string{"uid-c3"}
	if u, ok := r.Resolve("sess-l"); !ok || u != "uid-c3" {
		t.Fatalf("③ 解绑后解析 = (%q,%v)，want (\"uid-c3\",true)——沿用旧号说明 Unbind 没删干净", u, ok)
	}
	if r.Count() != 1 {
		t.Errorf("③ 重新分配后 Count = %d，want 1", r.Count())
	}
}

// TestCSSessUnbindMissingDoesNotMirrorDelete 未命中的 Unbind 必须是纯 no-op（尤其不得镜像删除）。
func TestCSSessUnbindMissingDoesNotMirrorDelete(t *testing.T) {
	st := newCSSessStore()
	r := New(Config{TTL: time.Hour, Store: st, Available: func() []string { return []string{"uid-a1"} }})
	if r.Unbind("nope") {
		t.Error("Unbind 不存在的 key 应返回 false")
	}
	if got := st.delKeys(); len(got) != 0 {
		t.Errorf("未命中的 Unbind 不得镜像 DelBind，实际 %v", got)
	}
	// 空 key / 空 uid 的 Bind 是 no-op（无会话则不绑）
	r.Bind("", "uid-a1")
	r.Bind("sess-z", "")
	if r.Count() != 0 {
		t.Errorf("Bind 空 key/空 uid 必须 no-op，Count = %d，want 0", r.Count())
	}
}

// TestCSSessGCTTLBoundaryIsStrictlyGreater GC 的 TTL 边界：恰好等于 TTL 必须保留，
// 超过 1ns 才清理（用 >= 会误删刚刚活跃的会话）。
func TestCSSessGCTTLBoundaryIsStrictlyGreater(t *testing.T) {
	st := newCSSessStore()
	ttl := time.Duration(60) * time.Second
	r := New(Config{TTL: ttl, Store: st, Available: func() []string { return []string{"uid-a1"} }})
	now := time.Date(2040, time.July, 6, 5, 4, 3, 0, time.UTC)
	csessSetEntry(r, "exactly-ttl", entry{uid: "uid-a1", lastActive: now.Add(-ttl)})
	csessSetEntry(r, "just-over-ttl", entry{uid: "uid-a1", lastActive: now.Add(-ttl - time.Nanosecond)})

	if got := r.gcOnce(now); got != 1 {
		t.Fatalf("gcOnce 清理数 = %d，want 1（恰好 TTL 的绑定必须保留；用 >= 比较会误删活跃会话）", got)
	}
	if _, ok := csessEntry(t, r, "exactly-ttl"); !ok {
		t.Error("恰好等于 TTL 的绑定被误删")
	}
	if _, ok := csessEntry(t, r, "just-over-ttl"); ok {
		t.Error("超过 TTL 1ns 的绑定未被清理")
	}
	if got := st.delKeys(); len(got) != 1 || got[0] != "just-over-ttl" {
		t.Errorf("镜像删除 = %v，want [just-over-ttl]", got)
	}
	if r.Count() != 1 {
		t.Errorf("Count = %d，want 1", r.Count())
	}
}

// TestCSSessExpiredBoundaryIsStrictlyGreater expired() 纯函数的 TTL 边界与零值语义。
func TestCSSessExpiredBoundaryIsStrictlyGreater(t *testing.T) {
	base := time.Date(2040, time.July, 6, 5, 4, 3, 0, time.UTC)
	ttl := time.Duration(60) * time.Second
	e := entry{uid: "uid-a1", lastActive: base}
	if expired(e, base, ttl) {
		t.Error("差值为 0 不得算过期")
	}
	if expired(e, base.Add(ttl), ttl) {
		t.Error("恰好等于 TTL 不得算过期（判据必须是严格大于）")
	}
	if !expired(e, base.Add(ttl+time.Nanosecond), ttl) {
		t.Error("超过 TTL 1ns 必须算过期")
	}
	// 零值 entry（lastActive 为零时刻）在正 TTL 下必定过期：零值不得被当成"刚刚活跃"
	if !expired(entry{}, base, ttl) {
		t.Error("零值 entry 必须算过期，否则零值绑定会永久粘住账号")
	}
	if expired(entry{}, time.Time{}, ttl) {
		t.Error("两个零时刻之差为 0，不大于 TTL")
	}
}

// TestCSSessStartGCRunsAndStopGCClosesGC 后台 GC 的启停契约：
// StartGC 幂等、后台真的跑过一次（事件驱动等待）、StopGC close(stop) 且可重启。
func TestCSSessStartGCRunsAndStopGCClosesGC(t *testing.T) {
	sig := &csessSignalStore{del: make(chan string, 1)}
	r := New(Config{
		TTL:        time.Hour,
		GCInterval: time.Duration(1) * time.Millisecond,
		Store:      sig,
		Available:  func() []string { return []string{"uid-a1"} },
	})
	csessSetEntry(r, "stale", entry{uid: "uid-a1", lastActive: csessPast})

	r.StartGC()
	defer r.StopGC() // 失败路径也不留下运行中的 GC goroutine

	st1 := csessStopChan(r)
	if st1 == nil {
		t.Fatal("StartGC 必须置位 stop channel")
	}
	r.StartGC() // 幂等：不得新建 channel / 再起一个 goroutine
	if st2 := csessStopChan(r); st2 != st1 {
		t.Fatal("StartGC 必须幂等（重复调用换了 channel 说明起了第二个 GC goroutine）")
	}

	// 后台 GC 是否真的清理过期绑定：等事件，不 sleep 轮询
	select {
	case key := <-sig.del:
		if key != "stale" {
			t.Fatalf("后台 GC 镜像删除的 key = %q，want \"stale\"", key)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("后台 GC 未在 10s 内清理过期绑定（ticker 分支没跑到）")
	}
	if r.Count() != 0 {
		t.Errorf("后台 GC 后 Count = %d，want 0", r.Count())
	}

	r.StopGC()
	select {
	case <-st1:
	default:
		t.Fatal("StopGC 未 close(stop)：goroutine 再也退不出（关停失效）")
	}
	if got := csessStopChan(r); got != nil {
		t.Error("StopGC 后 r.stop 必须为 nil（否则无法重启 GC）")
	}
	r.StopGC() // 幂等：重复 close 会 panic

	r.StartGC()
	st3 := csessStopChan(r)
	if st3 == nil || st3 == st1 {
		t.Fatal("StopGC 之后必须能重新启动 GC（新 channel）")
	}
	r.StopGC()
}

// TestCSSessLoadFromStoreBranches LoadFromStore 的三态：空镜像不写、本地权威不被覆盖、无冲突时恢复。
func TestCSSessLoadFromStoreBranches(t *testing.T) {
	// ① 空镜像：不得产生任何绑定（否则空 uid 占坑，会话被钉到无效号）
	empty := newCSSessStore()
	r := New(Config{TTL: time.Hour, Store: empty, Available: func() []string { return []string{"uid-a1"} }})
	r.LoadFromStore()
	if r.Count() != 0 {
		t.Fatalf("空 Store.LoadBinds 不得产生绑定，Count = %d，want 0", r.Count())
	}

	// ② 本地绑定为权威：Redis 值不得覆盖
	local := newCSSessStore()
	local.set("sess-l", "uid-redis")
	r2 := New(Config{TTL: time.Hour, Store: local, Available: func() []string { return []string{"uid-a1", "uid-redis"} }})
	r2.Bind("sess-l", "uid-a1")
	r2.LoadFromStore()
	if e, ok := csessEntry(t, r2, "sess-l"); !ok || e.uid != "uid-a1" {
		t.Fatalf("Redis 恢复覆盖了本地绑定：uid=%q ok=%v，want uid-a1", e.uid, ok)
	}
	if r2.Count() != 1 {
		t.Errorf("② Count = %d，want 1", r2.Count())
	}

	// ③ 无本地冲突时按 Redis 恢复，且恢复后的会话直接命中
	local.set("sess-r", "uid-redis")
	r2.LoadFromStore()
	if e, ok := csessEntry(t, r2, "sess-r"); !ok || e.uid != "uid-redis" {
		t.Fatalf("未冲突的 Redis 绑定未被恢复：uid=%q ok=%v", e.uid, ok)
	}
	if u, ok := r2.Resolve("sess-r"); !ok || u != "uid-redis" {
		t.Errorf("③ 恢复后的会话解析 = (%q,%v)，want (\"uid-redis\",true)", u, ok)
	}
}

// ── 2. 可用性维度（模型维度 / 空池）──────────────────────────────────────

// TestCSSessAvailableForModelDrivesReassignment 绑定校验必须带模型维度：
// 同一会话换到"绑定号不可用"的模型时必须换号（issue #31「限额后换不动号」的机制根因）。
func TestCSSessAvailableForModelDrivesReassignment(t *testing.T) {
	byModel := map[string][]string{
		"cn:glm-5.2": {"uid-a1"},
		"cn:glm-5.3": {"uid-b2"},
	}
	r := New(Config{
		TTL:               time.Duration(1) * time.Hour,
		Store:             newCSSessStore(),
		AvailableForModel: func(model string) []string { return byModel[model] },
	})

	if u, ok := r.ResolveForModel("sess-m", "cn:glm-5.2"); !ok || u != "uid-a1" {
		t.Fatalf("首次解析 = (%q,%v)，want (\"uid-a1\",true)", u, ok)
	}
	if u, ok := r.ResolveForModel("sess-m", "cn:glm-5.3"); !ok || u != "uid-b2" {
		t.Fatalf("换模型后 = (%q,%v)，want (\"uid-b2\",true)——仍是 uid-a1 说明绑定校验忽略了模型维度", u, ok)
	}
	if u, ok := r.ResolveForModel("sess-m", "cn:glm-5.2"); !ok || u != "uid-a1" {
		t.Fatalf("换回原模型 = (%q,%v)，want (\"uid-a1\",true)", u, ok)
	}
	if r.Count() != 1 {
		t.Errorf("换号不得产生重复条目，Count = %d，want 1", r.Count())
	}
}

// TestCSSessAvailableForModelPrecedenceAndArgForwarding AvailableForModel 优先于 Available，
// 且 Resolve 必须把空模型（"无模型维度"）转发给它，而不是偷换账号级列表。
func TestCSSessAvailableForModelPrecedenceAndArgForwarding(t *testing.T) {
	var mu sync.Mutex
	var models []string
	fallbackCalls := 0
	r := New(Config{
		TTL:   time.Duration(1) * time.Hour,
		Store: newCSSessStore(),
		Available: func() []string {
			mu.Lock()
			fallbackCalls++
			mu.Unlock()
			return []string{"uid-fallback"}
		},
		AvailableForModel: func(model string) []string {
			mu.Lock()
			models = append(models, model)
			mu.Unlock()
			return []string{"uid-model-only"}
		},
	})

	if u, ok := r.ResolveForModel("sess-p", "m-alpha"); !ok || u != "uid-model-only" {
		t.Fatalf("模型维度列表必须优先：= (%q,%v)，want (\"uid-model-only\",true)", u, ok)
	}
	if _, ok := r.Resolve("sess-q"); !ok {
		t.Fatal("Resolve 必须等价于 ResolveForModel(key, \"\")，不得失败")
	}

	mu.Lock()
	defer mu.Unlock()
	if fallbackCalls != 0 {
		t.Errorf("注入了 AvailableForModel 时不得再调 Available（fallbackCalls = %d）", fallbackCalls)
	}
	hasAlpha, hasEmpty := false, false
	for _, m := range models {
		switch m {
		case "m-alpha":
			hasAlpha = true
		case "":
			hasEmpty = true
		default:
			t.Errorf("向 AvailableForModel 传入了非预期模型 %q", m)
		}
	}
	if !hasAlpha {
		t.Error("AvailableForModel 未收到请求模型 m-alpha")
	}
	if !hasEmpty {
		t.Error("Resolve 必须把空模型转发给 AvailableForModel（不得静默改用账号级列表）")
	}
}

// TestCSSessEmptyPoolYieldsNotFound 空池（nil Available / nil 模型列表）必须报"无可用"，
// 且不得留下脏绑定；池恢复后同一 key 能正常分配。
func TestCSSessEmptyPoolYieldsNotFound(t *testing.T) {
	r := New(Config{TTL: time.Duration(1) * time.Hour, Store: newCSSessStore()})
	if u, ok := r.Resolve("sess-e"); ok || u != "" {
		t.Errorf("nil Available 即空池：= (%q,%v)，want (\"\",false)", u, ok)
	}
	if u, ok := r.ResolveForModel("sess-e", "m"); ok || u != "" {
		t.Errorf("空池的 ResolveForModel = (%q,%v)，want (\"\",false)", u, ok)
	}
	if r.Count() != 0 {
		t.Errorf("空池不得留下绑定，Count = %d，want 0", r.Count())
	}

	r2 := New(Config{
		TTL:               time.Hour,
		Store:             newCSSessStore(),
		AvailableForModel: func(string) []string { return nil },
	})
	if u, ok := r2.Resolve("sess-e"); ok || u != "" {
		t.Errorf("AvailableForModel 返回 nil 同样是空池：= (%q,%v)，want (\"\",false)", u, ok)
	}

	r.cfg.Available = func() []string { return []string{"uid-late"} }
	if u, ok := r.Resolve("sess-e"); !ok || u != "uid-late" {
		t.Errorf("池恢复后必须能分配：= (%q,%v)，want (\"uid-late\",true)", u, ok)
	}
}

// ── 3. 快照与序列化一致性 ────────────────────────────────────────────────

// TestCSSessSnapshotIsIndependentCopyAndSerializes 快照与内部 map 不共享可写引用，
// 且序列化字段/格式是面板契约（key / uid / last_active，RFC3339）。
func TestCSSessSnapshotIsIndependentCopyAndSerializes(t *testing.T) {
	r := New(Config{TTL: time.Hour, Store: newCSSessStore(), Available: func() []string { return []string{"uid-a1"} }})
	// 空 router：必须是"空但不是 nil"（否则面板 JSON 出 null，消费方要把 null 当"未知"）
	if got := r.Snapshot(); got == nil || len(got) != 0 {
		t.Fatalf("空 router 的 Snapshot = %#v，want 非 nil 空切片", got)
	}

	csessSetEntry(r, "sess-a", entry{uid: "uid-a1", lastActive: csessTimeA})
	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot 条数 = %d，want 1", len(snap))
	}
	if snap[0].Key != "sess-a" || snap[0].UID != "uid-a1" || !snap[0].LastActive.Equal(csessTimeA) {
		t.Fatalf("Snapshot[0] = %+v，want key=sess-a uid=uid-a1 last_active=%v", snap[0], csessTimeA)
	}

	// 改快照不得改 router（共享可写引用会让调用方污染粘性状态）
	snap[0].UID = "uid-injected"
	if e, _ := csessEntry(t, r, "sess-a"); e.uid != "uid-a1" {
		t.Fatal("Snapshot 与内部状态共享可写引用：改快照改到了 router")
	}
	// 反方向：后续绑定不得改变已取快照的长度（共享底层数组会越界/串味）
	csessSetEntry(r, "sess-b", entry{uid: "uid-b2", lastActive: csessTimeB})
	if len(snap) != 1 {
		t.Fatal("已取快照被后续绑定改变（共享底层数组）")
	}

	got := r.Snapshot()
	sort.Slice(got, func(i, j int) bool { return got[i].Key < got[j].Key })
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Snapshot 序列化失败：%v", err)
	}
	want := `[{"key":"sess-a","uid":"uid-a1","last_active":"2030-05-04T03:02:01Z"},` +
		`{"key":"sess-b","uid":"uid-b2","last_active":"2031-01-02T03:04:05Z"}]`
	if string(raw) != want {
		t.Errorf("Snapshot 序列化 = %s\nwant %s", raw, want)
	}
}

// ── 4. 并发读写 ──────────────────────────────────────────────────────────

// TestCSSessConcurrentResolveIsStable 并发同 key 解析恒同号（慢路径 re-check 防重复分配），
// 同时并发读 Count/Snapshot 与增删绑定，压 RWMutex；结束后内部状态自洽。
func TestCSSessConcurrentResolveIsStable(t *testing.T) {
	pool := []string{"uid-p1", "uid-p2", "uid-p3"}
	r := New(Config{
		TTL:       time.Duration(1) * time.Hour,
		Store:     newCSSessStore(),
		Available: func() []string { return pool },
	})

	const workers = 24
	const rounds = 25
	start := make(chan struct{}) // 屏障：让所有 worker 尽量同时进入热路径
	got := make([][]string, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			local := make([]string, 0, rounds)
			for j := 0; j < rounds; j++ {
				u, ok := r.Resolve("hot")
				if !ok {
					local = append(local, "<not-ok>")
					continue
				}
				local = append(local, u)
				r.Count()
				r.Snapshot()
				k := fmt.Sprintf("cold-%d-%d", idx, j)
				r.Bind(k, pool[idx%len(pool)])
				r.Unbind(k)
			}
			got[idx] = local
		}(i)
	}
	close(start)
	wg.Wait()

	first := ""
	for i, seq := range got {
		if len(seq) != rounds {
			t.Fatalf("worker %d 结果残缺：%v", i, seq)
		}
		for _, u := range seq {
			if u == "<not-ok>" {
				t.Fatalf("worker %d 在池非空时解析失败", i)
			}
			if first == "" {
				first = u
			}
			if u != first {
				t.Fatalf("同 key 并发解析出现不同账号：%q vs %q（慢路径 re-check 失效）", first, u)
			}
		}
	}
	inPool := false
	for _, u := range pool {
		if u == first {
			inPool = true
		}
	}
	if !inPool {
		t.Fatalf("解析结果 %q 不在可用池 %v 内", first, pool)
	}
	// 风暴后自洽：Count 与 Snapshot 长度一致、热键绑定未被冷键churn 改写
	snap := r.Snapshot()
	if r.Count() != len(snap) {
		t.Errorf("Count = %d 与 Snapshot 长度 %d 不一致（读路径锁不一致）", r.Count(), len(snap))
	}
	if e, ok := csessEntry(t, r, "hot"); !ok || e.uid != first {
		t.Errorf("风暴后热键绑定 = (%q,%v)，want (%q,true)", e.uid, ok, first)
	}
}

// ── 5. 视图与键派生的"未知即空" ─────────────────────────────────────────

// TestCSSessEmptyAndNilInputsStayUnknown 空 body / nil 视图 / 空 key：一律"未知即空"，
// 不得伪造键；同时 router 对同一 key（含空串）必须保持幂等。
func TestCSSessEmptyAndNilInputsStayUnknown(t *testing.T) {
	r := New(Config{
		TTL:       time.Hour,
		Store:     newCSSessStore(),
		Available: func() []string { return []string{"uid-only"} },
	})
	u1, ok1 := r.Resolve("")
	u2, ok2 := r.Resolve("")
	if !ok1 || !ok2 {
		t.Fatalf("空 key 解析不应失败：(%v,%v)", ok1, ok2)
	}
	if u1 != u2 || u1 != "uid-only" {
		t.Fatalf("空 key 的幂等性被破坏：%q vs %q（反复换号会打散同会话流量）", u1, u2)
	}
	if r.Count() != 1 {
		t.Errorf("Count = %d，want 1", r.Count())
	}

	if got := ExtractKey(nil); got != "" {
		t.Errorf("nil body 的会话键 = %q，want \"\"", got)
	}
	if got := ExtractKey([]byte("")); got != "" {
		t.Errorf("空 body 的会话键 = %q，want \"\"", got)
	}
	if got := ExtractKeyOptView(nil, true); got != "" {
		t.Errorf("nil 视图的会话键 = %q，want \"\"", got)
	}
	if got := ResolveConversationIDView(nil); got != "" {
		t.Errorf("nil 视图的 conversationId = %q，want \"\"", got)
	}
	if got := TurnKeyView(nil); got != "" {
		t.Errorf("nil 视图的轮级键 = %q，want \"\"", got)
	}
	// 反面对照：非空 body 必须给出非空派生键（上面的空值是"未知"，不是"没有"）
	if got := ExtractKey([]byte(`{"messages":[{"role":"user","content":"x"}]}`)); got == "" {
		t.Error("有 messages 的 body 派生键不得为空")
	}
}

// TestCSSessRequestViewNilSafeAccessorsAndZeroValue nil 接收者与零值视图：字符串类访问器
// 必须零值安全；零值视图（解析失败语义）全空。
func TestCSSessRequestViewNilSafeAccessorsAndZeroValue(t *testing.T) {
	var nilView *RequestView
	if got := nilView.Raw("model"); got != nil {
		t.Errorf("nil 视图 Raw = %s，want nil", got)
	}
	if got := nilView.Str("model"); got != "" {
		t.Errorf("nil 视图 Str = %q，want \"\"", got)
	}
	if got := nilView.Model(); got != nil {
		t.Errorf("nil 视图 Model = %s，want nil", got)
	}
	if got := nilView.ModelStr(); got != "" {
		t.Errorf("nil 视图 ModelStr = %q，want \"\"", got)
	}
	if nilView.Stream() {
		t.Error("nil 视图 Stream 必须为 false")
	}

	var zero RequestView
	if got := zero.Raw("model"); got != nil {
		t.Errorf("零值视图 Raw = %s，want nil", got)
	}
	if got := zero.Str("conversation_id"); got != "" {
		t.Errorf("零值视图 Str = %q，want \"\"", got)
	}
	if got := zero.Meta(); got != nil {
		t.Errorf("零值视图 Meta = %v，want nil", got)
	}
	if got := zero.MetaStr("conversation_id"); got != "" {
		t.Errorf("零值视图 MetaStr = %q，want \"\"", got)
	}
	if got := zero.Messages(); got != nil {
		t.Errorf("零值视图 Messages = %v，want nil", got)
	}
	if got := zero.Tools(); got != nil {
		t.Errorf("零值视图 Tools = %v，want nil", got)
	}
	if got := zero.Functions(); got != nil {
		t.Errorf("零值视图 Functions = %v，want nil", got)
	}
	if zero.Stream() {
		t.Error("零值视图 Stream 必须为 false")
	}

	if _, ok := ParseRequestView(nil); ok {
		t.Error("nil body 必须解析失败")
	}
	if _, ok := ParseRequestView([]byte(`[1,2]`)); ok {
		t.Error("顶层非对象 JSON 必须解析失败")
	}
}

// TestCSSessRequestViewMalformedSubtreesAreUnknown 非对象子树是"未知"（nil），
// 不得退化成"存在但为空"（会被消费方误判为"有元数据/无工具"）。
func TestCSSessRequestViewMalformedSubtreesAreUnknown(t *testing.T) {
	v, ok := ParseRequestView([]byte(`{"metadata":"x","messages":"y","tools":"z","functions":42}`))
	if !ok {
		t.Fatal("顶层对象应能解析")
	}
	if got := v.Meta(); got != nil {
		t.Errorf("metadata 非对象：Meta = %v，want nil", got)
	}
	if got := v.Meta(); got != nil {
		t.Errorf("metadata 非对象：Meta 二次调用（缓存路径）= %v，want nil", got)
	}
	if got := v.MetaStr("conversation_id"); got != "" {
		t.Errorf("metadata 非对象：MetaStr = %q，want \"\"", got)
	}
	if got := v.Messages(); got != nil {
		t.Errorf("messages 非数组：Messages = %v，want nil", got)
	}
	if got := v.Messages(); got != nil {
		t.Errorf("messages 非数组：Messages 二次调用 = %v，want nil", got)
	}
	if got := v.Tools(); got != nil {
		t.Errorf("tools 非数组：Tools = %v，want nil", got)
	}
	if got := v.Tools(); got != nil {
		t.Errorf("tools 非数组：Tools 二次调用 = %v，want nil", got)
	}
	if got := v.Functions(); got != nil {
		t.Errorf("functions 非数组：Functions = %v，want nil", got)
	}
	if got := v.Functions(); got != nil {
		t.Errorf("functions 非数组：Functions 二次调用 = %v，want nil", got)
	}
	if got := ResolveConversationIDView(&v); got != "" {
		t.Errorf("坏子树下 conversationId = %q，want \"\"", got)
	}
	if got := TurnKeyView(&v); got != "" {
		t.Errorf("坏子树下轮级键 = %q，want \"\"", got)
	}

	// 合法子树：长度正确 + 缓存幂等
	v2, ok := ParseRequestView([]byte(`{"metadata":{"conversation_id":"cid-9"},"tools":[{"n":1}],"functions":[{"n":2}]}`))
	if !ok {
		t.Fatal("合法 body 应能解析")
	}
	if got := v2.Tools(); len(got) != 1 {
		t.Errorf("Tools 长度 = %d，want 1", len(got))
	}
	if got := v2.Tools(); len(got) != 1 {
		t.Errorf("Tools 长度（缓存路径）= %d，want 1", len(got))
	}
	if got := v2.Functions(); len(got) != 1 {
		t.Errorf("Functions 长度 = %d，want 1", len(got))
	}
	if got := v2.Functions(); len(got) != 1 {
		t.Errorf("Functions 长度（缓存路径）= %d，want 1", len(got))
	}
	if got := v2.MetaStr("conversation_id"); got != "cid-9" {
		t.Errorf("MetaStr = %q，want cid-9", got)
	}
	if got := ResolveConversationIDView(&v2); got != "cid-9" {
		t.Errorf("ResolveConversationIDView = %q，want cid-9", got)
	}
}

// TestCSSessModelUnknownNeverRendersAsNumber model 的四态：missing / null / string / other。
// 硬纪律：unknown 不得输出数字或占位串——数字 123 必须仍是"未知"，而不是 "123"。
func TestCSSessModelUnknownNeverRendersAsNumber(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantStr   string
		wantRawOn bool
	}{
		{"数字（other → 未知）", `{"model":123}`, "", true},
		{"浮点（other → 未知）", `{"model":1.5}`, "", true},
		{"布尔（other → 未知）", `{"model":true}`, "", true},
		{"对象（other → 未知）", `{"model":{"a":1}}`, "", true},
		{"null", `{"model":null}`, "", true},
		{"missing", `{"stream":true}`, "", false},
		{"string", `{"model":"cn:glm-5.2"}`, "cn:glm-5.2", true},
		{"字符串 \"0\" 是合法观察值，不得当成未知", `{"model":"0"}`, "0", true},
	}
	for _, c := range cases {
		v, ok := ParseRequestView([]byte(c.body))
		if !ok {
			t.Fatalf("%s: 解析失败", c.name)
		}
		if got := v.ModelStr(); got != c.wantStr {
			t.Errorf("%s: ModelStr = %q，want %q（unknown 不得输出数字/占位串）", c.name, got, c.wantStr)
		}
		if got := v.Str("model"); got != c.wantStr {
			t.Errorf("%s: Str(model) = %q，want %q", c.name, got, c.wantStr)
		}
		if present := v.Model() != nil; present != c.wantRawOn {
			t.Errorf("%s: Model() 原始字节存在性 = %v，want %v", c.name, present, c.wantRawOn)
		}
	}

	// 其它会话字段上的数字同样不得被渲染成字符串事实
	v, ok := ParseRequestView([]byte(`{"prompt_cache_key":42,"conversation_id":7,"user_id":9}`))
	if !ok {
		t.Fatal("解析失败")
	}
	if got := v.Str("prompt_cache_key"); got != "" {
		t.Errorf("数字 prompt_cache_key = %q，want \"\"", got)
	}
	if got := v.Str("conversation_id"); got != "" {
		t.Errorf("数字 conversation_id = %q，want \"\"", got)
	}
	if got := v.MetaStr("user_id"); got != "" {
		t.Errorf("metadata 缺失时 MetaStr = %q，want \"\"", got)
	}
	// 数字 conversation_id 不得被当成会话键（"7" 是伪造的事实）
	if got := ExtractKey([]byte(`{"conversation_id":7,"messages":[{"role":"user","content":"z"}]}`)); got == "7" {
		t.Error("数字 conversation_id 被渲染成会话键（unknown 被当成事实）")
	}
}

// TestCSSessStrOrEmptyOnlyAcceptsJSONStrings 类型断言兜底分支：非字符串一律空，
// 不得用 fmt 兜底把数字渲染成字符串。
func TestCSSessStrOrEmptyOnlyAcceptsJSONStrings(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"字符串", "v", "v"},
		{"空字符串", "", ""},
		{"nil", nil, ""},
		{"数字", float64(42), ""},
		{"布尔", true, ""},
		{"对象", map[string]any{"a": "b"}, ""},
		{"数组", []any{"a"}, ""},
		{"raw 字节切片", json.RawMessage(`"x"`), ""},
	}
	for _, c := range cases {
		if got := strOrEmpty(c.in); got != c.want {
			t.Errorf("%s: strOrEmpty(%#v) = %q，want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestCSSessHasUserIDKeyMatchesDeriveGate user 维度标识判定：只有非空**字符串**才算，
// 且与派生闸（两条路径）口径一致。
func TestCSSessHasUserIDKeyMatchesDeriveGate(t *testing.T) {
	cases := []struct {
		name string
		obj  map[string]any
		want bool
	}{
		{"顶层 user_id", map[string]any{"user_id": "u1"}, true},
		{"metadata.user_id", map[string]any{"metadata": map[string]any{"user_id": "u1"}}, true},
		{"metadata 非对象时回落顶层", map[string]any{"metadata": "x", "user_id": "u1"}, true},
		{"都没有", map[string]any{"model": "m"}, false},
		{"空串不算", map[string]any{"user_id": ""}, false},
		{"数字不算", map[string]any{"user_id": float64(1)}, false},
		{"metadata 空串 + 顶层无", map[string]any{"metadata": map[string]any{"user_id": ""}}, false},
		{"nil 值不算", map[string]any{"metadata": map[string]any{"user_id": nil}}, false},
	}
	for _, c := range cases {
		if got := hasUserIDKey(c.obj); got != c.want {
			t.Errorf("%s: hasUserIDKey = %v，want %v", c.name, got, c.want)
		}
	}

	bodies := map[string]string{
		"顶层 user_id":       `{"user_id":"u1","messages":[{"role":"user","content":"x"}]}`,
		"metadata user_id": `{"metadata":{"user_id":"u1"},"messages":[{"role":"user","content":"x"}]}`,
		"数字 user_id":       `{"user_id":1,"messages":[{"role":"user","content":"x"}]}`,
		"无 user_id":        `{"messages":[{"role":"user","content":"x"}]}`,
	}
	for name, body := range bodies {
		var obj map[string]any
		if err := json.Unmarshal([]byte(body), &obj); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		derived := ExtractKeyOpt([]byte(body), true)
		if hasUserIDKey(obj) != (derived == "") {
			t.Errorf("%s: hasUserIDKey = %v 与派生结果 %q 不一致（派生闸两条路径口径不同）",
				name, hasUserIDKey(obj), derived)
		}
	}
}

// ── 6. messages 文本提取：legacy（any）与视图（RawMessage）同口径 ─────────

// TestCSSessMessageTextAgreesWithRawView messageText(any) 与 messageTextRaw(RawMessage)
// 在可比输入上必须逐位一致（派生键零漂移的前提）。
func TestCSSessMessageTextAgreesWithRawView(t *testing.T) {
	textParts := []any{
		map[string]any{"type": "text", "text": "a"},
		map[string]any{"type": "text", "text": "b"},
	}
	cases := []struct {
		name string
		raw  string
		in   any
		want string
	}{
		{"字符串形态", `"txt"`, "txt", "txt"},
		{"空串", `""`, "", ""},
		{"parts 全文本", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, textParts, "ab"},
		{
			"parts 含非文本（只取 text 字段）",
			`[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"u"}}]`,
			[]any{
				map[string]any{"type": "text", "text": "看"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u"}},
			},
			"看",
		},
		{"null", `null`, nil, ""},
		{"数字", `42`, float64(42), ""},
		{"对象（非数组）", `{"text":"z"}`, map[string]any{"text": "z"}, ""},
		{"空数组", `[]`, []any{}, ""},
	}
	for _, c := range cases {
		if got := messageText(c.in); got != c.want {
			t.Errorf("%s: messageText(%#v) = %q，want %q", c.name, c.in, got, c.want)
		}
		if got := messageTextRaw(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: messageTextRaw(%s) = %q，want %q（视图路径必须与 legacy 同口径）",
				c.name, c.raw, got, c.want)
		}
	}
}

// TestCSSessUserContentSignatureAgreesWithRawView 会话级内容签名（派生键用）：
// legacy(any) 与 raw(RawMessage) 一致；非文本 part 只入类型占位、**不入内容摘要**
// （图片 URL 逐轮变化不得破坏派生键稳定性——这是 fork 的既有契约）。
func TestCSSessUserContentSignatureAgreesWithRawView(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		in   any
		want string
	}{
		{"字符串形态", `"hello"`, "hello", "hello"},
		{"空串", `""`, "", ""},
		{"仅文本 part", `[{"type":"text","text":"看图"}]`, []any{map[string]any{"type": "text", "text": "看图"}}, "看图"},
		{
			"文本 + 图片：图片只入类型占位",
			`[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"u"}}]`,
			[]any{
				map[string]any{"type": "text", "text": "看图"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u"}},
			},
			"看图\n[image_url]",
		},
		{
			"纯图片：仍可签名（首图会话盲区修复）",
			`[{"type":"image_url","image_url":{"url":"u"}}]`,
			[]any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u"}}},
			"[image_url]",
		},
		{"空文本 part → 无签名", `[{"type":"text","text":""}]`, []any{map[string]any{"type": "text", "text": ""}}, ""},
		{"非字符串非数组", `42`, float64(42), ""},
		{"数组含非对象 part", `[1]`, []any{float64(1)}, ""},
		{"数组含字符串元素", `["plain"]`, []any{"plain"}, ""},
		{"空数组", `[]`, []any{}, ""},
		{"null", `null`, nil, ""},
	}
	for _, c := range cases {
		if got := userContentSignature(c.in); got != c.want {
			t.Errorf("%s: userContentSignature(%#v) = %q，want %q", c.name, c.in, got, c.want)
		}
		if got := userContentSignatureRaw(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: userContentSignatureRaw(%s) = %q，want %q（两条路径必须逐位一致）",
				c.name, c.raw, got, c.want)
		}
	}

	// marshal 失败（不可序列化内容）按"无内容"处理，不伪造签名
	if got := userContentSignature(make(chan int)); got != "" {
		t.Errorf("不可序列化 content 的签名 = %q，want \"\"", got)
	}
	// 图片 URL 变化不得改变签名（同一逻辑图片重发/换签名 URL）
	before := userContentSignature([]any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u1"}}})
	after := userContentSignature([]any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u2?sig=zzz"}}})
	if before == "" || before != after {
		t.Errorf("图片 URL 变化改变了签名：%q vs %q（会让派生键随轮漂移）", before, after)
	}
}

// TestCSSessContentTextLegacySemantics contentText（legacy 文本提取）的表语义。
func TestCSSessContentTextLegacySemantics(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"字符串形态", `"t"`, "t"},
		{"空字符串", `""`, ""},
		{"首尾空白保留（TrimSpace 只用于判形态）", `"  t  "`, "  t  "},
		{"null", `null`, ""},
		{"空白字节", `   `, ""},
		{"数字", `42`, ""},
		{"布尔", `true`, ""},
		{"对象（非数组）", `{"text":"a"}`, ""},
		{"坏 JSON", `{broken`, ""},
		{"parts 全文本拼接", `[{"text":"a"},{"text":"b"}]`, "ab"},
		{"parts 含无 text 的 part", `[{"text":"a"},{"type":"image_url"}]`, "a"},
		{"parts 含非对象元素", `["x"]`, ""},
		{"parts text 类型不符", `[{"text":1}]`, ""},
		{"空数组", `[]`, ""},
	}
	for _, c := range cases {
		if got := contentText(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: contentText(%s) = %q，want %q", c.name, c.raw, got, c.want)
		}
	}
}

// ── 7. 会话键派生：手写字面量 + 两实现互校 ──────────────────────────────

// TestCSSessDeriveKeyLiterals deriveKey 的键值：独立字面量（算法用 csessSha256Hex16 复述）。
func TestCSSessDeriveKeyLiterals(t *testing.T) {
	cases := []struct {
		name string
		obj  map[string]any
		want string
	}{
		{
			"system + 首条 user",
			csessBody([]any{csessMsg("system", "sys-A"), csessMsg("user", "ask-A")}),
			"d-" + csessSha256Hex16("sys-A\x00ask-A"),
		},
		{
			"历史追加不改键（多轮稳定）",
			csessBody([]any{
				csessMsg("system", "sys-A"), csessMsg("user", "ask-A"),
				csessMsg("assistant", "reply"), csessMsg("user", "ask-B"),
			}),
			"d-" + csessSha256Hex16("sys-A\x00ask-A"),
		},
		{
			"developer 等同 system",
			csessBody([]any{csessMsg("developer", "dev-D"), csessMsg("user", "ask-D")}),
			"d-" + csessSha256Hex16("dev-D\x00ask-D"),
		},
		{
			"首条 user 胜出（后续 user 不参与）",
			csessBody([]any{csessMsg("user", "first"), csessMsg("assistant", "r"), csessMsg("user", "second")}),
			"d-" + csessSha256Hex16("\x00first"),
		},
		{
			"纯图片首条 user：只入类型占位（不含内容摘要）",
			csessBody([]any{csessMsg("user", []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u"}}})}),
			"d-" + csessSha256Hex16("\x00[image_url]"),
		},
		{
			"多模态文本 + 图片",
			csessBody([]any{
				csessMsg("system", "sys-B"),
				csessMsg("user", []any{
					map[string]any{"type": "text", "text": "见图"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u"}},
				}),
			}),
			"d-" + csessSha256Hex16("sys-B\x00见图\n[image_url]"),
		},
		{"无 user 消息", csessBody([]any{csessMsg("system", "s")}), ""},
		{"messages 非数组", map[string]any{"messages": "x"}, ""},
		{"messages 空数组", csessBody([]any{}), ""},
		{"元素非对象被跳过", map[string]any{"messages": []any{"plain"}}, ""},
		{"user content 空串", csessBody([]any{csessMsg("user", "")}), ""},
		{"user content 非字符串非数组", csessBody([]any{csessMsg("user", float64(42))}), ""},
		{"user content 缺失", csessBody([]any{map[string]any{"role": "user"}}), ""},
		{"system/user 均无文本", csessBody([]any{csessMsg("system", []any{}), csessMsg("user", []any{})}), ""},
		{"无 messages 键", map[string]any{"model": "m"}, ""},
	}
	for _, c := range cases {
		if got := deriveKey(c.obj); got != c.want {
			t.Errorf("%s: deriveKey = %q，want %q", c.name, got, c.want)
		}
		if len(c.want) != 0 && len(c.want) != 2+32 {
			t.Errorf("%s: 期望字面量形态错误 %q（应为 d- + 32 hex）", c.name, c.want)
		}
	}
}

// TestCSSessHotPathDerivedKeysAreLiteralStable 热路径（ExtractKeyOptView）的派生键与
// 独立手写字面量逐位一致——存量绑定不会因重构漂移；同时覆盖各错误兜底分支。
func TestCSSessHotPathDerivedKeysAreLiteralStable(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"显式 conversation_id 优先于派生",
			`{"conversation_id":"cid-1","messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"ask-A"}]}`,
			"cid-1",
		},
		{
			"prompt_cache_key 优先于派生",
			`{"prompt_cache_key":"pc-1","messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"ask-A"}]}`,
			"pc-1",
		},
		{
			"system + 首条 user 派生",
			`{"messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"ask-A"}]}`,
			"d-" + csessSha256Hex16("sys-A\x00ask-A"),
		},
		{
			"历史追加不改键",
			`{"messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"ask-A"},{"role":"assistant","content":"r"},{"role":"tool","content":"t"},{"role":"user","content":"ask-B"}]}`,
			"d-" + csessSha256Hex16("sys-A\x00ask-A"),
		},
		{
			"developer 等同 system",
			`{"messages":[{"role":"developer","content":"dev-D"},{"role":"user","content":"ask-D"}]}`,
			"d-" + csessSha256Hex16("dev-D\x00ask-D"),
		},
		{
			"纯图片首条 user（占位不含摘要）",
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"u"}}]}]}`,
			"d-" + csessSha256Hex16("\x00[image_url]"),
		},
		{
			"多模态文本 + 图片",
			`{"messages":[{"role":"system","content":"sys-B"},{"role":"user","content":[{"type":"text","text":"见图"},{"type":"image_url","image_url":{"url":"u"}}]}]}`,
			"d-" + csessSha256Hex16("sys-B\x00见图\n[image_url]"),
		},
		{
			"仅一条 user 消息",
			`{"messages":[{"role":"user","content":"only"}]}`,
			"d-" + csessSha256Hex16("\x00only"),
		},
		{
			"metadata.user_id 抑制派生",
			`{"metadata":{"user_id":"u1"},"messages":[{"role":"user","content":"x"}]}`,
			"",
		},
		{
			"顶层 user_id 抑制派生",
			`{"user_id":"u1","messages":[{"role":"user","content":"x"}]}`,
			"",
		},
		{
			"system content 取不到文本 → 空 system 参与派生",
			`{"messages":[{"role":"system","content":[1]},{"role":"user","content":"ask-C"}]}`,
			"d-" + csessSha256Hex16("\x00ask-C"),
		},
		{
			"非对象消息条目被跳过",
			`{"messages":[1,{"role":"user","content":"ask-E"}]}`,
			"d-" + csessSha256Hex16("\x00ask-E"),
		},
		{"user content 为数字 → 不派生", `{"messages":[{"role":"user","content":42}]}`, ""},
		{"user content 数组含非对象 part → 不派生", `{"messages":[{"role":"user","content":[1]}]}`, ""},
		{"user content 空数组 → 不派生", `{"messages":[{"role":"user","content":[]}]}`, ""},
		{"content 为字符串形态的 system 不误入 user", `{"messages":[{"role":"system","content":"sys-F"}]}`, ""},
		{"无 messages", `{"model":"m"}`, ""},
	}
	for _, c := range cases {
		if got := ExtractKeyOpt([]byte(c.body), true); got != c.want {
			t.Errorf("%s: ExtractKeyOpt(derive=true) = %q，want %q", c.name, got, c.want)
		}
	}
}

// TestCSSessDeriveKeyLegacyMatchesHotPath 两实现互校：deriveKey（map 入参）与
// ExtractKeyOptView（字节入参）在同一内容上必须逐位一致（否则 legacy 与热路径分叉）。
func TestCSSessDeriveKeyLegacyMatchesHotPath(t *testing.T) {
	cases := []struct {
		name      string
		obj       map[string]any
		body      string
		wantEmpty bool
	}{
		{
			"文本轮",
			csessBody([]any{csessMsg("system", "sys-A"), csessMsg("user", "ask-A")}),
			`{"messages":[{"role":"system","content":"sys-A"},{"role":"user","content":"ask-A"}]}`,
			false,
		},
		{
			"纯图片轮",
			csessBody([]any{csessMsg("user", []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u"}}})}),
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"u"}}]}]}`,
			false,
		},
		{
			"多模态轮",
			csessBody([]any{
				csessMsg("system", "sys-B"),
				csessMsg("user", []any{
					map[string]any{"type": "text", "text": "见图"},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": "u"}},
				}),
			}),
			`{"messages":[{"role":"system","content":"sys-B"},{"role":"user","content":[{"type":"text","text":"见图"},{"type":"image_url","image_url":{"url":"u"}}]}]}`,
			false,
		},
		{
			"无 user 消息（两路径一致地不派生）",
			csessBody([]any{csessMsg("system", "s")}),
			`{"messages":[{"role":"system","content":"s"}]}`,
			true,
		},
	}
	for _, c := range cases {
		legacy := deriveKey(c.obj)
		hot := ExtractKeyOpt([]byte(c.body), true)
		if legacy != hot {
			t.Errorf("%s: deriveKey = %q，ExtractKeyOpt = %q（两条派生路径分叉 → 存量绑定漂移）", c.name, legacy, hot)
		}
		if c.wantEmpty && legacy != "" {
			t.Errorf("%s: 该用例不应派生键，实际 %q", c.name, legacy)
		}
		if !c.wantEmpty && legacy == "" {
			t.Errorf("%s: 该用例应派生非空键", c.name)
		}
	}
}

// TestCSSessMalformedRawBytesNeverFabricateSignatures 直接喂"坏原始字节"给三个内容
// 提取 helper：一律返回空串（不 panic、不返回半成品签名）。
//
// 可信边界说明：公开 API（ParseRequestView/TurnKey）不会把非法字节传到这些分支，
// 因此本用例直接调用包内 helper，把"防御性兜底"钉成契约——真实调用方（上游 handler、
// 未来的新消费方）若绕过 ParseRequestView 直接传原始字节，行为必须是"未知"而非"崩溃"。
func TestCSSessMalformedRawBytesNeverFabricateSignatures(t *testing.T) {
	badRaws := []struct {
		name string
		raw  string
	}{
		{"截断的字符串", `"unterminated`},
		{"截断的数组", `[1,`},
		{"只有左括号", `[`},
		{"截断的对象数组", `[{"text":"a"`},
	}
	for _, c := range badRaws {
		if got := contentText(json.RawMessage(c.raw)); got != "" {
			t.Errorf("%s: contentText(%s) = %q，want \"\"", c.name, c.raw, got)
		}
		if got := contentSignature(json.RawMessage(c.raw)); got != "" {
			t.Errorf("%s: contentSignature(%s) = %q，want \"\"（不得凭截断字节伪造轮级/粘性键）", c.name, c.raw, got)
		}
		if got := userContentSignatureRaw(json.RawMessage(c.raw)); got != "" {
			t.Errorf("%s: userContentSignatureRaw(%s) = %q，want \"\"", c.name, c.raw, got)
		}
	}
	// 合法字符串形态仍是原样文本（兜底不得连正常路径一起吞掉）
	if got := contentSignature(json.RawMessage(`"ok"`)); got != "ok" {
		t.Errorf("合法字符串签名 = %q，want ok", got)
	}
	if got := userContentSignatureRaw(json.RawMessage(`"ok"`)); got != "ok" {
		t.Errorf("合法字符串视图签名 = %q，want ok", got)
	}
	if got := contentText(json.RawMessage(`"ok"`)); got != "ok" {
		t.Errorf("合法字符串文本 = %q，want ok", got)
	}
}

// TestCSSessTurnKeyViewBranches 轮级键的视图路径分支（含坏内容兜底），与字节路径逐位一致。
func TestCSSessTurnKeyViewBranches(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"最后一条 user 胜出", `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`, "u2:c"},
		{"非对象消息条目被跳过", `{"messages":[1,{"role":"user","content":"a"}]}`, "u1:a"},
		{"content 非字符串非数组 → 无签名", `{"messages":[{"role":"user","content":42}]}`, ""},
		{"content 数组含非对象 part → 无签名", `{"messages":[{"role":"user","content":[1]}]}`, ""},
		{"parts 全文本", `{"messages":[{"role":"user","content":[{"type":"text","text":"ab"}]}]}`, "u0:ab"},
		{"role 缺失不算 user", `{"messages":[{"content":"a"}]}`, ""},
		{"messages 非数组", `{"messages":"x"}`, ""},
		{"空 messages", `{"messages":[]}`, ""},
		{"最后一条 user 无内容不往前找", `{"messages":[{"role":"user","content":"a"},{"role":"user","content":null}]}`, ""},
		{"全部条目都不是对象 → 倒序扫描全部跳过", `{"messages":[1,2]}`, ""},
	}
	for _, c := range cases {
		if got := TurnKey([]byte(c.body)); got != c.want {
			t.Errorf("%s: TurnKey = %q，want %q", c.name, got, c.want)
		}
		v, ok := ParseRequestView([]byte(c.body))
		if !ok {
			t.Fatalf("%s: 解析失败", c.name)
		}
		if got := TurnKeyView(&v); got != c.want {
			t.Errorf("%s: TurnKeyView = %q，want %q（视图路径必须与字节路径逐位一致）", c.name, got, c.want)
		}
	}
}
