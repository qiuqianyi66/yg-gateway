package panel

import (
	"strings"
	"testing"

	"yg-gateway/internal/upstream"
)

// TestDisposeTaskTable 钉住「同一个任务状态只能有一个处置口径」（A7 / D3）。
//
// 背景：`growthPending`（扫描入队）与 `runAutoAll`（一键完成）早先各写一份判定，
// 「达标未领」在入队侧会入队去领、在一键完成侧被 skipped 不领——同一账号状态换个
// 入口点，奖励领没领到不一样，且两条路径都不报错。现在两条入口只调 disposeTask。
func TestDisposeTaskTable(t *testing.T) {
	cases := []struct {
		name string
		task upstream.Task
		want taskDisposition
	}{
		{"已领取", caTask("chat_5", 5, 5, "claimed"), taskSkip},
		{"锁定（每日解锁窗口）", caLockedTask("chat_5", 0, 5, "accepted"), taskSkip},
		{"无自动化动作（已达标）", caTask("no_action", 5, 5, "accepted"), taskSkip},
		{"无自动化动作（未达标）", caTask("no_action", 0, 5, "accepted"), taskSkip},
		{"达标未领", caTask("chat_5", 5, 5, "accepted"), taskClaim},
		{"超出目标未领", caTask("chat_5", 7, 5, "accepted"), taskClaim},
		{"未达标", caTask("chat_5", 1, 5, "accepted"), taskRun},
		{"目标为 0（无进度概念）", caTask("chat_5", 0, 0, "accepted"), taskRun},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := disposeTask(c.task); got != c.want {
				t.Fatalf("disposeTask = %v, want %v（claimed=%v locked=%v %d/%d）",
					got, c.want, c.task.Claimed, c.task.Locked, c.task.Current, c.task.Target)
			}
			// 入队口径必须与处置口径一致：只有 taskSkip 不入队。
			if enqueue := growthPending(c.task); enqueue != (c.want != taskSkip) {
				t.Fatalf("growthPending = %v, want %v（与 disposeTask 分叉了）", enqueue, c.want != taskSkip)
			}
		})
	}
}

// TestTaskEntryPointsAgreeOnClaim 两条入口（一键完成 / 队列执行）对同一任务状态必须
// 给出同一个结论：达标未领都要领到奖、且都不再白跑一次动作；未达标都只跑动作不领奖。
//
// 这条是 A7/D3 的核心断言：以前「达标未领」经一键完成 0 次领奖、经队列 1 次领奖。
func TestTaskEntryPointsAgreeOnClaim(t *testing.T) {
	caFastGaps(t)
	cases := []struct {
		name        string
		task        upstream.Task
		wantClaimed bool
	}{
		{"达标未领", caTask("chat_5", 5, 5, "accepted"), true},
		{"已领取", caTask("chat_5", 5, 5, "claimed"), false},
		{"未达标", caTask("chat_5", 1, 5, "accepted"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 入口一：一键完成（runAutoAll）
			fAuto := caNewFake()
			fAuto.caSetTask(c.task)
			fAuto.claimCredit, fAuto.claimEnergy = 100, 5
			pAuto := caNewPanel(t, fAuto, caAccount("u1"))
			item := caFindItem(t, pAuto.runAutoAll(caAccount("u1")), "chat_5")
			autoClaims := fAuto.caCallCount("POST", "/activity/growth/tasks/chat_5/claim")

			// 入口二：队列执行（runGrowthQueued）
			fQueue := caNewFake()
			fQueue.caSetTask(c.task)
			fQueue.claimCredit, fQueue.claimEnergy = 100, 5
			pQueue := caNewPanel(t, fQueue, caAccount("u1"))
			if _, err := pQueue.runGrowthQueued(caAccount("u1"), "chat_5"); err != nil {
				t.Fatalf("队列执行报错: %v", err)
			}
			queueClaims := fQueue.caCallCount("POST", "/activity/growth/tasks/chat_5/claim")

			if autoClaims != queueClaims {
				t.Fatalf("两条入口领奖次数不一致：一键完成=%d，队列=%d", autoClaims, queueClaims)
			}
			want := 0
			if c.wantClaimed {
				want = 1
			}
			if autoClaims != want {
				t.Fatalf("领奖次数 = %d, want %d", autoClaims, want)
			}
			if c.wantClaimed {
				if got := caBool(t, item, "claimed"); !got {
					t.Fatalf("一键完成 claimed = false, want true（item=%+v）", item)
				}
				// 前端靠 claimable 打「可领」标记；达标未领项必须带出来（PA8 变异即红）。
				if got := caBool(t, item, "claimable"); !got {
					t.Fatalf("一键完成 claimable = false, want true（item=%+v）", item)
				}
			} else if v, ok := item["claimed"]; ok && v != false {
				t.Fatalf("不该领奖却写了 claimed=%v（item=%+v）", v, item)
			}
			// 达标未领只领奖，不跑动作：两条入口都不该有动作上报。
			if c.wantClaimed {
				for name, n := range map[string]int{
					"一键完成": fAuto.caCallCount("POST", "/v2/report"),
					"队列":   fQueue.caCallCount("POST", "/v2/report"),
				} {
					if n != 0 {
						t.Fatalf("%s 在达标未领时仍跑了动作：/v2/report 次数 %d", name, n)
					}
				}
			}
		})
	}
}

// TestClaimOnlyFailureIsVisible 达标未领的领奖失败必须在结果里看得见（A7/D3 的失败分支）：
// 一键完成要带 claim_error 与原因，队列执行要把原因写进返回文案——只回一句
// 「已达标未领」的话，运维既不知道奖励没到账，也不知道为什么（R10 的反面）。
//
// 这条是 PA7 变异咬出来的缺口：原先 claim-only 分支的失败路径没有任何测试。
func TestClaimOnlyFailureIsVisible(t *testing.T) {
	caFastGaps(t)
	f := caNewFake()
	f.caSetTask(caTask("chat_5", 5, 5, "accepted"))
	f.raw["POST /activity/growth/tasks/chat_5/claim"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"claim down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	item := caFindItem(t, p.runAutoAll(caAccount("u1")), "chat_5")
	if msg := caStr(t, item, "message"); !strings.Contains(msg, "领奖失败") {
		t.Fatalf("一键完成 message = %q, want 含「领奖失败」", msg)
	}
	if ce := caStr(t, item, "claim_error"); !strings.Contains(ce, "http 500") {
		t.Fatalf("claim_error = %q, want 带上游状态码 500", ce)
	}
	if caStr(t, item, "status") != "done" {
		t.Fatalf("status = %q, want done（领奖失败不改变主流程结果）", caStr(t, item, "status"))
	}

	msg, err := p.runGrowthQueued(caAccount("u1"), "chat_5")
	if err != nil {
		t.Fatalf("队列项不该因领奖失败中断: %v", err)
	}
	if !strings.Contains(msg, "领奖失败") || !strings.Contains(msg, "http 500") {
		t.Fatalf("队列文案 = %q, want 含「领奖失败」与状态码", msg)
	}
}
