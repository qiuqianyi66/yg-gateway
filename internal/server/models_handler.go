// models_handler.go /v1/models HTTP 簇：modelsCache + models/modelList/applyModelInfoFields +
// fetchGlobalModels/fetchDynamicModels。从 handler.go 机械拆出（2026-10-22 R-202），零行为变化。
package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/upstream"
)

// hiddenGlobalListingIDs 从 /v1/models 默认列表里隐藏的 global 模型 id（裸名，不带前缀/后缀）。
//
// 为什么隐藏而不是删除：这些 id 出现在上游 models 目录探测结果里，但实际 chat 接口对
// 当前池所有 global 号都返回 11102 "service info not found"——上游目录与后端部署不一致，
// 不是账号权限问题（2026-10-29 直连 qiuqiayi66/yanhang 双号实测，两个号都 11102）。
// 开源项目保留完整目录能力：手动指定 model=global:<id> 仍可路由（若未来某号后端部署了
// 该模型立即可用），只是默认下拉不列出来避免用户选了 11102。某 id 未来上游部署后从此
// 集合删掉即恢复可见。
var hiddenGlobalListingIDs = map[string]bool{
	"gpt-6-astra":            true, // 11102 service info not found（双号实测）
	"gpt-5.6-sol":            true, // 11102
	"gpt-6-sol":              true, // 11102
	"gpt-6.1-sol":            true, // 11102
	"kimi-k3":                true, // 11102
	"kimi-k3.1-pro":          true, // 11102
	"kimi-k3.1-pro-highspeed": true, // 11102
	"minimax-m3.1-flash":     true, // global 域 11102（CN 域同 id 可用）
	"o4-mini":                true, // global 域 11102
}

// modelsCache 动态模型目录的进程内缓存：id 目录 + 10min TTL + 5min 失败负缓存。
//
// 为什么是 Handler 的**实例字段**而不是包级全局（N3：T5 的结构性根治）：
// 包级可变全局在进程内跨用例存活，TestMain 的沙箱（HOME/cwd/TEMP）隔离不了它——任何
// 「假上游返回空目录/500」的用例都会把负缓存写成 now，于是同一二进制内后续（含 -count>1
// 的下一轮）所有 /v1/models 用例都被短路成空列表。T5 当时的修法是「每个用例首行手动清一次
// 这个缓存」，那是**约定**：新增用例忘清就复发，而复发症状是「组装逻辑的用例红成环境问题」，
// 极难归因。改成实例字段后隔离由构造保证，不靠纪律。同包的
// upstream.Client.fetchGlobalModelsCache 早就是「按实例持有、测试新建 Client 即隔离」
// 的模式，本处与它对齐。
//
// 并发：读多写少（每个 TTL 窗口一次写），RWMutex 足够。ids 仍是**共享的只读切片**
// （不复制，与改前逐位一致——调用方只读，不持有所有权）。
type modelsCache struct {
	mu       sync.RWMutex
	ids      []upstream.ModelInfo
	fetched  time.Time // 最近一次成功拉取时间
	lastFail time.Time // 最近一次拉取失败时间（负缓存）
}

// lookup 在**同一次读锁**内返回「TTL 内的成功快照」与「是否处于失败负缓存冷却期」。
//
// 为什么要合并（独立复审的修正）：改前的 fetchDynamicModels 是在一次 RLock 内连着判这两件事；
// 拆成 snapshot() + inFailCooldown() 两次取锁后，中间可能被 store() 插入 → 极小概率多打一轮
// 上游探测（2 次 FetchModels）。方向是安全的（只会多拉一次，不会返回错值），但既然这次重构
// 宣称「行为等价」，锁粒度也一并收回到与改前一致。
func (c *modelsCache) lookup() ([]upstream.ModelInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.ids) > 0 && time.Since(c.fetched) < dynamicModelsTTL {
		return c.ids, false
	}
	return nil, !c.lastFail.IsZero() && time.Since(c.lastFail) < modelsFetchFailCooldown
}

// snapshot 只读 TTL 内的成功快照；缓存冷、为空或已过期 → nil。
// 命中判据（非空 + 未过期）与改前 fetchDynamicModels / cachedModelsSnapshot 两处逐位一致。
func (c *modelsCache) snapshot() []upstream.ModelInfo {
	// 忽略第二个返回值（是否处于失败负缓存冷却期）：本函数的调用方 cachedModelsSnapshot
	// 只问「有没有可用的目录快照」，冷却态对它是无意义的（拉取策略由 lookup 的调用方决定）。
	hit, _ := c.lookup()
	return hit
}

// noteFail 记一次拉取失败（进 5min 负缓存）。
func (c *modelsCache) noteFail() {
	c.mu.Lock()
	c.lastFail = time.Now()
	c.mu.Unlock()
}

// store 记一次成功拉取，并清空负缓存（成功即证明上游可用）。
func (c *modelsCache) store(infos []upstream.ModelInfo) {
	c.mu.Lock()
	c.ids = infos
	c.fetched = time.Now()
	c.lastFail = time.Time{}
	c.mu.Unlock()
}

const (
	// dynamicModelsTTL 模型目录缓存时长。曾是 1h；缩到 10min 对齐「面板实时、
	// API 缓存」的漂移痛点（PR #38 报告）：目录新增模型时面板立即可见，公开
	// /v1/models 最多滞后一个 TTL。再短就不值得——每次失效都是 2 次上游探测。
	dynamicModelsTTL        = 10 * time.Minute
	modelsFetchFailCooldown = 5 * time.Minute
)

// models 返回模型列表：纯动态（缓存 10min），失败/无号返回空列表（无静态兜底——
// 拉不出目录即意味着上游不可用，假名单只会让客户端选到 11102 的模型）。
func (h *Handler) models(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   h.modelList(),
	})
}

// fmtCreditsPrefix 从上游 credits 原文提取倍率并格式化为 "[x0.05 credit]"。
// 上游格式不统一："x0.05 credits" / "x0.29" / "x0.00 credits" 等，
// 统一提取 x数字 部分，去 "credits" 后缀。
func fmtCreditsPrefix(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "credits")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return "[" + s + " credit]"
}

// nameRateSuffix 从上游 credits 原文提取倍率展示后缀（"x0.05 credits" → " ×0.05"）：
// 去 "credits" 后缀、剥首尾的 "x"（"0x" → "0"），拼 " ×" 前缀。上游未给倍率（空）→ ""
// ——不加后缀、不编造（AGENTS.md 四态：没覆盖 = 未知）。
func nameRateSuffix(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "credits")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "x")
	if s == "" {
		return ""
	}
	return " ×" + s
}

// effectiveRateRaw 返回模型当前生效倍率原文（有 PromoCredits 用它，否则牌价 Credits）。
// 供 id 后缀拼接用；与 upstream.effectiveModelRate 的区别是不做 normalize，保留
// "0x" / "x0.29" 原文格式以喂给 nameRateSuffix。
func effectiveRateRaw(mi upstream.ModelInfo) string {
	if strings.TrimSpace(mi.PromoCredits) != "" {
		return mi.PromoCredits
	}
	return mi.Credits
}

// applyModelInfoFields 把上游模型对象全字段（ModelInfo）按「空值省略」写出规则
// 合入 /v1/models 条目：name/description/credits/tags/vendor/能力旗标/
// max_allowed_size/reasoning_effort/reasoning_summary/promo_*。CN 动态分支与
// global 探测命中分支共用（两域模型对象同构），保证输出字段集一致。
// 不覆盖 id/object/created/owned_by 及调用方先前写好的基础字段；上游未下发的
// 字段（零值）整体省略——不编造。
func applyModelInfoFields(entry map[string]any, mi upstream.ModelInfo) map[string]any {
	// 生效价：有 PromoCredits（限时折扣/免费）时用生效价，否则用牌价 Credits。
	// 与 panel.panelModelEntry（app.js:1025）同口径——客户端只渲染 name 字段，
	// 牌价放进 credits 字段备查，生效价拼进 name 让 Cherry Studio 这类客户端
	// 在已添加模型列表里直接看到"×0 限时免费"而非过时的牌价"×0.29"。
	effectiveRate := mi.Credits
	if strings.TrimSpace(mi.PromoCredits) != "" {
		effectiveRate = mi.PromoCredits
	}
	if mi.Name != "" {
		// name 拼「显示名 + 生效倍率后缀 + 促销标签」：
		//   - 渲染 id 的客户端（Cherry Studio 弹窗）→ id 上的 " ×N" 已可见；
		//   - 渲染 name 的客户端（DSH 预览版等）→ 之前只看到裸名 GPT-5.5、没有倍率，
		//     现在 name 上同样带 " ×N"，两类客户端列表都直接显示价格。
		// 后缀与 id 同口径（nameRateSuffix(effectiveRateRaw)，生效价优先 promo）；
		// 上游未给倍率 → nameRateSuffix 返回 ""，不编造（四态：没覆盖=未知）。
		name := mi.Name + nameRateSuffix(effectiveRateRaw(mi))
		if mi.PromoLabel != "" {
			name += " " + mi.PromoLabel
		}
		entry["name"] = name
	}
	if mi.Description != "" {
		// 积分倍率前缀：与 name 同口径用生效价，统一为 "[x0.05 credit]" 前缀。
		if prefix := fmtCreditsPrefix(effectiveRate); prefix != "" {
			entry["description"] = prefix + " " + mi.Description
		} else {
			entry["description"] = mi.Description // descriptionZh 中文描述
		}
	}
	if mi.Credits != "" {
		entry["credits"] = mi.Credits // 牌价倍率原文（如 "x0.29"），仅展示
	}
	// 限时优惠透出（与 panel.go:517-526 同口径）：
	//   promo_factor=0 → 限时免费；0.5 → 五折；nil → 无机器可读折扣（错峰类仅标签）。
	//   promo_credits 是生效价原文（如 "0x" / "0.50x"）；promo_label 是徽章文案
	//   （限时免费/夜间折扣/错峰使用）；promo_note 是 hover 说明（含时段）。
	// 第三方客户端可据此高亮免费/折扣模型，不再把牌价当实时价。
	if mi.PromoFactor != nil {
		entry["promo_factor"] = *mi.PromoFactor
		entry["promo_credits"] = mi.PromoCredits
	}
	if mi.PromoLabel != "" {
		entry["promo_label"] = mi.PromoLabel
	}
	if mi.PromoNote != "" {
		entry["promo_note"] = mi.PromoNote
	}
	if len(mi.Tags) > 0 {
		entry["tags"] = mi.Tags
	}
	if mi.Vendor != "" {
		entry["vendor"] = mi.Vendor
	}
	if mi.IsDefault {
		entry["is_default"] = true
	}
	// 三值透出（AGENTS.md：缺失=未知，不是"不支持"）：
	//   *true → true；*false → false；nil（上游未声明）→ **字段省略**。
	// 旧实现只在 true 时 emit，于是「显式不支持」与「未知」都表现为省略，消费者
	// 无法区分；这里让显式 false 也落库，unknown 仍保持省略。
	if mi.SupportsImages != nil {
		entry["supports_images"] = *mi.SupportsImages
	}
	if mi.SupportsReasoning != nil {
		entry["supports_reasoning"] = *mi.SupportsReasoning
		if mi.CanDisableThinking {
			entry["can_disable_thinking"] = true
		}
	}
	if mi.SupportsToolCall {
		entry["supports_tool_call"] = true
	}
	if mi.OnlyReasoning {
		entry["only_reasoning"] = true
	}
	if mi.MaxAllowedSize > 0 {
		entry["max_allowed_size"] = mi.MaxAllowedSize
	}
	if mi.ReasoningEffort != "" {
		entry["reasoning_effort"] = mi.ReasoningEffort
	}
	if mi.ReasoningSummary != "" {
		entry["reasoning_summary"] = mi.ReasoningSummary
	}
	return entry
}

// modelList 模型列表：CN 模型输出统一加 "cn:" 前缀（gateway 路由协议，与 resolveModel
// 对称）；global.enabled=true 时追加 global: 前缀的国际版名单。
// 纯动态：动态拉取失败/无号 → 该域空列表，无静态兜底。
func (h *Handler) modelList() []map[string]any {
	out := make([]map[string]any, 0)
	for _, mi := range h.fetchDynamicModels() {
		// id 拼生效倍率后缀（"cn:hy3 ×0"）：弹窗列表只渲染 id，这样用户在
		// Cherry Studio 的"获取模型"弹窗里直接看到倍率。resolveModel 收到请求
		// 时会剥掉第一个空格之后的部分，还原成裸模型名路由。
		idSuffix := nameRateSuffix(effectiveRateRaw(mi))
		entry := map[string]any{
			"id":       "cn:" + mi.ID + idSuffix,
			"object":   "model",
			"created":  1753600000,
			"owned_by": "workbuddy",
		}
		// context_length / max_output_tokens 四级查找（upstream.model_catalog）：
		// 上游动态值（maxInputTokens/maxOutputTokens）权威 → 静态种子表 →
		// model.json 本地缓存 → models.dev 按需拉取（异步不阻塞本次响应，拉到后
		// 写 model.json 供下次命中）→ 1M 兜底 / max_output_tokens 省略。
		// 上游零值不再透出假 131072（误导 Codex/ZCode 等按 context_length 提前
		// 截断、白白丢上下文）。
		entry["context_length"] = upstream.ContextWindowListingV4(mi.ID, mi.ContextWindow, h.cfg.Upstream.HTTP)
		if mo, ok := upstream.MaxOutputTokensListingV4(mi.ID, mi.MaxTokens, h.cfg.Upstream.HTTP); ok {
			entry["max_output_tokens"] = mo
		}
		// 上游模型对象全字段透出（name/描述/标签/倍率/能力旗标等，空值省略）。
		entry = applyModelInfoFields(entry, mi)
		// effort 能力透出——远端 supportedEfforts 权威，缺失落到 CN 静态兜底表
		// （客户端可发现档位，不再盲传）。无档位 → 省略字段。
		// 双字段名兼容：reasoning_supported_efforts（既有名）+ supported_efforts
		// （部分客户端只认裸名，缺失时其内置表把模型当「不校验 effort」处理，
		// 弹「该模型不校验该参数」）。别名同值，消费方按字段名偏好自取。
		if efforts, def := upstream.EffortListing("cn", mi.ID, mi.Efforts, mi.DefaultEffort); efforts != nil {
			entry["reasoning_supported_efforts"] = efforts
			entry["supported_efforts"] = efforts
			if def != "" {
				entry["reasoning_default_effort"] = def
				entry["default_effort"] = def
			}
		}
		out = append(out, entry)
	}
	// global 模型名单：仅 GlobalEnabled=true 时列出（逃生门）。
	// 名单 = 纯动态探测结果（fetchGlobalModels，失败/无号 → 空）。
	if h.cfg.GlobalEnabled {
		// global 域 effort 能力三级查找：探测下发桶（权威）→ 静态兜底表 → 省略。
		// 先 fetchGlobalModels（内部探测并落 effort 桶），再按 id 取快照。
		globalIDs, globalAccount := h.fetchGlobalModels()
		// 探测对象形态的全字段条目（与 fetchGlobalModels 共享同一次探测缓存）：
		// 命中 id 才透出富字段；窄表/失败 → nil，按裸 ID 条目输出（不编造字段）。
		// globalAccount 为 nil（无 global 号）时返回 nil，跳过富字段映射。
		globalInfos := map[string]upstream.ModelInfo{}
		for _, mi := range h.cfg.Upstream.FetchGlobalModelInfos(globalAccount) {
			globalInfos[mi.ID] = mi
		}
		globalEfforts, globalDefaults := h.cfg.Upstream.GlobalEffortSnapshot()
		for _, id := range globalIDs {
			if hiddenGlobalListingIDs[id] {
				continue // 见 hiddenGlobalListingIDs：当前号池无权限，默认列表隐藏但保留路由
			}
			// 先取富字段条目，算 id 后缀（同 CN 分支口径）。
			var remoteCtx, remoteOut int64
			var idSuffix string
			if mi, ok := globalInfos[id]; ok {
				idSuffix = nameRateSuffix(effectiveRateRaw(mi))
				remoteCtx, remoteOut = mi.ContextWindow, mi.MaxTokens
			}
			entry := map[string]any{
				"id":       "global:" + id + idSuffix,
				"object":   "model",
				"created":  1753600000,
				"owned_by": "workbuddy",
			}
			// context_length / max_output_tokens 四级查找（与 CN 动态分支同口径）。
			if mi, ok := globalInfos[id]; ok {
				entry = applyModelInfoFields(entry, mi)
			}
			entry["context_length"] = upstream.ContextWindowListingV4(id, remoteCtx, h.cfg.Upstream.HTTP)
			if mo, ok := upstream.MaxOutputTokensListingV4(id, remoteOut, h.cfg.Upstream.HTTP); ok {
				entry["max_output_tokens"] = mo
			}
			if efforts, def := upstream.EffortListing("global", id, globalEfforts[id], globalDefaults[id]); efforts != nil {
				entry["reasoning_supported_efforts"] = efforts
				entry["supported_efforts"] = efforts // 双字段名兼容（见 CN 分支注释）
				if def != "" {
					entry["reasoning_default_effort"] = def
					entry["default_effort"] = def
				}
			}
			out = append(out, entry)
		}
	}
	return out
}

// fetchGlobalModels 返回 global 模型名单（纯动态探测结果）及被探测账号。
// 缓存/失败回落封在 upstream.FetchGlobalModels（内部 1h + 5min 负缓存）。
// 本方法只负责"何时探测"：池中无 global 账号 → 空名单 + nil 账号（零上游调用）。
// 返回的 acct 供调用方在同一账号上取富 ModelInfo（FetchGlobalModelInfos 与
// FetchGlobalModels 共享缓存，不会触发第二次上游探测）。
// GlobalEnabled=false 时 modelList 已不进入本分支（逃生门在调用方 gate）。
func (h *Handler) fetchGlobalModels() ([]string, *auth.Auth) {
	acct := h.cfg.Pool.PickExcludingForRealm(nil, "", "global")
	if acct == nil {
		return nil, nil
	}
	return h.cfg.Upstream.FetchGlobalModels(acct), acct
}

// fetchDynamicModels 从第一个可用 CN 账号拉模型列表（含 contextWindow/maxTokens），
// 缓存 10min。
// 选号与 /panel/api/models 完全同口径（AvailableUIDsForRealm("cn") 首个 + AuthByUID），
// 而非 Pool.Pick()：Pick 无 realm 过滤，混合池里可能选中 global 号去打 CN 端点，
// 表现为偶发失败/面板与 /v1/models 两套目录（PR #38 报告并给出的选号修复）。
// 缓存 + 5min 负缓存按既有语义**保留**（#38 原案整体删除缓存被拒）：公开端点逐请求
// 实时拉取 = 每次 2 个上游探测，客户端周期性刷新模型列表会持续打上游；上游故障时
// 无冷却窗口，客户端重试即放大请求量——负缓存正是为此设计（见 handler_test 吸收
// 上游 9832283 的注释）；且 cachedModelsSnapshot（gateway_hint 判定）依赖缓存写入。
func (h *Handler) fetchDynamicModels() []upstream.ModelInfo {
	hit, inCooldown := h.modelsCache.lookup()
	if len(hit) > 0 {
		return hit
	}
	// 失败负缓存：冷却期内不再请求上游。
	if inCooldown {
		return nil
	}

	uids := h.cfg.Pool.AvailableUIDsForRealm("cn")
	if len(uids) == 0 {
		return nil
	}
	acct := h.cfg.Pool.AuthByUID(uids[0])
	if acct == nil {
		return nil
	}
	infos, err := h.cfg.Upstream.FetchModels(acct)
	if err != nil || len(infos) == 0 {
		// 拉取失败只进负缓存（5min lastFail），不 NoteError：NoteError 喂的是 chat
		// 熔断器，models 端点偶发 5xx 跨界惩罚 chat 通道健康的账号；
		// models 拉取失败 ≠ 账号 chat 不可用。
		h.modelsCache.noteFail()
		return nil
	}
	h.modelsCache.store(infos)
	return infos
}

// cachedModelsSnapshot 只读模型目录缓存（TTL 内快照）；缓存冷/空 → nil。
// 不发起任何上游调用（hint 判定用：错误路径加一次 FetchModels 网络调用既拖慢
// 错误响应、又污染上游调用语义）。
func (h *Handler) cachedModelsSnapshot() []upstream.ModelInfo {
	return h.modelsCache.snapshot()
}
