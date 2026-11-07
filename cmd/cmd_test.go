package cmd

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func TestResolveSourceLocal(t *testing.T) {
	flagQuiet = true
	dir := t.TempDir()
	path, name, url, isLocal, err := resolveSource(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != dir || name != filepath.Base(dir) || url != "" || !isLocal {
		t.Fatalf("unexpected: %q %q %q %v", path, name, url, isLocal)
	}
}

func TestResolveSourceRejectsMissing(t *testing.T) {
	flagQuiet = true
	if _, _, _, _, err := resolveSource(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestPortMap(t *testing.T) {
	m := portMap([]*domain.Service{{Name: "a", HostPort: 3000}, {Name: "b", HostPort: 0}})
	if m["a"] != 3000 {
		t.Fatalf("map = %+v", m)
	}
	if _, ok := m["b"]; ok {
		t.Fatal("zero host port should be omitted")
	}
}

func TestDependencySet(t *testing.T) {
	deps := dependencySet([]*domain.Service{{Dependencies: []string{"Next", "pg"}}})
	if !deps["next"] || !deps["pg"] {
		t.Fatalf("deps = %+v", deps)
	}
}

func TestDescribeInfra(t *testing.T) {
	if got := describeInfra(nil); got == "" {
		t.Fatal("empty description")
	}
	got := describeInfra([]*domain.InfraService{
		{Name: "postgres", Mode: domain.InfraModeLocal},
		{Name: "redis", Mode: domain.InfraModeExternal, Provider: "upstash"},
	})
	if got == "" || !contains(got, "postgres") || !contains(got, "external:upstash") {
		t.Fatalf("got %q", got)
	}
}

func TestHumanTime(t *testing.T) {
	if humanTime(time.Time{}) != "-" {
		t.Fatal("zero time should be -")
	}
	if humanTime(time.Now().Add(-2*time.Hour)) == "" {
		t.Fatal("expected relative time")
	}
}

func TestShortHash(t *testing.T) {
	if shortHash("0123456789abcdef") != "0123456789ab" {
		t.Fatal("shortHash truncation failed")
	}
	if shortHash("abc") != "abc" {
		t.Fatal("shortHash short input failed")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
