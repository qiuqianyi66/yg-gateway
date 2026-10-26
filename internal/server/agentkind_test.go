package server

import "testing"

func TestClassifyAgentKind(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace", "   ", ""},
		{"claude-cli", "claude-cli/1.0.60 (external, cli)", "claude_code"},
		{"claude-code dash", "claude-code/0.2.0", "claude_code"},
		{"codex cli", "codex_cli_rs/0.1.20250101", "codex"},
		{"openai python", "OpenAI/Python 1.55.0", "openai_sdk"},
		{"openai node", "openai-node/4.0.0", "openai_sdk"},
		{"anthropic python", "anthropic-sdk-python/0.40.0", "anthropic_sdk"},
		{"anthropic dash", "anthropic-python/0.40.0", "anthropic_sdk"},
		{"browser", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36", "browser"},
		{"curl", "curl/8.4.0", "http_client"},
		{"python-requests", "python-requests/2.31.0", "http_client"},
		{"go-http-client", "Go-http-client/1.1", "http_client"},
		{"undici", "undici/6.19.0", "http_client"},
		{"unknown other", "MyCustomAgent/1.0", "other"},
		{"case insensitive", "CLAUDE-CLI/1.0", "claude_code"},
		{"trimmed", "  curl/8.0  ", "http_client"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyAgentKind(c.ua); got != c.want {
				t.Errorf("ClassifyAgentKind(%q) = %q want %q", c.ua, got, c.want)
			}
		})
	}
}
