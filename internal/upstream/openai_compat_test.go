package upstream

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAICompatChatCompletionsURL(t *testing.T) {
	e := OpenAICompatEndpoint{BaseURL: "https://api.deepseek.com/v1/", APIKey: "sk-x"}
	got := e.ChatCompletionsURL()
	want := "https://api.deepseek.com/v1/chat/completions"
	if got != want {
		t.Fatalf("ChatCompletionsURL=%q want %q", got, want)
	}
}

func TestOpenAICompatNewChatRequest(t *testing.T) {
	e := OpenAICompatEndpoint{BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-test"}
	req, err := e.NewChatRequest(context.Background(), strings.NewReader(`{}`), "text/event-stream")
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("url=%s", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("auth=%q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("ct=%q", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("accept=%q", req.Header.Get("Accept"))
	}
	if req.Method != http.MethodPost {
		t.Fatalf("method=%s", req.Method)
	}
}
