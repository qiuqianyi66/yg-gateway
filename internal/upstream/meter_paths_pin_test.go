package upstream

import (
	"strings"
	"testing"
)

// TestBillingMeterPathsRealmOrderPin 钉住 billing/checkin 的**按 realm 路径候选序列**。
//
// 为什么需要这条：`/billing/meter/*` 族的候选路径有两个形态——国际版（无 /v2 前缀）与
// CN 现状（有 /v2 前缀），消费方（report.go 的 billingMeterJSON）只在**首个候选 404**
// 时回落下一个。所以「顺序」本身就是行为：把 global 的候选顺序反转，会让国际版账号先打
// 一个必然 404 的 CN 路径（多一跳、且在限流配额下白烧一次请求）。
//
// 本文件由 2026-10-16 的变异核验催生：把 `billingMeterPaths` 的 global 分支反转
// （`[]string{billingMeterPath, billingMeterPathV2}` → `[]string{billingMeterPathV2, billingMeterPath}`）
// 后，**upstream 包内 5 条既有测试全绿**——既有断言只钉了 `/v2/...` 后缀（即只覆盖 CN 单路径形态），
// global 的候选序列与顺序完全没有断言。补上本测试后，同一变异立即 FAIL。
//
// 反向验证记录（在 %TEMP% 副本内实测，原件未改）：
//   - 未变异：本测试 PASS；
//   - 施加上述反转：FAIL，报 `billingMeterPaths(global)[0] = "/v2/billing/meter/get-user-resource"
//     want "/billing/meter/get-user-resource"`。
//
// 即：**还原「global 首选无 /v2、404 才回落 /v2」这一行为，本测试会红。**
func TestBillingMeterPathsRealmOrderPin(t *testing.T) {
	global := upstreamhttpGlobalAuth()
	cn := upstreamhttpCNAuth()

	// 生产默认：GlobalEnabled 缺省 true（见 NewClient）；此处显式打开以对齐路由启用态。
	on := &Client{GlobalEnabled: true}
	// 逃生门：GlobalEnabled=false 时即便账号是 global 也回落 CN 单路径（纯 CN 锁定）。
	off := &Client{}

	if !global.IsGlobal() {
		t.Fatalf("前置条件不满足：global 账号未被判定为 global（domain=%q realm=%q）", global.Domain, global.Realm())
	}
	if cn.IsGlobal() {
		t.Fatalf("前置条件不满足：CN 账号被误判为 global（domain=%q realm=%q）", cn.Domain, cn.Realm())
	}

	cases := []struct {
		name string
		got  []string
		want []string
		why  string
	}{
		{
			name: "global · get-user-resource：国际版无 /v2 首选，404 才回落 /v2",
			got:  on.billingMeterPaths(global),
			want: []string{"/billing/meter/get-user-resource", "/v2/billing/meter/get-user-resource"},
			why:  "顺序反转会让国际版账号先打必然 404 的 CN 路径",
		},
		{
			name: "global · daily-checkin：同上",
			got:  on.checkinMeterPaths(global),
			want: []string{"/billing/meter/daily-checkin", "/v2/billing/meter/daily-checkin"},
			why:  "签到同样只在 404 时回落",
		},
		{
			name: "cn · get-user-resource：单元素 /v2（现状逐字，零回归）",
			got:  on.billingMeterPaths(cn),
			want: []string{"/v2/billing/meter/get-user-resource"},
			why:  "CN 只有 /v2 形态；多出候选会引入一次无意义重试",
		},
		{
			name: "cn · daily-checkin：单元素 /v2",
			got:  on.checkinMeterPaths(cn),
			want: []string{"/v2/billing/meter/daily-checkin"},
			why:  "同上",
		},
		{
			name: "逃生门 · GlobalEnabled=false 时 global 账号回落 CN 单路径",
			got:  off.billingMeterPaths(global),
			want: []string{"/v2/billing/meter/get-user-resource"},
			why:  "纯 CN 锁定必须连候选序列一起锁，不能只锁 base",
		},
		{
			name: "逃生门 · GlobalEnabled=false 时 global 账号的签到回落 CN 单路径",
			got:  off.checkinMeterPaths(global),
			want: []string{"/v2/billing/meter/daily-checkin"},
			why:  "同上",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.got) != len(tc.want) {
				t.Fatalf("候选数 = %d %v，want %d %v（%s）", len(tc.got), tc.got, len(tc.want), tc.want, tc.why)
			}
			for i := range tc.want {
				if tc.got[i] != tc.want[i] {
					t.Errorf("候选[%d] = %q，want %q（%s）", i, tc.got[i], tc.want[i], tc.why)
				}
			}
		})
	}
}

// TestBillingMeterPathsCoverMeterFamily 钉住「只有 /billing/meter/* 族走候选序列」这条边界：
// report 与其他 billing 端点（growth 等）不参与，避免将来有人把候选机制扩散到它们的路径上。
func TestBillingMeterPathsCoverMeterFamily(t *testing.T) {
	global := upstreamhttpGlobalAuth()
	on := &Client{GlobalEnabled: true}

	for _, p := range append(on.billingMeterPaths(global), on.checkinMeterPaths(global)...) {
		if !strings.HasPrefix(p, "/billing/meter/") && !strings.HasPrefix(p, "/v2/billing/meter/") {
			t.Errorf("候选路径 %q 不在 /billing/meter 族内：候选机制不应扩散到 report/growth 等端点", p)
		}
	}

	// report 域路径是独立常量，必须仍在 /v2 单路径上（不在候选族内）。
	if !strings.HasPrefix(reportPath, "/v2/") {
		t.Errorf("reportPath = %q，want /v2/ 前缀（report 不参与 meter 候选机制）", reportPath)
	}
}
