package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in                  string
		owner, repo, branch string
		subdir              string
	}{
		{"https://github.com/acme/app", "acme", "app", "", ""},
		{"https://github.com/acme/app.git", "acme", "app", "", ""},
		{"https://github.com/acme/app/tree/dev/services/api", "acme", "app", "dev", "services/api"},
		{"git@github.com:acme/app.git", "acme", "app", "", ""},
		{"ssh://git@github.com/acme/app.git", "acme", "app", "", ""},
	}
	for _, c := range cases {
		p, err := ParseURL(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if p.Owner != c.owner || p.Repo != c.repo || p.Branch != c.branch || p.Subdir != c.subdir {
			t.Fatalf("%s => %+v", c.in, p)
		}
	}
}

func TestParseURLRejectsInvalid(t *testing.T) {
	for _, in := range []string{"", "not a url", "https://gitlab.com/a/b"} {
		if _, err := ParseURL(in); err == nil {
			t.Fatalf("expected rejection for %q", in)
		}
	}
}

func TestIsRemote(t *testing.T) {
	if !IsRemote("https://github.com/a/b") || !IsRemote("git@github.com:a/b.git") {
		t.Fatal("expected remote")
	}
	if IsRemote("./local/path") {
		t.Fatal("local path should not be remote")
	}
}

func TestScrubToken(t *testing.T) {
	tok := "supersecret"
	out := ScrubToken("clone failed for "+tok+" ok", tok)
	if out == "" || contains(out, tok) {
		t.Fatalf("token not scrubbed: %q", out)
	}
}

func TestCountFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "aaa")
	write(t, filepath.Join(root, "node_modules", "x", "y.js"), "bb")
	count, bytes, err := CountFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || bytes != 3 {
		t.Fatalf("count=%d bytes=%d", count, bytes)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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
