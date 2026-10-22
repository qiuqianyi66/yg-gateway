package usage

// 容量保护（ADR-011）：超 maxBuckets 时把桶数压回上限，避免异常流量把内存/文件撑爆。
//
// 与 usage.go 同包，故可直接引用包级常量（maxBuckets / minHourlyKeep /
// rollupTargetRatio / hourlyKeep）、类型（bucket / Recorder）与函数（bucketKey /
// bucketTime / hourLayout / dayLayout）。拆到独立文件仅为控制 usage.go 体积，避免越过
// 千行（深度棘轮：oversizeFiles 只许降不许涨）。

import (
	"log"
	"sort"
	"strings"
	"time"
)

// ancientDayScope 合成「远古」日桶的 scope。维度（realm/uid/model/rate/key）全空，
// 只累计总量，不进任何按维度下钻与逐日时序（见 SnapshotWindow 与 ADR-011）。
// 选纪元起点作合成日期：字典序恒早于任何真实 "d:" 日桶（不会污染"数据起点" since），
// 又能被 bucketTime 正常解析；Snapshot 显式跳过它，绝不进维度/时序/窗口聚合。
const ancientDayScope = "d:0001-01-01"

// Rollup 把过量增长的桶数压回上限（容量保护，ADR-011）。
//
// 两阶段：
//  1. 常态折叠（rollupNormal）：把 >hourlyKeep 的小时桶折成日桶——无论是否超上限都做，
//     维持「不存在 >90d 的小时桶」不变式。
//  2. 压力折叠（rollupWithLimits）：仅当桶数仍 >maxBuckets 时，逐次减半小时保留窗口
//     （90d→45d→…→24h 触底），把窗口内小时桶折成日桶压到 target；触底仍超则把最老
//     日桶合并进合成远古桶（dropOldestDailyUntil）。正常负载（桶数 ≤ 上限）完全不触达。
//
// 幂等：折叠先累加再删源桶，重复调用不重复计数。
func (r *Recorder) Rollup(now time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rollupNormal(now)
	if len(r.buckets) > maxBuckets {
		r.rollupWithLimits(now, int(float64(maxBuckets)*rollupTargetRatio), minHourlyKeep)
	}
}

// rollupNormal 折叠 >hourlyKeep 的小时桶为日桶（按本地日历日）。
func (r *Recorder) rollupNormal(now time.Time) {
	r.foldHourlyOlderThan(now.Add(-hourlyKeep))
}

// rollupWithLimits 压力期容量保护（见 Rollup 与 ADR-011）。参数化以便单测用极小值
// 直接验证算法，而不依赖 maxBuckets 真实常量。
//
// 小时保留窗口从 hourlyKeep 起逐次减半，每轮把窗口外的小时桶折成日桶；窗口触底
// （minKeep）后若仍超上限，则进入日桶丢弃安全阀（dropOldestDailyUntil）。
func (r *Recorder) rollupWithLimits(now time.Time, target int, minKeep time.Duration) {
	keep := hourlyKeep
	for len(r.buckets) > target {
		keep /= 2
		if keep < minKeep {
			keep = minKeep
		}
		r.foldHourlyOlderThan(now.Add(-keep))
		if keep == minKeep {
			break // 已折到最细窗口（最近 24h），再折会破坏尖峰粒度
		}
	}
	// 触底（24h 窗口）仍超上限：把最老日桶合并进合成远古桶，直到桶数 ≤ target。
	if len(r.buckets) > target {
		r.dropOldestDailyUntil(target)
	}
}

// foldHourlyOlderThan 把 ts 早于 cutoff 的小时桶折叠为日桶。已存在同日日桶则累加、
// 否则新建。幂等（先累加后删源桶）。调用方须持 r.mu。
func (r *Recorder) foldHourlyOlderThan(cutoff time.Time) {
	type move struct{ from, to string }
	var moves []move
	for k, b := range r.buckets {
		if !strings.HasPrefix(b.Scope, "h:") {
			continue
		}
		ts, err := time.ParseInLocation(hourLayout, strings.TrimPrefix(b.Scope, "h:"), time.Local)
		if err != nil || !ts.Before(cutoff) {
			continue
		}
		day := "d:" + ts.Format(dayLayout)
		moves = append(moves, move{from: k, to: bucketKey(day, b.Realm, b.UID, b.Model, b.Rate, b.Key)})
	}
	if len(moves) == 0 {
		return
	}
	for _, m := range moves {
		src := r.buckets[m.from]
		if src == nil {
			continue
		}
		dst := r.buckets[m.to]
		if dst == nil {
			cp := *src
			cp.Scope = strings.SplitN(m.to, "|", 2)[0]
			dst = &cp
			r.buckets[m.to] = dst
		} else {
			// COW（R-205）：拷出副本合并再换指针，不改已发布对象（快照读者透过指针读）。
			cp := *dst
			cp.Req += src.Req
			cp.Err += src.Err
			cp.PT += src.PT
			cp.CT += src.CT
			cp.TT += src.TT
			cp.LatMs += src.LatMs
			cp.LatN += src.LatN
			cp.TPS += src.TPS
			cp.TPSN += src.TPSN
			cp.CR += src.CR
			cp.CRN += src.CRN
			cp.CRT += src.CRT
			cp.CH += src.CH
			cp.CM += src.CM
			dst = &cp
			r.buckets[m.to] = dst
		}
		r.walDirty[m.to] = struct{}{}
		delete(r.buckets, m.from)
	}
	r.dirty = true
	log.Printf("[usage] 折叠 %d 个小时桶为日桶（容量保护）", len(moves))
}

// dropOldestDailyUntil 日桶丢弃安全阀（ADR-011）：把最老的真实日桶逐次合并进合成
// 远古桶（ancientDayScope，维度全空），直到桶数 ≤ target 或无日桶可折。累加不丢
// 总量，只丢极老历史的按维度下钻与逐日时序点。调用方须持 r.mu。
//
// 仅在「折叠到 24h 触底后仍超上限」这种极端情形触发（多年级运行、日桶=组合×天数
// 才会缓慢突破）。正常负载永不触达。若没有可折的真实日桶（例如压力全在最新生 24h
// 小时桶上），则保持原状——拒绝丢弃近期尖峰粒度，交由重启/恢复收敛。
func (r *Recorder) dropOldestDailyUntil(target int) {
	type dk struct {
		key  string
		date time.Time
	}
	var days []dk
	for k, b := range r.buckets {
		if !strings.HasPrefix(b.Scope, "d:") || b.Scope == ancientDayScope {
			continue
		}
		if t, ok := bucketTime(b.Scope); ok {
			days = append(days, dk{k, t})
		}
	}
	// 从最老的一天开始合并：避免每轮 O(n) 扫描，一次性排序后线性合并。
	sort.Slice(days, func(i, j int) bool { return days[i].date.Before(days[j].date) })

	abKey := bucketKey(ancientDayScope, "", "", "", "", "")
	for _, d := range days {
		if len(r.buckets) <= target {
			break
		}
		src := r.buckets[d.key]
		if src == nil {
			continue
		}
		ab := r.buckets[abKey]
		if ab == nil {
			ab = &bucket{Scope: ancientDayScope} // 维度全空，只进 totals
			r.buckets[abKey] = ab
		}
		// COW（R-205）：拷出副本合并再换指针。
		cp := *ab
		cp.Req += src.Req
		cp.Err += src.Err
		cp.PT += src.PT
		cp.CT += src.CT
		cp.TT += src.TT
		cp.LatMs += src.LatMs
		cp.LatN += src.LatN
		cp.TPS += src.TPS
		cp.TPSN += src.TPSN
		cp.CR += src.CR
		cp.CRN += src.CRN
		cp.CRT += src.CRT
		cp.CH += src.CH
		cp.CM += src.CM
		r.buckets[abKey] = &cp
		r.walDirty[abKey] = struct{}{}
		delete(r.buckets, d.key)
	}
	r.dirty = true
}
