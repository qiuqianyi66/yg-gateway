package pool

import (
	"encoding/json"
	"testing"

	"yg-gateway/internal/auth"
)

// statusOf 取账号状态；ok=false（账号不存在）视为测试失败。
func statusOf(t *testing.T, p *Pool, uid string) Status {
	t.Helper()
	st, ok := p.Status(uid)
	if !ok {
		t.Fatalf("Status(%q) not found", uid)
	}
	return st
}

// TestModelCostProvenanceInference 成本台账必须标出 tier 是**推断**且带样本数。
//
// 为什么：tier=收费 是从少量 credit 实测样本外推出来的。消费者（面板、agent、
// 养号决策）如果把它当成"观察到的事实"，就可能凭 1-2 条噪声样本把一个健康号
// 踢出选号——而且不报错。
//
// 牙齿：把 Provenance 字段去掉（或 kind 写成 observation）→ 本测试 FAIL。
func TestModelCostProvenanceInference(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)

	// 单条收费观测 → tier 2，但样本只有 1。
	p.NoteModelCost("u1", "hy4-preview", 2.9, 1000)

	st := statusOf(t, p, "u1")
	if len(st.ModelCosts) != 1 {
		t.Fatalf("ModelCosts len=%d want 1", len(st.ModelCosts))
	}
	mc := st.ModelCosts[0]
	if mc.Provenance == nil {
		t.Fatal("ModelCostStatus.Provenance 缺失：tier 是推断值，必须带来源标注")
	}
	if mc.Provenance.Kind != "inference" {
		t.Errorf("provenance.kind=%q want inference", mc.Provenance.Kind)
	}
	if mc.Provenance.Samples != 1 {
		t.Errorf("provenance.samples=%d want 1", mc.Provenance.Samples)
	}
	if mc.Provenance.Basis == "" {
		t.Error("provenance.basis 不得为空（要说明凭什么这么判）")
	}
}

// TestModelCostProvenanceSerializes provenance 必须出现在 /status 的 JSON 里
// （面板与 agent 都靠这个字段判断可信度）。
func TestModelCostProvenanceSerializes(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetCredits("u1", 1000, 0)
	p.NoteModelCost("u1", "m", 2.9, 1000)

	st := statusOf(t, p, "u1")
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	costs, ok := got["model_costs"].([]any)
	if !ok || len(costs) != 1 {
		t.Fatalf("model_costs missing or empty: %s", raw)
	}
	row, _ := costs[0].(map[string]any)
	prov, ok := row["provenance"].(map[string]any)
	if !ok {
		t.Fatalf("provenance missing in JSON: %s", raw)
	}
	if prov["kind"] != "inference" {
		t.Errorf("provenance.kind=%v want inference", prov["kind"])
	}
}

// TestEwmaProvenanceUnknownWhenCold EWMA 样本不足时必须标 unknown——
// 数值仍透出（保留 ADR-005 的既有透出契约：运维要能看到 EWMA 是否在工作），
// 但可信度标注为"不可作事实使用"。
//
// 设计取舍（本轮踩过一次并修正）：最初实现是「样本不足就不输出数字」，立刻
// 打破了 ADR-005 的透出契约（TestEwmaStatusExposure 抓到）。正确做法是 rea 的
// "Preserve partial facts"——**标注**不确定性，而不是把值藏掉；藏掉会让运维
// 失去观察手段。
//
// 牙齿：把 provenance 分档去掉（冷启动也标 derivation）→ 本测试 FAIL。
func TestEwmaProvenanceUnknownWhenCold(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetEwma(true, 0.1, 5) // minSamples=5

	// 只喂 2 条成功样本 < 5
	p.NoteSuccess("u1")
	p.NoteSuccess("u1")

	st := statusOf(t, p, "u1")
	if st.EwmaProvenance == nil {
		t.Fatal("EwmaProvenance 缺失")
	}
	if st.EwmaProvenance.Kind != "unknown" {
		t.Errorf("cold EWMA provenance.kind=%q want unknown", st.EwmaProvenance.Kind)
	}
	if st.EwmaProvenance.Basis == "" {
		t.Error("unknown 的 basis 应说明「需要什么才能消解」")
	}
	// 数值仍透出（既有 ADR-005 透出契约），但消费者能从 provenance 看出它不可信。
	if st.EwmaSuccess == nil {
		t.Error("样本不足时数值仍应透出（既有透出契约），只是标注为 unknown")
	}
}

// TestEwmaProvenanceDerivationWhenWarm 样本达标后标 derivation（EWMA 是平滑派生值，
// 不是真实成功率），且正常输出数字。
func TestEwmaProvenanceDerivationWhenWarm(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	p.SetEwma(true, 0.1, 3)

	for i := 0; i < 3; i++ {
		p.NoteSuccess("u1")
	}

	st := statusOf(t, p, "u1")
	if st.EwmaProvenance == nil {
		t.Fatal("EwmaProvenance 缺失")
	}
	if st.EwmaProvenance.Kind != "derivation" {
		t.Errorf("warm EWMA provenance.kind=%q want derivation", st.EwmaProvenance.Kind)
	}
	if st.EwmaSuccess == nil {
		t.Error("样本达标后应输出 ewma_success")
	}
}

// TestEwmaDisabledNoProvenance EWMA 未开启时不输出 provenance（避免给消费者
// 一个空的标注对象造成困惑）。
func TestEwmaDisabledNoProvenance(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999})
	// 不调 SetEwma

	st := statusOf(t, p, "u1")
	if st.EwmaProvenance != nil {
		t.Errorf("EWMA 未开启时不该输出 provenance，got %+v", st.EwmaProvenance)
	}
}
