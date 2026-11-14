package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestExampleConfigKeysRecognized 守卫 config.example.json 与 Config struct 的契约。
//
// encoding/json 对未知字段**静默忽略**（本项目没开 DisallowUnknownFields），
// 所以示例里写错一个键名（比如 min_request_interval 写成 min_request_intervals）
// 会「解析成功但不生效」，用户改了配置却没有效果、也没有任何报错。
// 这里做静态比对：示例出现的每个键都必须是某个 struct 字段的 json tag。
func TestExampleConfigKeysRecognized(t *testing.T) {
	raw, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatalf("读 config.example.json 失败: %v", err)
	}
	var ex map[string]any
	if err := json.Unmarshal(raw, &ex); err != nil {
		t.Fatalf("config.example.json 不是合法 JSON: %v", err)
	}
	// 先确认示例确实能被主解析器接受（语法/类型层面）。
	if _, err := ParseConfig(raw); err != nil {
		t.Fatalf("ParseConfig 拒绝 config.example.json: %v", err)
	}

	tags := map[string]bool{}
	collectJSONTags(reflect.TypeOf(Config{}), map[reflect.Type]bool{}, tags)

	// model_aliases 是自由键值对（键=用户自定义模型别名），不参与字段契约，整棵子树剔除。
	delete(ex, "model_aliases")

	keys := map[string]bool{}
	collectExampleKeys(ex, keys)

	for k := range keys {
		if tags[k] {
			continue
		}
		if isFreeFormKey(k) {
			continue
		}
		t.Errorf("config.example.json 的键 %q 不对应任何 struct json tag（拼错会被静默忽略）", k)
	}
}

// isFreeFormKey 判断是否是「用户自定义键名」的位置（模型别名/价格表用的模型名）。
func isFreeFormKey(k string) bool {
	return strings.HasPrefix(k, "glm") || strings.HasPrefix(k, "deepseek") ||
		strings.HasPrefix(k, "kimi") || strings.HasPrefix(k, "minimax") ||
		strings.HasPrefix(k, "step") || strings.HasPrefix(k, "hy") ||
		strings.HasPrefix(k, "doubao")
}

func collectJSONTags(t reflect.Type, seen map[reflect.Type]bool, out map[string]bool) {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice ||
		t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
		collectJSONTags(f.Type, seen, out)
	}
}

func collectExampleKeys(v any, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, cv := range x {
			out[k] = true
			collectExampleKeys(cv, out)
		}
	case []any:
		for _, e := range x {
			collectExampleKeys(e, out)
		}
	}
}
