package tunnel

import (
	"strings"
	"testing"
)

func TestExtractURL(t *testing.T) {
	cases := map[string]string{
		"2024 INF |  https://random-words-here.trycloudflare.com  |": "https://random-words-here.trycloudflare.com",
		"tunnel established at https://abc123.lhr.life":              "https://abc123.lhr.life",
		"see https://foo.localhost.run for details":                  "https://foo.localhost.run",
		"nothing here": "",
	}
	for in, want := range cases {
		if got := ExtractURL(in); got != want {
			t.Errorf("ExtractURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBuildArgs(t *testing.T) {
	cf := BuildCloudflaredArgs(3000)
	joined := strings.Join(cf, " ")
	if !strings.Contains(joined, "--url http://localhost:3000") {
		t.Fatalf("cloudflared args = %v", cf)
	}
	ssh := BuildSSHArgs(8080, "")
	joined = strings.Join(ssh, " ")
	if !strings.Contains(joined, "80:localhost:8080") || !strings.Contains(joined, "nokey@localhost.run") {
		t.Fatalf("ssh args = %v", ssh)
	}
}

func TestCloseNil(t *testing.T) {
	var tn *Tunnel
	if err := tn.Close(); err != nil {
		t.Fatal(err)
	}
	if tn.PublicURL() != "" {
		t.Fatal("nil tunnel should have no URL")
	}
}

func TestParsePort(t *testing.T) {
	if p := ParsePort("http://localhost:4321"); p != 4321 {
		t.Fatalf("got %d", p)
	}
}

func TestStartRejectsInvalidPort(t *testing.T) {
	if _, err := Start(t.Context(), 0, Options{}); err == nil {
		t.Fatal("expected error for invalid port")
	}
}
