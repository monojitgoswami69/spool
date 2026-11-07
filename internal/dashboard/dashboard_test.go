package dashboard

import (
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func TestServiceNames(t *testing.T) {
	lock := &domain.Lock{
		Services: []*domain.Service{{Name: "web"}, {Name: "api"}},
		Infra:    []*domain.InfraService{{Name: "postgres"}, {Name: "redis"}},
	}
	got := serviceNames(lock)
	want := []string{"api", "web", "postgres", "redis"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestClone(t *testing.T) {
	src := map[string]string{"A": "1"}
	dup := clone(src)
	dup["A"] = "2"
	if src["A"] != "1" {
		t.Fatal("clone must not mutate source")
	}
}

func TestViewDoesNotPanic(t *testing.T) {
	m := &Model{
		lock:     &domain.Lock{Project: "demo"},
		services: []string{"web"},
		width:    80,
		height:   24,
		tab:      tabControls,
	}
	if m.View() == "" {
		t.Fatal("empty view")
	}
	for _, tab := range []int{tabLogs, tabEnv, tabControls} {
		m.tab = tab
		if m.View() == "" {
			t.Fatalf("empty view for tab %d", tab)
		}
	}
}
