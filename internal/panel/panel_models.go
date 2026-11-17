// panel_models.go 面板「模型与档位」只读视图：实时查上游模型目录 + 单条目的构造规则。
//
// 从 panel.go 拆出（2026-11-17）：这段是本包唯一与上游模型目录强耦合的部分
// （双域探测、四级上下文窗口查找、三值能力字段的条件透出），单独成文件后
// panel.go 只留「装配 + 路由 + 账号运维」，读起来知道看哪。
//
// 三值语义（AGENTS.md 硬要求）：能力字段为 nil 表示**未知**，必须省略，
// 绝不能落成 false——否则前端 `=== false` 会把未知当成「不支持」筛掉。
package panel

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"yg-gateway/internal/upstream"
	"yg-gateway/internal/usage"
)

// models 实时查询上游模型列表与 reasoning 实际档位（直连上游，不读路由层 1h 缓存）：
// 回答"该模型到底支持哪几档思考"。顺带刷新 client 的 effort 降级能力缓存。
// 与 /v1/models 同口径的双域输出：CN 域模型加 "cn:" 前缀、global 域加 "global:" 前缀
// （gateway 路由协议，前端显示的 id 就是调用时要填的完整 model 值）。
// 各域独立探测、独立容错：某域无可用账号则整域跳过；两域全空时才报错
// （有错误明细回 502，一个账号都没有回 503）。
func (p *Panel) models(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	var fetchErrs []string

	// CN 域：有可用 CN 账号才查（此前无条件 Pool.Pick()+FetchModels——选中 global
	// 账号时打 CN 端点必然失败，混合池表现为偶发 502，纯 global 池必炸）。
	if uids := p.cfg.Pool.AvailableUIDsForRealm("cn"); len(uids) > 0 {
		if acct := p.cfg.Pool.AuthByUID(uids[0]); acct != nil {
			infos, err := p.cfg.Upstream.FetchModels(acct)
			if err != nil {
				fetchErrs = append(fetchErrs, "cn: "+err.Error())
			} else {
				for _, mi := range infos {
					out = append(out, panelModelEntry("cn", mi, mi.Efforts, mi.DefaultEffort, p.cfg.Upstream.HTTP))
				}
			}
		}
	}

	// global 域：路由开关开且有可用 global 账号才查（独立目录端点，FetchGlobalModelInfos；
	// Upstream.GlobalEnabled 是探测侧同一道闸，与 main 装配的 config global.enabled 一致）。
	if p.cfg.Upstream.GlobalEnabled {
		if uids := p.cfg.Pool.AvailableUIDsForRealm("global"); len(uids) > 0 {
			if acct := p.cfg.Pool.AuthByUID(uids[0]); acct != nil {
				infos := p.cfg.Upstream.FetchGlobalModelInfos(acct)
				if len(infos) == 0 {
					fetchErrs = append(fetchErrs, "global: 上游未返回可用模型")
				} else {
					efforts, defaults := p.cfg.Upstream.GlobalEffortSnapshot()
					for _, mi := range infos {
						out = append(out, panelModelEntry("global", mi, efforts[mi.ID], defaults[mi.ID], p.cfg.Upstream.HTTP))
					}
				}
			}
		}
	}

	if len(out) == 0 {
		if len(fetchErrs) > 0 {
			writeErr(w, http.StatusBadGateway, "fetch models: "+strings.Join(fetchErrs, "; "))
			return
		}
		writeErr(w, http.StatusServiceUnavailable, "没有可用账号：请先在面板添加账号再查询")
		return
	}
	// 实测积分成本率合并（usage 计费样本，72h 滚动窗口，derivation）：把
	// credit_by_model 的 credits_per_1m_tokens 按裸模型名挂到模型条目，前端在
	// 倍率列下方显示「实测 X 积分/M · N 样本」。无样本不显示——不伪造成本
	//（AGENTS.md 四态语义：没样本 = unknown，不是"免费/未知"）。
	if p.cfg.Usage != nil {
		snap := p.cfg.Usage.SnapshotWindow(usage.Window{Hours: 72}, nil, nil, nil, nil)
		costs := make(map[string]usage.CreditAgg, len(snap.CreditByModel))
		for _, ca := range snap.CreditByModel {
			costs[ca.Key] = ca
		}
		for i := range out {
			id, _ := out[i]["id"].(string) // 忽略：id 是本函数刚写入的 realm+模型名，断言失败只会让 base 为空串（无成本样本可挂），不影响响应
			base := id
			if j := strings.LastIndexByte(base, ':'); j >= 0 {
				base = base[j+1:]
			}
			if ca, ok := costs[base]; ok && ca.CreditSamples > 0 {
				out[i]["cost_ppm"] = ca.CreditsPer1MTokens
				out[i]["cost_samples"] = ca.CreditSamples
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "models": out})
}

// panelModelEntry 构造单个模型条目（两域共用）：id 带 realm 前缀（调用值即显示值），
// context_length / max_output_tokens 走四级查找链，effort 档位按 realm 域取
// EffortListing（远端权威 ∪ 静态兜底表）——与 /v1/models 同一口径，两侧不再漂移。
func panelModelEntry(realm string, mi upstream.ModelInfo, remoteEfforts []string, remoteDefault string, httpc *http.Client) map[string]any {
	entry := map[string]any{
		"id":                 realm + ":" + mi.ID,
		"name":               mi.Name,
		"default_effort":     mi.DefaultEffort,
		"supported_efforts":  mi.Efforts,
		"credits":            mi.Credits,
		"description":        mi.Description,
		"tags":               mi.Tags,
		"vendor":             mi.Vendor,
		"is_default":         mi.IsDefault,
		"supports_tool_call": mi.SupportsToolCall,
		"only_reasoning":     mi.OnlyReasoning,
		"reasoning_effort":   mi.ReasoningEffort,
		"reasoning_summary":  mi.ReasoningSummary,
	}
	// 两个能力字段是**三值**，必须条件透出：nil（上游未声明 = 未知）要**省略**，
	// 绝不能落成 false——否则前端 `m.supports_images === false` 会把未知模型当成
	// "不支持"筛掉，正是 AGENTS.md 明令禁止的形态（面板 known bug 的根因就在这一层）。
	if mi.SupportsReasoning != nil {
		entry["supports_reasoning"] = *mi.SupportsReasoning
	}
	if mi.SupportsImages != nil {
		entry["supports_images"] = *mi.SupportsImages
	}
	// can_disable_thinking 与 /v1/models 同口径：仅「推理能力已知 且 思考可关」时才透出
	// true；其余（推理未知 / 显式不可关）一律省略字段。此前无条件 emit 会把未知落成
	// false，前端 `!m.can_disable_thinking` 把思考档位未知的模型从 off 档筛选里藏掉
	// （AGENTS.md：缺失=未知，不是"不支持"）。
	if mi.SupportsReasoning != nil && mi.CanDisableThinking {
		entry["can_disable_thinking"] = true
	}
	// 限时优惠（modelPromotions）：credits 是牌价，promo_* 是当前生效折扣
	//（WorkBuddy 客户端显示的就是这个生效价）。前端据此显示「生效价+标签+划线牌价」。
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
	if mi.MaxAllowedSize > 0 {
		entry["max_allowed_size"] = mi.MaxAllowedSize
	}
	entry["context_length"] = upstream.ContextWindowListingV4(mi.ID, mi.ContextWindow, httpc)
	if mo, ok := upstream.MaxOutputTokensListingV4(mi.ID, mi.MaxTokens, httpc); ok {
		entry["max_output_tokens"] = mo
	}
	if efforts, def := upstream.EffortListing(realm, mi.ID, remoteEfforts, remoteDefault); efforts != nil {
		entry["supported_efforts"] = efforts
		if def != "" {
			entry["default_effort"] = def
		}
	}
	return entry
}

// modelProbes 返回模型输出上限的探测结果（scripts/probe_max_tokens.py --panel-out
// 写入的契约文件），供前端在「模型与档位」的实测列做风险标注。
//
// 设计边界：纯只读透传——文件缺失/未配置返回空集（面板退化为无标注，与历史行为
// 一致），网关自身不解析字段语义、不据此做任何路由或出站决策；上游改了限制后
// 重跑一次工具、下次查询即刷新，无需重启网关。
func (p *Panel) modelProbes(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"probes": map[string]json.RawMessage{}, "exists": false}
	if p.cfg.ProbeFile == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	raw, err := os.ReadFile(p.cfg.ProbeFile)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, out)
			return
		}
		writeErr(w, http.StatusInternalServerError, "read probes: "+err.Error())
		return
	}
	var f struct {
		Version int                        `json:"version"`
		Probes  map[string]json.RawMessage `json:"probes"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		writeErr(w, http.StatusBadGateway, "parse probes: "+err.Error())
		return
	}
	if f.Probes == nil {
		f.Probes = map[string]json.RawMessage{}
	}
	out["probes"] = f.Probes
	out["exists"] = true
	if fi, err := os.Stat(p.cfg.ProbeFile); err == nil {
		out["updated_at"] = fi.ModTime().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}
