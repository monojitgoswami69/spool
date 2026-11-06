package docker

import (
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func TestParsePsNDJSON(t *testing.T) {
	out := `{"ID":"abc","Name":"proj-web-1","Service":"web","State":"running","Status":"Up 2 minutes (healthy)","Health":"","Image":"img","Project":"proj","ExitCode":0}
{"ID":"def","Name":"proj-db-1","Service":"postgres","State":"exited","Status":"Exited (1)","Health":"","Image":"pg","Project":"proj","ExitCode":1}`
	cs, err := ParsePs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("expected 2, got %d", len(cs))
	}
	if cs[0].Health != "healthy" {
		t.Fatalf("expected health parsed from status, got %q", cs[0].Health)
	}
	if cs[1].State != "exited" || cs[1].ExitCode != 1 {
		t.Fatalf("unexpected exit row %+v", cs[1])
	}
}

func TestParsePsArray(t *testing.T) {
	out := `[{"ID":"a","Service":"web","State":"running","Health":"starting"}]`
	cs, err := ParsePs(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].Health != "starting" {
		t.Fatalf("unexpected %+v", cs)
	}
}

func TestParseStats(t *testing.T) {
	out := `{"Container":"abc","Name":"proj-web-1","CPUPerc":"2.50%","MemPerc":"10.00%","MemUsage":"1MiB / 2GiB","NetIO":"1kB / 0B","BlockIO":"0B / 0B","PIDs":"5"}`
	stats, err := ParseStats(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].CPUPerc != 2.5 || stats[0].MemPerc != 10 {
		t.Fatalf("unexpected %+v", stats)
	}
}

func TestExtractFailedService(t *testing.T) {
	cases := map[string]string{
		"#10 [web 3/5] RUN npm ci\nERROR: failed to solve": "web",
		"service 'api' failed to build":                    "api",
		"failed to build worker":                           "worker",
		"blah Dockerfile.frontend something":               "frontend",
	}
	for in, want := range cases {
		if got := ExtractFailedService(in); got != want {
			t.Errorf("ExtractFailedService(%q)=%q want %q", in, got, want)
		}
	}
}

func TestTailLines(t *testing.T) {
	s := "a\nb\nc\nd"
	if got := TailLines(s, 2); got != "c\nd\n" {
		t.Fatalf("got %q", got)
	}
	if got := TailLines("", 5); got != "" {
		t.Fatalf("empty should give empty, got %q", got)
	}
	if got := TailLines("a\nb", 0); got != "" {
		t.Fatalf("zero n should give empty, got %q", got)
	}
}

func TestComposeSatisfiesInterface(t *testing.T) {
	var _ domain.ComposeController = New("compose.yml", "/tmp", "proj")
	if !Available() && strings.TrimSpace("") != "" {
		t.Fatal("unreachable")
	}
}
