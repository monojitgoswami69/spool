package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/safefs"
)

type scriptedLLM struct {
	responses []string
	i         int
	calls     int
}

func (s *scriptedLLM) Call(ctx context.Context, system, user string) (string, error) {
	s.calls++
	if s.i >= len(s.responses) {
		return `{"final":"done"}`, nil
	}
	r := s.responses[s.i]
	s.i++
	return r, nil
}
func (s *scriptedLLM) Provider() string { return "fake" }
func (s *scriptedLLM) Model() string    { return "fake" }

func TestRegistryValidation(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{Name: "", Handler: func(context.Context, map[string]any) (string, error) { return "", nil }}); err == nil {
		t.Fatal("expected empty-name rejection")
	}
	if err := r.Register(Tool{Name: "x"}); err == nil {
		t.Fatal("expected nil-handler rejection")
	}
	ok := Tool{Name: "x", Description: "d", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }}
	if err := r.Register(ok); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ok); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	if got := len(r.LLMTools()); got != 1 {
		t.Fatalf("LLMTools = %d", got)
	}
}

func TestAgentRunExecutesToolThenFinalizes(t *testing.T) {
	called := 0
	llm := &scriptedLLM{responses: []string{
		`{"thinking":"use echo","tool_calls":[{"name":"echo","arguments":{"text":"hi"}}]}`,
		`{"final":"all done"}`,
	}}
	a, err := New(Options{LLM: llm, Budget: DefaultBudget(), AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Register(Tool{
		Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			called++
			return argString(args, "text"), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "sys", "say hi")
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 {
		t.Fatalf("expected tool called once, got %d", called)
	}
	if res.Final != "all done" || res.State != StateDone {
		t.Fatalf("unexpected result %+v", res)
	}
	found := false
	for _, e := range res.Events {
		if e.Type == "tool_start" && e.Tool == "echo" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected tool_start event")
	}
}

func TestMutatingToolDeniedWithoutConfirmation(t *testing.T) {
	called := false
	llm := &scriptedLLM{responses: []string{
		`{"tool_calls":[{"name":"danger","arguments":{}}]}`,
		`{"final":"stopped"}`,
	}}
	a, _ := New(Options{LLM: llm, Budget: DefaultBudget(), AutoApprove: false})
	_ = a.Register(Tool{
		Name: "danger", Description: "mutates", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			called = true
			return "done", nil
		},
	})
	if _, err := a.Run(context.Background(), "sys", "do it"); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("mutating tool must not run without confirmation")
	}
}

func TestMutatingToolConfirmed(t *testing.T) {
	called := false
	llm := &scriptedLLM{responses: []string{
		`{"tool_calls":[{"name":"danger","arguments":{}}]}`,
		`{"final":"ok"}`,
	}}
	confirmed := false
	a, _ := New(Options{LLM: llm, Budget: DefaultBudget(), Confirm: func(tool string, destructive bool, args map[string]any) bool {
		confirmed = true
		return true
	}})
	_ = a.Register(Tool{
		Name: "danger", Description: "mutates", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			called = true
			return "done", nil
		},
	})
	if _, err := a.Run(context.Background(), "sys", "do it"); err != nil {
		t.Fatal(err)
	}
	if !called || !confirmed {
		t.Fatalf("expected confirmed execution (called=%v confirmed=%v)", called, confirmed)
	}
}

func TestToolCallBudget(t *testing.T) {
	llm := &scriptedLLM{responses: []string{
		`{"tool_calls":[{"name":"noop","arguments":{}}]}`,
		`{"tool_calls":[{"name":"noop","arguments":{}}]}`,
	}}
	a, _ := New(Options{LLM: llm, Budget: Budget{MaxIterations: 5, MaxToolCalls: 1, MaxBytesRead: 1024, MaxDuration: time.Minute}, AutoApprove: true})
	_ = a.Register(Tool{Name: "noop", Description: "n", Parameters: map[string]any{"type": "object"}, Handler: func(context.Context, map[string]any) (string, error) { return "ok", nil }})
	if _, err := a.Run(context.Background(), "sys", "go"); err == nil {
		t.Fatal("expected budget error")
	} else if !domain.IsKind(err, domain.KindBudget) {
		t.Fatalf("expected budget kind, got %v", err)
	}
}

func TestReadRepoFileJailed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Place the outside dir as a sibling of the jail root.
	jail, err := safefs.NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{LLM: &scriptedLLM{}, RepoJail: jail})
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := a.Registry().Get("read_repo_file")
	if !ok {
		t.Fatal("read_repo_file not registered")
	}
	out, err := tool.Handler(context.Background(), map[string]any{"rel_path": "ok.txt"})
	if err != nil || out != "inside" {
		t.Fatalf("valid read failed: %q %v", out, err)
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"rel_path": "../secret.txt"}); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := tool.Handler(context.Background(), map[string]any{"rel_path": filepath.Join(outside, "secret.txt")}); err == nil {
		t.Fatal("expected absolute outside rejection")
	}
}

func TestDefaultToolsRegistered(t *testing.T) {
	a, err := New(Options{LLM: &scriptedLLM{}})
	if err != nil {
		t.Fatal(err)
	}
	names := a.Registry().Names()
	for _, want := range []string{
		"scan_repository", "infer_infrastructure", "synthesize_manifests", "orchestrate_stack",
		"inspect_logs_and_health", "heal_service", "read_repo_file", "explain_topology", "start_public_tunnel",
	} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing default tool %q", want)
		}
	}
}

func TestParseAgentResponseFallback(t *testing.T) {
	r := parseAgentResponse("just some prose")
	if r.Final != "just some prose" {
		t.Fatalf("got %q", r.Final)
	}
	r = parseAgentResponse(`{"final":"ok","thinking":"x"}`)
	if r.Final != "ok" || r.Thinking != "x" {
		t.Fatalf("got %+v", r)
	}
}

func TestSystemPromptNonEmpty(t *testing.T) {
	if strings.TrimSpace(SystemPrompt) == "" {
		t.Fatal("system prompt is empty")
	}
}
