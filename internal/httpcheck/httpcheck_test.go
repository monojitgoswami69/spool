package httpcheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func TestCheckHealthyBelow500(t *testing.T) {
	for _, code := range []int{200, 301, 401, 404} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		res := Check(context.Background(), srv.URL, time.Second)
		srv.Close()
		if !res.Healthy() || res.Code != code {
			t.Fatalf("code %d => %+v", code, res)
		}
	}
}

func TestCheckApplicationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	res := Check(context.Background(), srv.URL, time.Second)
	if res.Healthy() || res.Status != string(AppError) {
		t.Fatalf("expected application_error, got %+v", res)
	}
}

func TestCheckUnreachable(t *testing.T) {
	res := Check(context.Background(), "http://127.0.0.1:1/", 300*time.Millisecond)
	if res.Healthy() {
		t.Fatalf("expected unhealthy, got %+v", res)
	}
	if res.Status != string(Unreachable) && res.Status != string(Timeout) {
		t.Fatalf("unexpected status %q", res.Status)
	}
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	res := Check(context.Background(), srv.URL, 20*time.Millisecond)
	if res.Status != string(Timeout) {
		t.Fatalf("expected timeout, got %+v", res)
	}
}

func TestServicesAndAllHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	svcs := []*domain.Service{
		{Name: "web", HostPort: port},
		{Name: "worker", IsWorker: true},
	}
	results := Services(context.Background(), svcs, map[string]int{"web": port})
	if len(results) != 1 {
		t.Fatalf("expected 1 result (worker skipped), got %d", len(results))
	}
	if !AllHealthy(results) {
		t.Fatalf("expected healthy, got %+v", results)
	}
	if !AllHealthy(nil) {
		t.Fatal("empty set should be healthy")
	}
}
