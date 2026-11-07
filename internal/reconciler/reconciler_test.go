package reconciler

import (
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/portprobe"
)

func TestReconcileAvoidsReservedPort(t *testing.T) {
	alloc := portprobe.New()
	alloc.Reserve(3000)
	svcs := []*domain.Service{{Name: "web", Framework: domain.FrameworkNextJS, DefaultPort: 3000, StartCmd: "npm run start"}}
	if err := Reconcile(svcs, alloc, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if svcs[0].Port != 3000 {
		t.Fatalf("container port = %d", svcs[0].Port)
	}
	if svcs[0].HostPort == 3000 || svcs[0].HostPort == 0 {
		t.Fatalf("expected remapped host port, got %d", svcs[0].HostPort)
	}
	if svcs[0].Env["PORT"] != "3000" || svcs[0].Env["HOST"] != "0.0.0.0" {
		t.Fatalf("env = %+v", svcs[0].Env)
	}
}

func TestReconcileUniqueHostPorts(t *testing.T) {
	alloc := portprobe.New()
	svcs := []*domain.Service{
		{Name: "a", Framework: domain.FrameworkExpress, DefaultPort: 5000},
		{Name: "b", Framework: domain.FrameworkExpress, DefaultPort: 5000},
	}
	if err := Reconcile(svcs, alloc, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if svcs[0].HostPort == svcs[1].HostPort {
		t.Fatalf("duplicate host ports: %d", svcs[0].HostPort)
	}
}

func TestReconcileWorkersGetNoPorts(t *testing.T) {
	alloc := portprobe.New()
	svcs := []*domain.Service{{Name: "worker", IsWorker: true, DefaultPort: 0, Port: 8000, HostPort: 8000}}
	if err := Reconcile(svcs, alloc, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if svcs[0].Port != 0 || svcs[0].HostPort != 0 {
		t.Fatalf("worker ports = %d/%d", svcs[0].Port, svcs[0].HostPort)
	}
}

func TestRewriteStartCommand(t *testing.T) {
	cases := []struct {
		fw   domain.Framework
		in   string
		port int
		want []string
	}{
		{domain.FrameworkFastAPI, "uvicorn main:app", 8000, []string{"--port 8000", "--host 0.0.0.0"}},
		{domain.FrameworkFastAPI, "uvicorn main:app --port 9000", 8000, []string{"--port 8000"}},
		{domain.FrameworkDjango, "python manage.py runserver", 8001, []string{"runserver 0.0.0.0:8001"}},
		{domain.FrameworkVite, "npm run dev -- --host 0.0.0.0 --port 5173", 5174, []string{"--port 5174"}},
		{domain.FrameworkNextJS, "npm run start", 3000, []string{"-p 3000"}},
	}
	for _, c := range cases {
		got := RewriteStartCommand(c.in, c.fw, c.port)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s %q => %q, missing %q", c.fw, c.in, got, w)
			}
		}
	}
}

func TestFrontendBackendWiring(t *testing.T) {
	alloc := portprobe.New()
	svcs := []*domain.Service{
		{Name: "web", Framework: domain.FrameworkNextJS, DefaultPort: 3000, Env: map[string]string{
			"API_URL":             "http://localhost:8000",
			"NEXT_PUBLIC_API_URL": "your_api_url",
		}},
		{Name: "api", Framework: domain.FrameworkFastAPI, DefaultPort: 8000},
	}
	if err := Reconcile(svcs, alloc, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	web := svcs[0]
	if web.Env["API_URL"] != "http://api:8000" {
		t.Fatalf("API_URL = %q", web.Env["API_URL"])
	}
	wantPublic := "http://localhost:" + itoa(web.HostPort)
	_ = wantPublic
	if !strings.HasPrefix(web.Env["NEXT_PUBLIC_API_URL"], "http://localhost:") {
		t.Fatalf("NEXT_PUBLIC_API_URL = %q", web.Env["NEXT_PUBLIC_API_URL"])
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
