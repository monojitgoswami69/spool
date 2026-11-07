package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

type fakeCompose struct {
	ps        []domain.Container
	logs      string
	upCalls   int
	downCalls int
	volumes   bool
}

func (f *fakeCompose) Build(context.Context, string) (string, error) { return "", nil }
func (f *fakeCompose) Up(context.Context, ...string) (string, error) {
	f.upCalls++
	return "", nil
}
func (f *fakeCompose) Down(_ context.Context, volumes bool) (string, error) {
	f.downCalls++
	f.volumes = volumes
	return "", nil
}
func (f *fakeCompose) Ps(context.Context) ([]domain.Container, error) { return f.ps, nil }
func (f *fakeCompose) Logs(context.Context, string, int) (string, error) {
	return f.logs, nil
}
func (f *fakeCompose) Stats(context.Context) ([]domain.ContainerStat, error) { return nil, nil }
func (f *fakeCompose) Restart(context.Context, string) (string, error)       { return "", nil }
func (f *fakeCompose) Stop(context.Context, string) (string, error)          { return "", nil }
func (f *fakeCompose) Start(context.Context, string) (string, error)         { return "", nil }
func (f *fakeCompose) Recreate(context.Context, string) (string, error)      { return "", nil }
func (f *fakeCompose) Available() bool                                       { return true }

type fakeEnv struct {
	m      map[string]string
	setK   string
	setV   string
	unsetK string
	path   string
}

func (f *fakeEnv) List() (map[string]string, error) { return f.m, nil }
func (f *fakeEnv) Set(k, v string) error            { f.setK, f.setV = k, v; f.m[k] = v; return nil }
func (f *fakeEnv) Unset(k string) error             { f.unsetK = k; delete(f.m, k); return nil }
func (f *fakeEnv) Path() string                     { return f.path }

func demoProject() *domain.Project {
	return &domain.Project{
		ID:   "demo-1",
		Name: "demo",
		Services: []*domain.Service{
			{Name: "web", Framework: domain.FrameworkNextJS, Port: 3000, HostPort: 3000},
		},
		InfraServices: []*domain.InfraService{
			{Name: "postgres", Type: domain.InfraPostgres, Mode: domain.InfraModeLocal, Port: 5432, HostPort: 5433},
		},
	}
}

func demoContext() IntentContext {
	return IntentContext{
		Projects: []ProjectInfo{
			{ID: "demo-1", Name: "demo", Services: []string{"web"}},
			{ID: "other-1", Name: "other", Services: []string{"api"}},
		},
		CurrentProject: "demo",
	}
}

func TestParseIntentTable(t *testing.T) {
	ctx := demoContext()
	cases := []struct {
		text       string
		kind       IntentKind
		project    string
		service    string
		key        string
		value      string
		port       int
		confidence float64
		clarify    bool
	}{
		{"what's the status of demo", IntentStatus, "demo", "", "", "", 0, 0.95, false},
		{"explain the topology for other", IntentExplain, "other", "", "", "", 0, 0.95, false},
		{"show logs for web in demo", IntentLogs, "demo", "web", "", "", 0, 0.9, false},
		{"is it healthy", IntentHealth, "demo", "", "", "", 0, 0.9, false},
		{"set FOO=bar on demo", IntentEnvSet, "demo", "", "FOO", "bar", 0, 0.95, false},
		{"unset GROQ_API_KEY", IntentEnvUnset, "demo", "", "GROQ_API_KEY", "", 0, 0.9, false},
		{"show me the environment", IntentEnvList, "demo", "", "", "", 0, 0.85, false},
		{"nuke other", IntentNuke, "other", "", "", "", 0, 0.95, false},
		{"open a tunnel on port 3001", IntentTunnel, "demo", "", "", "", 3001, 0.9, false},
		{"read file src/app/page.tsx", IntentReadFile, "demo", "", "", "", 0, 0.85, false},
		{"scan this repo", IntentScan, "", "", "", "", 0, 0.9, false},
		{"use project other", IntentSwitchProject, "other", "", "", "", 0, 0.9, false},
		{"is the app up", IntentStatus, "demo", "", "", "", 0, 0.95, false},
		{"what's running", IntentStatus, "demo", "", "", "", 0, 0.95, false},
		{"spin up the app", IntentUp, "demo", "", "", "", 0, 0.85, false},
		{"check the health", IntentHealth, "demo", "", "", "", 0, 0.9, false},
		{"restart the web service", IntentRestart, "demo", "web", "", "", 0, 0.9, false},
		{"take it down", IntentDown, "demo", "", "", "", 0, 0.9, false},
		{"show me the logs", IntentLogs, "demo", "", "", "", 0, 0.9, false},
		{"tell me a joke", IntentChat, "demo", "", "", "", 0, 0.0, false},
	}
	for _, c := range cases {
		it := ParseIntent(c.text, ctx)
		if it.Kind != c.kind {
			t.Errorf("%q kind=%q want %q", c.text, it.Kind, c.kind)
		}
		if c.project != "" && it.Project != c.project {
			t.Errorf("%q project=%q want %q", c.text, it.Project, c.project)
		}
		if c.service != "" && it.Service != c.service {
			t.Errorf("%q service=%q want %q", c.text, it.Service, c.service)
		}
		if c.key != "" && it.Key != c.key {
			t.Errorf("%q key=%q want %q", c.text, it.Key, c.key)
		}
		if c.value != "" && it.Value != c.value {
			t.Errorf("%q value=%q want %q", c.text, it.Value, c.value)
		}
		if c.port != 0 && it.Port != c.port {
			t.Errorf("%q port=%d want %d", c.text, it.Port, c.port)
		}
		if c.confidence > 0 && it.Confidence < c.confidence {
			t.Errorf("%q confidence=%.2f want >= %.2f", c.text, it.Confidence, c.confidence)
		}
		if it.NeedsClarification != c.clarify {
			t.Errorf("%q clarify=%v want %v", c.text, it.NeedsClarification, c.clarify)
		}
	}
}

func TestParseIntentAmbiguousProject(t *testing.T) {
	ctx := demoContext()
	ctx.CurrentProject = ""
	it := ParseIntent("restart it", ctx)
	if it.Kind != IntentRestart {
		t.Fatalf("kind = %q", it.Kind)
	}
	if !it.NeedsClarification || !strings.Contains(it.Clarification, "Which project") {
		t.Fatalf("expected clarification, got %+v", it)
	}
}

func TestRunIntentDeterministicWithoutLLM(t *testing.T) {
	compose := &fakeCompose{ps: []domain.Container{{Service: "web", State: "running", Health: "healthy"}}}
	a, err := New(Options{Project: demoProject(), Compose: compose, Budget: DefaultBudget()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.RunIntent(context.Background(), "status of demo", demoContext())
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent == nil || res.Intent.Kind != IntentStatus {
		t.Fatalf("intent = %+v", res.Intent)
	}
	for _, want := range []string{"demo", "web", "running", "healthy"} {
		if !strings.Contains(res.Final, want) {
			t.Errorf("status missing %q:\n%s", want, res.Final)
		}
	}
}

func TestRunIntentExplain(t *testing.T) {
	a, _ := New(Options{Project: demoProject(), Budget: DefaultBudget()})
	res, err := a.RunIntent(context.Background(), "explain topology", demoContext())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Final, "Spool topology") || !strings.Contains(res.Final, "web") {
		t.Fatalf("unexpected explain:\n%s", res.Final)
	}
}

func TestRunIntentEnvSetStripsProjectSuffix(t *testing.T) {
	env := &fakeEnv{m: map[string]string{}}
	a, _ := New(Options{Project: demoProject(), Env: env, Budget: DefaultBudget()})
	res, err := a.RunIntent(context.Background(), "set FOO=bar on demo", demoContext())
	if err != nil {
		t.Fatal(err)
	}
	if env.setK != "FOO" || env.setV != "bar" {
		t.Fatalf("set %q=%q", env.setK, env.setV)
	}
	if !strings.Contains(res.Final, "Set `FOO`") {
		t.Fatalf("unexpected final: %s", res.Final)
	}
}

func TestRunIntentEnvListMasksSecrets(t *testing.T) {
	env := &fakeEnv{m: map[string]string{"FOO": "bar", "JWT_SECRET": "supersecret", "MISSING": ""}}
	a, _ := New(Options{Project: demoProject(), Env: env, Budget: DefaultBudget()})
	res, err := a.RunIntent(context.Background(), "show environment", demoContext())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Final, "supersecret") {
		t.Fatalf("secret leaked:\n%s", res.Final)
	}
	for _, want := range []string{"FOO", "bar", "JWT_SECRET", "••••••••", "MISSING", "missing"} {
		if !strings.Contains(res.Final, want) {
			t.Errorf("env list missing %q:\n%s", want, res.Final)
		}
	}
}

func TestRunIntentLogs(t *testing.T) {
	compose := &fakeCompose{logs: "line1\nline2"}
	a, _ := New(Options{Project: demoProject(), Compose: compose, Budget: DefaultBudget()})
	res, err := a.RunIntent(context.Background(), "show logs for web", demoContext())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Final, "line1") || !strings.Contains(res.Final, "web") {
		t.Fatalf("unexpected logs:\n%s", res.Final)
	}
}

func TestRunIntentClarifiesAmbiguityWithoutAction(t *testing.T) {
	compose := &fakeCompose{}
	a, _ := New(Options{Project: demoProject(), Compose: compose, Budget: DefaultBudget()})
	ctx := demoContext()
	ctx.CurrentProject = ""
	res, err := a.RunIntent(context.Background(), "restart it", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Final, "Which project") {
		t.Fatalf("expected clarification:\n%s", res.Final)
	}
	if compose.upCalls != 0 {
		t.Fatal("must not act on an ambiguous request")
	}
}

func TestRunIntentNukeRequiresApproval(t *testing.T) {
	compose := &fakeCompose{}
	a, _ := New(Options{Project: demoProject(), Compose: compose, Budget: DefaultBudget(), AutoApprove: false})
	_, err := a.RunIntent(context.Background(), "nuke demo", demoContext())
	if err == nil {
		t.Fatal("expected permission error")
	}
	if compose.downCalls != 0 {
		t.Fatal("destructive action ran without approval")
	}

	approved := &fakeCompose{}
	a2, _ := New(Options{Project: demoProject(), Compose: approved, Budget: DefaultBudget(), AutoApprove: true})
	if _, err := a2.RunIntent(context.Background(), "nuke demo", demoContext()); err != nil {
		t.Fatal(err)
	}
	if approved.downCalls != 1 || !approved.volumes {
		t.Fatalf("expected nuke with volumes, got calls=%d volumes=%v", approved.downCalls, approved.volumes)
	}
}

func TestRunIntentSwitchProject(t *testing.T) {
	a, _ := New(Options{Project: demoProject(), Budget: DefaultBudget()})
	res, err := a.RunIntent(context.Background(), "use project other", demoContext())
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent == nil || res.Intent.Kind != IntentSwitchProject || res.Intent.Project != "other" {
		t.Fatalf("intent = %+v", res.Intent)
	}
	if !strings.Contains(res.Final, "Switching context to other") {
		t.Fatalf("unexpected final: %s", res.Final)
	}
}

func TestRunIntentEmitsIntentEvent(t *testing.T) {
	var events []Event
	a, _ := New(Options{Project: demoProject(), Budget: DefaultBudget(), EventHandler: func(e Event) { events = append(events, e) }})
	if _, err := a.RunIntent(context.Background(), "status of demo", demoContext()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == "intent" && strings.Contains(e.Message, "status") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected intent event, got %+v", events)
	}
}
