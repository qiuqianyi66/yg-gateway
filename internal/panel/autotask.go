// autotask.go 面板「一键完成」任务的编排核心：动作注册表、单任务/全量 HTTP 入口、
// 领奖与回读、per-account 互斥锁。
//
// 剧本实现按业务域拆到同包文件：
//   - autotask_mp.go     小程序口径簇（mpTaskCodes 双口径查询、growth/Sequential 链）
//   - autotask_scripts.go PC 域剧本（chat/Buddy/桌面指纹事件链/专家召唤/主题模板画布）
//
// 设计依据：上游 scripts/task_*.py 实测结论 + 2026-09-12 桌面指纹协议逆向
// （data/desktop-task-protocol.md）——
//   - first_buddy（+300 分）：report（前置解锁）→ agreement → buddy/first
//   - chat_5（+100 分）：累计 5 条 chat_request_send 上报
//   - Model_chat_GLM5.2（+100 分）：accept → 用 glm-5.2 真实对话一次 → 上报（模型字段对齐）
//   - RichMeow_Chat：桌面指纹（workbuddy-desktop）完整对话事件链，纯 API 可点亮（三账号实测）
//   - Buddy_App / Buddy_App_QQ：buddyapp 五连事件，纯 API 可点亮（两账号实测）
//   - automation_1：automated_task_create_suc 事件，纯 API 可点亮（两账号实测）
//   - Library_read：web 域 web_element_click(library_doc_intro_click)（三账号实测）
//   - template_5 / playbook_prompt / create_canvas：asar 逆向出的判据事件
//     （template_used / playbook_prompt_send / wbx_design_canvas_*），纯 API 可点亮（三账号实测）
//   - expert_5 / Expert_team_use_3：真实专家列表 + 召唤链 + 真实 chat（服务端 requestId）
//   - expert_actual_use（三账号实测）
//   - Hp_Appearance：appearance/set + appearance_skin_apply 事件（两账号实测）
//
// 已补齐（原记为「仍未破解 / 不做」，实现见下表条目）：skill_1（runSkillFresh，人杰2 点亮）、
// Expert_lighthouse（runExpertLighthouse，两账号点亮）。
// 仍不做：Expert_Philanthropy（真实捐款）。
//
// 所有动作幂等：已 claimed/已达标的任务直接跳过，不重复消耗上游配额。
package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/logfmt"
	"yg-gateway/internal/upstream"
)

// autoAction 一个可自动化的任务动作。
type autoAction struct {
	TaskCode string // 目标任务 code
	Desc     string // 展示用说明
	Attempt  bool   // true = 尝试型（上游未证实可脚本化，跑了可能不点亮）
	run      func(p *Panel, a *auth.Auth) (string, error)
}

// autoActions 已实现的任务动作表（顺序即执行顺序：先解锁依赖项）。
// first_buddy 依赖活跃上报解锁，故 chat_5/first_buddy 的执行都自带 report 步骤。
var autoActions = []autoAction{
	{
		TaskCode: "chat_5",
		Desc:     "上报 5 条对话活跃事件（自动补足差额）",
		run:      runChat5,
	},
	{
		TaskCode: "first_buddy",
		Desc:     "上报解锁 → 同意协议 → 领取第一只 Buddy（+300 分）",
		run:      runFirstBuddy,
	},
	{
		TaskCode: "Model_chat_GLM5.2",
		Desc:     "接受任务 → glm-5.2 真实对话一次 → 对齐模型上报",
		run:      runModelChat,
	},
	{
		TaskCode: "RichMeow_Chat",
		Desc:     "桌面指纹事件链上报（已验证：纯 API 可点亮，三账号实测）",
		run:      runRichMeow,
	},
	{
		TaskCode: "Buddy_App",
		Desc:     "上报「进入 Buddy 应用」事件链（已验证：纯 API 可点亮）",
		run:      runBuddyApp,
	},
	{
		TaskCode: "Buddy_App_QQ",
		Desc:     "上报「进入企鹅教师助手」事件链（已验证：纯 API 可点亮）",
		run:      runBuddyApp,
	},
	{
		TaskCode: "automation_1",
		Desc:     "上报「定时任务创建」事件（已验证：纯 API 可点亮）",
		run:      runAutomationCreate,
	},
	{
		TaskCode: "Library_read",
		Desc:     "上报「读资料库介绍」事件（已验证：纯 API 可点亮）",
		run:      runLibraryRead,
	},
	{
		TaskCode: "template_5",
		Desc:     "上报「使用模板创建任务」事件组 ×5（已验证：三账号点亮）",
		run:      runTemplateUse,
	},
	{
		TaskCode: "playbook_prompt",
		Desc:     "上报「灵感案例做同款发送 Prompt」事件组（已验证：三账号点亮）",
		run:      runPlaybookPrompt,
	},
	{
		TaskCode: "create_canvas",
		Desc:     "上报「设计创意画布创建」事件组（已验证：三账号点亮，+300 分）",
		run:      runCreateCanvas,
	},
	{
		TaskCode: "expert_5",
		Desc:     "真实专家召唤+使用链 ×5（专家市场列表+真实 chat，已验证：三账号点亮）",
		run:      runExpertUse,
	},
	{
		TaskCode: "Expert_team_use_3",
		Desc:     "真实专家团召唤+使用链 ×3（已验证：三账号点亮）",
		run:      runExpertTeamUse,
	},
	{
		TaskCode: "Hp_Appearance",
		Desc:     "设置主题 API + 皮肤生效事件（已验证：两账号点亮）",
		run:      runAppearance,
	},
	{
		TaskCode: "skill_1",
		Desc:     "真实对话 + skill_info 技能加载事件（已验证：人杰2 点亮）",
		run:      runSkillFresh,
	},
	{
		TaskCode: "Expert_lighthouse",
		Desc:     "真实轻量云专家召唤+使用链（chat 链带 has_expert，已验证：两账号点亮）",
		run:      runExpertLighthouse,
	},
	{
		TaskCode: "black_cat",
		Desc:     "夜猫子：23:00–08:00 窗口内 glm-5.2 对话补足（窗口外提示稍后再试）",
		Attempt:  true,
		run:      runBlackCat,
	},
	{
		TaskCode: "school_season",
		Desc:     "校园日（小程序口径）：accept → mini 对话+activityId 上报 → 领奖（+100c+5e）",
		run:      runSchoolSeason,
	},
	{
		TaskCode: "Sequential_Tasks_1",
		Desc:     "小程序首对话（小程序口径）：accept → mini 对话上报 → 领奖（+100c+5e）",
		run:      runSequentialChat,
	},
	{
		TaskCode: "Sequential_Tasks_2",
		Desc:     "小程序选专家对话（小程序口径）：市场专家 id → accept → expert_actual_use 上报 → 领奖（+200c+5e）",
		run:      runMiniExpert,
	},
	{
		TaskCode: "Sequential_Tasks_3",
		Desc:     "小程序五次对话（小程序口径）：accept → mini 对话上报 ×5（自动补差额）→ 领奖（+300c+5e）",
		run:      runSequentialChat5,
	},
	{
		TaskCode: "Sequential_Tasks_4",
		Desc:     "小程序定时任务（预留，每日零点解锁一环）：accept → 定时任务创建事件（PC 同源）→ 领奖（判据待解锁验证）",
		run:      runSequentialAutomation,
	},
	{
		TaskCode: "Sequential_Tasks_5",
		Desc:     "小程序使用 GLM5.2（预留）：accept → 带模型字段的 mini 对话上报 → 领奖（判据待解锁验证）",
		run:      runSequentialModelChat,
	},
	{
		TaskCode: "Sequential_Tasks_6",
		Desc:     "小程序十次对话（预留）：accept → mini 对话上报 ×target（自动补差额）→ 领奖",
		run:      runSequentialChat10,
	},
	{
		TaskCode: "Sequential_Tasks_7",
		Desc:     "体验灵感功能（预留，疑 PC 口径 +500c+5e）：accept → 灵感事件组（PC+mp 双形态）→ 领奖（判据待解锁验证）",
		run:      runSequentialPlaybook,
	},
}

// autoActionFor 查任务对应的动作；无则返回 nil（不可自动化）。
func autoActionFor(code string) *autoAction {
	for i := range autoActions {
		if autoActions[i].TaskCode == strings.TrimSpace(code) {
			return &autoActions[i]
		}
	}
	return nil
}

// autoActionIndex 任务在 autoActions 中的顺序（队列执行按依赖序排；未知返回大值）。
func autoActionIndex(code string) int {
	for i := range autoActions {
		if autoActions[i].TaskCode == code {
			return i
		}
	}
	return 1 << 20
}

// reportGap 连续上报之间的间隔（对齐上游脚本实测的 1.05s 口径，避免风控）。
var reportGap = 1050 * time.Millisecond

// accountTaskAuto 一键完成单个任务：执行对应动作 → 回读进度 → 汇报结果。
func (p *Panel) accountTaskAuto(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	var body struct {
		TaskCode string `json:"task_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TaskCode == "" {
		writeErr(w, http.StatusBadRequest, "task_code required")
		return
	}
	act := autoActionFor(body.TaskCode)
	if act == nil {
		writeErr(w, http.StatusNotImplemented,
			"该任务需要客户端内交互（无对应接口），无法自动完成；请按任务说明在官方客户端操作")
		return
	}
	// per-account 互斥：同账号的任务动作正在跑（单任务或全量）时直接 409，
	// 不并发重跑（动作幂等但 expert/skill 系含真实对话，重跑浪费配额）。
	if !p.tryLockAccount(uid) {
		writeErr(w, http.StatusConflict, "该账号有任务动作正在执行中，请等本轮结束后再试")
		return
	}
	defer p.unlockAccount(uid)
	// 前置读取：已完成的任务直接跳过（幂等，不浪费上游调用）。
	// taskByCode 已双口径（mp 专属码自动回落 mp 列表）。
	before, err := p.taskByCode(a, act.TaskCode)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list tasks: "+err.Error())
		return
	}
	if before == nil {
		writeErr(w, http.StatusNotFound, "该账号没有此任务")
		return
	}
	isMP := isMPTaskCode(act.TaskCode)
	if before.Claimed {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "skipped": true, "message": "该任务已领取过奖励"})
		return
	}
	msg, err := act.run(p, a)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "执行失败: "+err.Error())
		return
	}
	// 回读验证：上报 200 ≠ 计分（上游可能静默丢弃 + 计分异步），
	// 用有界轮询等异步计时落定，再决定是否自动领奖（mp 任务同口径回读）。
	var after *upstream.Task
	var aerr error
	if isMP {
		after, aerr = p.taskByCodeMP(a, act.TaskCode)
	} else {
		after, aerr = p.taskByCodeWaiting(a, act.TaskCode)
	}
	progressBefore, progressAfter := taskProgressText(before), ""
	claimable := false
	if aerr == nil && after != nil {
		progressAfter = taskProgressText(after)
		claimable = after.Claimable
	}
	resp := map[string]any{
		"ok":               true,
		"message":          msg,
		"progress_before":  progressBefore,
		"progress_after":   progressAfter,
		"claimable":        claimable,
		"attempt":          act.Attempt,
		"verify_supported": true,
	}
	// 达标即自动领奖（Web 端 claim）：把"完成→领奖"收敛成一步，无需用户再点一次。
	if claimable {
		credit, energy, cerr := p.claimRewardFor(a, act.TaskCode)
		if cerr == nil {
			resp["claimed"] = true
			resp["credit"] = credit
			resp["energy"] = energy
			if credit > 0 || energy > 0 {
				resp["message"] = msg + fmt.Sprintf("；已自动领奖 +%d 分 +%d 能", credit, energy)
			} else {
				resp["message"] = msg + "；奖励此前已领取"
			}
		} else {
			resp["claim_error"] = cerr.Error()
			resp["message"] = msg + "；达标但领奖失败，可在任务列表手动点「领取」重试"
		}
	}
	log.Printf("panel: 任务动作 uid=%s code=%s progress %s -> %s claimable=%v claimed=%v",
		uid, act.TaskCode, progressBefore, progressAfter, claimable, resp["claimed"])
	writeJSON(w, http.StatusOK, resp)
}

// taskProgressText 任务进度的可读表示（回读对比用）。
func taskProgressText(t *upstream.Task) string {
	if t == nil {
		return "?"
	}
	if t.Target > 0 {
		return fmt.Sprintf("%d/%d", t.Current, t.Target)
	}
	if t.Claimed {
		return "claimed"
	}
	return t.AcceptStatus
}

// truncateStr 截断错误文本（避免把上游长响应原样透给前端）。
// 复用 logfmt.Truncate：切点落在多字节字符中间时回退 rune 边界（中文上游错误
// 响应按字节切会出半截序列非法 UTF-8）；n<=0 返回空串。
func truncateStr(s string, n int) string {
	return logfmt.Truncate(s, n)
}

// runAutoAll 对单账号依次执行所有可自动化任务，返回逐项结果。
// 供「一键完成全部可自动任务」使用；单项失败不影响后续项。
//
// 流程：先把所有未接受的任务批量 accept（规范状态机；上游脚本建议"先 accept"），
// 再逐项执行行为链路。accept 不是进度产生的必要条件，但让后续状态流转规范。
func (p *Panel) runAutoAll(a *auth.Auth) []map[string]any {
	var out []map[string]any

	// 阶段 0：批量接受尚未接受的任务（失败不阻塞——行为事件才是进度唯一判据）。
	if tasks, err := p.cfg.Upstream.ListTasks(a); err == nil {
		var codes []string
		for _, t := range tasks {
			if !t.Claimed && !t.Locked && t.AcceptStatus != "accepted" && t.AcceptStatus != "completed" {
				codes = append(codes, t.TaskCode)
			}
		}
		if len(codes) > 0 {
			if err := p.cfg.Upstream.AcceptTasks(a, codes); err != nil {
				out = append(out, map[string]any{
					"task_code": "(批量接受)", "status": "error",
					"message": "接受任务失败（不阻塞后续）: " + err.Error(),
				})
			} else {
				out = append(out, map[string]any{
					"task_code": "(批量接受)", "status": "done",
					"message": fmt.Sprintf("已接受 %d 个任务", len(codes)),
				})
				time.Sleep(reportGap)
			}
		}
	}

	// 阶段 0b：小程序口径任务单独接受（默认列表不含 mp 码；失败不阻塞）。
	if mpTasks, err := p.cfg.Upstream.ListTasksMP(a); err == nil {
		var mpCodes []string
		for _, t := range mpTasks {
			if !t.Claimed && !t.Locked && t.AcceptStatus != "accepted" && t.AcceptStatus != "completed" {
				mpCodes = append(mpCodes, t.TaskCode)
			}
		}
		if len(mpCodes) > 0 {
			if err := p.cfg.Upstream.AcceptTasksMP(a, mpCodes); err != nil {
				out = append(out, map[string]any{
					"task_code": "(批量接受-mp)", "status": "error",
					"message": "接受小程序任务失败（不阻塞后续）: " + err.Error(),
				})
			} else {
				out = append(out, map[string]any{
					"task_code": "(批量接受-mp)", "status": "done",
					"message": fmt.Sprintf("已接受 %d 个小程序任务", len(mpCodes)),
				})
				time.Sleep(reportGap)
			}
		}
	}

	for _, act := range autoActions {
		item := map[string]any{"task_code": act.TaskCode, "desc": act.Desc}
		before, err := p.taskByCode(a, act.TaskCode)
		if err != nil {
			item["status"] = "error"
			item["message"] = "查询失败: " + err.Error()
			out = append(out, item)
			continue
		}
		if before == nil {
			item["status"] = "skipped"
			item["message"] = "该账号无此任务"
			out = append(out, item)
			continue
		}
		switch disposeTask(*before) {
		case taskSkip:
			item["status"] = "skipped"
			item["message"] = "已完成（" + taskProgressText(before) + "）"
			out = append(out, item)
			continue
		case taskClaim:
			// 达标未领：与队列路径同一口径——不跑动作，直接领奖（A7 / D3）。
			// 以前这里是 skipped（不领），于是同一个任务走队列能领到奖、走「一键完成」
			// 领不到，而两条路径都不报错。判定统一后由 disposeTask 决定，本处只执行。
			p.autoAllClaimOnly(a, act.TaskCode, item)
			out = append(out, item)
			time.Sleep(reportGap) // 项间节流
			continue
		}
		msg, err := act.run(p, a)
		if err != nil {
			item["status"] = "error"
			item["message"] = err.Error()
			out = append(out, item)
			continue
		}
		// 进度回读失败必须在日志里可见（R10 / ERROR-HANDLING.md 判据④「影响计量」）：
		// after 为 nil 时下面的自动领奖会静默跳过，与「该任务确实不存在」（本就无需领奖）
		// 不可区分 —— 奖励会一直没被领而运维看不出来。只加 WARN，不改返回值格式。
		var after *upstream.Task
		var aerr error
		if isMPTaskCode(act.TaskCode) {
			after, aerr = p.taskByCodeMP(a, act.TaskCode)
		} else {
			after, aerr = p.taskByCodeWaiting(a, act.TaskCode)
		}
		if aerr != nil {
			log.Printf("WARN: [panel] 进度回读失败 %s code=%s: %v（已跳过自动领奖，可在任务列表手动领取）",
				logfmt.Label(a.UID, a.Nickname), act.TaskCode, aerr)
		}
		item["status"] = "done"
		item["message"] = msg
		item["progress_after"] = taskProgressText(after)
		// 进度达标即自动领奖（mp 任务走 chat 域 mp 口径，其余 Web 端接口）。
		// 领奖失败不掩盖主流程结果：status 仍为 done，附加 claim_error 供前端提示。
		if after != nil && after.Claimable {
			item["claimable"] = true
			credit, energy, cerr := p.claimRewardFor(a, act.TaskCode)
			if cerr == nil {
				item["claimed"] = true
				item["credit"] = credit
				item["energy"] = energy
				if credit > 0 || energy > 0 {
					item["message"] = msg + fmt.Sprintf("；已自动领奖 +%d 分 +%d 能", credit, energy)
				} else {
					item["message"] = msg + "；奖励此前已领取"
				}
			} else {
				item["claim_error"] = cerr.Error()
				item["message"] = msg + "；达标但领奖失败（可在列表手动重试）"
			}
		}
		out = append(out, item)
		time.Sleep(reportGap) // 项间节流
	}
	return out
}

// autoAllClaimOnly 达标未领项的领奖分支（A7 / D3）：只领奖、不跑动作，
// 结果按既有 item 契约写回（claimable/claimed/credit/energy/claim_error + message）。
// 单独成函数是为了让 runAutoAll 别越过「>120 行」的深度棘轮，同时本分歧口径只有一处。
func (p *Panel) autoAllClaimOnly(a *auth.Auth, code string, item map[string]any) {
	item["claimable"] = true
	credit, energy, cerr := p.claimRewardFor(a, code)
	item["status"] = "done"
	if cerr != nil {
		item["claim_error"] = cerr.Error()
		item["message"] = "已达标未领；领奖失败（可在列表手动重试）: " + cerr.Error()
		return
	}
	item["claimed"] = true
	item["credit"] = credit
	item["energy"] = energy
	if credit > 0 || energy > 0 {
		item["message"] = fmt.Sprintf("已达标未领：已领奖 +%d 分 +%d 能", credit, energy)
		return
	}
	item["message"] = "已达标未领：奖励为 0 或此前已领取"
}

// accountTaskAutoAll 一键完成该账号全部可自动任务。
func (p *Panel) accountTaskAutoAll(w http.ResponseWriter, r *http.Request) {
	uid := r.PathValue("uid")
	a := p.accountByUID(w, uid)
	if a == nil {
		return
	}
	// per-account 互斥（与单任务动作共用一把锁）：重复点击 409。
	if !p.tryLockAccount(uid) {
		writeErr(w, http.StatusConflict, "该账号有任务动作正在执行中，请等本轮结束后再试")
		return
	}
	// 用 context 兜底超时（多项任务串联 + 每项含真实对话，可能耗时较长）。
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	done := make(chan []map[string]any, 1)
	go func() {
		defer p.unlockAccount(uid) // 流水线真正结束（而非 HTTP 超时返回）才放锁
		done <- p.runAutoAll(a)
	}()
	select {
	case results := <-done:
		log.Printf("panel: 一键完成可自动任务 uid=%s 共 %d 项", uid, len(results))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": results})
	case <-ctx.Done():
		// HTTP 侧超时返回，但后台流水线仍在跑——锁在流水线 goroutine 内释放，
		// 期间重复点击会被 409 挡住，不会出现两轮并发。
		writeErr(w, http.StatusGatewayTimeout, "执行超时（任务仍在后台继续）")
	}
}
