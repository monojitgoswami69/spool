package cmd

import (
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func TestChooseURL(t *testing.T) {
	lock := &domain.Lock{
		Project: "demo",
		PortMap: map[string]int{"web": 3001, "api": 8000},
		Services: []*domain.Service{
			{Name: "worker", IsWorker: true},
			{Name: "web", HostPort: 3001},
			{Name: "api", HostPort: 8000},
		},
	}
	url, svc, ok := chooseURL(lock, "")
	if !ok || svc != "web" || url != "http://localhost:3001" {
		t.Fatalf("default = %q %q %v", url, svc, ok)
	}
	url, svc, ok = chooseURL(lock, "api")
	if !ok || svc != "api" || url != "http://localhost:8000" {
		t.Fatalf("api = %q %q %v", url, svc, ok)
	}
	if _, _, ok := chooseURL(lock, "missing"); ok {
		t.Fatal("expected missing service to fail")
	}
	if _, _, ok := chooseURL(lock, "worker"); ok {
		t.Fatal("worker has no URL")
	}
}

func TestChooseURLFallsBackToHostPort(t *testing.T) {
	lock := &domain.Lock{
		Services: []*domain.Service{{Name: "web", HostPort: 4321}},
	}
	url, _, ok := chooseURL(lock, "")
	if !ok || url != "http://localhost:4321" {
		t.Fatalf("got %q ok=%v", url, ok)
	}
}
