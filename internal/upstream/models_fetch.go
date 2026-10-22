// models_fetch.go 模型目录拉取：ModelInfo/dynModelEntry、FetchModels、v3/config 探测、
// efforts/modelRates 缓存。从 client.go 机械拆出（2026-10-22 R-201），零行为变化。
package upstream

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"yg-gateway/internal/auth"
)

// ModelInfo 动态模型信息（含 maxInputTokens/maxOutputTokens + 上游模型对象全字段）。
// CN /console 与 global /v2 的模型对象同构，故共用此结构；上游省略的字段保持零值，
// /v1/models 侧按「空值省略」透出（不编造）。
type ModelInfo struct {
	ID            string
	Name          string
	ContextWindow int64    // = maxInputTokens
	MaxTokens     int64    // = maxOutputTokens（思考与最终回答共享此预算，上游无独立思考上限字段）
	Efforts       []string // reasoning.supportedEfforts（空=未知/固定档）
	DefaultEffort string   // reasoning.defaultEffort（新模型键）或 reasoning.effort（老模型键）；空=未返回

	// 模型目录全字段（models-full-fields）：
	Description        string   // descriptionZh 中文描述
	Credits            string   // credits 积分倍率原文（如 "x0.05"），仅展示不参与选号
	Tags               []string // tags 模型标签（含 badge:限时免费 等）
	Vendor             string   // vendor 厂商标识
	IsDefault          bool     // isDefault 是否默认模型
	SupportsReasoning  *bool    // supportsReasoning；nil=目录未声明=**未知**，不是"不支持"（AGENTS.md「缺失≠否定」）
	SupportsToolCall   bool     // supportsToolCall 是否支持工具调用
	OnlyReasoning      bool     // onlyReasoning 是否纯推理模型
	SupportsImages     *bool    // 顶层 supportsImages（多模态能力，透出到 /v1/models；nil=未知）
	MaxAllowedSize     int64    // maxAllowedSize 最大允许上下文（与 maxInputTokens 口径并列，上游各自下发）
	CanDisableThinking bool     // reasoning.canDisableThinking：思考可关（off 档可用）
	ReasoningEffort    string   // reasoning.effort 推理模式（与 supportedEfforts 数组不同源）
	ReasoningSummary   string   // reasoning.summary 推理摘要模式（如 "auto"）

	// 优惠（modelPromotions，/v3/config data.modelPromotions）：Credits 是**牌价**
	//（转正后基准倍率），Promo* 是当前生效的限时优惠——面板据此显示「生效价 +
	// 标签 + 牌价」。PromoFactor 为 nil 表示无 machine-readable 折扣（如「错峰
	// 使用」只有时段文案无 factor），仅挂标签/提示。
	PromoFactor  *float64 // 折扣系数（0=限时免费，0.5=五折）；nil=无
	PromoCredits string   // 折扣后倍率原文（如 "0x" / "0.50x"），仅展示
	PromoLabel   string   // 徽章文案（限时免费 / 夜间折扣 / 错峰使用）
	PromoNote    string   // hover 说明原文（含时段/日期描述）
}

// dynModelEntry 上游模型目录（CN /console 与 global /v2 同构）的单条模型解析形态，
// FetchModels 与 global_models.go 的探测共用。iconUrl/descriptionEn/生成参数等
// 按「不透出」原则不解析。modelInfo() 是 dynEntry→ModelInfo 映射的单一事实来源，
// 杜绝两域映射漂移。
type dynModelEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ModelID / Model id 的宽松回退键（仅 global 目录的多信封兜底用，CN 目录
	// 不下发这两个键；字段加在这里只是让 typed 解析能"看见"它们）。
	ModelID         string   `json:"modelId"`
	Model           string   `json:"model"`
	Description     string   `json:"descriptionZh"`
	Credits         string   `json:"credits"`
	Tags            []string `json:"tags"`
	Vendor          string   `json:"vendor"`
	IsDefault       bool     `json:"isDefault"`
	MaxInputTokens  int64    `json:"maxInputTokens"`
	MaxOutputTokens int64    `json:"maxOutputTokens"`
	MaxAllowedSize  int64    `json:"maxAllowedSize"`
	Disabled        bool     `json:"disabled"`
	SupportsImages  *bool    `json:"supportsImages"`    // 指针：省略=nil（未知），非 false
	SupportsReason  *bool    `json:"supportsReasoning"` // 指针：省略=nil（未知），非 false
	SupportsTool    bool     `json:"supportsToolCall"`
	OnlyReasoning   bool     `json:"onlyReasoning"`
	Reasoning       struct {
		Effort             string   `json:"effort"`
		Summary            string   `json:"summary"`
		DefaultEffort      string   `json:"defaultEffort"`
		CanDisableThinking bool     `json:"canDisableThinking"`
		SupportedEfforts   []string `json:"supportedEfforts"`
	} `json:"reasoning"`
}

// modelInfo 按解析条目构造 ModelInfo（dynEntry→ModelInfo 映射的单一事实来源）。
// defaultEffort 新老双键兼容：defaultEffort 优先，缺省回落 effort。
func (m dynModelEntry) modelInfo() ModelInfo {
	def := m.Reasoning.DefaultEffort
	if def == "" {
		def = m.Reasoning.Effort
	}
	return ModelInfo{
		ID:                 m.ID,
		Name:               m.Name,
		ContextWindow:      m.MaxInputTokens,
		MaxTokens:          m.MaxOutputTokens,
		Efforts:            m.Reasoning.SupportedEfforts,
		DefaultEffort:      def,
		SupportsImages:     m.SupportsImages,
		Description:        m.Description,
		Credits:            m.Credits,
		Tags:               m.Tags,
		Vendor:             m.Vendor,
		IsDefault:          m.IsDefault,
		SupportsReasoning:  m.SupportsReason,
		SupportsToolCall:   m.SupportsTool,
		OnlyReasoning:      m.OnlyReasoning,
		MaxAllowedSize:     m.MaxAllowedSize,
		CanDisableThinking: m.Reasoning.CanDisableThinking,
		ReasoningEffort:    m.Reasoning.Effort,
		ReasoningSummary:   m.Reasoning.Summary,
	}
}

// nonChatModel 判定是否非对话模型（应从模型列表过滤掉）。
// 来源：harness buddy.ts:547-555。三类规则：
//   - id 前缀 nes-/completion-/codewise-：嵌入/补全/代码专用模型，选了报 code=11102。
//   - maxOutputTokens ≤ 256：tiny 输出非对话模型。
//   - tags 含生成类标签（图片/视频）：生成模型走各自专用端点，作为对话模型
//     选上去只会报 11102，非本网关用途。
//
// 生成类标签随上游扩充：早期只有 text-to-image，桌面端目录（2026-10-02 实测）
// 另有 text-to-video / image-to-video（seedance 系列）与 image-to-image
// （gpt-image 系列）——后者已由 text-to-image 覆盖，此处补齐视频两类。
// 注意本函数 CN 与 global 共用，新增标签对两域同时生效。
func nonChatModel(id string, maxOutputTokens int64, tags []string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range [...]string{"nes-", "completion-", "codewise-"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	if maxOutputTokens > 0 && maxOutputTokens <= 256 {
		return true
	}
	for _, t := range tags {
		switch t {
		case "text-to-image", "image-to-image", "text-to-video", "image-to-video":
			return true
		}
	}
	return false
}

// codeBuddyIDEUA /v3/config 要求能解析出 CodeBuddy 版本号的 UA。
// CLI 三段式 WorkBuddy UA 会拿到精简目录（flash 输出 128K、无 supportedEfforts）；
// 官方 IDE 头 `CodeBuddyIDE/4.12.0 CodeBuddy/4.12.0` 才返回完整能力
// （flash：393216 + low/high/max）。
// 版本号需随上游 IDE 发版跟进：UAn 版本过旧时该端点可能同样返回精简目录。
const codeBuddyIDEUA = "CodeBuddyIDE/4.12.0 CodeBuddy/4.12.0"

// codeBuddyCLIUA CLI 三段式 UA。**实测（2026-09-22）该端点对不同 UA 下发的模型集合不同**：
//   - IDE UA  → 14 条（10 个 chat：含 o4-mini / enhance-1.0 / auto-chat，**无 deepseek 系列**）
//   - CLI UA  → 22 条（22 个 chat：**含 deepseek-v4.1-flash / deepseek-v4.1-flash-sg /
//     gpt-6-astra / kimi-k2.8-preview**，但无 o4-mini / enhance-1.0 / auto-chat）
//
// 注意两点，都与旧注释相反，勿再按旧注释推断：
//  1. 旧注释称「CLI UA 拿到精简目录、IDE UA 才返回完整能力」——实测模型数量恰好相反，
//     但 **IDE 响应体积更大**（26003B vs 21111B），故「完整能力」应理解为**单条字段更全**，
//     而非模型更多。两路各有独有模型，缺一不可。
//  2. 该常量仅用于 global 侧第二路探测；CN 侧仍走 codeBuddyIDEUA 单路。
const codeBuddyCLIUA = "CLI/2.63.2 CodeBuddy/2.63.2"

// FetchModels 调上游动态模型接口（CN 侧；global 账号见 global_models.go 家族）。
//
// v3-config-merge：动态目录 = /v3/config（主，IDE UA 完整能力版）+ 企业端点
// （/console，cli 面过滤，补缺）的并集，两路**并发**探测。合并去重 key = 模型 id，
// v3 条目优先（credits 等字段以 v3 为准），企业端点只补 v3 缺失的模型。
// /v3 失败（400/网络错/解析失败）不拖累企业端点结果——降级为仅企业端点，warn 日志；
// 反之亦然（两路独立容错）。
func (c *Client) FetchModels(a *auth.Auth) ([]ModelInfo, error) {
	type probeResult struct {
		infos []ModelInfo
		err   error
	}
	enterpriseCh := make(chan probeResult, 1)
	v3Ch := make(chan probeResult, 1)
	go func() {
		infos, err := c.fetchEnterpriseModels(a)
		enterpriseCh <- probeResult{infos, err}
	}()
	go func() {
		infos, err := c.fetchV3Models(a)
		v3Ch <- probeResult{infos, err}
	}()
	enterprise := <-enterpriseCh
	v3 := <-v3Ch
	if enterprise.err != nil && v3.err != nil {
		return nil, enterprise.err // 两路全失败：返回企业端点错误（既有调用方语义零漂移）
	}
	if v3.err != nil {
		// /v3 失败降级：不拖累企业端点结果（降级仅企业端点 + warn）。
		log.Printf("WARN: [upstream] fetch models: v3/config probe failed (degraded to enterprise endpoint): %v", v3.err)
	}
	if enterprise.err != nil {
		log.Printf("WARN: [upstream] fetch models: enterprise endpoint failed (v3/config only): %v", enterprise.err)
	}
	out := mergeModelInfos(v3.infos, enterprise.infos)
	if len(out) == 0 {
		return nil, fmt.Errorf("models api returned empty list")
	}
	c.storeModelRates(a.Realm(), out)
	// 刷新 effort 能力缓存（供请求体降级；无 supportedEfforts 的模型不入桶）。
	// 空桶时跳过写：避免「某探测无档位数据」清掉既有桶。
	cache := make(map[string][]string, len(out))
	defCache := make(map[string]string, len(out))
	for _, mi := range out {
		if len(mi.Efforts) > 0 {
			cache[mi.ID] = mi.Efforts
		}
		if mi.DefaultEffort != "" {
			defCache[mi.ID] = mi.DefaultEffort
		}
	}
	if len(cache) == 0 && len(defCache) == 0 {
		return out, nil
	}
	// 按探测账号的 realm 写入对应桶：CN 探测只进 cn 桶，global 同模型名不被污染（C-2）。
	c.storeEfforts(a.Realm(), cache, defCache)
	return out, nil
}

// mergeModelInfos 合并两路模型目录：primary 为主（同 id 以 primary 条目为准——
// credits 等字段以主端点为权威），secondary 只补 primary 缺失的 id。
// 去重 key = 模型 id；输出顺序 = primary 原序在前、secondary 补充项（secondary 原序）
// 在后——稳定输出，不依赖 map 迭代序。
func mergeModelInfos(primary, secondary []ModelInfo) []ModelInfo {
	if len(secondary) == 0 {
		return primary
	}
	seen := make(map[string]bool, len(primary)+len(secondary))
	out := make([]ModelInfo, 0, len(primary)+len(secondary))
	for _, mi := range primary {
		if mi.ID == "" || seen[mi.ID] {
			continue
		}
		seen[mi.ID] = true
		out = append(out, mi)
	}
	for _, mi := range secondary {
		if mi.ID == "" || seen[mi.ID] {
			continue
		}
		seen[mi.ID] = true
		out = append(out, mi)
	}
	return out
}

// fetchEnterpriseModels 单路探测企业模型端点（/console/enterprises/personal/models）。
// 解析口径：agents[cli].models 过滤 + nonChatModel 剔除 + disabled 剔除。
func (c *Client) fetchEnterpriseModels(a *auth.Auth) ([]ModelInfo, error) {
	// 局部变量名避开 url（本包已 import net/url，同名会造成阅读混淆）。
	endpoint := c.chatBase(a) + "/console/enterprises/personal/models"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.CommonHeaders(req, a) // 复用共享请求头（Origin/Referer/UA/Accept/Content-Type）
	// AccessToken 加锁快照（见 auth.AccessTokenValue：keepalive 刷新在 a.mu 内改写）。
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		// 读失败 → 传输层错误（handler 侧该路径不 NoteError）。
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models api status %d: %s", resp.StatusCode, truncate(string(raw), 120))
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Models []dynModelEntry `json:"models"`
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("models parse: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("models api code=%d", env.Code)
	}
	var cliIDs []string
	for _, ag := range env.Data.Agents {
		if ag.Name == "cli" {
			cliIDs = ag.Models
			break
		}
	}
	if len(cliIDs) == 0 {
		return nil, fmt.Errorf("no cli agent models found")
	}
	// dynMap 收集模型字段；nonChatModel 过滤在写入 dynMap 前执行，
	// 确保非对话条目（nes-/completion-/codewise- 前缀、maxOutputTokens≤256、
	// tags 含 text-to-image）根本不进返回列表（来源：harness buddy.ts:547-555）。
	dynMap := make(map[string]dynModelEntry, len(env.Data.Models))
	for _, m := range env.Data.Models {
		if nonChatModel(m.ID, m.MaxOutputTokens, m.Tags) {
			continue
		}
		dynMap[m.ID] = m
	}
	out := make([]ModelInfo, 0, len(cliIDs))
	for _, id := range cliIDs {
		if m, ok := dynMap[id]; ok && !m.Disabled {
			out = append(out, m.modelInfo())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models api returned empty list")
	}
	return out, nil
}

// fetchV3Models 单路探测 /v3/config（IDE UA 完整能力版，见 codeBuddyIDEUA）。
// v3 面取全量 models（不按 agents[cli] 过滤，与 global 探测口径一致），按同一
// nonChatModel 规则剔除非对话条目（selected 会选模型报 code=11102）。
// 失败返回错误（调用方降级为仅企业端点）。
func (c *Client) fetchV3Models(a *auth.Auth) ([]ModelInfo, error) {
	byID, err := c.fetchV3ConfigModelMap(a, codeBuddyIDEUA)
	if err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(byID))
	for _, mi := range byID {
		if nonChatModel(mi.ID, mi.MaxTokens, mi.Tags) {
			continue
		}
		out = append(out, mi)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("v3/config returned empty models")
	}
	return out, nil
}

// storeEfforts 按 realm 写入 effort 能力缓存桶（efforts + defaultEfforts），并发安全。
// 供 CN FetchModels 与 global 探测共用：拉取到的模型档位落桶后，出站请求体
// normalizeReasoningEffort 才能按域降级。efforts 与 defs 均空时删除该 realm 桶
// （等价「该域无可降级档位」）。调用方负责在「无新数据」时跳过写。
func (c *Client) storeEfforts(realm string, efforts map[string][]string, defs map[string]string) {
	c.effortsMu.Lock()
	defer c.effortsMu.Unlock()
	if c.efforts == nil {
		c.efforts = make(map[string]map[string][]string)
	}
	if c.defaultEfforts == nil {
		c.defaultEfforts = make(map[string]map[string]string)
	}
	k := realmKey(realm)
	if len(efforts) == 0 && len(defs) == 0 {
		delete(c.efforts, k)
		delete(c.defaultEfforts, k)
		return
	}
	c.efforts[k] = efforts
	c.defaultEfforts[k] = defs
}

// normalizeModelRate 把上游倍率原文规范化为可比较的数值键。
// 兼容 "x0.05" / "x0.05 credits" / "0.50x" 等形态；无法数值化时保留去除
// credits 后缀与空白后的原文，避免编造倍率。
func normalizeModelRate(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(s), "credits") {
		s = strings.TrimSpace(s[:len(s)-len("credits")])
	}
	if strings.HasPrefix(strings.ToLower(s), "x") {
		s = strings.TrimSpace(s[1:])
	} else if strings.HasSuffix(strings.ToLower(s), "x") {
		s = strings.TrimSpace(s[:len(s)-1])
	}
	if s == "" {
		return ""
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// effectiveModelRate 返回模型当前生效倍率：有机器可读优惠时取折扣价，
// 否则取牌价；两者均缺省时为空。
func effectiveModelRate(mi ModelInfo) string {
	if mi.PromoFactor != nil && strings.TrimSpace(mi.PromoCredits) != "" {
		return normalizeModelRate(mi.PromoCredits)
	}
	return normalizeModelRate(mi.Credits)
}

// storeModelRates 按 realm 整体替换模型倍率快照。目录成功刷新但没有可解析
// 倍率时写入空桶，使旧倍率不会继续冒充当前价。
func (c *Client) storeModelRates(realm string, infos []ModelInfo) {
	rates := make(map[string]string, len(infos))
	for _, mi := range infos {
		if mi.ID == "" {
			continue
		}
		if rate := effectiveModelRate(mi); rate != "" {
			rates[mi.ID] = rate
		}
	}
	c.effortsMu.Lock()
	defer c.effortsMu.Unlock()
	if c.modelRates == nil {
		c.modelRates = make(map[string]map[string]string)
	}
	c.modelRates[realmKey(realm)] = rates
}

// ModelRate 返回最近成功刷新的指定域模型生效倍率；未知返回空串。
func (c *Client) ModelRate(realm, model string) string {
	if c == nil || model == "" {
		return ""
	}
	c.effortsMu.RLock()
	defer c.effortsMu.RUnlock()
	return c.modelRates[realmKey(realm)][model]
}

// GlobalEffortSnapshot 导出 global 域 effort 能力缓存（探测下发 ∪ 静态兜底合并后的桶），
// 供 /v1/models 输出 reasoning_supported_efforts / reasoning_default_effort。
// 返回副本；桶未填充（无 global 账号或从未探测）→ nil（调用方回落静态兜底表）。
func (c *Client) GlobalEffortSnapshot() (efforts map[string][]string, defaults map[string]string) {
	return c.effortsSnapshot("global"), c.defaultEffortsSnapshot("global")
}

// v3ConfigDomain /v3/config 的 X-Domain：优先账号落盘 domain，否则 chatBase host。
func v3ConfigDomain(a *auth.Auth, chatBase string) string {
	if a != nil {
		// Domain 加锁快照（见 auth.DomainValue：keepalive 刷新在 a.mu 内改写）。
		if d := strings.TrimSpace(a.DomainValue()); d != "" {
			d = strings.TrimPrefix(d, "https://")
			d = strings.TrimPrefix(d, "http://")
			return strings.TrimSuffix(d, "/")
		}
	}
	if u, err := url.Parse(chatBase); err == nil && u.Host != "" {
		return u.Host
	}
	return "copilot.tencent.com"
}

// fetchV3ConfigModelMap 拉官方 IDE 配置目录，按模型 id 建能力表。
// 该端点对 UA 敏感：必须带 CodeBuddy/CodeBuddyIDE 版本，否则 400 code=12403。
// ua 为该次请求的 User-Agent；空串等价 codeBuddyIDEUA。该端点对 UA 敏感且**不同 UA 下发
// 不同模型集合**（见 codeBuddyCLIUA 注释），global 探测据此并发两路取并集。
func (c *Client) fetchV3ConfigModelMap(a *auth.Auth, ua string) (map[string]ModelInfo, error) {
	req, err := http.NewRequest(http.MethodGet, c.chatBase(a)+"/v3/config", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	// AccessToken 加锁快照（同 fetchEnterpriseModels）。
	req.Header.Set("Authorization", "Bearer "+a.AccessTokenValue())
	if a != nil && a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	req.Header.Set("X-Domain", v3ConfigDomain(a, c.chatBase(a)))
	req.Header.Set("X-Product", "SaaS")
	if ua == "" {
		ua = codeBuddyIDEUA
	}
	req.Header.Set("User-Agent", ua)
	c.injectCodeBuddyRequest(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		// 读失败 → 传输层错误：半截 body 不进解析（不罚号）。
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("v3/config status %d: %s", resp.StatusCode, truncate(string(raw), 120))
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Models []dynModelEntry `json:"models"`
			// 试用模型横幅：上游把「N 天免费试用」的模型放在这里，**不在 data.models 里**。
			// 实测 global 侧 hy4-preview-f 只出现在此（modelId=hy4-preview-f、
			// targetModelId=hy4-preview、trialDays=14），纯 data.models 解析会漏掉它。
			ProductFeaturesConfig struct {
				ModelTrialBanner struct {
					Banners []struct {
						ModelID       string `json:"modelId"`
						TargetModelID string `json:"targetModelId"`
					} `json:"banners"`
				} `json:"ModelTrialBanner"`
			} `json:"productFeaturesConfig"`
			ModelPromotions []v3ModelPromotion `json:"modelPromotions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("v3/config parse: %w", err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("v3/config code=%d", env.Code)
	}
	out := make(map[string]ModelInfo, len(env.Data.Models))
	for _, m := range env.Data.Models {
		if strings.TrimSpace(m.ID) == "" {
			continue
		}
		out[m.ID] = m.modelInfo()
	}
	// 补入试用横幅模型（ModelTrialBanner）：上游把「N 天免费试用」的模型只放在这里，
	// data.models 里没有，故纯目录解析会漏（实测 global 侧 hy4-preview-f 即如此，
	// 但该模型**实际可调用**）。
	//
	// 元数据口径：能力字段（context/maxTokens/efforts/reasoning 等）从 targetModelId
	// 的既有条目继承——试用版与其转正目标是同族模型，能力应当一致；
	// 但 **Credits 与 Tags 显式清空**——它们描述的是"转正后"的计费与营销信息
	// （如 hy4-preview 的 x0.29 与 badge），用在免费试用版上会误导下游展示。
	//
	// firstUseTimeKey / trialDays 属**账号级**试用状态，不透出给下游。
	for _, b := range env.Data.ProductFeaturesConfig.ModelTrialBanner.Banners {
		id := strings.TrimSpace(b.ModelID)
		if id == "" {
			continue
		}
		if _, exists := out[id]; exists {
			continue
		}
		mi := ModelInfo{ID: id}
		if tgt := strings.TrimSpace(b.TargetModelID); tgt != "" {
			if base, ok := out[tgt]; ok {
				mi = base
				mi.ID = id
			}
		}
		mi.Credits = ""
		mi.Tags = nil
		out[id] = mi
	}
	// 挂当前生效的限时优惠（modelPromotions）：Credits 字段是**牌价**（转正后基准
	// 倍率，如 hy4-preview-f 的 x0.29），而 WorkBuddy 客户端显示的是生效价（试用/
	// 折扣窗口内 factor 打折）——面板据此展示「生效价 + 标签 + 牌价」。
	applyModelPromotions(out, env.Data.ModelPromotions)

	if len(out) == 0 {
		return nil, fmt.Errorf("v3/config returned empty models")
	}
	return out, nil
}
