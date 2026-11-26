// opsagent_model_test.go 模型适配器的 wire 契约（httptest，零外呼）。
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yg-gateway/internal/opsagent"
)

func TestGatewayRuntimeModelComplete(t *testing.T) {
	var got gatewayWireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"看状态","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"gateway_status","arguments":"{}"}}
			]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}
		}`))
	}))
	defer srv.Close()

	m := newGatewayRuntimeModel(&Config{Listen: strings.TrimPrefix(srv.URL, "http://"), APIKey: "test-key"}, "fallback-model")
	resp, err := m.Complete(context.Background(), opsagent.RuntimeModelRequest{
		Model: "m1", MaxTokens: 100,
		Messages: []opsagent.RuntimeMessage{{Role: "user", Content: "巡检"}},
		Tools:    []opsagent.RuntimeToolSchema{{Name: "gateway_status", Description: "状态", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Model != "m1" || got.Stream || len(got.Messages) != 1 || len(got.Tools) != 1 {
		t.Fatalf("wire=%#v", got)
	}
	if got.Tools[0].Function.Parameters == nil {
		t.Fatalf("tool parameters 未传: %#v", got.Tools[0])
	}
	if resp.Content != "看状态" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "gateway_status" {
		t.Fatalf("resp=%#v", resp)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Fatalf("usage=%#v", resp.Usage)
	}
}

func TestGatewayRuntimeModelHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream 503", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	m := newGatewayRuntimeModel(&Config{Listen: strings.TrimPrefix(srv.URL, "http://"), APIKey: "k"}, "m")
	_, err := m.Complete(context.Background(), opsagent.RuntimeModelRequest{Messages: []opsagent.RuntimeMessage{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err=%v", err)
	}
}

func TestLocalGatewayEndpoint(t *testing.T) {
	cases := map[string]string{
		":7863":          "http://127.0.0.1:7863",
		"0.0.0.0:7863":   "http://127.0.0.1:7863",
		"127.0.0.1:7863": "http://127.0.0.1:7863",
		"https://x.test": "https://x.test",
	}
	for in, want := range cases {
		if got := localGatewayEndpoint(in); got != want {
			t.Fatalf("localGatewayEndpoint(%q)=%q, want %q", in, got, want)
		}
	}
}
