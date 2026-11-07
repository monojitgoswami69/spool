// Package state persists per-project state under ~/.spool/projects/<id>.
// Writes are atomic and 0600; deletion is confined by safefs.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/safefs"
)

const (
	lockFile      = "spool.lock.json"
	envFile       = ".env"
	overridesFile = "env-overrides.json"
	repairsFile   = "repairs.json"
)

// Manager owns one project's state directory.
type Manager struct {
	ID  string
	Dir string
}

// Ensure creates (if needed) and returns the manager for id.
func Ensure(id string) (*Manager, error) {
	if !config.ValidProjectID(id) {
		return nil, domain.E(domain.KindUnsafePath, "state.Ensure", "invalid project id "+id, nil)
	}
	root, err := config.ProjectsDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, id)
	if err := safefs.EnsureDir(dir, 0o700); err != nil {
		return nil, domain.E(domain.KindIO, "state.Ensure", "create project dir", err)
	}
	return &Manager{ID: id, Dir: dir}, nil
}

// For returns a manager without creating directories.
func For(id string) (*Manager, error) {
	if !config.ValidProjectID(id) {
		return nil, domain.E(domain.KindUnsafePath, "state.For", "invalid project id "+id, nil)
	}
	root, err := config.ProjectsDir()
	if err != nil {
		return nil, err
	}
	return &Manager{ID: id, Dir: filepath.Join(root, id)}, nil
}

func (m *Manager) LockPath() string      { return filepath.Join(m.Dir, lockFile) }
func (m *Manager) EnvPath() string       { return filepath.Join(m.Dir, envFile) }
func (m *Manager) OverridesPath() string { return filepath.Join(m.Dir, overridesFile) }
func (m *Manager) RepairsPath() string   { return filepath.Join(m.Dir, repairsFile) }
func (m *Manager) ComposePath() string   { return filepath.Join(m.Dir, "docker-compose.yml") }

// SaveLock writes the lock atomically.
func (m *Manager) SaveLock(l *domain.Lock) error {
	if l == nil {
		return domain.E(domain.KindValidation, "state.SaveLock", "nil lock", nil)
	}
	if err := safefs.EnsureDir(m.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return domain.E(domain.KindValidation, "state.SaveLock", "marshal", err)
	}
	return safefs.WriteFileAtomic(m.LockPath(), append(data, '\n'), 0o600)
}

// LoadLock reads the lock, returning MissingLockError when absent.
func (m *Manager) LoadLock() (*domain.Lock, error) {
	data, err := os.ReadFile(m.LockPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &domain.MissingLockError{Project: m.ID}
		}
		return nil, domain.E(domain.KindIO, "state.LoadLock", "read lock", err)
	}
	var l domain.Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, domain.E(domain.KindValidation, "state.LoadLock", "parse lock", err)
	}
	return &l, nil
}

// SaveEnv writes the project .env with 0600 permissions.
func (m *Manager) SaveEnv(env map[string]string) error {
	data := renderEnv(env)
	return safefs.WriteFileAtomic(m.EnvPath(), []byte(data), 0o600)
}

// LoadEnv reads the project .env (empty map when absent).
func (m *Manager) LoadEnv() (map[string]string, error) {
	data, err := os.ReadFile(m.EnvPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	return parseEnv(string(data)), nil
}

// SaveOverrides persists dashboard env overrides.
func (m *Manager) SaveOverrides(o map[string]string) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return safefs.WriteFileAtomic(m.OverridesPath(), append(data, '\n'), 0o600)
}

// LoadOverrides reads dashboard env overrides.
func (m *Manager) LoadOverrides() (map[string]string, error) {
	data, err := os.ReadFile(m.OverridesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RenderEnvWithOverrides merges base env (from the lock) with saved overrides.
func (m *Manager) RenderEnvWithOverrides(base map[string]string) (map[string]string, error) {
	overrides, err := m.LoadOverrides()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out, nil
}

// Delete removes the project's state directory, guarded by safefs.
func (m *Manager) Delete() error {
	root, err := config.ProjectsDir()
	if err != nil {
		return err
	}
	return safefs.SafeRemoveAll(m.Dir, root)
}

// List returns all projects that have a lock, most recent first.
func List() ([]*Manager, error) {
	root, err := config.ProjectsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type item struct {
		m    *Manager
		when time.Time
	}
	var items []item
	for _, e := range entries {
		if !e.IsDir() || !config.ValidProjectID(e.Name()) {
			continue
		}
		m := &Manager{ID: e.Name(), Dir: filepath.Join(root, e.Name())}
		if _, err := os.Stat(m.LockPath()); err != nil {
			continue
		}
		lock, err := m.LoadLock()
		if err != nil {
			continue
		}
		items = append(items, item{m: m, when: lock.LastInit})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].when.Equal(items[j].when) {
			return items[i].m.ID < items[j].m.ID
		}
		return items[i].when.After(items[j].when)
	})
	out := make([]*Manager, len(items))
	for i := range items {
		out[i] = items[i].m
	}
	return out, nil
}

// Resolve returns the manager and lock for a project name. An empty name picks
// the most recent project.
func Resolve(name string) (*Manager, *domain.Lock, error) {
	managers, err := List()
	if err != nil {
		return nil, nil, err
	}
	if len(managers) == 0 {
		return nil, nil, domain.ErrNoProjects
	}
	if strings.TrimSpace(name) == "" {
		m := managers[0]
		l, err := m.LoadLock()
		return m, l, err
	}
	target := Canonicalize(name)
	var available []string
	for _, m := range managers {
		l, err := m.LoadLock()
		if err != nil {
			continue
		}
		available = append(available, m.ID)
		if m.ID == name || Canonicalize(m.ID) == target || Canonicalize(l.Project) == target {
			return m, l, nil
		}
	}
	return nil, nil, &domain.UnknownProjectError{Name: name, Available: available}
}

// Canonicalize normalizes a project name for matching (never used as a path).
func Canonicalize(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' || r == '_'
		if ok {
			b.WriteRune(r)
			lastDash = r == '-'
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-.")
}

// IDFor produces a stable, filesystem-safe project id from a name and source.
func IDFor(name, source string) string {
	base := Canonicalize(name)
	if base == "" {
		base = "project"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	sum := sha256.Sum256([]byte(source))
	return base + "-" + hex.EncodeToString(sum[:4])
}

// HashDetection hashes the detection-relevant fields of services deterministically.
func HashDetection(services []*domain.Service) string {
	type snap struct {
		Name, Path, Language, Framework, PM, Runtime, Build, Start string
		DefaultPort, Port                                          int
		IsSPA, IsWorker                                            bool
		Deps                                                       []string
	}
	list := make([]snap, 0, len(services))
	for _, s := range services {
		deps := append([]string(nil), s.Dependencies...)
		sort.Strings(deps)
		list = append(list, snap{
			Name: s.Name, Path: s.Path, Language: string(s.Language), Framework: string(s.Framework),
			PM: string(s.PackageManager), Runtime: s.RuntimeVersion, Build: s.BuildCmd, Start: s.StartCmd,
			DefaultPort: s.DefaultPort, Port: s.Port, IsSPA: s.IsSPA, IsWorker: s.IsWorker, Deps: deps,
		})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, _ := json.Marshal(list)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func renderEnv(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Generated by Spool\n")
	for _, k := range keys {
		v := env[k]
		if strings.ContainsAny(v, " #\"\n\t") {
			q, _ := json.Marshal(v)
			v = string(q)
		}
		b.WriteString(k + "=" + v + "\n")
	}
	return b.String()
}

func parseEnv(data string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			var s string
			if json.Unmarshal([]byte(v), &s) == nil {
				v = s
			}
		}
		out[k] = v
	}
	return out
}
