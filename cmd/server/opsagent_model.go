// opsagent_model.go 运维 agent 的模型适配器：只调本机 /v1/chat/completions。
//
// 不走 internal/upstream（R6：出站集中），也不引新依赖。默认模型为空 =
// 装配侧根本不构造 runtime；这里只负责 wire 形状转换与错误收敛。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"yg-gateway/internal/opsagent"
)

type gatewayRuntimeModel struct {
	endpoint string
	apiKey   string
	model    string
	client   *http.Client
}

func newGatewayRuntimeModel(cfg *Config, model string) *gatewayRuntimeModel {
	return &gatewayRuntimeModel{
		endpoint: localGatewayEndpoint(cfg.Listen),
		apiKey:   cfg.APIKey,
		model:    model,
		client:   &http.Client{},
	}
}

func localGatewayEndpoint(listen string) string {
	s := strings.TrimSpace(listen)
	if s == "" {
		s = ":7863"
	}
	if strings.Contains(s, "://") {
		return strings.TrimRight(s, "/")
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "http://" + strings.TrimRight(s, "/")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

type gatewayWireMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []gatewayWireTool `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
}

type gatewayWireTool struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function gatewayWireFunction `json:"function"`
}

type gatewayWireToolDef struct {
	Type     string              `json:"type"`
	Function gatewayWireFunction `json:"function"`
}

type gatewayWireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type gatewayWireRequest struct {
	Model     string               `json:"model"`
	Messages  []gatewayWireMessage `json:"messages"`
	Tools     []gatewayWireToolDef `json:"tools,omitempty"`
	MaxTokens int                  `json:"max_tokens,omitempty"`
	Stream    bool                 `json:"stream"`
}

type gatewayWireResponse struct {
	Choices []struct {
		Message struct {
			Content   string            `json:"content"`
			ToolCalls []gatewayWireTool `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (m *gatewayRuntimeModel) Complete(ctx context.Context, req opsagent.RuntimeModelRequest) (opsagent.RuntimeModelResponse, error) {
	model := req.Model
	if model == "" {
		model = m.model
	}
	if model == "" {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: 未配置模型")
	}
	wire := gatewayWireRequest{Model: model, Stream: false, MaxTokens: req.MaxTokens}
	for _, msg := range req.Messages {
		wm := gatewayWireMessage{Role: msg.Role, Content: msg.Content, ToolCallID: msg.ToolCallID, Name: msg.Name}
		for _, tc := range msg.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, gatewayWireTool{
				ID: tc.ID, Type: "function",
				Function: gatewayWireFunction{Name: tc.Name, Arguments: rawArgsString(tc.Arguments)},
			})
		}
		wire.Messages = append(wire.Messages, wm)
	}
	for _, tool := range req.Tools {
		wire.Tools = append(wire.Tools, gatewayWireToolDef{
			Type:     "function",
			Function: gatewayWireFunction{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters},
		})
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+m.apiKey)
	httpReq.Header.Set("User-Agent", "yg-gateway-opsagent/1.0")
	resp, err := m.client.Do(httpReq)
	if err != nil {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: do: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: HTTP %d: %s", resp.StatusCode, truncateString(string(raw), 512))
	}
	var out gatewayWireResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: decode: %w", err)
	}
	if len(out.Choices) == 0 {
		return opsagent.RuntimeModelResponse{}, fmt.Errorf("opsagent model: 无 choices")
	}
	choice := out.Choices[0]
	r := opsagent.RuntimeModelResponse{
		Content: choice.Message.Content, FinishReason: choice.FinishReason,
		Usage: opsagent.RuntimeUsage{PromptTokens: out.Usage.PromptTokens, CompletionTokens: out.Usage.CompletionTokens},
	}
	for i, tc := range choice.Message.ToolCalls {
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call-%d", i)
		}
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		r.ToolCalls = append(r.ToolCalls, opsagent.RuntimeToolCall{
			ID: id, Name: tc.Function.Name, Arguments: json.RawMessage(args),
		})
	}
	return r, nil
}

func rawArgsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
