package jsonsafe

import (
	"encoding/json"
	"testing"
)

// 共用样本：覆盖"正常、类型不符、null、缺失"四类形态。
func sample() map[string]any {
	return map[string]any{
		"obj":    map[string]any{"inner": "v"},
		"arr":    []any{1.0, 2.0},
		"str":    "hello",
		"yes":    true,
		"no":     false,
		"f":      1.5,
		"i":      7,
		"i64":    int64(9),
		"f32":    float32(3),
		"num":    json.Number("42"),
		"badnum": json.Number("4.2"),
		"nullv":  nil,
	}
}

func TestMap(t *testing.T) {
	m := sample()
	if got, ok := Map(m, "obj"); !ok || got["inner"] != "v" {
		t.Fatalf("Map(obj) = %v, %v; want inner=v, true", got, ok)
	}
	// 类型不符 / null / 缺失 一律 false——不许返回空 map 冒充"取到了空对象"。
	for _, key := range []string{"str", "arr", "nullv", "missing"} {
		if v, ok := Map(m, key); ok || v != nil {
			t.Errorf("Map(%q) = %v, %v; want nil, false", key, v, ok)
		}
	}
	if v, ok := Map(nil, "obj"); ok || v != nil {
		t.Errorf("Map(nil, obj) = %v, %v; want nil, false", v, ok)
	}
}

func TestSlice(t *testing.T) {
	m := sample()
	if got, ok := Slice(m, "arr"); !ok || len(got) != 2 {
		t.Fatalf("Slice(arr) = %v, %v; want len 2, true", got, ok)
	}
	for _, key := range []string{"obj", "str", "nullv", "missing"} {
		if v, ok := Slice(m, key); ok || v != nil {
			t.Errorf("Slice(%q) = %v, %v; want nil, false", key, v, ok)
		}
	}
	if _, ok := Slice(nil, "arr"); ok {
		t.Error("Slice(nil, arr) ok = true, want false")
	}
}

func TestString(t *testing.T) {
	m := sample()
	if got, ok := String(m, "str"); !ok || got != "hello" {
		t.Fatalf("String(str) = %q, %v; want hello, true", got, ok)
	}
	// 关键：缺失/类型不符必须 false，不得返回空串冒充"字段是空串"。
	for _, key := range []string{"i", "obj", "nullv", "missing"} {
		if got, ok := String(m, key); ok || got != "" {
			t.Errorf("String(%q) = %q, %v; want \"\", false", key, got, ok)
		}
	}
	if _, ok := String(nil, "str"); ok {
		t.Error("String(nil, str) ok = true, want false")
	}
}

func TestBool(t *testing.T) {
	m := sample()
	if got, ok := Bool(m, "yes"); !ok || !got {
		t.Fatalf("Bool(yes) = %v, %v; want true, true", got, ok)
	}
	if got, ok := Bool(m, "no"); !ok || got {
		t.Fatalf("Bool(no) = %v, %v; want false, true", got, ok)
	}
	// 缺失 ≠ false：这是 AGENTS.md「禁止用 !field 判否」的机器化落点。
	for _, key := range []string{"str", "nullv", "missing"} {
		if _, ok := Bool(m, key); ok {
			t.Errorf("Bool(%q) ok = true, want false (缺失/类型不符 ≠ false)", key)
		}
	}
	if _, ok := Bool(nil, "yes"); ok {
		t.Error("Bool(nil, yes) ok = true, want false")
	}
}

func TestFloatStrict(t *testing.T) {
	m := sample()
	if got, ok := Float(m, "f"); !ok || got != 1.5 {
		t.Fatalf("Float(f) = %v, %v; want 1.5, true", got, ok)
	}
	// 严格性回归：credit 事故的原始形态——上游给字符串数字时必须是"未知"，不是 0。
	if got, ok := Float(map[string]any{"credit": "0.1"}, "credit"); ok || got != 0 {
		t.Errorf(`Float(credit="0.1") = %v, %v; want 0, false（未知不得冒充免费）`, got, ok)
	}
	for _, key := range []string{"i", "num", "f32", "nullv", "missing"} {
		if _, ok := Float(m, key); ok {
			t.Errorf("Float(%q) ok = true, want false（严格只认 float64）", key)
		}
	}
	if _, ok := Float(nil, "f"); ok {
		t.Error("Float(nil, f) ok = true, want false")
	}
}

func TestInt(t *testing.T) {
	m := sample()
	cases := []struct {
		key  string
		want int64
	}{
		{"f", 1},    // float64 截断
		{"i", 7},    // int
		{"i64", 9},  // int64
		{"f32", 3},  // float32
		{"num", 42}, // json.Number 可解析
	}
	for _, c := range cases {
		got, ok := Int(m, c.key)
		if !ok || got != c.want {
			t.Errorf("Int(%q) = %d, %v; want %d, true", c.key, got, ok, c.want)
		}
	}
	// json.Number("4.2") 走 Int64() 失败 → false（与旧 read() 闭包一致）
	for _, key := range []string{"badnum", "str", "obj", "nullv", "missing"} {
		if _, ok := Int(m, key); ok {
			t.Errorf("Int(%q) ok = true, want false", key)
		}
	}
	if _, ok := Int(nil, "i"); ok {
		t.Error("Int(nil, i) ok = true, want false")
	}
}
