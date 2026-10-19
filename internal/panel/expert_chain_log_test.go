package panel

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"yg-gateway/internal/upstream"
)

// TestExpertChainPartialFailureIsLogged 钉住「专家召唤链的部分失败必须留痕」。
//
// 缺口来源（2026-10-19 变异 EX1/EX3 双双存活）：
//   - 把 `fail++` 删掉、或把 `if fail > 0` 的 WARN 关掉，测试**全绿**；
//   - 也就是「召唤失败对运维完全不可见」这件事没有任何断言盯着——
//     而返回文案只说成功数（"已对 0 位…完成"），看起来像正常结果。
//
// 判据：全部召唤失败时，返回值仍是可读文案（不 panic/不报错），但日志必须点名
// ok/fail/type 三个数字，否则运维没有任何入口知道这次链路根本没跑通。
func TestExpertChainPartialFailureIsLogged(t *testing.T) {
	f := caNewFake()
	f.experts = []upstream.MarketExpert{
		{ExpertID: "ex_chain_1", ExpertType: "agent", DisplayNameZH: "专家一"},
		{ExpertID: "ex_chain_2", ExpertType: "agent", DisplayNameZH: "专家二"},
	}
	// 召唤事件上报一律 500：两个专家都 fail，且不会走到 chat（因此没有 expertSummonGap 等待）。
	f.raw["POST /v2/report"] = caRaw{Status: 500, Body: `{"code":10000,"msg":"report down"}`}
	p := caNewPanel(t, f, caAccount("u1"))

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	msg, err := runExpertBatch(p, caAccount("u1"), "agent", 2)
	if err != nil {
		t.Fatalf("部分失败不该冒泡 error: %v", err)
	}
	if !strings.Contains(msg, "已对 0 位") {
		t.Fatalf("返回文案 = %q, want 含「已对 0 位」（成功数为 0）", msg)
	}
	logs := buf.String()
	if !strings.Contains(logs, "expert chain partial failure: ok=0 fail=2 type=agent") {
		t.Fatalf("日志缺少部分失败留痕（ok/fail/type）：\n%s", logs)
	}
}
