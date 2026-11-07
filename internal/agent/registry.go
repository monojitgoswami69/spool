// Package agent implements Spool's conversational agent harness: a typed tool
// registry, a bounded state machine, and blast-radius guardrails. Every
// filesystem capability is jailed and every mutating action is gated.
package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/monojitgoswami69/spool/internal/llm"
)

// Tool is a schema-validated capability.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Mutating    bool
	Destructive bool
	// Timeout bounds a single tool invocation. Zero means the default (3m).
	Timeout time.Duration
	Handler func(ctx context.Context, args map[string]any) (string, error)
}

// Registry holds tools by name.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool, rejecting duplicates and invalid definitions.
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return fmt.Errorf("agent: tool name is empty")
	}
	if t.Handler == nil {
		return fmt.Errorf("agent: tool %q has no handler", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("agent: tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns tools in registration order.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out
}

// LLMTools returns the registry projected into llm.Tool values.
func (r *Registry) LLMTools() []llm.Tool {
	list := r.List()
	out := make([]llm.Tool, 0, len(list))
	for _, t := range list {
		out = append(out, llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	return out
}

// Names returns the sorted tool names (useful for docs/tests).
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	sort.Strings(out)
	return out
}
