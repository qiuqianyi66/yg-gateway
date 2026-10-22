package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// R-205：增量 WAL 落库——日常 flush 只追加 WAL，不重写基线；重启回放恢复。

// 日常 flush（force=false）：只追加 WAL，不重写 usage.json 基线；重启后回放 WAL 恢复全部数值。
func TestWALIncrementalFlushNoBaseRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r1 := New(path)
	r1.Add(time.Now(), "cn", "u1", "glm-5.2", Delta{PromptTokens: 42, HasPromptTokens: true, TotalTokens: 42, HasTotal: true}, true)
	r1.Add(time.Now(), "cn", "u1", "glm-5.2", Delta{PromptTokens: 8, HasPromptTokens: true, TotalTokens: 8, HasTotal: true}, true)
	r1.flush(false) // 增量：应只追加 WAL，不写基线

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("日常 flush 不应重写基线 usage.json（R-205 增量落库）")
	}
	raw, err := os.ReadFile(walPathOf(path))
	if err != nil {
		t.Fatalf("WAL 应已追加: %v", err)
	}
	if !strings.Contains(string(raw), "u1") {
		t.Fatalf("WAL 应包含脏桶记录: %s", raw)
	}

	// 重启：加载空基线 + 回放 WAL → 数值完整恢复。
	r2 := New(path)
	snap := r2.SnapshotWindow(Window{}, nil, nil, nil, nil)
	if snap.Totals.TotalTokens != 50 {
		t.Fatalf("回放后 totalTokens=%d，期望 50", snap.Totals.TotalTokens)
	}
}

// force flush（Stop/Save）：重写完整基线并截断 WAL。
func TestWALForceCheckpointTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r1 := New(path)
	r1.Add(time.Now(), "cn", "u1", "m", Delta{PromptTokens: 7, HasPromptTokens: true, TotalTokens: 7, HasTotal: true}, true)
	r1.flush(true) // 强制基线重写

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("force flush 应写出基线 usage.json: %v", err)
	}
	raw, err := os.ReadFile(walPathOf(path))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("WAL 应已截断/清空: %v", err)
	}
	if len(strings.TrimSpace(string(raw))) != 0 {
		t.Fatalf("基线写完后 WAL 应截断为空，实际: %s", raw)
	}

	r2 := New(path)
	if got := r2.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.TotalTokens; got != 7 {
		t.Fatalf("基线恢复后 totalTokens=%d，期望 7", got)
	}
}

// WAL 坏行（崩溃残留半行）跳过不炸；好行正常回放。
func TestWALBadLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	// 手工构造：一个好记录（u1=42）+ 一个半截坏行。
	wl := walPathOf(path)
	if err := os.MkdirAll(filepath.Dir(wl), 0o755); err != nil {
		t.Fatal(err)
	}
	good := `{"b":{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":42,"t":42,"q":1}}`
	if err := os.WriteFile(wl, []byte(good+"\n{\"b\":{\"s\":\"broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	// 好行回放成功。
	if got := r.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.TotalTokens; got != 42 {
		t.Fatalf("坏行应跳过、好行应回放，totalTokens=%d，期望 42", got)
	}
}

// 回放合并规则 = 逐字段取大：旧 WAL 记录不覆盖更新的基线值（消解回滚时序回退）。
func TestWALReplayTakesMax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	// 基线：u1 PT=100。
	base := `{"version":6,"saved":"2026-10-22T10:00:00Z","buckets":[{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":100,"t":100,"q":1}]}`
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	// WAL：同一桶 PT=80（更旧）→ max 应保留基线 100，不被旧 WAL 覆盖。
	wl := walPathOf(path)
	old := `{"b":{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":80,"t":80,"q":1}}`
	if err := os.WriteFile(wl, []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	if got := r.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.PromptTokens; got != 100 {
		t.Fatalf("旧 WAL 不得覆盖更新基线（逐字段取大），pt=%d，期望 100", got)
	}
}

// 旧格式（v6，无 WAL）文件可正常加载（向后兼容，升级不丢数据）。
func TestLoadLegacyV6StillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	legacy := `{"version":6,"saved":"2026-10-22T10:00:00Z","buckets":[{"s":"h:2026-10-22T10","r":"cn","u":"u1","m":"m","p":13,"t":13,"q":1}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	if got := r.SnapshotWindow(Window{}, nil, nil, nil, nil).Totals.TotalTokens; got != 13 {
		t.Fatalf("旧 v6 基线应正常加载，tt=%d，期望 13", got)
	}
}

// ---- R-205 v2：折叠桶 WAL delete op（消除复活窗口）----

// 折叠-崩溃-重启往返：基线有源桶，WAL 里先有旧 upsert（折叠前的源桶记录）、再有 delete
// 记录、再有目标桶 upsert。回放后：源桶不再复活，目标桶值正确。
func TestWALDeleteRemovesFoldedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	srcKey := bucketKey("h:2026-10-22T10", "cn", "u1", "m", "", "")
	dstKey := bucketKey("d:2026-10-22", "cn", "u1", "m", "", "")
	buckets := map[string]*bucket{}
	monthly := map[string]int64{}

	// WAL = 旧 upsert 源（折叠前 flush 已写）→ delete 源（折叠后 flush 写）→ upsert 目标（折叠合并值）。
	recs := []walRecord{
		{B: &bucket{Scope: "h:2026-10-22T10", Realm: "cn", UID: "u1", Model: "m", PT: 42, TT: 42, Req: 1}},
		{D: srcKey},
		{B: &bucket{Scope: "d:2026-10-22", Realm: "cn", UID: "u1", Model: "m", PT: 42, TT: 42, Req: 1}},
	}
	var lines []string
	for i := range recs {
		b, _ := json.Marshal(&recs[i])
		lines = append(lines, string(b))
	}
	wl := walPathOf(path)
	if err := os.MkdirAll(filepath.Dir(wl), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wl, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replayWAL(wl, buckets, monthly)

	if _, ok := buckets[srcKey]; ok {
		t.Fatalf("delete 后源桶不应复活（折叠-崩溃-重启往返）")
	}
	got, ok := buckets[dstKey]
	if !ok {
		t.Fatalf("目标桶应存在")
	}
	if got.PT != 42 {
		t.Fatalf("目标桶 PT=%d，期望 42", got.PT)
	}
}

// delete 后 upsert 同键重立：折叠后源键又被新流量重建——先删后建，最终存在新值。
func TestWALDeleteThenUpsertRecreates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	k := bucketKey("h:2026-10-22T11", "cn", "u1", "m", "", "")
	buckets := map[string]*bucket{}
	monthly := map[string]int64{}
	recs := []walRecord{
		{D: k},
		{B: &bucket{Scope: "h:2026-10-22T11", Realm: "cn", UID: "u1", Model: "m", PT: 7, TT: 7, Req: 1}},
	}
	var lines []string
	for i := range recs {
		b, _ := json.Marshal(&recs[i])
		lines = append(lines, string(b))
	}
	wl := walPathOf(path)
	_ = os.MkdirAll(filepath.Dir(wl), 0o755)
	_ = os.WriteFile(wl, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	replayWAL(wl, buckets, monthly)
	got, ok := buckets[k]
	if !ok {
		t.Fatalf("delete+upsert 后键应被重建")
	}
	if got.PT != 7 {
		t.Fatalf("重建键 PT=%d，期望 7", got.PT)
	}
}

// delete 与 merge-max 共存：旧 upsert 被 max 保留、随后 delete 才是终态（删除是确定性操作）。
func TestWALDeleteCoexistsWithMergeMax(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	k := bucketKey("h:2026-10-22T10", "cn", "u1", "m", "", "")
	// 基线：PT=100。
	buckets := map[string]*bucket{k: {Scope: "h:2026-10-22T10", Realm: "cn", UID: "u1", Model: "m", PT: 100, TT: 100, Req: 1}}
	monthly := map[string]int64{}
	// WAL：旧 upsert PT=80（merge-max 不覆盖）→ delete 键。
	recs := []walRecord{
		{B: &bucket{Scope: "h:2026-10-22T10", Realm: "cn", UID: "u1", Model: "m", PT: 80, TT: 80, Req: 1}},
		{D: k},
	}
	var lines []string
	for i := range recs {
		b, _ := json.Marshal(&recs[i])
		lines = append(lines, string(b))
	}
	wl := walPathOf(path)
	_ = os.MkdirAll(filepath.Dir(wl), 0o755)
	_ = os.WriteFile(wl, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	replayWAL(wl, buckets, monthly)
	if _, ok := buckets[k]; ok {
		t.Fatalf("delete 是确定性终态：merge-max 之后 delete 应真删除，键不应存在")
	}
}

// ---- R-205 v2：walCheckpointAfter 装配期配置化 ----

// 阈值生效：累计 WAL 记录达阈值即重写基线并截断 WAL。
func TestCheckpointThresholdTriggersBaseRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r := NewWithCheckpoint(path, 2) // 阈值=2：每 2 条 WAL 记录重写基线
	r.Add(time.Now(), "cn", "u1", "m", Delta{PromptTokens: 1, HasPromptTokens: true, TotalTokens: 1, HasTotal: true}, true)
	r.flush(false) // recs=1, walCount=1 <2 → 不写基线
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("walCount=1 < 阈值 2，不应重写基线")
	}
	r.Add(time.Now(), "cn", "u2", "m", Delta{PromptTokens: 1, HasPromptTokens: true, TotalTokens: 1, HasTotal: true}, true)
	r.flush(false) // recs=1, walCount→2；本次判定时 walCount=1 仍 <2 → 不写基线
	r.Add(time.Now(), "cn", "u3", "m", Delta{PromptTokens: 1, HasPromptTokens: true, TotalTokens: 1, HasTotal: true}, true)
	r.flush(false) // recs=1, walCount(pre)=2 >=2 → 重写基线 + 截断 WAL
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("walCount(pre)=2 达阈值，应重写基线: %v", err)
	}
	// 基线重写后 WAL 应被截断。
	if raw, err := os.ReadFile(walPathOf(path)); err == nil && len(strings.TrimSpace(string(raw))) != 0 {
		t.Fatalf("基线重写后 WAL 应截断: %s", raw)
	}
}

// 默认回落：New(path) / 阈值<=0 都回落默认 20000，小流量不触发基线重写。
func TestCheckpointDefaultFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	r := New(path) // 默认阈值 20000
	r.Add(time.Now(), "cn", "u1", "m", Delta{PromptTokens: 1, HasPromptTokens: true, TotalTokens: 1, HasTotal: true}, true)
	r.flush(false)
	r.Add(time.Now(), "cn", "u2", "m", Delta{PromptTokens: 1, HasPromptTokens: true, TotalTokens: 1, HasTotal: true}, true)
	r.flush(false) // 累计 2 条 << 20000，不应重写基线
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("默认阈值 20000 下，2 条记录不应触发基线重写")
	}
	if r.walCheckpointAfter != walCheckpointAfterDefault {
		t.Fatalf("默认回落后 walCheckpointAfter=%d，期望 %d", r.walCheckpointAfter, walCheckpointAfterDefault)
	}
}
