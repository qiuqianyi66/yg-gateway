package panel

import (
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
