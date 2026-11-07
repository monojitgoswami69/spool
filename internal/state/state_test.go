package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func setup(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SPOOL_HOME", home)
	return home
}

func TestIDForStableAndSafe(t *testing.T) {
	a := IDFor("My App!", "/tmp/app")
	b := IDFor("My App!", "/tmp/app")
	c := IDFor("My App!", "/tmp/other")
	if a != b {
		t.Fatal("IDFor must be stable")
	}
	if a == c {
		t.Fatal("different sources should differ")
	}
	if !validID(a) {
		t.Fatalf("invalid id %q", a)
	}
}

func validID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func TestLockRoundTripAndPermissions(t *testing.T) {
	setup(t)
	m, err := Ensure("demo-1234")
	if err != nil {
		t.Fatal(err)
	}
	lock := &domain.Lock{Project: "demo", ProjectID: "demo-1234", RepoPath: "/repo", WorkDir: m.Dir, LastInit: time.Now()}
	if err := m.SaveLock(lock); err != nil {
		t.Fatal(err)
	}
	got, err := m.LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "demo" || got.ProjectID != "demo-1234" {
		t.Fatalf("unexpected lock %+v", got)
	}
	info, err := os.Stat(m.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %o", info.Mode().Perm())
	}
}

func TestEnvIs0600(t *testing.T) {
	setup(t)
	m, _ := Ensure("env-1234")
	if err := m.SaveEnv(map[string]string{"A": "1", "B": "two words"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(m.EnvPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env mode = %o", info.Mode().Perm())
	}
	env, err := m.LoadEnv()
	if err != nil || env["B"] != "two words" {
		t.Fatalf("env = %+v err=%v", env, err)
	}
}

func TestOverridesPrecedence(t *testing.T) {
	setup(t)
	m, _ := Ensure("ov-1234")
	if err := m.SaveOverrides(map[string]string{"A": "override"}); err != nil {
		t.Fatal(err)
	}
	merged, err := m.RenderEnvWithOverrides(map[string]string{"A": "base", "B": "keep"})
	if err != nil {
		t.Fatal(err)
	}
	if merged["A"] != "override" || merged["B"] != "keep" {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestListAndResolve(t *testing.T) {
	setup(t)
	oldM, _ := Ensure("old-0001")
	if err := oldM.SaveLock(&domain.Lock{Project: "old", ProjectID: "old-0001", LastInit: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	newM, _ := Ensure("new-0002")
	if err := newM.SaveLock(&domain.Lock{Project: "new", ProjectID: "new-0002", LastInit: time.Now()}); err != nil {
		t.Fatal(err)
	}
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "new-0002" {
		t.Fatalf("list = %+v", list)
	}
	m, lock, err := Resolve("")
	if err != nil || m.ID != "new-0002" || lock.Project != "new" {
		t.Fatalf("resolve default = %v %v %v", m, lock, err)
	}
	m, _, err = Resolve("old")
	if err != nil || m.ID != "old-0001" {
		t.Fatalf("resolve by name = %v %v", m, err)
	}
	if _, _, err := Resolve("missing"); err == nil {
		t.Fatal("expected unknown project error")
	}
}

func TestDeleteConfined(t *testing.T) {
	setup(t)
	m, _ := Ensure("del-1234")
	if err := m.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Dir); !os.IsNotExist(err) {
		t.Fatal("project dir should be removed")
	}
}

func TestHashDetectionDeterministic(t *testing.T) {
	svcs := []*domain.Service{
		{Name: "web", Path: ".", Language: domain.LangNode, Framework: domain.FrameworkNextJS, Port: 3000, Dependencies: []string{"next", "react"}},
	}
	a := HashDetection(svcs)
	b := HashDetection(svcs)
	if a != b {
		t.Fatal("hash must be deterministic")
	}
	svcs[0].Dependencies = append(svcs[0].Dependencies, "zod")
	if HashDetection(svcs) == a {
		t.Fatal("dependency change should change the hash")
	}
}

func TestForRejectsTraversal(t *testing.T) {
	setup(t)
	if _, err := For("../escape"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := For(""); err == nil {
		t.Fatal("expected empty rejection")
	}
}

func TestComposePath(t *testing.T) {
	setup(t)
	m, _ := Ensure("cp-1234")
	if filepath.Base(m.ComposePath()) != "docker-compose.yml" {
		t.Fatal("bad compose path")
	}
}
