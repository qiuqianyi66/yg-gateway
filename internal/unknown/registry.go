// Package unknown 未解问题的登记与消解（带 revision 的一等公民实体）。
//
// 来源：morluto/rea 的 `record_unknown` / `update_unknown` / `list_unknowns`
// 四件套语义。rea 把"不知道的事"当作**可跟踪的实体**，而不是一句免责声明：
//   - 登记时必须写明 requires（需要什么才能消解）；
//   - 消解带 revision，用过期的 revision 必须报错并重读，禁止盲重试；
//   - 消解只能由**合格的观察证据**完成。
//
// 为什么本项目需要它：网关里存在大量"暂时不知道、但可消解"的事实。例如
// 「账号 X 对模型 Y 是收费还是免费」在实测到第一次 credit 之前是未知；
// 旧实现把这个未知压成一个枚举值（tier 1），没人跟踪它何时能被消解、
// 也没人能回答"还差什么"。结果是未知被静默当成默认行为处理（放行），
// 而这些未知恰恰是养号决策最需要的那部分信息。
//
// 设计约束：
//   - 零依赖（domain 层），不 import 任何 internal 包；
//   - 强制纪律：requires 非空（说不清就不给登记）、消解必须有证据 ID；
//   - 同一 (question, requires) 登记幂等去重，避免面板被同一问题刷屏。
package unknown

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

var (
	// ErrStaleRevision 调用方持有的 revision 已过期：必须重读条目后再重试，
	// 不允许盲目覆盖（rea 的 update_unknown 契约）。
	ErrStaleRevision = errors.New("unknown: stale revision, re-read the item before retrying")
	// ErrNotFound 条目不存在（既不静默成功，也不新建）。
	ErrNotFound = errors.New("unknown: item not found")
	// ErrRequiresEmpty 登记时未说明「需要什么才能消解」。
	ErrRequiresEmpty = errors.New("unknown: requires must not be empty")
	// ErrQuestionEmpty 登记时问题描述为空。
	ErrQuestionEmpty = errors.New("unknown: question must not be empty")
	// ErrEvidenceRequired 消解未提供证据 ID。
	ErrEvidenceRequired = errors.New("unknown: resolution requires an evidence id")
	// ErrReasonRequired 重开未说明理由（知识为何又变成未知）。
	ErrReasonRequired = errors.New("unknown: reopen requires a reason")
)

// Item 一条被跟踪的未知。
type Item struct {
	// ID 稳定标识（由 question+requires 派生），重复登记复用同一 ID。
	ID string `json:"id"`
	// Question 不知道的是什么（人可读）。
	Question string `json:"question"`
	// Requires 需要什么才能消解（人可读，如"一次上游返回 credit 的调用"）。
	// 与 Fact.Basis 的语义区分：Fact.Basis 回答"这个结论凭什么"，
	// Requires 回答"还缺什么才能有结论"。
	Requires string `json:"requires"`
	// Evidence 消解时引用的证据 ID（可多条，供审计追溯）。
	Evidence []string `json:"evidence,omitempty"`
	// Revision 乐观并发版本号：登记时为 1，每次成功消解或重开 +1。
	// 调用方必须回传拿到的 revision，过期即报错。
	Revision int `json:"revision"`
	// Resolved 是否已消解。
	Resolved bool `json:"resolved"`
}

// Registry 未知条目注册表。并发安全。
type Registry struct {
	mu    sync.Mutex
	items map[string]*Item
	order []string // 登记顺序（List/All 稳定输出，便于面板 diff）
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{items: map[string]*Item{}}
}

// Record 登记一条未知。同一 (question, requires) 重复登记返回既有条目（幂等）。
//
// question 与 requires 都必须非空：说不清"需要什么才能消解"的条目无法被跟踪，
// 也永远不会被消解，登记它只会制造噪音。这是刻意的 fail-closed。
func (r *Registry) Record(question, requires string, evidence ...string) (Item, error) {
	if question == "" {
		return Item{}, ErrQuestionEmpty
	}
	if requires == "" {
		return Item{}, ErrRequiresEmpty
	}
	id := DeriveID(question, requires)

	r.mu.Lock()
	defer r.mu.Unlock()
	if it, ok := r.items[id]; ok {
		return *it, nil // 幂等：复用既有条目（不重置 revision/证据）
	}
	it := &Item{
		ID:       id,
		Question: question,
		Requires: requires,
		Evidence: append([]string(nil), evidence...),
		Revision: 1,
	}
	r.items[id] = it
	r.order = append(r.order, id)
	return *it, nil
}

// Resolve 用证据消解一条未知。
//
// 契约（rea update_unknown 的移植）：
//   - ID 不存在 → ErrNotFound；
//   - rev 与当前 Revision 不符 → ErrStaleRevision（调用方必须重读后重试）；
//   - 已消解且 rev 相符 → 幂等返回（不重复递增 revision，便于重放）；
//   - evidenceID 为空 → ErrEvidenceRequired（消解只能由合格观察证据完成）。
func (r *Registry) Resolve(id string, rev int, evidenceID string) (Item, error) {
	if evidenceID == "" {
		return Item{}, ErrEvidenceRequired
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	it, ok := r.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	if rev != it.Revision {
		return Item{}, ErrStaleRevision
	}
	if it.Resolved {
		return *it, nil // 幂等：重放不产生副作用
	}
	it.Evidence = append(it.Evidence, evidenceID)
	it.Revision++
	it.Resolved = true
	return *it, nil
}

// Reopen 把**已消解**的条目标记为未消解——知识过期后重新变未知。
//
// 为什么需要：多数消解是单向的（观测一次就永远知道）；但有些未知带 TTL——例如
// 模型成本的观测 6h 后过期，此后该 (账号,模型) 又回到"不知道"。没有 Reopen，这类
// 条目会永久停在 Resolved，未消解计数就会撒谎（读 0 而实际未知）。
//
// 契约（与 Resolve 对称）：
//   - reason 为空 → ErrReasonRequired（重开必须有理由，供审计）；
//   - ID 不存在 → ErrNotFound；
//   - rev 与当前 Revision 不符 → ErrStaleRevision（调用方须重读后重试，禁止盲重试）；
//   - 未消解的条目 → 幂等返回（不递增 revision、不追加证据）；
//   - 已消解且 rev 相符 → Resolved=false、Revision++、evidence 追加 reason；
//     **原消解证据保留**，审计能看到"曾被什么消解过、又因什么重新变未知"。
func (r *Registry) Reopen(id string, rev int, reason string) (Item, error) {
	if reason == "" {
		return Item{}, ErrReasonRequired
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	it, ok := r.items[id]
	if !ok {
		return Item{}, ErrNotFound
	}
	if rev != it.Revision {
		return Item{}, ErrStaleRevision
	}
	if !it.Resolved {
		return *it, nil // 幂等：本来就未消解
	}
	it.Evidence = append(it.Evidence, reason)
	it.Revision++
	it.Resolved = false
	return *it, nil
}

// Get 取单条（值拷贝，外部改不到内部状态）。
func (r *Registry) Get(id string) (Item, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[id]
	if !ok {
		return Item{}, false
	}
	return *it, true
}

// List 返回**未消解**的未知（按登记顺序）。
// 已消解的不再占据注意力——这是 List 与 All 的唯一区别。
func (r *Registry) List() []Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Item, 0, len(r.order))
	for _, id := range r.order {
		if it, ok := r.items[id]; ok && !it.Resolved {
			out = append(out, *it)
		}
	}
	return out
}

// All 返回全部条目（含已消解，供审计追溯"这些未知是怎么被消解的"）。
func (r *Registry) All() []Item {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Item, 0, len(r.order))
	for _, id := range r.order {
		if it, ok := r.items[id]; ok {
			out = append(out, *it)
		}
	}
	return out
}

// Len 未消解条目数（面板徽章用）。
func (r *Registry) Len() int {
	return len(r.List())
}

// DeriveID 由 (question, requires) 派生稳定 ID。
//
// 用内容哈希而不是自增序号：同一问题重复登记天然复用同一条目，且跨重启/
// 跨进程稳定（未来若需持久化，ID 不会漂移）。
//
// 导出是给调用方的性能路径用：调用方可以**预先算出 ID**，把它当作缓存 key
// （避免每请求都进 Record 的锁），需要时再用同一 ID 查/消解。
func DeriveID(question, requires string) string {
	sum := sha256.Sum256([]byte(question + "\x00" + requires))
	return "u-" + hex.EncodeToString(sum[:8])
}
