package ui

import (
	"strings"
	"testing"
)

func TestStatusBadge(t *testing.T) {
	cases := []struct{ state, health, want string }{
		{"running", "healthy", "healthy"},
		{"running", "unhealthy", "unhealthy"},
		{"exited", "", "exited"},
		{"", "", "unknown"},
	}
	for _, c := range cases {
		got := StatusBadge(c.state, c.health)
		if !strings.Contains(got, c.want) {
			t.Errorf("StatusBadge(%q,%q)=%q missing %q", c.state, c.health, got, c.want)
		}
	}
}

func TestTable(t *testing.T) {
	out := Table([]string{"A", "B"}, [][]string{{"1", "2"}}, "title")
	if !strings.Contains(out, "A") || !strings.Contains(out, "1") {
		t.Fatalf("unexpected table:\n%s", out)
	}
	if Table([]string{"A"}, nil, "") == "" {
		t.Fatal("empty table should render a placeholder")
	}
}

func TestMarkdown(t *testing.T) {
	out := Markdown("# Title\n- one\n**bold** and `code`")
	if !strings.Contains(out, "Title") || !strings.Contains(out, "•") || !strings.Contains(out, "bold") {
		t.Fatalf("unexpected markdown:\n%s", out)
	}
}

func TestKeyHints(t *testing.T) {
	out := KeyHints([][2]string{{"q", "quit"}})
	if !strings.Contains(out, "quit") {
		t.Fatalf("unexpected hints: %q", out)
	}
}

func TestSetNoColor(t *testing.T) {
	SetNoColor()
	out := StatusBadge("running", "healthy")
	if !strings.Contains(out, "healthy") {
		t.Fatalf("unexpected: %q", out)
	}
}
