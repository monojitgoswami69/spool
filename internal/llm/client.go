// Package llm is a zero-SDK net/http client for Gemini, OpenAI, Anthropic,
// Groq, and Ollama. It supports plain completion and native tool calling.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/domain"
)

const (
	defaultTimeout = 180 * time.Second
	maxAttempts    = 3
)

// Config configures a Client.
type Config struct {
	Provider   string
	Model      string
	APIKey     string
	BaseURL    string
	Timeout    time.Duration
	JSONMode   bool
	HTTPClient *http.Client
}

// Tool is a schema-validated capability exposed to the model.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResponse is the model's reply, possibly containing tool calls.
type ToolResponse struct {
	Text      string     `json:"text"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Raw       string     `json:"raw,omitempty"`
}

// Client is a provider-agnostic LLM client.
type Client struct {
	provider string
	model    string
	apiKey   string
	baseURL  string
	jsonMode bool
	http     *http.Client
}

// New validates cfg and returns a client.
func New(cfg Config) (*Client, error) {
	provider := config.NormalizeProvider(cfg.Provider)
	if provider == "" {
		return nil, domain.E(domain.KindConfig, "llm.New", "provider is empty", domain.ErrNotConfigured)
	}
	switch provider {
	case "gemini", "openai", "anthropic", "groq", "ollama":
	default:
		return nil, domain.E(domain.KindConfig, "llm.New", "unsupported provider "+provider, nil)
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = config.DefaultModel(provider)
	}
	if provider != "ollama" && strings.TrimSpace(cfg.APIKey) == "" {
		return nil, domain.E(domain.KindConfig, "llm.New", "missing API key for "+provider, domain.ErrNotConfigured)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		hc = &http.Client{Timeout: timeout}
	}
	base := strings.TrimSuffix(cfg.BaseURL, "/")
	if base == "" {
		switch provider {
		case "openai":
			base = "https://api.openai.com"
		case "groq":
			base = "https://api.groq.com/openai"
		case "anthropic":
			base = "https://api.anthropic.com"
		case "gemini":
			base = "https://generativelanguage.googleapis.com"
		case "ollama":
			base = "http://localhost:11434"
		}
	}
	return &Client{
		provider: provider, model: cfg.Model, apiKey: cfg.APIKey,
		baseURL: base, jsonMode: cfg.JSONMode, http: hc,
	}, nil
}

// NewFromResolved builds a client from config.Resolve output.
func NewFromResolved(r *config.Resolved, jsonMode bool) (*Client, error) {
	if r == nil {
		return nil, domain.E(domain.KindConfig, "llm.NewFromResolved", "nil resolved config", domain.ErrNotConfigured)
	}
	return New(Config{Provider: r.Provider, Model: r.Model, APIKey: r.APIKey, BaseURL: r.BaseURL, JSONMode: jsonMode})
}

// Provider returns the normalized provider.
func (c *Client) Provider() string { return c.provider }

// Model returns the model name.
func (c *Client) Model() string { return c.model }

// Call performs a plain text completion.
func (c *Client) Call(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	resp, err := c.CallTools(ctx, systemPrompt, userPrompt, nil)
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

// CallTools performs a completion with optional native tools.
func (c *Client) CallTools(ctx context.Context, systemPrompt, userPrompt string, tools []Tool) (*ToolResponse, error) {
	switch c.provider {
	case "openai", "groq":
		return c.callOpenAI(ctx, systemPrompt, userPrompt, tools)
	case "anthropic":
		return c.callAnthropic(ctx, systemPrompt, userPrompt, tools)
	case "gemini":
		return c.callGemini(ctx, systemPrompt, userPrompt, tools)
	case "ollama":
		return c.callOllama(ctx, systemPrompt, userPrompt, tools)
	default:
		return nil, domain.E(domain.KindLLM, "llm.CallTools", "unsupported provider "+c.provider, nil)
	}
}

func (c *Client) post(ctx context.Context, url string, headers map[string]string, body any) ([]byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, domain.E(domain.KindLLM, "llm.post", "marshal request", err)
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return nil, domain.E(domain.KindLLM, "llm.post", "build request", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, domain.E(domain.KindCancelled, "llm.post", "request cancelled", err)
			}
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, domain.E(domain.KindLLM, "llm.post", "read response", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return raw, nil
		}
		lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(raw), 400))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
			continue
		}
		return nil, domain.E(domain.KindLLM, "llm.post", fmt.Sprintf("provider returned HTTP %d", resp.StatusCode), lastErr)
	}
	return nil, domain.E(domain.KindLLM, "llm.post", "request failed after retries", lastErr)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
