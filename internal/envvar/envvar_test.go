package envvar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env.example")
	if err := os.WriteFile(p, []byte("A=1\nexport B=two\nC=\"three four\"\nD=x # comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ParseEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if m["A"] != "1" || m["B"] != "two" || m["C"] != "three four" || m["D"] != "x" {
		t.Fatalf("unexpected parse: %+v", m)
	}
}

func TestIsSecretName(t *testing.T) {
	cases := map[string]bool{
		"API_KEY":                true,
		"JWT_SECRET":             true,
		"DATABASE_PASSWORD":      true,
		"AUTHORIZATION":          true,
		"OPENAI_API_KEY":         true,
		"MONKEY":                 false,
		"HOCKEY":                 false,
		"TOKENIZERS_PARALLELISM": false,
		"DATABASE_URL":           false,
	}
	for k, want := range cases {
		if got := IsSecretName(k); got != want {
			t.Errorf("IsSecretName(%q)=%v want %v", k, got, want)
		}
	}
}

func TestGenerateDevSecret(t *testing.T) {
	a, err := GenerateDevSecret("dev")
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateDevSecret("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "dev_") || len(a) != len("dev_")+32 {
		t.Fatalf("unexpected secret format %q", a)
	}
	if a == b {
		t.Fatal("secrets should be random")
	}
}

func TestDiscoverEnvVarsDeterministic(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".env.example", "ZED=1\nAPI_KEY=\n")
	write(t, root, "src/app.js", "const a = process.env.NODE_TOKEN; const b = process.env['MY_VAR'];")
	write(t, root, "app/main.py", "import os\nx = os.getenv('PY_VAR')\ny = os.environ['PY_TWO']\n")
	write(t, root, "settings.py", "class S:\n    DATABASE_URL: str\n    SECRET_KEY: str = Field(default='')\n")

	first := DiscoverEnvVars(root)
	second := DiscoverEnvVars(root)
	if len(first) != len(second) {
		t.Fatalf("length mismatch")
	}
	for i := range first {
		if first[i].Key != second[i].Key {
			t.Fatalf("nondeterministic order at %d: %q vs %q", i, first[i].Key, second[i].Key)
		}
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Key > first[i].Key {
			t.Fatalf("keys not sorted: %q > %q", first[i-1].Key, first[i].Key)
		}
	}
	keys := map[string]bool{}
	for _, ev := range first {
		keys[ev.Key] = true
	}
	for _, want := range []string{"NODE_TOKEN", "MY_VAR", "PY_VAR", "PY_TWO", "DATABASE_URL", "SECRET_KEY", "ZED", "API_KEY"} {
		if !keys[want] {
			t.Errorf("missing discovered key %q", want)
		}
	}
}

func TestMergeEnvironmentPrecedence(t *testing.T) {
	discovered := []*domain.EnvVar{
		{Key: "API_KEY", Required: true},
		{Key: "OPENAI_API_KEY", Required: true},
		{Key: "CORS_ORIGINS", Default: "http://a.com,http://b.com"},
		{Key: "DATABASE_URL", Default: "postgres://old"},
		{Key: "OPTIONAL_FLAG"},
	}
	infra := map[string]string{"DATABASE_URL": "postgresql://app:app@postgres:5432/app_db"}
	existing := map[string]string{"CUSTOM": "user"}

	merged, err := MergeEnvironment(existing, discovered, infra)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(merged["API_KEY"], "dev_") {
		t.Errorf("API_KEY should be synthesized: %q", merged["API_KEY"])
	}
	if merged["OPENAI_API_KEY"] != "" {
		t.Errorf("vendor key must not be fabricated: %q", merged["OPENAI_API_KEY"])
	}
	if merged["CORS_ORIGINS"] != `["http://a.com", "http://b.com"]` {
		t.Errorf("origins = %q", merged["CORS_ORIGINS"])
	}
	if merged["DATABASE_URL"] != "postgresql://app:app@postgres:5432/app_db" {
		t.Errorf("infra should override discovered: %q", merged["DATABASE_URL"])
	}
	if merged["CUSTOM"] != "user" {
		t.Errorf("existing env must win: %q", merged["CUSTOM"])
	}
	if _, ok := merged["OPTIONAL_FLAG"]; !ok {
		t.Error("optional flag should be present (empty)")
	}
}

func TestExistingFileWins(t *testing.T) {
	root := t.TempDir()
	envPath := filepath.Join(root, ".env")
	write(t, root, ".env", "API_KEY=user-provided\n")
	merged, err := MergeWithExistingFile(envPath, []*domain.EnvVar{{Key: "API_KEY", Required: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if merged["API_KEY"] != "user-provided" {
		t.Fatalf("existing file should win, got %q", merged["API_KEY"])
	}
}

func TestRenderEnvDeterministicAndSorted(t *testing.T) {
	m := map[string]string{"B": "2", "A": "1", "C": "has space"}
	a := RenderEnv(m)
	b := RenderEnv(m)
	if a != b {
		t.Fatal("RenderEnv is not deterministic")
	}
	if strings.Index(a, "A=1") > strings.Index(a, "B=2") {
		t.Fatalf("keys not sorted:\n%s", a)
	}
	if !strings.Contains(a, `C="has space"`) {
		t.Fatalf("expected quoting:\n%s", a)
	}
}

func TestWriteEnvFilePermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	if err := WriteEnvFile(p, map[string]string{"A": "1"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected 0600, got %o", mode)
	}
}
