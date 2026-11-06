package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
)

// --- OpenAI / Groq (chat completions) --------------------------------------

type oaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type oaTool struct {
	Type     string        `json:"type"`
	Function oaFunctionDef `json:"function"`
}

func (c *Client) callOpenAI(ctx context.Context, system, user string, tools []Tool) (*ToolResponse, error) {
	req := map[string]any{
		"model":       c.model,
		"messages":    []oaMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		"temperature": 0.1,
		"max_tokens":  16384,
	}
	if len(tools) > 0 {
		oaTools := make([]oaTool, 0, len(tools))
		for _, t := range tools {
			oaTools = append(oaTools, oaTool{Type: "function", Function: oaFunctionDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
		}
		req["tools"] = oaTools
		req["tool_choice"] = "auto"
	}
	raw, err := c.post(ctx, c.baseURL+"/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + c.apiKey}, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, domain.E(domain.KindLLM, "llm.openai", "decode response", err)
	}
	out := &ToolResponse{Raw: string(raw)}
	if len(resp.Choices) > 0 {
		out.Text = resp.Choices[0].Message.Content
		for _, tc := range resp.Choices[0].Message.ToolCalls {
			args := map[string]any{}
			if tc.Function.Arguments != "" {
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{Name: tc.Function.Name, Arguments: args})
		}
	}
	return out, nil
}

// --- Anthropic (messages) --------------------------------------------------

func (c *Client) callAnthropic(ctx context.Context, system, user string, tools []Tool) (*ToolResponse, error) {
	req := map[string]any{
		"model":       c.model,
		"system":      system,
		"messages":    []map[string]any{{"role": "user", "content": user}},
		"max_tokens":  16384,
		"temperature": 0.1,
	}
	if len(tools) > 0 {
		anthTools := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			anthTools = append(anthTools, map[string]any{
				"name": t.Name, "description": t.Description, "input_schema": t.Parameters,
			})
		}
		req["tools"] = anthTools
	}
	raw, err := c.post(ctx, c.baseURL+"/v1/messages",
		map[string]string{"x-api-key": c.apiKey, "anthropic-version": "2023-06-01"}, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, domain.E(domain.KindLLM, "llm.anthropic", "decode response", err)
	}
	out := &ToolResponse{Raw: string(raw)}
	var text strings.Builder
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{Name: block.Name, Arguments: block.Input})
		}
	}
	out.Text = text.String()
	return out, nil
}

// --- Gemini (generateContent) ----------------------------------------------

func (c *Client) callGemini(ctx context.Context, system, user string, tools []Tool) (*ToolResponse, error) {
	genCfg := map[string]any{"temperature": 0.1, "maxOutputTokens": 65536}
	if c.jsonMode && len(tools) == 0 {
		genCfg["responseMimeType"] = "application/json"
	}
	req := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]any{{"text": system}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]any{{"text": user}}}},
		"generationConfig":  genCfg,
	}
	if len(tools) > 0 {
		decls := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			decls = append(decls, map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters})
		}
		req["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	u := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", c.baseURL, url.PathEscape(c.model), url.QueryEscape(c.apiKey))
	raw, err := c.post(ctx, u, nil, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text"`
					FunctionCall *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, domain.E(domain.KindLLM, "llm.gemini", "decode response", err)
	}
	out := &ToolResponse{Raw: string(raw)}
	var text strings.Builder
	if len(resp.Candidates) > 0 {
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.Text != "" {
				text.WriteString(part.Text)
			}
			if part.FunctionCall != nil {
				out.ToolCalls = append(out.ToolCalls, ToolCall{Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args})
			}
		}
	}
	out.Text = text.String()
	return out, nil
}

// --- Ollama (chat) ---------------------------------------------------------

func (c *Client) callOllama(ctx context.Context, system, user string, tools []Tool) (*ToolResponse, error) {
	req := map[string]any{
		"model":    c.model,
		"messages": []oaMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
		"stream":   false,
		"options":  map[string]any{"temperature": 0.1},
	}
	if len(tools) > 0 {
		oTools := make([]oaTool, 0, len(tools))
		for _, t := range tools {
			oTools = append(oTools, oaTool{Type: "function", Function: oaFunctionDef{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
		}
		req["tools"] = oTools
	}
	raw, err := c.post(ctx, c.baseURL+"/api/chat", nil, req)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, domain.E(domain.KindLLM, "llm.ollama", "decode response", err)
	}
	out := &ToolResponse{Raw: string(raw), Text: resp.Message.Content}
	for _, tc := range resp.Message.ToolCalls {
		args := map[string]any{}
		if len(tc.Function.Arguments) > 0 {
			// Ollama may return arguments as an object or a JSON string.
			if err := json.Unmarshal(tc.Function.Arguments, &args); err != nil {
				var s string
				if json.Unmarshal(tc.Function.Arguments, &s) == nil {
					_ = json.Unmarshal([]byte(s), &args)
				}
			}
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{Name: tc.Function.Name, Arguments: args})
	}
	return out, nil
}
