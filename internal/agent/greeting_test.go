package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func TestIsGreeting(t *testing.T) {
	yes := []string{"hello", "hi!", "hey there", "good morning", "thank you", "what's up", "whats up", "sup", "yo"}
	no := []string{"hey can you show logs", "start the stack", "hello, what is the status of demo", "fix it", ""}
	for _, s := range yes {
		if !isGreeting(s) {
			t.Errorf("isGreeting(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isGreeting(s) {
			t.Errorf("isGreeting(%q) = true, want false", s)
		}
	}
}

func TestParseIntentGreetingBeatsUpMatch(t *testing.T) {
	ctx := demoContext()
	for _, s := range []string{"hello", "what's up", "hey there"} {
		it := ParseIntent(s, ctx)
		if it.Kind != IntentGreeting {
			t.Errorf("%q kind = %q, want greeting", s, it.Kind)
		}
		if it.Confidence < 0.9 {
			t.Errorf("%q confidence = %.2f", s, it.Confidence)
		}
	}
}

func TestRunIntentGreetingSkipsToolsAndLLM(t *testing.T) {
	compose := &fakeCompose{ps: []domain.Container{{Service: "web", State: "running", Health: "healthy"}}}
	a, err := New(Options{Project: demoProject(), Compose: compose, Budget: DefaultBudget()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.RunIntent(context.Background(), "hello", demoContext())
	if err != nil {
		t.Fatal(err)
	}
	if res.Intent == nil || res.Intent.Kind != IntentGreeting {
		t.Fatalf("intent = %+v", res.Intent)
	}
	if !strings.Contains(res.Final, "Spool") {
		t.Fatalf("final = %q", res.Final)
	}
	for _, ev := range res.Events {
		if ev.Type == "tool_start" || ev.Type == "tool_end" {
			t.Fatalf("greeting invoked a tool: %+v", ev)
		}
	}
}
