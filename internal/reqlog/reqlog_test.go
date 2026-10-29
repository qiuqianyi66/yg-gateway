package reqlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRecorderMetricsAndRecent(t *testing.T) {
	r := New(Config{})
	r.Begin()
	r.Record(Event{RequestID: "ok", Status: 200, OK: true, Outcome: OutcomeSuccess, DurationMs: 10})
	r.Begin()
	r.Record(Event{RequestID: "bad", Status: 429, OK: false, Outcome: OutcomeHTTPError, DurationMs: 30})

	s := r.Snapshot()
	if s.Completed != 2 || s.InFlight != 0 || s.Succeeded != 1 || s.Failed != 1 {
		t.Fatalf("counts = %+v", s)
	}
	if s.SuccessRate != 50 || s.HTTPSuccessRate != 50 || s.AvgDurationMs != 20 {
		t.Fatalf("rates = success:%v http:%v avg:%v", s.SuccessRate, s.HTTPSuccessRate, s.AvgDurationMs)
	}
	if len(s.Recent) != 2 || s.Recent[0].RequestID != "bad" || s.Recent[1].RequestID != "ok" {
		t.Fatalf("recent = %+v, want newest first", s.Recent)
	}
}

// TestRecentRingBufferWrap 环形缓冲回绕：写满并超过 recentLimit 后，Recent 必须
// 恰好保留最近 recentLimit 条、且仍是「最近在前」。
//
// 牙齿：若 recentEvents 的索引方向写反（或 recentHead 不取模），超过容量后
// 顺序会错乱或重复旧条目 → 本测试 FAIL。这是环形缓冲最易写错的点。
func TestRecentRingBufferWrap(t *testing.T) {
	r := New(Config{})
	total := recentLimit + 25 // 故意超过容量，触发回绕
	for i := 0; i < total; i++ {
		r.Record(Event{RequestID: "req-" + strconv.Itoa(i), Status: 200, OK: true})
	}
	s := r.Snapshot()
	if len(s.Recent) != recentLimit {
		t.Fatalf("Recent 长度 = %d, want %d（环形缓冲容量）", len(s.Recent), recentLimit)
	}
	// 最近一条是最后写入的 req-(total-1)，最旧一条是 req-(total-recentLimit)。
	if got := s.Recent[0].RequestID; got != "req-"+strconv.Itoa(total-1) {
		t.Fatalf("Recent[0] = %q, want req-%d（最近在前）", got, total-1)
	}
	if got := s.Recent[recentLimit-1].RequestID; got != "req-"+strconv.Itoa(total-recentLimit) {
		t.Fatalf("Recent[%d] = %q, want req-%d（最旧的被挤出的边界）",
			recentLimit-1, got, total-recentLimit)
	}
	// 顺序必须严格单调递减的序号（无重复、无跳变）。
	for i := 1; i < len(s.Recent); i++ {
		prev, err1 := strconv.Atoi(strings.TrimPrefix(s.Recent[i-1].RequestID, "req-"))
		cur, err2 := strconv.Atoi(strings.TrimPrefix(s.Recent[i].RequestID, "req-"))
		if err1 != nil || err2 != nil {
			t.Fatalf("RequestID 解析失败: %q / %q", s.Recent[i-1].RequestID, s.Recent[i].RequestID)
		}
		if prev-1 != cur {
			t.Fatalf("Recent 顺序断裂：index %d = %d, index %d = %d（应连续递减）", i-1, prev, i, cur)
		}
	}
}

// TestRecentEmpty 无记录时 Recent 必须是空切片（不是 null）——前端把 null 与
// 「归档关闭」混在一起会走错分支。
func TestRecentEmpty(t *testing.T) {
	r := New(Config{})
	s := r.Snapshot()
	if s.Recent == nil {
		t.Fatal("Recent 应为空切片而非 nil")
	}
	if len(s.Recent) != 0 {
		t.Fatalf("Recent = %+v, want empty", s.Recent)
	}
}

func TestArchiveRotationReadAndFilter(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 120, MaxBytes: 1 << 20, RetentionDays: 7})
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 12; i++ {
		r.Record(Event{
			Time:       base.Add(time.Duration(i) * time.Second),
			RequestID:  "multi-" + string(rune('a'+i)),
			Model:      "glm-5.3",
			Account:    "账号(uid8)",
			Status:     200,
			OK:         true,
			Outcome:    OutcomeSuccess,
			DurationMs: int64(i + 1),
		})
	}
	r.Record(Event{
		Time:       base.Add(20 * time.Second),
		RequestID:  "other",
		Model:      "other-model",
		Status:     500,
		Outcome:    OutcomeHTTPError,
		DurationMs: 99,
	})
	r.Close()

	stats := r.Snapshot().Archive
	if !stats.Enabled || stats.Files < 2 || stats.Bytes == 0 || stats.DroppedWrites != 0 {
		t.Fatalf("archive stats = %+v", stats)
	}
	rows, err := r.ReadArchive(5, Filter{Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 || rows[0].RequestID != "multi-l" || rows[4].RequestID != "multi-h" {
		t.Fatalf("filtered rows = %+v", rows)
	}
}

// TestReadAllFullWindow 全量读取契约：跨多个归档文件、无条数截断、
// 时间升序、按 filter 过滤。与 ReadArchive（倒序+截断）对比钉住差异。
func TestReadAllFullWindow(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 1, MaxBytes: 1 << 20, RetentionDays: 7})
	base := time.Now().Add(-2 * time.Hour)
	const n = 12
	for i := 0; i < n; i++ {
		r.Record(Event{
			Time:      base.Add(time.Duration(i) * time.Second),
			RequestID: "ev-" + string(rune('a'+i)),
			Model:     "glm-5.3",
			Account:   "账号(uid8)",
			Status:    200,
			OK:        true,
			Outcome:   OutcomeSuccess,
		})
	}
	r.Record(Event{
		Time:      base.Add(30 * time.Second),
		RequestID: "other",
		Model:     "other-model",
		Status:    500,
		OK:        false,
		Outcome:   OutcomeHTTPError,
	})
	r.Close()

	// 全量：12+1 条全回，升序（第一条是最早写入的 ev-a）。
	all, err := r.ReadAll(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != n+1 {
		t.Fatalf("ReadAll len = %d, want %d（无截断）", len(all), n+1)
	}
	if all[0].RequestID != "ev-a" || all[len(all)-1].RequestID != "other" {
		t.Fatalf("ReadAll 应时间升序：first=%s last=%s", all[0].RequestID, all[len(all)-1].RequestID)
	}

	// 过滤 + 升序仍成立。
	filtered, err := r.ReadAll(Filter{Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != n || filtered[0].RequestID != "ev-a" || filtered[len(filtered)-1].RequestID != "ev-l" {
		t.Fatalf("filtered = %d 条, first=%s last=%s", len(filtered), filtered[0].RequestID, filtered[len(filtered)-1].RequestID)
	}

	// 对照：ReadArchive 同 filter 是倒序+截断。
	recent, err := r.ReadArchive(5, Filter{Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 5 || recent[0].RequestID != "ev-l" {
		t.Fatalf("ReadArchive = %+v, want 5 条倒序", recent)
	}

	// 未启用归档 → 空列表（不是 nil）。
	r2 := New(Config{})
	empty, err := r2.ReadAll(Filter{})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("未启用 ReadAll = %v err=%v, want 空列表", empty, err)
	}
}

// TestReadAllAggregateDaily 端到端：全量读回 → 按天聚合，跨文件跨天归组正确。
func TestReadAllAggregateDaily(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 1, MaxBytes: 1 << 20, RetentionDays: 7})
	d1 := time.Now().Add(-26 * time.Hour)
	d2 := time.Now().Add(-2 * time.Hour)
	r.Record(Event{Time: d1, RequestID: "a1", Account: "acc1", OK: true})
	r.Record(Event{Time: d1.Add(time.Minute), RequestID: "a2", Account: "acc1", OK: false})
	r.Record(Event{Time: d2, RequestID: "b1", Account: "acc2", OK: true})
	r.Close()

	all, err := r.ReadAll(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	days := AggregateDaily(all)
	if len(days) != 2 {
		t.Fatalf("days = %d, want 2（跨两天）", len(days))
	}
	// 升序：d1 组在前。
	if days[0].Date != d1.Local().Format("2006-01-02") || days[1].Date != d2.Local().Format("2006-01-02") {
		t.Fatalf("day order = %v, %v", days[0].Date, days[1].Date)
	}
	if days[0].Requests != 2 || days[0].Succeeded != 1 || days[0].Failed != 1 || days[0].ActiveAccounts != 1 {
		t.Fatalf("d1 = %+v", days[0])
	}
	if days[1].Requests != 1 || days[1].Succeeded != 1 || days[1].Failed != 0 || days[1].ActiveAccounts != 1 {
		t.Fatalf("d2 = %+v", days[1])
	}
}

func TestReadArchiveOrdersByEventTimeNotFileMtime(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 1, MaxBytes: 1 << 20, RetentionDays: 7})
	base := time.Now().Add(-time.Hour)
	const n = 5
	for i := 0; i < n; i++ {
		r.Record(Event{
			Time:      base.Add(time.Duration(i) * time.Second),
			RequestID: "ev-" + string(rune('a'+i)),
			Model:     "glm-5.3",
			Status:    200,
			OK:        true,
			Outcome:   OutcomeSuccess,
		})
	}
	r.Close()

	// FileMaxBytes=1 让每个事件独占一个归档文件：基准文件 requests-<day>.jsonl 装的是
	// 最早的事件，字典序却排在 requests-<day>.N.jsonl 之后。按 os.ReadDir 顺序递增地
	// 设置 mtime，最早事件所在文件就拿到最大 mtime、被放在最后读取。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != n {
		t.Fatalf("归档文件数 = %d, want %d", len(entries), n)
	}
	stamp := time.Unix(1700000000, 0)
	for i, e := range entries {
		ts := stamp.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(filepath.Join(dir, e.Name()), ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := r.ReadArchive(n, Filter{Model: "glm"})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rows))
	for _, e := range rows {
		got = append(got, e.RequestID)
	}
	if len(rows) != n {
		t.Fatalf("rows = %d, want %d", len(rows), n)
	}
	for i := range rows {
		want := "ev-" + string(rune('a'+n-1-i))
		if rows[i].RequestID != want {
			t.Fatalf("rows[%d] = %s, want %s（须按事件时间倒序，与文件 mtime 无关）\n got %v", i, rows[i].RequestID, want, got)
		}
	}
}

// 来源字段随事件落盘并可被 client_ip / user_agent 过滤（包含匹配、大小写不敏感）。
func TestArchiveFilterByClientInfo(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
	base := time.Now().Add(-time.Minute)
	rows := []Event{
		{RequestID: "a", Model: "glm-5.3", Status: 200, OK: true, Outcome: OutcomeSuccess,
			ClientIP: "203.0.113.7", UserAgent: "python-requests/2.31.0"},
		{RequestID: "b", Model: "glm-5.3", Status: 200, OK: true, Outcome: OutcomeSuccess,
			ClientIP: "198.51.100.4", UserAgent: "Mozilla/5.0 Chrome/120.0.0.0"},
		{RequestID: "c", Model: "glm-5.3", Status: 200, OK: true, Outcome: OutcomeSuccess},
	}
	for i, e := range rows {
		e.Time = base.Add(time.Duration(i) * time.Second)
		r.Record(e)
	}
	r.Close()

	got, err := r.ReadArchive(10, Filter{ClientIP: "203.0.113."})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RequestID != "a" {
		t.Fatalf("client ip filter = %+v", got)
	}
	if got[0].UserAgent != "python-requests/2.31.0" {
		t.Fatalf("user agent not persisted: %+v", got[0])
	}

	// UA 片段过滤大小写不敏感（面板搜索框直接传用户输入）。
	got, err = r.ReadArchive(10, Filter{UserAgent: "chrome/120"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RequestID != "b" {
		t.Fatalf("ua filter = %+v", got)
	}

	// 来源为空的旧事件不该被非空来源条件误命中。
	got, err = r.ReadArchive(10, Filter{ClientIP: "10.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unmatched ip filter = %+v", got)
	}
}

// 时间区间过滤（「今天」/「自定义」）：闭区间，任一侧为零值即该侧不设界。
func TestArchiveFilterByTimeRange(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 30})
	base := time.Now().Add(-72 * time.Hour).Truncate(time.Minute)
	ids := []string{"d3", "d2", "d1", "now"}
	for i, id := range ids {
		r.Record(Event{
			Time: base.Add(time.Duration(i) * 24 * time.Hour), RequestID: id,
			Status: 200, OK: true, Outcome: OutcomeSuccess,
		})
	}
	r.Close()

	rows, err := r.ReadArchive(10, Filter{From: base.Add(24 * time.Hour), To: base.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(rows))
	for _, e := range rows {
		got = append(got, e.RequestID)
	}
	// 倒序返回，闭区间命中 d2、d1。
	if len(got) != 2 || got[0] != "d1" || got[1] != "d2" {
		t.Fatalf("时间区间过滤 = %v, want [d1 d2]", got)
	}

	onlyFrom, err := r.ReadArchive(10, Filter{From: base.Add(48 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyFrom) != 2 || onlyFrom[0].RequestID != "now" {
		t.Fatalf("仅 From 过滤 = %+v, want 2 条（d1、now）", onlyFrom)
	}

	onlyTo, err := r.ReadArchive(10, Filter{To: base})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyTo) != 1 || onlyTo[0].RequestID != "d3" {
		t.Fatalf("仅 To 过滤 = %+v, want 仅 d3", onlyTo)
	}

	// 时间区间与其它条件是与关系。
	combined, err := r.ReadArchive(10, Filter{
		From: base.Add(24 * time.Hour), To: base.Add(48 * time.Hour), Outcome: OutcomeHTTPError,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(combined) != 0 {
		t.Fatalf("区间+结果组合应为空（这几条都是成功）: %+v", combined)
	}
}

func TestArchiveQueueDropCounter(t *testing.T) {
	w := &archiveWriter{
		cfg:  Config{Enabled: true, Dir: t.TempDir()},
		ch:   make(chan Event, 1),
		done: make(chan struct{}),
	}
	w.ch <- Event{RequestID: "occupied"}
	w.enqueue(Event{RequestID: "dropped"})
	if got := w.dropped.Load(); got != 1 {
		t.Fatalf("dropped=%d want 1", got)
	}
}

func TestArchivePruneHonorsSize(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"requests-2026-09-20.jsonl", "requests-2026-09-21.jsonl", "requests-2026-09-22.jsonl"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().AddDate(0, 0, -10+i)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	w := &archiveWriter{cfg: Config{Enabled: true, Dir: dir, RetentionDays: 30, MaxBytes: 50}, done: make(chan struct{})}
	w.prune()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "requests-2026-09-22.jsonl" {
		t.Fatalf("remaining = %+v, want newest file only", entries)
	}
}

// TestArchiveDefaultLimitAndMaxCap 钉住归档读取的两个边界（2026-10-19 变异 RQ1/RQ2 咬出来的缺口）：
//   - limit<=0 → 默认 200（不是更小：面板默认页看的就是这个口径）；
//   - limit 超上限 → 夹到 1000（防止一次把整个归档读进内存）。
//
// 只按条数判定，不依赖具体事件内容。
func TestArchiveDefaultLimitAndMaxCap(t *testing.T) {
	const n = 1005
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 1 << 30, RetentionDays: 7})
	base := time.Now().Add(-time.Hour)
	for i := 0; i < n; i++ {
		r.Record(Event{
			Time:      base.Add(time.Duration(i) * time.Millisecond),
			RequestID: "ev-" + strconv.Itoa(i),
			Status:    200,
			OK:        true,
			Outcome:   OutcomeSuccess,
		})
	}
	r.Close()

	rows, err := r.ReadArchive(0, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 200 {
		t.Fatalf("limit=0 应回默认 200 条, got %d", len(rows))
	}

	rows, err = r.ReadArchive(100000, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != defaultReadMax {
		t.Fatalf("limit=100000 应夹到 %d, got %d", defaultReadMax, len(rows))
	}
}

// TestReadArchiveDayRangeCoarseFilter 文件级粗筛（C-3）：跨三个自然日写事件后，
// From/To 落在中间一天 → 结果集与逐文件全读完全一致。粗筛只按文件名日期段跳过
// 整文件（< fromDay 或 > toDay），行内匹配逻辑不动——结果不许有任何差别。
func TestReadArchiveDayRangeCoarseFilter(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 30})
	// 跨三个自然日：e0(day A)、e1(day B)、e2(day C)，避开边界小时。
	base := time.Now().Add(-72 * time.Hour).Truncate(24 * time.Hour).Add(12 * time.Hour)
	events := []Event{
		{Time: base, RequestID: "day-a", Model: "m", Status: 200, OK: true, Outcome: OutcomeSuccess},
		{Time: base.Add(24 * time.Hour), RequestID: "day-b", Model: "m", Status: 200, OK: true, Outcome: OutcomeSuccess},
		{Time: base.Add(48 * time.Hour), RequestID: "day-c", Model: "m", Status: 200, OK: true, Outcome: OutcomeSuccess},
	}
	for _, e := range events {
		r.Record(e)
	}
	r.Close()

	// From=To=day B 的时间点：A 整文件 < fromDay 跳过，C 整文件 > toDay 跳过，只命中 B。
	rows, err := r.ReadArchive(10, Filter{From: base.Add(24 * time.Hour), To: base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RequestID != "day-b" {
		t.Fatalf("闭区间中点命中 = %+v, want [day-b]", rows)
	}

	// 仅 From=day B：A 被整文件跳过；C 的文件日 > fromDay 不跳，保留。
	rows, err = r.ReadArchive(10, Filter{From: base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RequestID != "day-c" || rows[1].RequestID != "day-b" {
		t.Fatalf("仅 From = %+v, want [day-c day-b]", rows)
	}

	// 仅 To=day B：C 被整文件跳过；A 的文件日 < toDay 不跳，保留。
	rows, err = r.ReadArchive(10, Filter{To: base.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].RequestID != "day-b" || rows[1].RequestID != "day-a" {
		t.Fatalf("仅 To = %+v, want [day-b day-a]", rows)
	}
}

// kind 维度（V3-8）：归档精确过滤 + 历史行的三值语义。
// 缺失 = 未知：V3-8 之前的行没有 kind 字段，不得被 Kind="chat" 命中（那是把
// 未分类读成已分类），也不得在 wire 上序列化成 "kind":""。
func TestArchiveFilterByKind(t *testing.T) {
	dir := t.TempDir()
	r := New(Config{Enabled: true, Dir: dir, MaxBytes: 1 << 20, RetentionDays: 7})
	base := time.Now().Add(-time.Minute)
	tagged := Event{RequestID: "chat-1", Path: "/v1/chat/completions", Kind: KindChat,
		Status: 200, OK: true, Outcome: OutcomeSuccess}
	legacy := Event{RequestID: "legacy-1", Path: "/v1/chat/completions",
		Status: 200, OK: true, Outcome: OutcomeSuccess} // V3-8 之前的行：无 kind
	for i, e := range []Event{tagged, legacy} {
		e.Time = base.Add(time.Duration(i) * time.Second)
		r.Record(e)
	}
	r.Close()

	// 精确匹配只命中显式打标的行，历史行不跟聊。
	got, err := r.ReadArchive(10, Filter{Kind: KindChat})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RequestID != "chat-1" {
		t.Fatalf("kind=chat 过滤 = %+v, want [chat-1]（历史行不命中）", got)
	}

	// 词表里其他种类当前无入账来源（probe/farming 直连 upstream 不经此处），
	// 过滤必须回零行而不是误命中——这一行为留给未来的记录方兑现。
	got, err = r.ReadArchive(10, Filter{Kind: KindFarming})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("kind=farming 过滤 = %+v, want 空（farming 尚无入账来源）", got)
	}

	// 不筛 kind → 全部行可见，历史行读回保持 kind 缺失（空串）。
	got, err = r.ReadArchive(10, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("空 kind 过滤 = %+v, want 两行都可见", got)
	}
	for _, e := range got {
		if e.RequestID == "legacy-1" && e.Kind != "" {
			t.Fatalf("历史行 kind = %q, want 空串（缺失=未知，不冒充任何词表值）", e.Kind)
		}
	}

	// wire 三态：kind 零值不出现在 JSON 上（与 /v1/models 的字段省略口径同源）；
	// 显式值原样落盘。
	raw, err := json.Marshal(Event{RequestID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"kind"`) {
		t.Fatalf("kind 零值不应出现在 wire 上: %s", raw)
	}
	raw, err = json.Marshal(Event{RequestID: "x", Kind: KindProbe})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kind":"probe"`) {
		t.Fatalf("kind 显式值应原样落盘: %s", raw)
	}
}
