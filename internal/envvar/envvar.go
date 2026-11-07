// Package envvar discovers environment variables from source code and example
// env files, synthesizes real development secrets with crypto/rand, and merges
// them into a deterministic .env.
package envvar

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/safefs"
)

var (
	jsEnvRe   = regexp.MustCompile(`(?:process\.env|import\.meta\.env)(?:\.([A-Za-z_][A-Za-z0-9_]*)|\[\s*["']([A-Za-z_][A-Za-z0-9_]*)["']\s*\])`)
	pyEnvRe   = regexp.MustCompile(`(?:os\.environ\.get|os\.getenv|os\.environ)\s*(?:\[\s*|\(\s*)["']([A-Za-z_][A-Za-z0-9_]*)["']`)
	pyFieldRe = regexp.MustCompile(`^\s+([A-Z][A-Z0-9_]{2,})\s*(?::\s*[A-Za-z_][\w\[\], |.]*)?\s*=\s*Field\(`)
	pyAnnotRe = regexp.MustCompile(`^\s+([A-Z][A-Z0-9_]{2,})\s*:\s*(?:str|int|bool|float|bytes|Any|List|Dict|Optional|list|dict)\b`)

	walkSkip = map[string]bool{
		"node_modules": true, ".git": true, ".next": true, "dist": true, "build": true,
		"vendor": true, "legacy": true, ".spool": true, "venv": true, ".venv": true,
		"__pycache__": true, "target": true, ".nuxt": true, ".svelte-kit": true,
	}

	vendorPrefixes = []string{
		"HF_", "HUGGINGFACE_", "TWILIO_", "STRIPE_", "SLACK_", "DISCORD_", "SENTRY_",
		"DATADOG_", "OPENAI_", "ANTHROPIC_", "GROQ_", "GEMINI_", "AWS_", "GOOGLE_",
		"AZURE_", "FIREBASE_", "SUPABASE_", "RESEND_", "SENDGRID_", "MAPBOX_",
		"CLOUDINARY_", "GITHUB_", "GH_",
	}

	secretSegments = map[string]bool{
		"SECRET": true, "SECRETS": true, "KEY": true, "TOKEN": true, "PASSWORD": true,
		"PASSWD": true, "PWD": true, "CREDENTIAL": true, "CREDENTIALS": true,
		"AUTH": true, "AUTHORIZATION": true, "PRIVATE": true,
	}
)

// ParseEnvFile parses a dotenv-style file.
func ParseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := make(map[string]string)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimSuffix(sc.Text(), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if key != "" {
			out[key] = val
		}
	}
	return out, sc.Err()
}

func isExampleEnvFile(name string) bool {
	n := strings.ToLower(name)
	if !strings.HasPrefix(n, ".env") && n != "env.example" {
		return false
	}
	for _, marker := range []string{"example", "sample", "template", "dist"} {
		if strings.Contains(n, marker) {
			return true
		}
	}
	return false
}

// DiscoverEnvVars scans repoRoot for environment references. Results are
// sorted by key so output never depends on filesystem or map iteration order.
func DiscoverEnvVars(repoRoot string) []*domain.EnvVar {
	found := make(map[string]*domain.EnvVar)

	add := func(key string, src domain.EnvVarSource, def string, required bool) {
		key = strings.TrimSpace(key)
		if key == "" || ignoredKey(key) {
			return
		}
		if ev, ok := found[key]; ok {
			// Prefer a concrete example default over a code-only stub.
			if ev.Default == "" && def != "" {
				ev.Default = def
				ev.Value = def
				ev.Source = src
				ev.Required = required
			}
			return
		}
		found[key] = &domain.EnvVar{Key: key, Value: def, Default: def, Source: src, Required: required}
	}

	_ = filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != repoRoot && walkSkip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if isExampleEnvFile(name) {
			if m, err := ParseEnvFile(path); err == nil {
				keys := make([]string, 0, len(m))
				for k := range m {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					add(k, domain.EnvSourceExample, m[k], m[k] == "")
				}
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range jsEnvRe.FindAllStringSubmatch(string(data), -1) {
				key := m[1]
				if key == "" {
					key = m[2]
				}
				add(key, domain.EnvSourceCode, "", true)
			}
		case ".py":
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			text := string(data)
			for _, m := range pyEnvRe.FindAllStringSubmatch(text, -1) {
				add(m[1], domain.EnvSourceCode, "", true)
			}
			for _, line := range strings.Split(text, "\n") {
				if m := pyFieldRe.FindStringSubmatch(line); m != nil {
					add(m[1], domain.EnvSourceSchema, "", true)
					continue
				}
				if m := pyAnnotRe.FindStringSubmatch(line); m != nil {
					add(m[1], domain.EnvSourceSchema, "", true)
				}
			}
		}
		return nil
	})

	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*domain.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, found[k])
	}
	return out
}

func ignoredKey(key string) bool {
	if strings.HasSuffix(key, "_") {
		return true
	}
	switch strings.ToLower(key) {
	case "node_env", "path", "home", "pwd", "user", "lang", "term":
		return true
	}
	return false
}

// IsSecretName reports whether a variable name denotes a secret. It splits on
// separators and matches whole segments, so MONKEY and TOKENIZERS are not
// considered secrets while API_KEY and AUTHORIZATION are.
func IsSecretName(name string) bool {
	up := strings.ToUpper(name)
	segs := strings.FieldsFunc(up, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})
	for _, s := range segs {
		if secretSegments[s] {
			return true
		}
	}
	return false
}

// IsValidEnvName reports whether name is a valid shell/Docker environment
// variable name: [A-Za-z_][A-Za-z0-9_]*.
func IsValidEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func isVendorKey(name string) bool {
	up := strings.ToUpper(name)
	for _, p := range vendorPrefixes {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

func isPlaceholder(val string) bool {
	v := strings.ToLower(strings.TrimSpace(val))
	if v == "" {
		return true
	}
	for _, marker := range []string{"your_", "your-", "<", "change-me", "changeme", "placeholder", "replace-me", "replace_me", "dummy", "todo", "xxxx"} {
		if strings.Contains(v, marker) {
			return true
		}
	}
	return false
}

// GenerateDevSecret returns a 128-bit random secret with the given prefix.
func GenerateDevSecret(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", domain.E(domain.KindConfig, "envvar.GenerateDevSecret", "crypto/rand failed", err)
	}
	if prefix == "" {
		prefix = "dev"
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

// MergeEnvironment applies Spool's precedence: discovered defaults < synthesized
// secrets < infra bindings < existing user env. The returned map is safe to
// serialize deterministically (keys are written sorted by WriteEnvFile).
func MergeEnvironment(existing map[string]string, discovered []*domain.EnvVar, infraBindings map[string]string) (map[string]string, error) {
	merged := make(map[string]string)

	sorted := append([]*domain.EnvVar(nil), discovered...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	for _, ev := range sorted {
		val := firstNonEmpty(ev.Value, ev.Default)
		if isPlaceholder(val) {
			if IsSecretName(ev.Key) && !isVendorKey(ev.Key) {
				secret, err := GenerateDevSecret("dev")
				if err != nil {
					return nil, err
				}
				val = secret
			} else {
				val = ""
			}
		}
		if endsWithOrigins(ev.Key) && strings.Contains(val, ",") && !strings.HasPrefix(strings.TrimSpace(val), "[") {
			val = originsToJSON(val)
		}
		merged[ev.Key] = val
	}

	for k, v := range infraBindings {
		merged[k] = v
	}
	for k, v := range existing {
		if strings.TrimSpace(k) == "" {
			continue
		}
		merged[k] = v
	}
	return merged, nil
}

func endsWithOrigins(key string) bool {
	return strings.HasSuffix(strings.ToUpper(key), "ORIGINS")
}

func originsToJSON(v string) string {
	parts := strings.Split(v, ",")
	quoted := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		quoted = append(quoted, strconv.Quote(p))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// RenderEnv serializes an env map with deterministic, sorted keys.
func RenderEnv(envMap map[string]string) string {
	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Generated by Spool — autonomous infrastructure control plane\n\n")
	for _, k := range keys {
		v := envMap[k]
		switch {
		case strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]"):
			b.WriteString(k + "=" + v + "\n")
		case v == "":
			b.WriteString(k + "=\n")
		case strings.ContainsAny(v, " #\"'") || strings.ContainsAny(v, "\n\t"):
			b.WriteString(k + "=" + strconv.Quote(v) + "\n")
		default:
			b.WriteString(k + "=" + v + "\n")
		}
	}
	return b.String()
}

// WriteEnvFile writes the environment atomically with 0600 permissions.
func WriteEnvFile(path string, envMap map[string]string) error {
	if err := safefs.EnsureDir(filepath.Dir(path), 0o700); err != nil {
		return domain.E(domain.KindIO, "envvar.WriteEnvFile", "create dir", err)
	}
	if err := safefs.WriteFileAtomic(path, []byte(RenderEnv(envMap)), 0o600); err != nil {
		return domain.E(domain.KindIO, "envvar.WriteEnvFile", "write", err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// MergeWithExistingFile loads an existing env file (if any) and merges it.
func MergeWithExistingFile(path string, discovered []*domain.EnvVar, infraBindings map[string]string) (map[string]string, error) {
	existing := map[string]string{}
	if m, err := ParseEnvFile(path); err == nil {
		existing = m
	}
	return MergeEnvironment(existing, discovered, infraBindings)
}

// FormatSummary returns a deterministic "N variables (M secrets)" summary.
func FormatSummary(envMap map[string]string) string {
	secrets := 0
	for k := range envMap {
		if IsSecretName(k) {
			secrets++
		}
	}
	return fmt.Sprintf("%d variables (%d secret-classified)", len(envMap), secrets)
}
