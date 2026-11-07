package cmd

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/state"
)

func newEnvPanelFixture(t *testing.T) (*state.Manager, *domain.Lock) {
	t.Helper()
	t.Setenv("SPOOL_HOME", t.TempDir())
	id := state.IDFor("demo", "/tmp/demo-src")
	mgr, err := state.Ensure(id)
	if err != nil {
		t.Fatal(err)
	}
	lock := &domain.Lock{
		Version: "test", Project: "demo", ProjectID: id, RepoPath: "/tmp/demo-src",
		WorkDir: mgr.Dir, NetworkName: id + "-net", LastInit: time.Now(),
		Env: []*domain.EnvVar{
			{Key: "DATABASE_URL", Source: domain.EnvSourceCode},
			{Key: "JWT_SECRET", Source: domain.EnvSourceCode},
			{Key: "PORT", Source: domain.EnvSourceInfra, Value: "3000", Default: "3000"},
		},
	}
	if err := mgr.SaveLock(lock); err != nil {
		t.Fatal(err)
	}
	return mgr, lock
}

func TestEnvPanelKeysUnionSorted(t *testing.T) {
	lock := &domain.Lock{Env: []*domain.EnvVar{
		{Key: "PORT"}, {Key: "API_KEY"}, {Key: "TRAILING_"},
	}}
	keys := envPanelKeys(lock, map[string]string{"ZZZ_CUSTOM": "1", "PORT": "3000"})
	want := []string{"API_KEY", "PORT", "ZZZ_CUSTOM"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestApplyEnvEditPersistsOverrideAndEnvFile(t *testing.T) {
	mgr, lock := newEnvPanelFixture(t)
	overrides := map[string]string{}
	if err := applyEnvEdit(mgr, lock, overrides, "JWT_SECRET", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if overrides["JWT_SECRET"] != "s3cret" {
		t.Fatalf("override = %q", overrides["JWT_SECRET"])
	}
	saved, err := mgr.LoadOverrides()
	if err != nil || saved["JWT_SECRET"] != "s3cret" {
		t.Fatalf("saved overrides = %v, err=%v", saved, err)
	}
	env, err := mgr.LoadEnv()
	if err != nil || env["JWT_SECRET"] != "s3cret" {
		t.Fatalf("env file = %v, err=%v", env, err)
	}
	if err := applyEnvUnset(mgr, lock, overrides, "JWT_SECRET"); err != nil {
		t.Fatal(err)
	}
	saved, _ = mgr.LoadOverrides()
	if saved["JWT_SECRET"] != "" {
		t.Fatalf("unset override = %q, want empty", saved["JWT_SECRET"])
	}
}

func TestEnvPanelEditFlow(t *testing.T) {
	mgr, lock := newEnvPanelFixture(t)
	m, err := newEnvPanelModel(mgr, lock)
	if err != nil {
		t.Fatal(err)
	}
	m.height = 30
	// DATABASE_URL is first alphabetically; move to JWT_SECRET and edit it.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.keys[m.cursor] != "JWT_SECRET" {
		t.Fatalf("cursor on %q", m.keys[m.cursor])
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != envModeEdit || m.editKey != "JWT_SECRET" {
		t.Fatalf("mode=%v editKey=%q", m.mode, m.editKey)
	}
	m.input.SetValue("new-secret")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != envModeList || m.changed != 1 {
		t.Fatalf("mode=%v changed=%d", m.mode, m.changed)
	}
	if !strings.Contains(m.View(), "saved JWT_SECRET=") {
		t.Fatalf("status missing:\n%s", m.View())
	}
	saved, _ := mgr.LoadOverrides()
	if saved["JWT_SECRET"] != "new-secret" {
		t.Fatalf("override = %q", saved["JWT_SECRET"])
	}
}

func TestEnvPanelNewKeyFlowRejectsInvalidName(t *testing.T) {
	mgr, lock := newEnvPanelFixture(t)
	m, err := newEnvPanelModel(mgr, lock)
	if err != nil {
		t.Fatal(err)
	}
	m.height = 30
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.mode != envModeNewKey {
		t.Fatalf("mode = %v", m.mode)
	}
	m.input.SetValue("9BAD NAME")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != envModeNewKey || m.errText == "" {
		t.Fatalf("expected invalid-name error, mode=%v err=%q", m.mode, m.errText)
	}
	m.input.SetValue("MY_NEW_VAR")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != envModeEdit || m.editKey != "MY_NEW_VAR" {
		t.Fatalf("mode=%v editKey=%q", m.mode, m.editKey)
	}
	m.input.SetValue("v1")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	saved, _ := mgr.LoadOverrides()
	if saved["MY_NEW_VAR"] != "v1" {
		t.Fatalf("override = %q", saved["MY_NEW_VAR"])
	}
}

func TestEnvPanelSecretMasking(t *testing.T) {
	mgr, lock := newEnvPanelFixture(t)
	m, err := newEnvPanelModel(mgr, lock)
	if err != nil {
		t.Fatal(err)
	}
	m.height = 30
	m.overrides["JWT_SECRET"] = "super-secret"
	view := m.View()
	if strings.Contains(view, "super-secret") {
		t.Fatal("secret leaked in masked view")
	}
	if !strings.Contains(view, "••••••••") {
		t.Fatal("mask missing")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	if !strings.Contains(m.View(), "super-secret") {
		t.Fatal("reveal did not show the secret")
	}
}

func TestPartitionURLsGatesOnRunning(t *testing.T) {
	pairs := []urlPair{
		{Service: "web", URL: "http://localhost:3000"},
		{Service: "api", URL: "http://localhost:4000"},
	}
	ps := []domain.Container{
		{Service: "web", State: "running", Health: "starting"},
		{Service: "api", State: "exited"},
	}
	live, stopped := partitionURLs(pairs, ps)
	if len(live) != 1 || live[0].Service != "web" {
		t.Fatalf("live = %v", live)
	}
	if len(stopped) != 1 || stopped[0].Service != "api" {
		t.Fatalf("stopped = %v", stopped)
	}
	// Missing containers count as stopped.
	live, stopped = partitionURLs(pairs, nil)
	if len(live) != 0 || len(stopped) != 2 {
		t.Fatalf("live=%v stopped=%v", live, stopped)
	}
}
