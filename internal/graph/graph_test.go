package graph

import (
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func testProject() *domain.Project {
	return &domain.Project{
		Services: []*domain.Service{
			{Name: "web", Language: domain.LangNode, Framework: domain.FrameworkNextJS, Port: 3000, HostPort: 3002,
				Env: map[string]string{"API_URL": "http://api:8000"}},
			{Name: "api", Language: domain.LangPython, Framework: domain.FrameworkFastAPI, Port: 8000, HostPort: 8001,
				Env: map[string]string{"DATABASE_URL": "postgresql://app:app@postgres:5432/app"}},
		},
		InfraServices: []*domain.InfraService{
			{Type: domain.InfraPostgres, Name: "postgres", Port: 5432, HostPort: 5433, Mode: domain.InfraModeLocal},
		},
	}
}

func TestBuildDeterministic(t *testing.T) {
	a := Build(testProject())
	b := Build(testProject())
	if len(a.Nodes) != 3 || len(b.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d/%d", len(a.Nodes), len(b.Nodes))
	}
	for i := range a.Nodes {
		if a.Nodes[i].ID != b.Nodes[i].ID {
			t.Fatalf("node order differs: %v vs %v", a.Nodes[i].ID, b.Nodes[i].ID)
		}
	}
	for i := range a.Edges {
		if a.Edges[i] != b.Edges[i] {
			t.Fatalf("edge order differs at %d", i)
		}
	}
}

func TestStartOrderDependenciesFirst(t *testing.T) {
	g := Build(testProject())
	order := g.StartOrder()
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["postgres"] > pos["api"] {
		t.Fatalf("postgres must start before api: %v", order)
	}
	if pos["api"] > pos["web"] {
		t.Fatalf("api must start before web: %v", order)
	}
}

func TestStartOrderExcludesExternal(t *testing.T) {
	p := testProject()
	p.InfraServices = append(p.InfraServices, &domain.InfraService{
		Type: domain.InfraRedis, Name: "redis", Mode: domain.InfraModeExternal, Provider: "upstash",
	})
	g := Build(p)
	for _, id := range g.StartOrder() {
		if id == "redis" {
			t.Fatal("external node must not be in start order")
		}
	}
}

func TestRenderDeterministic(t *testing.T) {
	g := Build(testProject())
	r1 := g.Render()
	r2 := g.Render()
	if r1 != r2 {
		t.Fatal("render is not deterministic")
	}
	for _, want := range []string{"Applications", "Infrastructure", "Start order", "postgres"} {
		if !strings.Contains(r1, want) {
			t.Errorf("missing %q in render", want)
		}
	}
}

func TestEmptyGraph(t *testing.T) {
	g := Build(nil)
	if len(g.Nodes) != 0 || len(g.StartOrder()) != 0 {
		t.Fatal("nil project should produce empty graph")
	}
}
