package usage

import (
	"os"
	"sort"
	"strings"
	"time"
)

// 本文件承载「窗口聚合」的深模块（2026-11-16 从 usage.go 的 SnapshotWindow 切出）。
//
// 为什么切：SnapshotWindow 曾是一个 193 代码行的长函数，内部把 11 个维度累加器
// 写成 11 个局部 map，每个维度都手抄一遍「nil 就 new 再 add」的样板，然后在一个
// 巨循环里对每桶重复这 11 段。切法不是按行数硬砍，而是按职责：
//
//	过滤（哪些桶进本次窗口）→ 累加（一桶进全部维度）→ 组装（快照 + 时序排序 + 回显）
//
// 拆开后维度扩展只改 usageAgg 与本文件的 add 方法，主函数回到「读一眼就懂」的长度；
// usage.go 也从 997 行（阈值 1000，只差 3 行）回到 764 行，解除最大文件压力。

// usageAgg 一次窗口聚合的全部累加器。
type usageAgg struct {
	total aggAcc

	realm map[string]*aggAcc
	acct  map[string]*aggAcc
	model map[string]*aggAcc
	key   map[string]*aggAcc
	hour  map[string]*aggAcc
	day   map[string]*aggAcc

	// 一个账号只属于一个 realm：记下来供前端展示「域」列（keyed() 的 Realm 字段
	// 是空的——它按 key 分组，不知道 realm）。
	acctRealm map[string]string

	creditAcct  map[string]*creditAcc
	creditModel map[string]*creditAcc
	creditKey   map[string]*creditAcc
	// 当前倍率回填的 per-realm+model 缓存：同一模型在大量旧桶上只问一次。
	rateCache map[string]string
}

func newUsageAgg() *usageAgg {
	return &usageAgg{
		realm:       map[string]*aggAcc{},
		acct:        map[string]*aggAcc{},
		model:       map[string]*aggAcc{},
		key:         map[string]*aggAcc{},
		hour:        map[string]*aggAcc{},
		day:         map[string]*aggAcc{},
		acctRealm:   map[string]string{},
		creditAcct:  map[string]*creditAcc{},
		creditModel: map[string]*creditAcc{},
		creditKey:   map[string]*creditAcc{},
		rateCache:   map[string]string{},
	}
}

// bumpAcc 取（必要时创建）某维度累加器并累加一桶——替代 11 处手抄的 nil 样板。
func bumpAcc(m map[string]*aggAcc, k string, b *bucket, usd float64) {
	acc := m[k]
	if acc == nil {
		acc = &aggAcc{}
		m[k] = acc
	}
	acc.add(b, usd)
}

// consume 把一桶累加进全部维度。usd 由调用方按桶预算一次传进来，
// 保证 total / 各维度 / 时序的美元值同源。
func (a *usageAgg) consume(b *bucket, usd float64, nicks, keyNames map[string]string, currentRate func(realm, model string) string) {
	a.total.add(b, usd)

	bumpAcc(a.realm, b.Realm, b, usd)
	bumpAcc(a.acct, b.UID, b, usd)
	bumpAcc(a.model, b.Model, b, usd)
	// 按下游 Key 归因（ADR-001 F-005 二期）。空 Key（旧桶 / 未启用鉴权的放行
	// 请求）统一落在同一个空键分组，由 label 函数翻成「未归因」展示名。
	bumpAcc(a.key, b.Key, b, usd)

	if strings.HasPrefix(b.Scope, "h:") {
		bumpAcc(a.hour, strings.TrimPrefix(b.Scope, "h:"), b, usd)
	} else {
		bumpAcc(a.day, strings.TrimPrefix(b.Scope, "d:"), b, usd)
	}

	if a.acctRealm[b.UID] == "" {
		a.acctRealm[b.UID] = b.Realm
	}

	// 积分只统计与 credit 同时观测到的桶（CRN = 0 的桶没有 credit 样本）。
	if b.CRN > 0 {
		a.consumeCredit(b, nicks, keyNames, currentRate)
	}
}

// consumeCredit 累加积分的三个维度（账号 / 模型 / 下游 Key）。
//
// 倍率回填：旧桶可能缺 Rate（历史版本没记），此时用 currentRate 现取；取不到的
// 保持空串 = 「未知倍率」，由前端原样展示，不伪造价格。
func (a *usageAgg) consumeCredit(b *bucket, nicks, keyNames map[string]string, currentRate func(realm, model string) string) {
	ca := a.creditAcct[b.UID]
	if ca == nil {
		ca = &creditAcc{CreditAgg: CreditAgg{
			Key:      b.UID,
			Realm:    b.Realm,
			Nickname: nicks[b.UID],
		}}
		a.creditAcct[b.UID] = ca
	}
	ca.add(b)

	model := canonicalUsageModel(b.Model)
	rate := b.Rate
	if rate == "" && currentRate != nil {
		cacheKey := b.Realm + "\x00" + model
		if cached, ok := a.rateCache[cacheKey]; ok {
			rate = cached
		} else {
			rate = currentRate(b.Realm, model)
			a.rateCache[cacheKey] = rate
		}
	}
	modelKey := model + "\x00" + rate
	cm := a.creditModel[modelKey]
	if cm == nil {
		cm = &creditAcc{CreditAgg: CreditAgg{Key: model, Rate: rate}}
		a.creditModel[modelKey] = cm
	}
	cm.add(b)

	// 积分按下游 Key 归因：与账号/模型维度同口径（只统计与 credit 同时
	// 观测到的 Token），展示名走 keyNames 映射。
	ck := a.creditKey[b.Key]
	if ck == nil {
		ck = &creditAcc{CreditAgg: CreditAgg{
			Key:      b.Key,
			Nickname: keyNames[b.Key],
		}}
		a.creditKey[b.Key] = ck
	}
	ck.add(b)
}

// assemble 组装最终快照：维度排序、时序拼接、文件大小、数据起点、窗口回显。
func (a *usageAgg) assemble(w Window, explicit bool, matched int, since, path string, nicks, keyNames map[string]string) Snapshot {
	snap := Snapshot{
		Totals: a.total.finish(),
		ByRealm: keyed(a.realm, func(k string) (string, string) {
			return k, ""
		}),
		ByAccount: keyed(a.acct, func(k string) (string, string) {
			return k, nicks[k]
		}),
		ByModel: keyed(a.model, func(k string) (string, string) { return k, "" }),
		// Key 维度的展示名：有映射用映射，没映射（已删除的 Key / 旧桶）回落显示
		// ID 本身；空 ID 是一条真实分组（未启用鉴权时放行的请求、版本 5 之前的
		// 旧桶），标签化为「未归因」而不是伪造一把不存在的 Key。
		ByKey: keyed(a.key, func(k string) (string, string) {
			if k == "" {
				return "(未归因)", ""
			}
			return k, keyNames[k]
		}),
		CreditByAccount: creditKeyed(a.creditAcct),
		CreditByModel:   creditKeyed(a.creditModel),
		CreditByKey: creditKeyedLabeled(a.creditKey, func(k string) (string, string) {
			if k == "" {
				return "(未归因)", ""
			}
			return k, keyNames[k]
		}),
		Buckets:   matched,
		Generated: time.Now().Format(time.RFC3339),
	}
	for i := range snap.ByAccount {
		snap.ByAccount[i].Realm = a.acctRealm[snap.ByAccount[i].Key]
	}

	// 日点（升序）+ 小时点（升序）拼成一条连续时序。
	dayKeys := make([]string, 0, len(a.day))
	for k := range a.day {
		dayKeys = append(dayKeys, k)
	}
	sort.Strings(dayKeys)
	for _, k := range dayKeys {
		snap.Series = append(snap.Series, Point{T: k, Scope: "day", Agg: a.day[k].finish()})
	}
	hourKeys := make([]string, 0, len(a.hour))
	for k := range a.hour {
		hourKeys = append(hourKeys, k)
	}
	sort.Strings(hourKeys)
	for _, k := range hourKeys {
		snap.Series = append(snap.Series, Point{T: k, Scope: "hour", Agg: a.hour[k].finish()})
	}

	if path != "" {
		if fi, err := os.Stat(path); err == nil {
			snap.FileBytes = fi.Size()
		}
	}
	// since 去掉 scope 前缀（"h:2026-09-16T13" → "2026-09-16T13"）给前端展示；
	// 无任何桶时保持空（无数据不伪造起点）。
	snap.Since = strings.TrimPrefix(strings.TrimPrefix(since, "h:"), "d:")
	// 回显实际生效的区间：**仅显式区间口径**。滚动窗口由 hours 表达（前端自己
	// 知道选的是哪个预设），全部历史没有区间——两者回显都会变成噪音。
	if explicit {
		if !w.From.IsZero() {
			snap.WindowFrom = w.From.Format(time.RFC3339)
		}
		if !w.To.IsZero() {
			snap.WindowTo = w.To.Format(time.RFC3339)
		}
	}
	return snap
}

// SnapshotWindow 聚合**所选窗口内**的桶，产出面板一次拉取的全部用量视图数据。
//
// 窗口语义见 Window：滚动窗口（Hours）/ 显式区间（From-To）/ 全部历史。卡片汇总、
// 按域、按账号、按模型、按 Key、时序**全部**按同一窗口口径统计——切窗口时所有数字
// 随之变化（曾长期是"卡片为全部历史累计、hours 只改时序分片"的口径，界面上被读成
// "筛选没生效"，已废弃）。小时桶按整点入窗；日桶（Rollup 折叠出的长期数据）按日
// 起点入窗，故小时窗口天然不含更早的日桶。
//
// keyNames 是下游 Key ID→展示名映射（来自 livecfg 快照，仅用于展示，不含明文）。
// nil 或缺失的 ID 展示为空名，前端回落显示 ID 本身。
// currentRate 同 SnapshotWithRates。
// pricing 按**裸模型名**返回 OpenRouter 参考价（$/1M tokens），折算美元价值；
// nil 或返回零值 = 该模型不折算（不伪造 $ 数）。纯展示层，不影响任何 token 计数。
func (r *Recorder) SnapshotWindow(w Window, nicks map[string]string, keyNames map[string]string, currentRate func(realm, model string) string, pricing func(model string) USDPrice) Snapshot {
	if r == nil {
		return Snapshot{Generated: time.Now().Format(time.RFC3339)}
	}
	from, to := w.bounds()
	explicit := !w.From.IsZero() || !w.To.IsZero()
	windowed := !from.IsZero() || !to.IsZero()

	// R-205 读路径优化：锁内只拷桶**指针**（~8B/桶，不再整桶深拷贝 ~90B/桶）。
	// 正确性靠 COW 发布（Add/Rollup 改桶时换指针，不改已发布对象）——快照透过指针
	// 读到的是一个一致时点，无数据竞争。大桶场景锁持有时间从 O(N×桶体) 降到 O(N×指针)。
	r.mu.Lock()
	views := make([]*bucket, 0, len(r.buckets))
	for _, b := range r.buckets {
		views = append(views, b)
	}
	r.mu.Unlock()

	agg := newUsageAgg()
	// 数据起点（全库最早分片）：不受窗口影响，表示"记录自何时开始"。scope 字典序
	// 即时间序（同前缀内同格式排序；"d:" 恒早于 "h:"——日桶只来自 90 天前的小时折叠）。
	since := ""
	matched := 0
	for i := range views {
		b := views[i]
		// 合成远古桶（ancientDayScope，维度全空）：只进"全部历史"的 totals，绝不进
		// 维度/时序/窗口聚合，也不污染"数据起点" since（ADR-011）。窗口口径下它字典序
		// 极老、本就不在任何真实窗口内，故直接跳过——避免 Years-ago 的聚合堆到图表
		// 最左端或生成空维度行。
		if b.Scope == ancientDayScope {
			if !windowed {
				var usd float64
				if pricing != nil {
					usd = bucketUSD(b, pricing(canonicalUsageModel(b.Model)))
				}
				agg.total.add(b, usd)
			}
			continue
		}
		if b.Scope < since || since == "" {
			since = b.Scope
		}
		if windowed {
			ts, ok := bucketTime(b.Scope)
			// 解析失败的脏桶不进窗口聚合（也不该出现在任何口径里）。
			if !ok {
				continue
			}
			if !from.IsZero() && ts.Before(from) {
				continue
			}
			if !to.IsZero() && ts.After(to) {
				continue
			}
		}
		matched++
		// 美元折算（纯展示）：按桶内模型查 OpenRouter 参考价，无定价则 usd=0。
		// 每桶只算一次，传给所有维度的累加器，保证 total/byModel/byAccount/
		// byKey/时序的美元值同源。
		var usd float64
		if pricing != nil {
			usd = bucketUSD(b, pricing(canonicalUsageModel(b.Model)))
		}
		agg.consume(b, usd, nicks, keyNames, currentRate)
	}

	return agg.assemble(w, explicit, matched, since, r.path, nicks, keyNames)
}
