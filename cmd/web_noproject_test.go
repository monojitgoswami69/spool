package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/monojitgoswami69/spool/internal/agent"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/web"
)

func seedNoProjectFixture(t *testing.T, names ...string) {
	t.Helper()
	t.Setenv("SPOOL_HOME", t.TempDir())
	for _, name := range names {
		id := state.IDFor(name, "/tmp/"+name+"-src")
		mgr, err := state.Ensure(id)
		if err != nil {
			t.Fatal(err)
		}
		lock := &domain.Lock{
			Version: "test", Project: name, ProjectID: id, WorkDir: mgr.Dir,
			NetworkName: id + "-net", LastInit: time.Now(),
			Services: []*domain.Service{{Name: name + "-web", Language: "node", Framework: "nextjs", Port: 3000, HostPort: 3000}},
			PortMap:  map[string]int{name + "-web": 3000},
		}
		if err := mgr.SaveLock(lock); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWebSendNoProjectClarifiesWithMultipleProjects(t *testing.T) {
	seedNoProjectFixture(t, "alpha", "beta")
	b := &localWebBackend{}
	out, err := b.Send(context.Background(), web.ChatRequest{Text: "show me the status"}, func(agent.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Final, "Which project") {
		t.Fatalf("expected project clarification, got %q", out.Final)
	}
}

func TestWebSendNoProjectNamedProjectBindsForTurn(t *testing.T) {
	seedNoProjectFixture(t, "alpha", "beta")
	b := &localWebBackend{}
	out, err := b.Send(context.Background(), web.ChatRequest{Text: "status of beta"}, func(agent.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Final, "beta") {
		t.Fatalf("expected beta status, got %q", out.Final)
	}
	if out.Project != "beta" {
		t.Fatalf("outcome project = %q", out.Project)
	}
}

func TestWebSendNoProjectGreetingAndZeroProjects(t *testing.T) {
	seedNoProjectFixture(t, "alpha", "beta")
	b := &localWebBackend{}
	out, err := b.Send(context.Background(), web.ChatRequest{Text: "hello"}, func(agent.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Final, "Spool") {
		t.Fatalf("greeting final = %q", out.Final)
	}

	// Zero projects: project-requiring asks point at onboarding instead of erroring.
	seedNoProjectFixture(t)
	b2 := &localWebBackend{}
	out2, err := b2.Send(context.Background(), web.ChatRequest{Text: "show me the status"}, func(agent.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2.Final, "No projects are onboarded yet") {
		t.Fatalf("zero-project final = %q", out2.Final)
	}
}
