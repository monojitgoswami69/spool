package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorKindWrapping(t *testing.T) {
	base := E(KindUnsafePath, "safefs.Resolve", "escape", ErrUnsafePath)
	wrapped := fmt.Errorf("outer: %w", base)

	if !IsKind(wrapped, KindUnsafePath) {
		t.Fatalf("expected KindUnsafePath through wrap chain")
	}
	if IsKind(wrapped, KindDocker) {
		t.Fatalf("did not expect KindDocker")
	}
	if !errors.Is(wrapped, ErrUnsafePath) {
		t.Fatalf("expected errors.Is to find ErrUnsafePath")
	}
	k, ok := KindOf(base)
	if !ok || k != KindUnsafePath {
		t.Fatalf("KindOf = %q,%v", k, ok)
	}
}

func TestUnknownProjectError(t *testing.T) {
	err := &UnknownProjectError{Name: "missing", Available: []string{"a", "b"}}
	if got := err.Error(); got == "" || !contains(got, "missing") {
		t.Fatalf("unexpected message %q", got)
	}
}

func TestResultHealthy(t *testing.T) {
	if !(Result{Status: "healthy"}).Healthy() {
		t.Fatal("healthy result should be healthy")
	}
	if (Result{Status: "application_error"}).Healthy() {
		t.Fatal("application_error must not be healthy")
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
