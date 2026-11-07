package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/state"
)

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInitPipelineNoRunE2E(t *testing.T) {
	t.Setenv("SPOOL_HOME", t.TempDir())
	repo := t.TempDir()
	writeFixture(t, repo, "package.json", `{
		"name": "demo-web",
		"private": true,
		"dependencies": {"next": "16.0.0", "react": "19.0.0"},
		"scripts": {"build": "next build", "start": "next start"}
	}`)
	writeFixture(t, repo, "services/api/pyproject.toml", `[project]
name = "demo-api"
requires-python = ">=3.11"
dependencies = ["fastapi>=0.110", "uvicorn[standard]>=0.29", "psycopg2-binary>=2.9"]
`)
	writeFixture(t, repo, "services/api/main.py", "from fastapi import FastAPI\napp = FastAPI()\n")
	writeFixture(t, repo, "services/api/.env.example", "DATABASE_URL=\nJWT_SECRET=\nCORS_ORIGINS=http://localhost:3000,http://localhost:5173\n")
	writeFixture(t, repo, "services/api/.env", "DATABASE_URL=\nJWT_SECRET=local-dev-secret\n")

	// Save and restore global flags used by runInit.
	oldQuiet, oldNoRun, oldNoAgent, oldMax, oldName := flagQuiet, initNoRun, initNoAgent, initMaxSvc, initName
	flagQuiet, initNoRun, initNoAgent, initMaxSvc, initName = true, true, true, 12, ""
	defer func() {
		flagQuiet, initNoRun, initNoAgent, initMaxSvc, initName = oldQuiet, oldNoRun, oldNoAgent, oldMax, oldName
	}()

	if err := runInit(nil, []string{repo}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	managers, err := state.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(managers) != 1 {
		t.Fatalf("expected 1 project, got %d", len(managers))
	}
	mgr := managers[0]
	lock, err := mgr.LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Services) < 2 {
		t.Fatalf("expected >=2 services, got %d", len(lock.Services))
	}
	var havePostgres bool
	for _, inf := range lock.Infra {
		if inf.Type == domain.InfraPostgres && inf.Mode == domain.InfraModeLocal {
			havePostgres = true
		}
	}
	if !havePostgres {
		t.Fatalf("expected a local postgres, got %+v", lock.Infra)
	}
	if lock.DetectionHash == "" {
		t.Fatal("missing detection hash")
	}
	if lock.PortMap["demo-web"] == 0 {
		t.Fatalf("missing host port map: %+v", lock.PortMap)
	}

	composePath := filepath.Join(mgr.Dir, "docker-compose.yml")
	compose, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(compose)
	if strings.Contains(text, repo) || strings.Contains(text, mgr.Dir) {
		t.Fatalf("compose must use portable relative paths:\n%s", text)
	}
	if !strings.Contains(text, "service_healthy") {
		t.Fatalf("expected health-gated dependency:\n%s", text)
	}
	for _, svc := range lock.Services {
		df := filepath.Join(mgr.Dir, "Dockerfile."+svc.Name)
		if _, err := os.Stat(df); err != nil {
			t.Errorf("missing %s: %v", df, err)
		}
	}

	envPath := filepath.Join(mgr.Dir, ".env")
	info, err := os.Stat(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %o", info.Mode().Perm())
	}
	env, _ := os.ReadFile(envPath)
	if !strings.Contains(string(env), "postgres:5432") {
		t.Fatalf("expected internal postgres URL:\n%s", env)
	}
	if strings.Contains(string(env), "JWT_SECRET=\n") {
		t.Fatalf("repo .env secret was not imported:\n%s", env)
	}
}

func TestUpdatePreservesHostPortsE2E(t *testing.T) {
	t.Setenv("SPOOL_HOME", t.TempDir())
	repo := t.TempDir()
	writeFixture(t, repo, "package.json", `{"name":"demo-web","dependencies":{"next":"16.0.0"},"scripts":{"build":"next build","start":"next start"}}`)
	writeFixture(t, repo, "services/api/pyproject.toml", "[project]\nname=\"demo-api\"\ndependencies=[\"fastapi\",\"psycopg2-binary\"]\n")
	writeFixture(t, repo, "services/api/main.py", "from fastapi import FastAPI\napp=FastAPI()\n")
	writeFixture(t, repo, "services/api/.env.example", "JWT_SECRET=\n")

	oldQuiet, oldNoRun, oldNoAgent, oldMax, oldName := flagQuiet, initNoRun, initNoAgent, initMaxSvc, initName
	flagQuiet, initNoRun, initNoAgent, initMaxSvc, initName = true, true, true, 12, ""
	defer func() {
		flagQuiet, initNoRun, initNoAgent, initMaxSvc, initName = oldQuiet, oldNoRun, oldNoAgent, oldMax, oldName
	}()
	if err := runInit(nil, []string{repo}); err != nil {
		t.Fatal(err)
	}
	managers, _ := state.List()
	if len(managers) != 1 {
		t.Fatalf("expected 1 project, got %d", len(managers))
	}
	mgr := managers[0]
	before, err := mgr.LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	webPort := before.PortMap["demo-web"]
	if webPort == 0 {
		t.Fatalf("missing web port: %+v", before.PortMap)
	}

	writeFixture(t, repo, "apps/extra/package.json", `{"name":"extra","dependencies":{"express":"4"},"scripts":{"start":"node server.js"}}`)

	oldUR, oldUA := updateNoRun, updateNoAgent
	updateNoRun, updateNoAgent = true, true
	defer func() { updateNoRun, updateNoAgent = oldUR, oldUA }()
	if err := runUpdate(nil, []string{mgr.ID}); err != nil {
		t.Fatal(err)
	}

	after, err := mgr.LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	if after.PortMap["demo-web"] != webPort {
		t.Fatalf("host port changed: %d -> %d", webPort, after.PortMap["demo-web"])
	}
	found := false
	for _, svc := range after.Services {
		if svc.Name == "extra" {
			found = true
			if svc.HostPort == 0 {
				t.Fatal("new service has no host port")
			}
		}
	}
	if !found {
		t.Fatalf("update did not detect the new service: %+v", after.Services)
	}
	if _, err := os.Stat(filepath.Join(mgr.Dir, "Dockerfile.extra")); err != nil {
		t.Fatalf("missing new Dockerfile: %v", err)
	}
}

func TestAskOnboardNoRunE2E(t *testing.T) {
	t.Setenv("SPOOL_HOME", t.TempDir())
	repo := t.TempDir()
	writeFixture(t, repo, "package.json", `{"name":"ask-onboard","dependencies":{"next":"16"},"scripts":{"build":"next build","start":"next start"}}`)

	oldQuiet, oldYes := flagQuiet, flagYes
	flagQuiet, flagYes = true, true
	defer func() { flagQuiet, flagYes = oldQuiet, oldYes }()

	if err := runAsk(nil, []string{"onboard", repo, "--no-run"}); err != nil {
		t.Fatalf("ask onboard: %v", err)
	}
	managers, err := state.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(managers) != 1 {
		t.Fatalf("expected 1 project, got %d", len(managers))
	}
	lock, err := managers[0].LoadLock()
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Services) != 1 {
		t.Fatalf("expected 1 service, got %+v", lock.Services)
	}
}
