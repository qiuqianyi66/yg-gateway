package main

import (
	"encoding/json"
	"testing"
)

// T7：配置保存回归（ADR-001 §七 第 9 项）。
// 面板保存其他配置时，api_keys 必须原样保留——这是「mergeConfigMaps 深合并 +
// CFG_MAP 白名单不含 api_keys」两个机制共同保证的。这里直接测 mergeConfigMaps
// 的深合并行为，钉住「incoming 不含 api_keys 时，cur 的 api_keys 不被覆盖」。

func TestMergeConfigMapsPreservesAPIKeys(t *testing.T) {
	cur := map[string]any{}
	json.Unmarshal([]byte(`{
		"api_key": "old-single",
		"api_keys": [{"id":"k1","key":"sk-a","name":"甲","models":["m1"],"realm":"cn"}],
		"listen": ":7863"
	}`), &cur)

	// 面板提交的 incoming：只改 listen，不含 api_keys（CFG_MAP 白名单里没有它）。
	incoming := map[string]any{}
	json.Unmarshal([]byte(`{"listen":":9999"}`), &incoming)

	merged := mergeConfigMaps(cur, incoming)

	// api_keys 必须原样保留
	keys, ok := merged["api_keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("api_keys 应原样保留，got %#v", merged["api_keys"])
	}
	k0 := keys[0].(map[string]any)
	if k0["id"] != "k1" || k0["key"] != "sk-a" {
		t.Fatalf("api_keys[0] 内容被改，got %#v", k0)
	}
	// listen 被覆盖
	if merged["listen"] != ":9999" {
		t.Fatalf("listen 应被覆盖为 :9999，got %#v", merged["listen"])
	}
	// api_key 单值也保留（incoming 不含它）
	if merged["api_key"] != "old-single" {
		t.Fatalf("api_key 应保留，got %#v", merged["api_key"])
	}
}

// TestMergeConfigMapsUpdatesAPIKeysWhenSubmitted 若 incoming 显式带 api_keys（未来面板
// 管理页提交时），应整体覆盖（深合并对数组是替换语义）。
func TestMergeConfigMapsUpdatesAPIKeysWhenSubmitted(t *testing.T) {
	cur := map[string]any{}
	json.Unmarshal([]byte(`{"api_keys":[{"id":"k1","key":"sk-a"}]}`), &cur)
	incoming := map[string]any{}
	json.Unmarshal([]byte(`{"api_keys":[{"id":"k2","key":"sk-b"}]}`), &incoming)

	merged := mergeConfigMaps(cur, incoming)
	keys := merged["api_keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["id"] != "k2" {
		t.Fatalf("incoming 显式带 api_keys 应整体覆盖，got %#v", merged["api_keys"])
	}
}
