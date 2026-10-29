package panel

// trends_endpoint_test.go —— /panel/api/trends 时间序列证据端点。
//
// 覆盖：真实 reqlog 归档（临时目录）+ 真实 pool（注入账号）→ 经鉴权路由
// 断言 days 聚合 / accounts 现态 / 四态字段（success_rate unknown 语义、
// last_success_known）。期望值全部手写字面量，不复用被测函数输出。

import (
	"net/http"
	"testing"
	"time"

	"yg-gateway/internal/auth"
	"yg-gateway/internal/pool"
	"yg-gateway/internal/reqlog"
)

// chTrendsBool JSON 数字/布尔提取（JSON 解出的是 float64 或 bool）。
func chTrendsBool(t *testing.T, v any) bool {
	t.Helper()
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("值不是 bool：%v (%T)", v, v)
	}
	return b
}

// TestPanelTrendsAggregateAndAccounts 全链路：归档跨天聚合 + 账号现态。
func TestPanelTrendsAggregateAndAccounts(t *testing.T) {
	dir := t.TempDir()
	rl := reqlog.New(reqlog.Config{Enabled: true, Dir: dir, FileMaxBytes: 1 << 20, MaxBytes: 4 << 20, RetentionDays: 7})
	t.Cleanup(rl.Close)

	// 跨两天（本地时区）：day1 3 条（2 成 1 败 / acc1+acc2）、day2 2 条（2 成 / acc1）。
	now := time.Now()
	d1 := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local).AddDate(0, 0, -1)
	rl.Record(reqlog.Event{Time: d1, Account: "acc1", Model: "m", OK: true, PromptTokens: 100})
	rl.Record(reqlog.Event{Time: d1.Add(time.Minute), Account: "acc1", OK: false})
	rl.Record(reqlog.Event{Time: d1.Add(2 * time.Minute), Account: "acc2", OK: true})
	rl.Record(reqlog.Event{Time: d1.AddDate(0, 0, 1), Account: "acc1", OK: true})
	rl.Record(reqlog.Event{Time: d1.AddDate(0, 0, 1).Add(time.Minute), Account: "acc1", OK: true})
	rl.Close()

	pl := pool.New("")
	pl.Add(&auth.Auth{UID: "acc1", Nickname: "甲"})
	pl.SetCredits("acc1", 500, 0)
	// 从未成功过的号：LastSuccessKnown=false（unknown 语义）。
	pl.Add(&auth.Auth{UID: "acc2", Nickname: "乙"})
	pl.SetCredits("acc2", 100, 0)
	pl.NoteSuccess("acc1") // 有成功历史 → last_success_known=true

	p := chPanel(t, Config{Pool: pl, RequestLog: rl})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/trends", ""), http.StatusOK)

	days := chRows(t, out["days"])
	if len(days) != 2 {
		t.Fatalf("days = %d, want 2（跨两天）", len(days))
	}
	// 时间升序：day1 组在前。
	if chStr(t, days[0]["date"]) >= chStr(t, days[1]["date"]) {
		t.Fatalf("days 应时间升序：%v, %v", days[0]["date"], days[1]["date"])
	}
	d0 := days[0]
	if chNum(t, d0["requests"]) != 3 || chNum(t, d0["succeeded"]) != 2 ||
		chNum(t, d0["failed"]) != 1 || chNum(t, d0["active_accounts"]) != 2 {
		t.Fatalf("day1 = %+v", d0)
	}
	if chNum(t, d0["prompt_tokens"]) != 100 {
		t.Fatalf("day1 prompt_tokens = %v, want 100", d0["prompt_tokens"])
	}
	d1pt := days[1]
	if chNum(t, d1pt["requests"]) != 2 || chNum(t, d1pt["succeeded"]) != 2 ||
		chNum(t, d1pt["failed"]) != 0 || chNum(t, d1pt["active_accounts"]) != 1 {
		t.Fatalf("day2 = %+v", d1pt)
	}

	// accounts 现态：acc1 有成功历史（known）、acc2 从未成功（unknown 显式标记）。
	accts := chRows(t, out["accounts"])
	if len(accts) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accts))
	}
	var a1, a2 map[string]any
	for _, a := range accts {
		if chStr(t, a["uid"]) == "acc1" {
			a1 = a
		} else {
			a2 = a
		}
	}
	if a1 == nil || a2 == nil {
		t.Fatalf("accounts 缺 acc1/acc2：%+v", accts)
	}
	if chStr(t, a1["nickname"]) != "甲" || chNum(t, a1["credits"]) != 500 {
		t.Fatalf("acc1 = %+v", a1)
	}
	if chTrendsBool(t, a1["last_success_known"]) != true {
		t.Fatalf("acc1 last_success_known 应为 true（有成功历史）")
	}
	if chTrendsBool(t, a2["last_success_known"]) != false {
		t.Fatalf("acc2 last_success_known 应为 false（从未成功 = unknown）")
	}
	if chTrendsBool(t, a2["disabled"]) != false {
		t.Fatalf("acc2 disabled 应为 false")
	}

	// window = 归档事件总数。
	if chNum(t, out["window"]) != 5 {
		t.Fatalf("window = %v, want 5", out["window"])
	}
}

// TestPanelTrendsNoRequestLog trends 在未接线归档时回 501（与 request_metrics 同口径）。
func TestPanelTrendsNoRequestLog(t *testing.T) {
	p := chPanel(t, Config{Pool: pool.New("")})
	out := chWant(t, chDo(t, p, "GET", "/panel/api/trends", ""), http.StatusNotImplemented)
	if got := chErrMsg(t, out); got != "request logger not available" {
		t.Fatalf("err = %q", got)
	}
}
