package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/healer"
	"github.com/monojitgoswami69/spool/internal/llm"
	"github.com/monojitgoswami69/spool/internal/portprobe"
	"github.com/monojitgoswami69/spool/internal/safefs"
	"github.com/monojitgoswami69/spool/internal/tunnel"
)

// State is the agent's coarse lifecycle state.
type State string

const (
	StateIdle      State = "idle"
	StatePlanning  State = "planning"
	StateActing    State = "acting"
	StateObserving State = "observing"
	StateDone      State = "done"
	StateFailed    State = "failed"
)

// Budget bounds an agent run.
type Budget struct {
	MaxIterations int
	MaxToolCalls  int
	MaxBytesRead  int
	MaxDuration   time.Duration
}

// DefaultBudget returns the production defaults.
func DefaultBudget() Budget {
	return Budget{MaxIterations: 10, MaxToolCalls: 20, MaxBytesRead: 256 * 1024, MaxDuration: 5 * time.Minute}
}

// Event is emitted for UI streaming.
type Event struct {
	Type    string         `json:"type"`
	State   State          `json:"state,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Message string         `json:"message,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Success bool           `json:"success,omitempty"`
	Bytes   int            `json:"bytes,omitempty"`
}

// EnvAccess exposes project environment state to intent dispatch.
type EnvAccess interface {
	List() (map[string]string, error)
	Set(key, value string) error
	Unset(key string) error
	Path() string
}

// Options configures an Agent.
type Options struct {
	LLM          domain.LLMClient
	Budget       Budget
	AutoApprove  bool
	Confirm      func(tool string, destructive bool, args map[string]any) bool
	EventHandler func(Event)

	RepoJail  *safefs.Jail
	WorkJail  *safefs.Jail
	Project   *domain.Project
	Compose   domain.ComposeController
	Healer    *healer.Healer
	Allocator *portprobe.Allocator
	RepoRoot  string
	WorkDir   string
	Env       EnvAccess
}

// Result is the outcome of a run.
type Result struct {
	Final      string  `json:"final"`
	State      State   `json:"state"`
	Iterations int     `json:"iterations"`
	ToolCalls  int     `json:"tool_calls"`
	BytesRead  int     `json:"bytes_read"`
	Intent     *Intent `json:"intent,omitempty"`
	Events     []Event `json:"-"`
}

// Agent is the bounded tool-using harness.
type Agent struct {
	opts     Options
	registry *Registry
	state    State
	events   []Event
	tunnel   *tunnel.Tunnel
	bytes    int
}

// New constructs an agent and registers the default tool set.
func New(opts Options) (*Agent, error) {
	if opts.Budget.MaxIterations == 0 {
		opts.Budget = DefaultBudget()
	}
	a := &Agent{opts: opts, registry: NewRegistry(), state: StateIdle}
	if err := a.registerDefaultTools(); err != nil {
		return nil, err
	}
	return a, nil
}

// Registry exposes the tool registry (for tests and docs).
func (a *Agent) Registry() *Registry { return a.registry }

// Register adds an extra tool.
func (a *Agent) Register(t Tool) error { return a.registry.Register(t) }

// State returns the current lifecycle state.
func (a *Agent) State() State { return a.state }

// Close releases the tunnel if one is running.
func (a *Agent) Close() error {
	if a.tunnel != nil {
		err := a.tunnel.Close()
		a.tunnel = nil
		return err
	}
	return nil
}

func (a *Agent) emit(e Event) {
	e.State = a.state
	a.events = append(a.events, e)
	if a.opts.EventHandler != nil {
		a.opts.EventHandler(e)
	}
}

func (a *Agent) setState(s State, msg string) {
	a.state = s
	a.emit(Event{Type: "state", State: s, Message: msg})
}

type agentResponse struct {
	Thinking  string `json:"thinking"`
	ToolCalls []struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"tool_calls"`
	Final     string `json:"final"`
	Done      bool   `json:"done"`
	Unfixable bool   `json:"unfixable"`
}

// Run executes the planning/acting/observing loop until the model finalizes or
// the budget is exhausted.
func (a *Agent) Run(ctx context.Context, systemPrompt, userPrompt string) (*Result, error) {
	if a.opts.LLM == nil {
		return nil, domain.E(domain.KindConfig, "agent.Run", "nil LLM", domain.ErrNotConfigured)
	}
	start := time.Now()
	res := &Result{State: StatePlanning}
	a.setState(StatePlanning, "understanding the request")

	transcript := []string{"USER REQUEST:\n" + userPrompt}

	for iter := 1; iter <= a.opts.Budget.MaxIterations; iter++ {
		if time.Since(start) > a.opts.Budget.MaxDuration {
			a.setState(StateFailed, "time budget exceeded")
			return res, domain.E(domain.KindBudget, "agent.Run", "time budget exceeded", domain.ErrBudgetExceeded)
		}
		res.Iterations = iter
		a.setState(StatePlanning, fmt.Sprintf("planning iteration %d", iter))

		prompt := a.buildPrompt(transcript)
		raw, err := a.opts.LLM.Call(ctx, systemPrompt, prompt)
		if err != nil {
			a.setState(StateFailed, "LLM call failed")
			return res, err
		}
		resp := parseAgentResponse(raw)
		if resp.Thinking != "" {
			a.emit(Event{Type: "thinking", Message: resp.Thinking})
		}

		if len(resp.ToolCalls) == 0 {
			res.Final = strings.TrimSpace(resp.Final)
			if res.Final == "" {
				res.Final = strings.TrimSpace(raw)
			}
			res.State = StateDone
			a.setState(StateDone, "finished")
			a.emit(Event{Type: "final", Message: res.Final})
			res.Events = append([]Event{}, a.events...)
			res.ToolCalls = a.toolCallCount()
			res.BytesRead = a.bytes
			return res, nil
		}

		a.setState(StateActing, "executing tools")
		for _, call := range resp.ToolCalls {
			if a.toolCallCount() >= a.opts.Budget.MaxToolCalls {
				a.setState(StateFailed, "tool-call budget exceeded")
				return res, domain.E(domain.KindBudget, "agent.Run", "tool-call budget exceeded", domain.ErrBudgetExceeded)
			}
			out, cerr := a.invoke(ctx, call.Name, call.Arguments)
			transcript = append(transcript, renderToolResult(call.Name, call.Arguments, out, cerr))
		}
		a.setState(StateObserving, "reviewing tool output")
	}

	a.setState(StateFailed, "iteration budget exceeded")
	return res, domain.E(domain.KindBudget, "agent.Run", "iteration budget exceeded", domain.ErrBudgetExceeded)
}

func (a *Agent) toolCallCount() int {
	n := 0
	for _, e := range a.events {
		if e.Type == "tool_start" {
			n++
		}
	}
	return n
}

func (a *Agent) buildPrompt(transcript []string) string {
	tools := a.registry.LLMTools()
	toolJSON, _ := json.MarshalIndent(tools, "", "  ")
	body := strings.Join(transcript, "\n\n")
	if len(body) > 64*1024 {
		body = body[:64*1024] + "\n...transcript truncated..."
	}
	return fmt.Sprintf(`AVAILABLE TOOLS (JSON schema):
%s

TRANSCRIPT:
%s

Respond with a single JSON object:
{"thinking":"short reasoning","tool_calls":[{"name":"tool_name","arguments":{}}],"final":"answer when done","done":false,"unfixable":false}
Use at most one or two tools per turn. When you have finished, set final and omit tool_calls.`, string(toolJSON), body)
}

func parseAgentResponse(raw string) agentResponse {
	body := llm.ExtractJSON(raw)
	if body == "" {
		return agentResponse{Final: strings.TrimSpace(raw)}
	}
	var resp agentResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return agentResponse{Final: strings.TrimSpace(raw)}
	}
	return resp
}

func renderToolResult(name string, args map[string]any, out string, err error) string {
	argJSON, _ := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("TOOL %s %s FAILED: %v", name, argJSON, err)
	}
	if len(out) > 8000 {
		out = out[:8000] + "\n...truncated..."
	}
	return fmt.Sprintf("TOOL %s %s RESULT:\n%s", name, argJSON, out)
}

func (a *Agent) invoke(ctx context.Context, name string, args map[string]any) (string, error) {
	tool, ok := a.registry.Get(name)
	if !ok {
		a.emit(Event{Type: "tool_end", Tool: name, Message: "unknown tool", Success: false})
		return "", domain.E(domain.KindValidation, "agent.invoke", "unknown tool "+name, nil)
	}
	if args == nil {
		args = map[string]any{}
	}
	destructive := tool.Destructive
	if tool.Name == "orchestrate_stack" && strings.EqualFold(argString(args, "action"), "nuke") {
		destructive = true
	}
	if tool.Mutating || destructive {
		approved := a.opts.AutoApprove
		if !approved && a.opts.Confirm != nil {
			approved = a.opts.Confirm(tool.Name, destructive, args)
		}
		if !approved {
			a.emit(Event{Type: "permission_denied", Tool: tool.Name, Args: args, Message: "mutating tool denied"})
			return "", domain.E(domain.KindValidation, "agent.invoke", "permission denied for mutating tool "+tool.Name, nil)
		}
	}

	a.emit(Event{Type: "tool_start", Tool: tool.Name, Args: args})
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := tool.Handler(callCtx, args)
	if err != nil {
		a.emit(Event{Type: "tool_end", Tool: tool.Name, Args: args, Success: false, Message: err.Error()})
		return "", err
	}

	// Enforce the byte budget across all tool output.
	remaining := a.opts.Budget.MaxBytesRead - a.bytes
	if remaining <= 0 {
		a.emit(Event{Type: "tool_end", Tool: tool.Name, Args: args, Success: false, Message: "byte budget exhausted"})
		return "", domain.E(domain.KindBudget, "agent.invoke", "byte budget exhausted", domain.ErrBudgetExceeded)
	}
	if len(out) > remaining {
		out = out[:remaining] + "\n...byte budget truncated..."
	}
	a.bytes += len(out)
	a.emit(Event{Type: "tool_end", Tool: tool.Name, Args: args, Success: true, Bytes: len(out), Message: summarize(out)})
	return out, nil
}

func summarize(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// SystemPrompt is the standing instruction for the Spool agent.
const SystemPrompt = `You are Spool, an autonomous infrastructure control-plane agent.
Before acting, restate the user's intent in one sentence and identify the target project/service.
If the request is ambiguous, ask exactly one clarifying question and do not act.
You inspect repositories, infer backing services, synthesize container manifests, orchestrate
stacks, diagnose failures, and heal them. Prefer concrete tools over speculation. Never claim a
stack is healthy without checking. Only read files through read_repo_file (paths are jailed).
Mutating and destructive tools may be denied; if denied, explain and continue safely.`

// helper for tools that need a string argument.
func argString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return def
}
