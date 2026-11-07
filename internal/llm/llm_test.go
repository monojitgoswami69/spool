package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`{"a":1}`, `{"a":1}`},
		{"prose before {\"a\": {\"b\": 2}} trailing", `{"a": {"b": 2}}`},
		{"```json\n{\"x\":\"y\"}\n```", `{"x":"y"}`},
		{`no json here`, ``},
	}
	for _, c := range cases {
		if got := ExtractJSON(c.in); got != c.want {
			t.Errorf("ExtractJSON(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestCleanContent(t *testing.T) {
	if got := CleanContent("```\nhello\n```"); got != "hello\n" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenAICall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"hello"}}]}`)
	}))
	defer srv.Close()

	c, err := New(Config{Provider: "openai", Model: "gpt", APIKey: "key", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Call(context.Background(), "sys", "user")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenAIToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["tools"] == nil {
			t.Error("expected tools in request")
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"function":{"name":"read_repo_file","arguments":"{\"path\":\"a.txt\"}"}}]}}]}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Provider: "groq", Model: "m", APIKey: "k", BaseURL: srv.URL})
	resp, err := c.CallTools(context.Background(), "s", "u", []Tool{{Name: "read_repo_file", Description: "read", Parameters: map[string]any{"type": "object"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_repo_file" {
		t.Fatalf("unexpected %+v", resp)
	}
	if resp.ToolCalls[0].Arguments["path"] != "a.txt" {
		t.Fatalf("args = %+v", resp.ToolCalls[0].Arguments)
	}
}

func TestAnthropicCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic headers")
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"part1"},{"type":"tool_use","name":"scan_repository","input":{"path":"."}}]}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Provider: "anthropic", Model: "claude", APIKey: "key", BaseURL: srv.URL})
	resp, err := c.CallTools(context.Background(), "s", "u", []Tool{{Name: "scan_repository"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "part1" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "scan_repository" {
		t.Fatalf("unexpected %+v", resp)
	}
}

func TestGeminiCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v1beta/models/") || r.URL.Query().Get("key") != "key" {
			t.Errorf("bad url %s", r.URL.String())
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gc, _ := body["generationConfig"].(map[string]any)
		if gc["responseMimeType"] != "application/json" {
			t.Error("expected JSON mode")
		}
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"gem"},{"functionCall":{"name":"explain_topology","args":{"project":"x"}}}]}}]}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Provider: "gemini", Model: "gemini-3.5-flash-lite", APIKey: "key", BaseURL: srv.URL, JSONMode: true})
	resp, err := c.CallTools(context.Background(), "s", "u", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "gem" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "explain_topology" {
		t.Fatalf("unexpected %+v", resp)
	}
}

func TestOllamaCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"message":{"content":"local","tool_calls":[{"function":{"name":"heal_service","arguments":{"service":"api"}}}]}}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Provider: "ollama", Model: "llama3.1", BaseURL: srv.URL})
	resp, err := c.CallTools(context.Background(), "s", "u", []Tool{{Name: "heal_service"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "local" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments["service"] != "api" {
		t.Fatalf("unexpected %+v", resp)
	}
}

func TestProviderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"bad"}`)
	}))
	defer srv.Close()
	c, _ := New(Config{Provider: "openai", Model: "m", APIKey: "k", BaseURL: srv.URL})
	if _, err := c.Call(context.Background(), "s", "u"); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{Provider: "nope"}); err == nil {
		t.Fatal("expected unsupported provider error")
	}
	if _, err := New(Config{Provider: "openai", APIKey: ""}); err == nil {
		t.Fatal("expected missing key error")
	}
	if _, err := New(Config{Provider: "ollama", Model: "llama"}); err != nil {
		t.Fatalf("ollama should not require a key: %v", err)
	}
}
