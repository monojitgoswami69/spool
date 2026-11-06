// Package graph builds a deterministic service/infrastructure dependency DAG
// and computes a stable start order via Kahn's algorithm.
package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
)

// NodeKind classifies a graph node.
type NodeKind string

const (
	NodeApp      NodeKind = "app"
	NodeInfra    NodeKind = "infra"
	NodeExternal NodeKind = "external"
)

// Node is a service or infrastructure node.
type Node struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Kind      NodeKind `json:"kind"`
	Language  string   `json:"language,omitempty"`
	Framework string   `json:"framework,omitempty"`
	Port      int      `json:"port,omitempty"`
	HostPort  int      `json:"host_port,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	Mode      string   `json:"mode,omitempty"`
	Evidence  string   `json:"evidence,omitempty"`
}

// EdgeKind classifies a dependency edge.
type EdgeKind string

const (
	EdgeDepends    EdgeKind = "depends"
	EdgeEnvBinding EdgeKind = "env-binding"
)

// Edge connects two nodes.
type Edge struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Kind   EdgeKind `json:"kind"`
	EnvVar string   `json:"env_var,omitempty"`
}

// Graph is the full topology.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Build constructs the graph deterministically.
func Build(p *domain.Project) *Graph {
	g := &Graph{}
	if p == nil {
		return g
	}
	for _, svc := range p.Services {
		g.Nodes = append(g.Nodes, Node{
			ID:        svc.Name,
			Label:     string(svc.Framework),
			Kind:      NodeApp,
			Language:  string(svc.Language),
			Framework: string(svc.Framework),
			Port:      svc.Port,
			HostPort:  svc.HostPort,
			Evidence:  fmt.Sprintf("detected %s/%s", svc.Language, svc.Framework),
		})
	}
	for _, infra := range p.InfraServices {
		kind := NodeInfra
		if infra.Mode == domain.InfraModeExternal {
			kind = NodeExternal
		}
		g.Nodes = append(g.Nodes, Node{
			ID:       infra.Name,
			Label:    string(infra.Type),
			Kind:     kind,
			Port:     infra.Port,
			HostPort: infra.HostPort,
			Provider: infra.Provider,
			Mode:     string(infra.Mode),
			Evidence: infra.Reason,
		})
	}

	seen := map[string]bool{}
	addEdge := func(e Edge) {
		key := e.From + "|" + e.To + "|" + string(e.Kind) + "|" + e.EnvVar
		if seen[key] {
			return
		}
		seen[key] = true
		g.Edges = append(g.Edges, e)
	}

	infraNames := map[string]bool{}
	for _, infra := range p.InfraServices {
		infraNames[infra.Name] = true
	}
	for _, app := range p.Services {
		for _, infra := range p.InfraServices {
			envVar := ""
			if app.DependsOn != nil {
				for _, d := range app.DependsOn {
					if d == infra.Name {
						envVar = ""
					}
				}
			}
			for k, v := range app.Env {
				if strings.Contains(v, infra.Name+":") || strings.Contains(v, "//"+infra.Name+":") || strings.Contains(v, infra.Name+".") {
					envVar = k
					break
				}
			}
			kind := EdgeDepends
			if envVar != "" {
				kind = EdgeEnvBinding
			}
			addEdge(Edge{From: app.Name, To: infra.Name, Kind: kind, EnvVar: envVar})
		}
		for _, other := range p.Services {
			if other.Name == app.Name {
				continue
			}
			for k, v := range app.Env {
				if strings.Contains(v, "//"+other.Name+":") || strings.Contains(v, other.Name+":") {
					addEdge(Edge{From: app.Name, To: other.Name, Kind: EdgeEnvBinding, EnvVar: k})
					break
				}
			}
		}
	}

	sort.Slice(g.Nodes, func(i, j int) bool {
		if g.Nodes[i].Kind != g.Nodes[j].Kind {
			return g.Nodes[i].Kind < g.Nodes[j].Kind
		}
		return g.Nodes[i].ID < g.Nodes[j].ID
	})
	sort.Slice(g.Edges, func(i, j int) bool {
		a, b := g.Edges[i], g.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.EnvVar < b.EnvVar
	})
	return g
}

// Find returns the node with id, or nil.
func (g *Graph) Find(id string) *Node {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

// StartOrder returns infrastructure and application node IDs in a valid
// dependency order. External nodes are omitted. Ties are broken by ID, and
// cycles are broken deterministically.
func (g *Graph) StartOrder() []string {
	ids := make([]string, 0, len(g.Nodes))
	indeg := make(map[string]int)
	adj := make(map[string][]string)
	external := make(map[string]bool)
	for _, n := range g.Nodes {
		if n.Kind == NodeExternal {
			external[n.ID] = true
			continue
		}
		ids = append(ids, n.ID)
		indeg[n.ID] = 0
	}
	sort.Strings(ids)
	for _, e := range g.Edges {
		if external[e.From] || external[e.To] {
			continue
		}
		if _, ok := indeg[e.From]; !ok {
			continue
		}
		if _, ok := indeg[e.To]; !ok {
			continue
		}
		// An edge From -> To means From depends on To, so To must start first.
		adj[e.To] = append(adj[e.To], e.From)
		indeg[e.From]++
	}
	for k := range adj {
		sort.Strings(adj[k])
	}

	var order []string
	done := make(map[string]bool)
	for len(order) < len(ids) {
		progressed := false
		for _, id := range ids {
			if done[id] || indeg[id] != 0 {
				continue
			}
			done[id] = true
			order = append(order, id)
			for _, next := range adj[id] {
				indeg[next]--
			}
			progressed = true
			break
		}
		if !progressed {
			// Cycle: append remaining nodes in sorted order.
			for _, id := range ids {
				if !done[id] {
					done[id] = true
					order = append(order, id)
				}
			}
		}
	}
	return order
}

// Render returns a deterministic plain-text topology summary.
func (g *Graph) Render() string {
	var b strings.Builder
	b.WriteString("Spool topology\n")
	b.WriteString("Applications\n")
	anyApp := false
	for _, n := range g.Nodes {
		if n.Kind != NodeApp {
			continue
		}
		anyApp = true
		b.WriteString(fmt.Sprintf("  %s (%s) container:%d host:%d\n", n.ID, n.Label, n.Port, n.HostPort))
	}
	if !anyApp {
		b.WriteString("  (none)\n")
	}
	b.WriteString("Infrastructure\n")
	anyInfra := false
	for _, n := range g.Nodes {
		if n.Kind == NodeApp {
			continue
		}
		anyInfra = true
		mode := "local"
		if n.Kind == NodeExternal {
			mode = "external"
			if n.Provider != "" {
				mode += " · " + n.Provider
			}
		}
		b.WriteString(fmt.Sprintf("  %s (%s) %s container:%d host:%d\n", n.ID, n.Label, mode, n.Port, n.HostPort))
	}
	if !anyInfra {
		b.WriteString("  (none)\n")
	}
	b.WriteString("Dependencies\n")
	anyEdge := false
	for _, e := range g.Edges {
		anyEdge = true
		if e.EnvVar != "" {
			b.WriteString(fmt.Sprintf("  %s -> %s (%s)\n", e.From, e.To, e.EnvVar))
		} else {
			b.WriteString(fmt.Sprintf("  %s -> %s\n", e.From, e.To))
		}
	}
	if !anyEdge {
		b.WriteString("  (none)\n")
	}
	b.WriteString("Start order: " + strings.Join(g.StartOrder(), ", ") + "\n")
	return b.String()
}
