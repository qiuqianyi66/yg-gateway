// 持久化：本地 state.json 落盘/加载、Redis 快照镜像（StoreSnapshotter）、
// 后台 flusher、择新恢复（RestoreFromSnapshot）。
package pool

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"yg-gateway/internal/auth"
)

var flushInterval = 5 * time.Second

// persistLogEvery 连续落盘失败每 N 次打一条提醒（flusher 5s 一把 ≈ 1 分钟一次），
// 避免磁盘持续满/权限丢失时日志刷屏。
const persistLogEvery = 12

// snapshot 池状态快照（Redis 镜像用）。与本地 state.json 同源（stateFile），
// 额外带 savedAt 时间戳供"择新恢复"（比较本地与 Redis 快照的新旧）。
type snapshot struct {
	stateFile
	SavedAt time.Time `json:"saved_at"`
}

// Pool 账号池。
type StoreSnapshotter interface {
	SaveState(data []byte)
	LoadState() ([]byte, bool)
}

// RestoreFromSnapshot 择新恢复：比较本地 state.json 与 Redis 快照，采用较新者。
//
// 本地**可用**时按新旧择一（快照不早于本地 → 采用快照，否则本地优先）；本地**不可用**
// （state.json 缺失或不可读，典型为首次在新卷/新节点启动）时**采用快照**——此时本地根本
// 没有可"优先"的状态，快照是本轮唯一的运行态来源，这正是快照作为「启动恢复备份」的核心
// 场景。分支情形：无快照 / 快照无 savedAt → 本地优先（无判据可比）。
//
// 每种情形都打一条对应的恢复来源日志，便于对账。必须在 SyncToDir 之前调用
// （SyncToDir 只增删不入值：值只能来自本地 load 或本函数采用快照）。
func (p *Pool) RestoreFromSnapshot() {
	store := p.store
	if store == nil || p.stateFp == "" {
		return
	}
	localInfo, localErr := os.Stat(p.stateFp)
	raw, ok := store.LoadState()
	if !ok {
		if localErr == nil {
			log.Printf("pool: 恢复来源=本地 state.json（无 Redis 快照）")
		}
		return
	}
	var snap snapshot
	if json.Unmarshal(raw, &snap) != nil || snap.SavedAt.IsZero() {
		// 快照无 savedAt：无法比较新旧，本地优先。
		log.Printf("pool: 恢复来源=本地 state.json（Redis 快照无 saved_at）")
		return
	}
	if localErr != nil {
		// 本地不可用 → 采用快照（本地没有可"优先"的状态）。
		//
		// 旧实现把该情形与「本地较新」合并成同一个 fall-through：既不改内存、不置 dirty
		//（有效快照被静默丢弃），又打出"本地 state.json（较新于 Redis 快照 …）"——一次
		// 从未发生过的比较，把排障引向根本不存在的本地文件；随后 SyncToDir 只增删不入值，
		// 全池运行态（credits/冷却/熔断计数/usedSeq/lastUsed）被清零。
		p.adoptSnapshot(snap)
		log.Printf("pool: 恢复来源=Redis 快照 (saved_at=%s)（本地 state.json 不可用: %v）",
			snap.SavedAt.Format(time.RFC3339), localErr)
		return
	}
	if !localInfo.ModTime().After(snap.SavedAt) {
		// 快照不早于本地 → 采用快照。
		p.adoptSnapshot(snap)
		log.Printf("pool: 恢复来源=Redis 快照 (saved_at=%s)", snap.SavedAt.Format(time.RFC3339))
		return
	}
	// 走到这里必然是「本地存在且严格新于快照」，日志结论属实。
	log.Printf("pool: 恢复来源=本地 state.json（较新于 Redis 快照 %s）", snap.SavedAt.Format(time.RFC3339))
}

// Acquire 为账号占一个在途名额；false 表示该账号已达上限（或不存在）。
// 必须在成功 Pick 后调用；调用方负责 defer Release。
func (p *Pool) startFlusher() {
	stopCh := make(chan struct{})
	// 在启动 goroutine 前同步写入（避免与测试对 flushInterval 的恢复写竞争）；
	// stopCh 同步登记，Close 才能可靠停止（New 与 startFlusher 之间无并发窗口）。
	p.stopCh = stopCh
	interval := flushInterval
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				// P2 两段式：持锁只做「快照收集」（snapshotForSaveLocked 内含静默期
				// 兜底清理，ADR-005 D-500——即使无脏数据也周期性清理过期模型级台账，
				// 之前由 pick 热路径承担，移出后不能漏掉），序列化与磁盘 IO 在锁外由
				// saveMu 串行化。别图省事在这里持锁 saveLocked。
				p.mu.Lock()
				job, save := p.snapshotForSaveLocked()
				p.mu.Unlock()
				if save {
					p.saveUnlocked(job)
				}
			case <-stopCh:
				return
			}
		}
	}()
}

// pruneAllLocked 全池清理过期模型级台账（modelCost/modelCooldowns）。
// 调用方必须已持有 p.mu 写锁。仅做内存卫生，不置 dirty（过期条目落盘本就
// 被 stateOverviewLocked 过滤，删除不改变持久化内容）。
func (p *Pool) pruneAllLocked(now time.Time) {
	for uid, e := range p.byUID {
		// 观测过期 = 重新变未知：把已消解的条目重新打开，否则 unknowns_open 会与真实
		// 未知脱钩（消解是单向闩锁，而成本观测带 modelCostTTL）。
		// 锁序 p.mu(写) → registry.mu：registry 是零依赖 domain 包，不会回拿 p.mu。
		for _, m := range e.pruneExpiredModelCosts(now) {
			p.reopenCostUnknown(uid, m)
		}
		e.pruneExpiredModelCooldowns(now)
	}
}

// Flush 同步把内存状态落盘（幂等：无变更不写盘）。供进程退出前调用。
// 两段式：持锁收集快照 → 解锁 → 锁外落盘（P2，退出路径也不进序列化临界区）。
func (p *Pool) Flush() {
	p.mu.Lock()
	job, save := p.snapshotForSaveLocked()
	p.mu.Unlock()
	if save {
		p.saveUnlocked(job)
	}
}

// Add 加入账号；已存在则保留原状态、更新凭证（upsert 单账号，不影响其他账号）。
func (p *Pool) load() {
	raw, err := os.ReadFile(p.stateFp)
	if err != nil {
		// 文件不存在 = 首次启动，不是错误；其它读错误（权限等）也保持旧行为不致命
		// （账号状态可由 auths/ 重建，丢失的只是健康度观测）。
		return
	}
	var sf stateFile
	if json.Unmarshal(raw, &sf) != nil {
		// 历史实现这里直接 return —— 损坏文件被静默当成「没有状态」，账号健康度全丢。
		// 至少要让启动方看见（ADR-009 同族：加载失败不得静默）。
		p.loadErr = fmt.Errorf("state.json 解析失败（%s）：文件损坏，账号健康度无法恢复", p.stateFp)
		return
	}
	if sf.Version > stateFileVersion {
		p.loadErr = fmt.Errorf("state.json 版本 %d 比本程序支持的 %d 更新（典型是回滚到旧版）；"+
			"旧程序读新文件会静默丢字段，因此拒绝启动。请用新版本程序，或把 %s 换成升级前的备份",
			sf.Version, stateFileVersion, p.stateFp)
		return
	}
	p.applyAccountsLocked(sf.Accounts)
}

// LoadErr 返回加载期错误（ADR-009 同族）。构造期写、之后只读；启动方据此决定是否拒绝启动。
// 只在「文件存在但损坏」或「版本比程序新」时非空——文件不存在（首次启动）不算错误。
func (p *Pool) LoadErr() error { return p.loadErr }

// applyAccountsLocked 用持久化账号状态覆盖/插入 byUID（placeholder 凭证，Add 时换全）。
// 本地 load() 与 Redis 快照恢复共用；调用方必须已持有 p.mu。
func (p *Pool) applyAccountsLocked(accounts map[string]stateAccount) {
	now := time.Now()
	for uid, s := range accounts {
		// err_total 优先；旧文件的 err_count（连续错误）作一次性迁移源映射进来（二者取较大者，
		// 尽最大可能保留历史观测信号——旧语义下 err_count 也真实发生过错误，不应丢）。
		errTotal := s.ErrTotal
		if int64(s.ErrCount) > errTotal {
			errTotal = int64(s.ErrCount)
		}
		e := &entry{
			a:                        &auth.Auth{UID: uid}, // placeholder，Add 时会换成完整凭证
			credits:                  s.Credits,
			creditsTotal:             s.CreditsTotal,
			creditsExpiring:          s.CreditsExpiring,
			creditsEarliestExpiry:    s.CreditsEarliestExpiry,
			creditsEarliestRemaining: s.CreditsEarliestRemaining,
			disabled:                 s.Disabled,
			paused:                   s.Paused,
			reason:                   s.Reason,
			until:                    s.Until,
			coolKind:                 s.CoolKind,
			successCount:             s.SuccessCount,
			errTotal:                 errTotal,
			lastErr:                  s.LastErr,
			lastSuccess:              s.LastSuccess,
			lastCheckinDay:           s.LastCheckinDay,
			tokenUsage:               s.TokenUsage,
			softStreak:               s.SoftStreak,
			sessionDeadFails:         s.SessionDeadFails,
			consecutiveFails:         s.ConsecutiveFails,
		}
		// 到期快照按当前时刻惰性清洗：已过期、零剩余或超出总余额的脏数据不恢复。
		if e.creditsExpiring < 0 {
			e.creditsExpiring = 0
		}
		if e.creditsExpiring > e.credits {
			e.creditsExpiring = e.credits
		}
		if e.creditsEarliestRemaining < 0 || e.creditsEarliestRemaining > e.credits {
			e.creditsEarliestRemaining = 0
		}
		if e.creditsEarliestRemaining == 0 || e.creditsEarliestExpiry.IsZero() || !now.Before(e.creditsEarliestExpiry) {
			e.creditsEarliestExpiry = time.Time{}
			e.creditsEarliestRemaining = 0
		}
		// 熔断器持久化恢复：breakerUntil 未过期才恢复（过期不复活），retryCount 仅在
		// 熔断仍有效时保留（否则归零，不保留无用退避指数）。
		if s.BreakerUntil != nil && now.Before(*s.BreakerUntil) {
			e.breakerUntil = *s.BreakerUntil
			e.retryCount = s.RetryCount
		}
		// 连败降权：未过期才恢复（过期/零值不写不复活）。
		if s.DegradeUntil != nil && now.Before(*s.DegradeUntil) {
			e.degradeUntil = *s.DegradeUntil
		}
		// 模型级独立冷却（6004 重置墙钟 / 11102 负缓存）：惰性过滤已过期条目。
		if len(s.ModelCooldowns) > 0 {
			for m, mc := range s.ModelCooldowns {
				if mc.Until.IsZero() || !now.Before(mc.Until) {
					continue
				}
				if e.modelCooldowns == nil {
					e.modelCooldowns = map[string]modelCooldown{}
				}
				e.modelCooldowns[m] = modelCooldown{Until: mc.Until, ResetAt: mc.ResetAt, Reason: mc.Reason, AuditOnly: mc.AuditOnly}
			}
		}
		// 成本账本：惰性过滤过期（modelCostTTL 外不恢复）+ 剔除结构破损条目
		// （负 per1k / 零 LastSeen——上游异常或旧文件手改产生的脏数据）。
		if len(s.ModelCosts) > 0 {
			for m, mc := range s.ModelCosts {
				if mc.LastSeen.IsZero() || now.Sub(mc.LastSeen) > modelCostTTL || mc.CostPer1k < 0 {
					continue
				}
				if e.modelCost == nil {
					e.modelCost = map[string]modelCostEntry{}
				}
				e.modelCost[m] = modelCostEntry{CostPer1k: mc.CostPer1k, LastSeen: mc.LastSeen, Samples: mc.Samples}
			}
		}
		p.byUID[uid] = e
	}
}

// applySnapshotLocked 用 Redis 快照覆盖内存状态（已在择新判定后采用）。调用方必须已持有 p.mu。
// adoptSnapshot 采用 Redis 快照为当前池状态，并置 dirty 让下一次落盘把它物化回本地
// state.json（否则快照只在内存生效，下次崩溃恢复又回到旧本地文件）。
func (p *Pool) adoptSnapshot(s snapshot) {
	p.mu.Lock()
	p.applySnapshotLocked(s)
	p.mu.Unlock()
	p.dirty.Store(true)
}
func (p *Pool) applySnapshotLocked(s snapshot) {
	p.byUID = map[string]*entry{}
	p.applyAccountsLocked(s.Accounts)
}

// saveJob 一次「锁外落盘」任务：状态快照 + 落盘时才读的依赖（store）。
// store 必须在持 p.mu 的收集点一并捕获——SetStore 在 p.mu 下写 p.store，若让
// saveUnlocked 自己去读，就变成「无锁读 vs 持锁写」的理论数据竞争（-race 可证）；
// 快照收集点同锁，天然建立 happens-before。
type saveJob struct {
	sf    stateFile
	store StoreSnapshotter
}

// snapshotForSaveLocked 持锁收集一次落盘快照：先 prune（静默期兜底清理，ADR-005
// D-500——即使无脏数据也要做），再原子抢占 dirty，最后收集 stateFile 与 store。
// 返回 ok=false = 无需落盘（无脏数据或无 stateFp）。调用方必须已持有 p.mu 写锁。
//
// 与 saveUnlocked 配对构成「持锁收集 + 锁外落盘」两段式：把 JSON 序列化与磁盘 IO
// 移出选号临界区（P2）。stateOverviewLocked 产出的是全新 stateFile（值拷贝、无 map
// 别名，tokenUsage 为值类型），因此锁外序列化不会与并发选号竞争。
func (p *Pool) snapshotForSaveLocked() (saveJob, bool) {
	p.pruneAllLocked(time.Now())
	if p.stateFp == "" || !p.dirty.Swap(false) {
		return saveJob{}, false
	}
	return saveJob{sf: p.stateOverviewLocked(), store: p.store}, true
}

// saveUnlocked 序列化 + 原子写 + Redis 镜像。**调用方不得持有 p.mu**（这是 P2 修复的
// 要点：重活不放热路径）；并发安全性由 saveMu 保证，多调用方可同时进入。
// 除参数 job 外只碰 saveMu 保护的 persistFails 与构造后不可变的 stateFp。
func (p *Pool) saveUnlocked(job saveJob) {
	p.saveMu.Lock()
	defer p.saveMu.Unlock()

	raw, err := json.MarshalIndent(job.sf, "", "  ")
	if err != nil {
		p.notePersistFail(err)
		return
	}
	if dir := filepath.Dir(p.stateFp); dir != "" {
		// 忽略：MkdirAll 失败必然导致下一步 WriteFile(tmp) 失败并走 notePersistFail 上报，
		// 失败不会消失（判据③）；不影响状态快照的真值——原子性由下面的 tmp+rename 保证。
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := p.stateFp + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		p.notePersistFail(err)
		return
	}
	if err := os.Rename(tmp, p.stateFp); err != nil {
		p.notePersistFail(err)
		return
	}
	if p.persistFails > 0 {
		// 从连续失败中恢复：打一条恢复日志，避免"错误打完却无人知道已恢复"。
		log.Printf("pool: state.json 落盘恢复（此前连续失败 %d 次）", p.persistFails)
		p.persistFails = 0
	}
	// 同步镜像一份快照到 Redis（fire-and-forget），与本地 state.json 并存作恢复备份。
	if job.store != nil {
		snapRaw, err := json.Marshal(snapshot{stateFile: job.sf, SavedAt: time.Now()})
		if err == nil {
			job.store.SaveState(snapRaw)
		}
	}
}

// saveLocked 持锁保存的便捷入口：= snapshotForSaveLocked + saveUnlocked。
//
// **只用于低频/管理路径与测试**（当前仅 syncLocked）。它仍在调用方的 p.mu 内完成
// 序列化与写盘——热路径（flusher 每 5s / Flush / Remove）一律走两段式，
// 别图省事调这里：那正是 P2 要消掉的「持 p.mu 做全量序列化」。
func (p *Pool) saveLocked() {
	if job, ok := p.snapshotForSaveLocked(); ok {
		p.saveUnlocked(job)
	}
}

// notePersistFail 记录一次本地 state.json 落盘失败，并按节流规则决定是否打日志：
// 首败（状态成功→失败）打完整错误、每 persistLogEvery 次连续失败打一条提醒、
// 其余连续失败静默（flusher 5s 一把，磁盘持续满时不刷屏）。
// 恢复成功的日志由 saveLocked 在成功路径统一打。与 redisstore 三处异步写的
// "失败仅打日志、不向上抛"范式对齐，但落盘失败对运维是盲区，故多一层节流（notification）。
func (p *Pool) notePersistFail(err error) {
	if p.persistFails == 0 {
		log.Printf("pool: state.json 落盘失败: %v", err)
	} else if p.persistFails%persistLogEvery == 0 {
		log.Printf("pool: state.json 连续落盘失败 %d 次: %v", p.persistFails, err)
	}
	p.persistFails++
}

// stateOverviewLocked 收集当前内存状态为 stateFile（供落盘 + 快照镜像复用）。调用方必须已持 p.mu。
func (p *Pool) stateOverviewLocked() stateFile {
	now := time.Now()
	sf := stateFile{Version: stateFileVersion, Accounts: map[string]stateAccount{}}
	for uid, e := range p.byUID {
		s := stateAccount{
			Credits:                  e.credits,
			CreditsTotal:             e.creditsTotal,
			Disabled:                 e.disabled,
			Paused:                   e.paused,
			Reason:                   e.reason,
			Until:                    e.until,
			CoolKind:                 e.coolKind,
			SuccessCount:             e.successCount,
			ErrTotal:                 e.errTotal,
			LastSuccess:              e.lastSuccess,
			LastErr:                  e.lastErr,
			LastCheckinDay:           e.lastCheckinDay,
			TokenUsage:               e.tokenUsage,
			SoftStreak:               e.softStreak,
			SessionDeadFails:         e.sessionDeadFails,
			ConsecutiveFails:         e.consecutiveFails,
			CreditsExpiring:          e.creditsExpiring,
			CreditsEarliestExpiry:    e.creditsEarliestExpiry,
			CreditsEarliestRemaining: e.creditsEarliestRemaining,
		}
		// 熔断截止：仅未过期才落盘（指针 nil 才能被 omitempty 真省略）。
		if !e.breakerUntil.IsZero() && now.Before(e.breakerUntil) {
			u := e.breakerUntil
			s.BreakerUntil = &u
			s.RetryCount = e.retryCount
		}
		// 连败降权截止：仅未过期才落盘。
		if !e.degradeUntil.IsZero() && now.Before(e.degradeUntil) {
			u := e.degradeUntil
			s.DegradeUntil = &u
		}
		// 模型级独立冷却：惰性过滤已过期条目（Hits 不落盘，重启后 11102 退避从基数重学）。
		if len(e.modelCooldowns) > 0 {
			for m, mc := range e.modelCooldowns {
				if mc.Until.IsZero() || !now.Before(mc.Until) {
					continue
				}
				if s.ModelCooldowns == nil {
					s.ModelCooldowns = map[string]stateModelCooldown{}
				}
				s.ModelCooldowns[m] = stateModelCooldown{Until: mc.Until, ResetAt: mc.ResetAt, Reason: mc.Reason, AuditOnly: mc.AuditOnly}
			}
		}
		// 成本账本：惰性过滤过期观测（modelCostTTL 外不写——陈旧价格不复活）。
		if len(e.modelCost) > 0 {
			for m, mc := range e.modelCost {
				if mc.LastSeen.IsZero() || now.Sub(mc.LastSeen) > modelCostTTL {
					continue
				}
				if s.ModelCosts == nil {
					s.ModelCosts = map[string]stateModelCost{}
				}
				s.ModelCosts[m] = stateModelCost{CostPer1k: mc.CostPer1k, LastSeen: mc.LastSeen, Samples: mc.Samples}
			}
		}
		sf.Accounts[uid] = s
	}
	return sf
}
