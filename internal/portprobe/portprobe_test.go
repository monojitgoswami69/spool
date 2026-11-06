package portprobe

import (
	"net"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func TestAllocateUnique(t *testing.T) {
	a := New()
	first, err := a.Allocate(50000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Allocate(50000)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("allocator returned duplicate port %d", first)
	}
	if !a.IsReserved(first) || !a.IsReserved(second) {
		t.Fatal("allocated ports should be reserved")
	}
}

func TestReserveSkips(t *testing.T) {
	a := New()
	a.Reserve(51000)
	got, err := a.Allocate(51000)
	if err != nil {
		t.Fatal(err)
	}
	if got == 51000 {
		t.Fatal("allocator handed out a reserved port")
	}
}

func TestAllocateExhaustion(t *testing.T) {
	a := NewWithOptions(Options{MaxScan: 1})
	a.Reserve(52000)
	if _, err := a.Allocate(52000); err == nil {
		t.Fatal("expected exhaustion error")
	} else if !domain.IsKind(err, domain.KindConfig) {
		t.Fatalf("expected config kind, got %v", err)
	}
}

func TestAllocateClampsPrivilegedPort(t *testing.T) {
	a := New()
	got, err := a.Allocate(80)
	if err != nil {
		t.Fatal(err)
	}
	if got < MinPort {
		t.Fatalf("allocator returned privileged port %d", got)
	}
}

func TestIsFreeOnDetectsListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if IsFreeOn("127.0.0.1", port) {
		t.Fatalf("port %d should be reported busy", port)
	}
}

func TestProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if r := Probe("127.0.0.1", port, 500); !r.Open {
		t.Fatalf("expected open, got %+v", r)
	}
}
