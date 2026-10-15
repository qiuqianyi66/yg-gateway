package evidence

import (
	"encoding/json"
	"testing"
)

// TestFactKinds 四态标记的基本构造。
//
// 牙齿：把任一构造函数的 Kind 写错（如 Infer 误置 observation）→ 本测试 FAIL。
func TestFactKinds(t *testing.T) {
	o := Observe(402, "upstream status")
	if o.Kind != KindObservation {
		t.Errorf("Observe kind=%q want %q", o.Kind, KindObservation)
	}

	d := Derive(0.87, "EWMA alpha=0.1", 12)
	if d.Kind != KindDerivation || d.Samples != 12 {
		t.Errorf("Derive kind=%q samples=%d want derivation/12", d.Kind, d.Samples)
	}

	i := Infer("charged", "2 credit samples", 2)
	if i.Kind != KindInference || i.Samples != 2 {
		t.Errorf("Infer kind=%q samples=%d want inference/2", i.Kind, i.Samples)
	}

	u := Unknown("no call yet")
	if u.Kind != KindUnknown {
		t.Errorf("Unknown kind=%q want %q", u.Kind, KindUnknown)
	}
}

// TestFactMarshalJSON 序列化必须把 provenance 带出去——消费者（面板/Prometheus/
// agent）靠 kind 判断这个值能不能当事实用于决策。
//
// 牙齿：去掉 MarshalJSON（退化成裸 value）→ 本测试 FAIL。
func TestFactMarshalJSON(t *testing.T) {
	raw, err := json.Marshal(Observe(402, "upstream status"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["kind"] != "observation" {
		t.Errorf("kind=%v want observation (raw=%s)", got["kind"], raw)
	}
	if got["value"] != float64(402) {
		t.Errorf("value=%v want 402 (raw=%s)", got["value"], raw)
	}
	if got["basis"] != "upstream status" {
		t.Errorf("basis=%v want 'upstream status' (raw=%s)", got["basis"], raw)
	}
}

// TestFactUnknownOmitsSamples unknown 且**无样本**时不该带 samples 字段
// （0 表示"没有样本"，不是"样本为零"）。
func TestFactUnknownOmitsSamples(t *testing.T) {
	raw, _ := json.Marshal(Unknown("no call yet"))
	if string(raw) == "" {
		t.Fatal("empty marshal")
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if _, has := got["samples"]; has {
		t.Errorf("unknown fact without samples must omit the field, got %s", raw)
	}
}

// TestFactUnknownKeepsPartialSamples unknown 但**已有部分样本**时必须带上样本数
// ——那是"离消解还差多远"的唯一结构化表达（如 EWMA 冷启动 2/5）。
//
// 牙齿：把 MarshalJSON 里的 samples 条件改回「仅 derivation/inference 输出」
// → 本测试 FAIL。
func TestFactUnknownKeepsPartialSamples(t *testing.T) {
	f := Unknown("成功率样本不足（2/5），需继续观测")
	f.Samples = 2
	raw, _ := json.Marshal(f)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["kind"] != "unknown" {
		t.Fatalf("kind=%v want unknown", got["kind"])
	}
	if got["samples"] != float64(2) {
		t.Errorf("partial samples lost: %s（消费者会误以为完全没有数据）", raw)
	}
}

// TestFactZeroValueUseful 零值 Fact 必须表现为 unknown（不是 observation）——
// 这样「忘了标注」默认落到最保守的一档，而不是最危险的那一档。
//
// 断言的是**行为**而不是内部字符串表示：零值的 Kind 字段是 ""，但对外
// （IsUnknown / MarshalJSON）必须呈现为 unknown。
func TestFactZeroValueUseful(t *testing.T) {
	var f Fact
	if !f.IsUnknown() {
		t.Error("zero Fact IsUnknown() = false, want true（未标注必须落 unknown）")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal zero fact: %v", err)
	}
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["kind"] != "unknown" {
		t.Errorf("zero Fact marshals to kind=%v want unknown (raw=%s)", got["kind"], raw)
	}
}

// TestFactIsUnknown 判定辅助。
func TestFactIsUnknown(t *testing.T) {
	if Observe(1, "x").IsUnknown() {
		t.Error("observation must not be unknown")
	}
	if !Unknown("x").IsUnknown() {
		t.Error("Unknown() must be unknown")
	}
}
