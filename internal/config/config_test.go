package config

import (
	"os"
	"path/filepath"
	"testing"
)

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SPOOL_HOME", "SPOOL_LLM_PROVIDER", "SPOOL_LLM_MODEL", "SPOOL_LLM_API_KEY",
		"SPOOL_LLM_BASE_URL", "SPOOL_ENV_FILE", "LLM_API_KEY", "GITHUB_PAT", "GH_TOKEN",
		"GEMINI_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GROQ_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestParseDotEnv(t *testing.T) {
	data := []byte(`
# comment
export FOO=bar
QUOTED="hello world"
SINGLE='single value'
INLINE=value # trailing comment
EMPTY=
`)
	m := ParseDotEnv(data)
	if m["FOO"] != "bar" {
		t.Errorf("FOO=%q", m["FOO"])
	}
	if m["QUOTED"] != "hello world" {
		t.Errorf("QUOTED=%q", m["QUOTED"])
	}
	if m["SINGLE"] != "single value" {
		t.Errorf("SINGLE=%q", m["SINGLE"])
	}
	if m["INLINE"] != "value" {
		t.Errorf("INLINE=%q", m["INLINE"])
	}
	if v, ok := m["EMPTY"]; !ok || v != "" {
		t.Errorf("EMPTY=%q ok=%v", v, ok)
	}
}

func TestResolveFromEnvVarFallback(t *testing.T) {
	clearEnv(t)
	t.Setenv("SPOOL_HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "env-key-123")

	r, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Provider != "gemini" {
		t.Fatalf("provider = %q", r.Provider)
	}
	if r.APIKey != "env-key-123" {
		t.Fatalf("key = %q", r.APIKey)
	}
	if r.Model == "" {
		t.Fatal("model should have a default")
	}
}

func TestResolveFromDotEnv(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("SPOOL_HOME", home)
	t.Setenv("HOME", home)
	dotenv := "OPENAI_API_KEY=dotenv-key\nSPOOL_LLM_PROVIDER=openai\nSPOOL_LLM_MODEL=gpt-4o\n"
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.Provider != "openai" || r.Model != "gpt-4o" || r.APIKey != "dotenv-key" {
		t.Fatalf("unexpected resolved: %+v", r)
	}
}

func TestResolveFromConfigFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("SPOOL_HOME", t.TempDir())
	cfg := &Config{LLMProvider: "gemini", LLMModel: "gemini-3.5-flash-lite", LLMAPIKey: "cfg-key"}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if r.APIKey != "cfg-key" || r.Provider != "gemini" {
		t.Fatalf("unexpected resolved: %+v", r)
	}
}

func TestResolveUnconfigured(t *testing.T) {
	clearEnv(t)
	t.Setenv("SPOOL_HOME", t.TempDir())
	if _, err := Resolve(); err == nil {
		t.Fatal("expected unconfigured error")
	}
}

func TestSavePermissions(t *testing.T) {
	clearEnv(t)
	home := t.TempDir()
	t.Setenv("SPOOL_HOME", home)
	if err := Save(&Config{LLMProvider: "gemini", LLMModel: "m", LLMAPIKey: "k"}); err != nil {
		t.Fatal(err)
	}
	p, _ := ConfigPath()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected 0600, got %o", mode)
	}
}

func TestValidProjectID(t *testing.T) {
	good := []string{"my-app", "a_b.c", "App123"}
	for _, id := range good {
		if !ValidProjectID(id) {
			t.Errorf("expected valid: %q", id)
		}
	}
	bad := []string{"", ".", "..", "../x", "a/b", `a\b`, "has space", "quo'te"}
	for _, id := range bad {
		if ValidProjectID(id) {
			t.Errorf("expected invalid: %q", id)
		}
	}
}

func TestProjectDirRejectsTraversal(t *testing.T) {
	clearEnv(t)
	t.Setenv("SPOOL_HOME", t.TempDir())
	if _, err := ProjectDir("../escape"); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestNormalizeProvider(t *testing.T) {
	cases := map[string]string{
		"Google":    "gemini",
		"CLAUDE":    "anthropic",
		" openai ":  "openai",
		"unknown-x": "unknown-x",
	}
	for in, want := range cases {
		if got := NormalizeProvider(in); got != want {
			t.Errorf("NormalizeProvider(%q)=%q want %q", in, got, want)
		}
	}
}
