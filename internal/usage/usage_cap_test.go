package usage

import (
	"testing"
	"time"
)

// 直接注入小时桶 / 日桶（绕过 Add 的维度约束），专测折叠算法本身。
func injectHourly(r *Recorder, ts time.Time, realm, uid, model, rate, key string, req, pt int64) {
	scope := "h:" + ts.Format(hourLayout)
	r.buckets[bucketKey(scope, realm, uid, model, rate, key)] = &bucket{
		Scope: scope, Realm: realm, UID: uid, Model: model, Rate: rate, Key: key,
		Req: req, PT: pt,
	}
}

func injectDaily(r *Recorder, day, realm, uid, model, rate, key string, req, pt int64) {
	scope := "d:" + day
	r.buckets[bucketKey(scope, realm, uid, model, rate, key)] = &bucket{
		Scope: scope, Realm: realm, UID: uid, Model: model, Rate: rate, Key: key,
		Req: req, PT: pt,
	}
}

// sumBuckets 累加 map 中所有桶的 req/pt（校验"折叠/丢弃不丢总量"）。
func sumBuckets(r *Recorder) (req, pt int64) {
	for _, b := range r.buckets {
		req += b.Req
		pt += b.PT
	}
	return
}

// TestRollupCapNormalUnchanged：桶数 ≤ 上限时，Rollup 只折 >90d 小时桶，窗口内小时桶
// 保留，且**不产生**远古桶（压力路径不触达，行为零回归）。对应 ADR-011 验收用例 1。
func TestRollupCapNormalUnchanged(t *testing.T) {
	r := New("")
	now := time.Now()
	old := now.AddDate(0, 0, -100) // >90d，必被常态折叠
	injectHourly(r, old, "cn", "u", "m", "0.5", "k", 1, 10)
	injectHourly(r, old.Add(time.Hour), "cn", "u", "m", "0.5", "k", 1, 10)
	injectHourly(r, now, "cn", "u", "m", "0.5", "k", 1, 10)
	injectHourly(r, now.Add(-time.Hour), "cn", "u", "m", "0.5", "k", 1, 10)

	r.Rollup(now)

	// 2 个 >90d 小时桶折成 1 个日桶；2 个窗口内小时桶保留 → 共 3 个桶。
	if len(r.buckets) != 3 {
		t.Fatalf("常态折叠后桶数 = %d, want 3（2 老折 1 日 + 2 窗口内小时）", len(r.buckets))
	}
	for k, b := range r.buckets {
		if b.Scope == ancientDayScope {
			t.Fatalf("常态负载不应产生远古桶，却出现在 %s", k)
		}
	}
	req, pt := sumBuckets(r)
	if req != 4 || pt != 40 {
		t.Fatalf("总量 req/pt = %d/%d, want 4/40（折叠不丢量）", req, pt)
	}
}

// TestRollupCapPressureFoldsToTarget：桶数 > 上限（全为窗口内小时桶，过去 30 天每天 4 个）
// 时，rollupWithLimits 把窗口外小时桶逐次折成日桶，压到 ≤ target；总量不丢、幂等；
// 本场景日桶很少，阀门（合日桶）不应触发。对应 ADR-011 验收用例 2 + 幂等。
func TestRollupCapPressureFoldsToTarget(t *testing.T) {
	r := New("")
	now := time.Now()
	base := now.Truncate(24 * time.Hour)
	var totalReq, totalPT int64
	for k := 0; k < 30; k++ {
		for _, h := range []int{0, 6, 12, 18} {
			ts := base.Add(-time.Duration(k)*24*time.Hour + time.Duration(h)*time.Hour)
			injectHourly(r, ts, "cn", "u", "m", "0.5", "k", 1, 10)
			totalReq++
			totalPT += 10
		}
	}
	if len(r.buckets) != 120 {
		t.Fatalf("注入桶数 = %d, want 120", len(r.buckets))
	}

	// 极小参数直接验证算法：target=51, minKeep=24h。
	r.rollupWithLimits(now, 51, 24*time.Hour)

	if len(r.buckets) == 0 || len(r.buckets) > 51 {
		t.Fatalf("压力折叠后桶数 = %d, want (0,51]", len(r.buckets))
	}
	for _, b := range r.buckets {
		if b.Scope == ancientDayScope {
			t.Fatal("本场景日桶很少，不应产生远古桶")
		}
	}
	req, pt := sumBuckets(r)
	if req != totalReq || pt != totalPT {
		t.Fatalf("折叠后总量 req/pt = %d/%d, want %d/%d（不丢量）", req, pt, totalReq, totalPT)
	}

	// 幂等：再调一次，桶数与总量不变。
	before := len(r.buckets)
	req2, pt2 := sumBuckets(r)
	r.rollupWithLimits(now, 51, 24*time.Hour)
	if len(r.buckets) != before {
		t.Fatalf("二次折叠桶数 = %d, want %d（幂等被破坏）", len(r.buckets), before)
	}
	if req2 != req || pt2 != pt {
		t.Fatalf("二次折叠总量变化: %d/%d vs %d/%d", req2, pt2, req, pt)
	}
}

// TestRollupCapDailyDropValve：折到 24h 触底仍超上限（100 个真实日桶）时，丢弃安全阀把
// 最老日桶合并进合成远古桶，直到桶数 ≤ target；总量不丢；远古桶恰好 1 个且维度全空。
// 对应 ADR-011 验收用例 3。
func TestRollupCapDailyDropValve(t *testing.T) {
	r := New("")
	now := time.Now()
	base := now.Truncate(24 * time.Hour)
	for k := 1; k <= 100; k++ {
		day := base.Add(-time.Duration(k) * 24 * time.Hour).Format(dayLayout)
		injectDaily(r, day, "cn", "u", "m", "0.5", "k", 1, 10)
	}
	if len(r.buckets) != 100 {
		t.Fatalf("注入日桶数 = %d, want 100", len(r.buckets))
	}
	totalReq, totalPT := sumBuckets(r) // 100, 1000

	// target=34, minKeep=24h：日桶过多 → 触底后触发丢弃安全阀。
	r.rollupWithLimits(now, 34, 24*time.Hour)

	if len(r.buckets) > 34 {
		t.Fatalf("阀门后桶数 = %d, want ≤ 34", len(r.buckets))
	}
	ancientCount := 0
	for _, b := range r.buckets {
		if b.Scope == ancientDayScope {
			ancientCount++
			if b.Realm != "" || b.UID != "" || b.Model != "" || b.Rate != "" || b.Key != "" {
				t.Fatal("远古桶维度应全空")
			}
		}
	}
	if ancientCount != 1 {
		t.Fatalf("远古桶数 = %d, want 1", ancientCount)
	}
	req, pt := sumBuckets(r)
	if req != totalReq || pt != totalPT {
		t.Fatalf("阀门总量 req/pt = %d/%d, want %d/%d（总量不丢）", req, pt, totalReq, totalPT)
	}
}

// TestSnapshotExcludesAncientBucket：注入远古桶后，Snapshot 把其量只进 totals（全部历史），
// 绝不进 by_* / 时序 / since；窗口口径下完全不进任何聚合。对应 ADR-011 验收用例 4。
func TestSnapshotExcludesAncientBucket(t *testing.T) {
	r := New("")
	now := time.Now()
	// 正常窗口内小时桶（1 请求 / 5 prompt）。
	r.Add(now, "cn", "u1", "m", Delta{PromptTokens: 5, HasPromptTokens: true}, true)
	// 注入远古桶（维度全空，100 请求 / 1000 prompt）。
	r.buckets[bucketKey(ancientDayScope, "", "", "", "", "")] = &bucket{
		Scope: ancientDayScope, Req: 100, PT: 1000,
	}

	// 全部历史口径：totals 含远古量（101/1005），by_* / 时序 / since 不含。
	all := r.SnapshotWindow(Window{}, nil, nil, nil, nil)
	if all.Totals.Requests != 101 || all.Totals.PromptTokens != 1005 {
		t.Fatalf("全部历史 totals = %d/%d, want 101/1005（含远古）", all.Totals.Requests, all.Totals.PromptTokens)
	}
	if len(all.ByAccount) != 1 || all.ByAccount[0].Requests != 1 || all.ByAccount[0].PromptTokens != 5 {
		t.Fatalf("by_account 不应含远古行: %+v", all.ByAccount)
	}
	if len(all.Series) != 1 || all.Series[0].Scope != "hour" {
		t.Fatalf("时序不应含远古日点: %+v", all.Series)
	}
	if all.Since == "0001-01-01" {
		t.Fatalf("since 不应被远古桶污染: %q", all.Since)
	}

	// 窗口口径（24h）：远古桶完全不进任何聚合（含 totals）。
	win := r.Snapshot(24, nil)
	if win.Totals.Requests != 1 || win.Totals.PromptTokens != 5 {
		t.Fatalf("24h 窗口 totals = %d/%d, want 1/5（远古应被排除）", win.Totals.Requests, win.Totals.PromptTokens)
	}
}

// TestRollupCapPersistence：远古桶随 usage.json 落盘并经 load 无损恢复；恢复后 Snapshot
// 仍只把它进 totals。保证安全阀的结果可跨重启保留。
func TestRollupCapPersistence(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/usage.json"
	r1 := New(path)
	now := time.Now()
	base := now.Truncate(24 * time.Hour)
	for k := 1; k <= 100; k++ {
		day := base.Add(-time.Duration(k) * 24 * time.Hour).Format(dayLayout)
		injectDaily(r1, day, "cn", "u", "m", "0.5", "k", 1, 10)
	}
	r1.rollupWithLimits(now, 34, 24*time.Hour)
	r1.Save()

	r2 := New(path)
	ancientCount := 0
	for _, b := range r2.buckets {
		if b.Scope == ancientDayScope {
			ancientCount++
		}
	}
	if ancientCount != 1 {
		t.Fatalf("重启后远古桶数 = %d, want 1（应随落盘恢复）", ancientCount)
	}
	all := r2.SnapshotWindow(Window{}, nil, nil, nil, nil)
	if all.Totals.Requests != 100 || all.Totals.PromptTokens != 1000 {
		t.Fatalf("重启后 totals = %d/%d, want 100/1000", all.Totals.Requests, all.Totals.PromptTokens)
	}
}
