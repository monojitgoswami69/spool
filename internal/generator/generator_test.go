package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func pyService(deps ...string) *domain.Service {
	return &domain.Service{
		Name: "api", Path: ".", Language: domain.LangPython, Framework: domain.FrameworkFastAPI,
		PackageManager: domain.PMPip, StartCmd: "uvicorn main:app --host 0.0.0.0 --port 8000",
		Port: 8000, HostPort: 8000, DefaultPort: 8000, Dependencies: deps,
	}
}

func TestPythonDockerfileNoPyTorchIndexByDefault(t *testing.T) {
	out := GenerateDockerfile(pyService("fastapi", "uvicorn"), Options{})
	if strings.Contains(out, "pytorch.org") {
		t.Fatalf("pytorch index must not be injected without torch:\n%s", out)
	}
	if !strings.Contains(out, "FROM python:3.11-slim") {
		t.Fatalf("missing python base:\n%s", out)
	}
}

func TestPythonDockerfilePyTorchOnlyWhenDetected(t *testing.T) {
	out := GenerateDockerfile(pyService("torch", "fastapi"), Options{})
	if !strings.Contains(out, "download.pytorch.org/whl/cpu") {
		t.Fatalf("expected pytorch index when torch detected:\n%s", out)
	}
}

func TestNodeDockerfile(t *testing.T) {
	svc := &domain.Service{
		Name: "web", Path: "apps/web", Language: domain.LangNode, Framework: domain.FrameworkNextJS,
		PackageManager: domain.PMPnpm, BuildCmd: "pnpm run build", StartCmd: "pnpm run start",
		Port: 3000, HostPort: 3002, DefaultPort: 3000,
	}
	out := GenerateDockerfile(svc, Options{Monorepo: true})
	for _, want := range []string{"FROM node:20-alpine", "corepack enable", "pnpm install", "EXPOSE 3000", "CMD ["} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in:\n%s", want, out)
		}
	}
}

func TestSPADockerfileUsesNginx(t *testing.T) {
	svc := &domain.Service{
		Name: "front", Path: ".", Language: domain.LangNode, Framework: domain.FrameworkVite,
		PackageManager: domain.PMNpm, BuildCmd: "npm run build", IsSPA: true,
		Port: 5173, HostPort: 5173, DefaultPort: 5173,
	}
	out := GenerateDockerfile(svc, Options{})
	for _, want := range []string{"nginx:alpine", "/dist", "EXPOSE 5173"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in:\n%s", want, out)
		}
	}
}

func TestGoDockerfile(t *testing.T) {
	svc := &domain.Service{
		Name: "svc", Path: ".", Language: domain.LangGo, Framework: domain.FrameworkGo,
		BuildCmd: "go build -o app .", StartCmd: "./app", Port: 9090, HostPort: 9090,
	}
	out := GenerateDockerfile(svc, Options{})
	if !strings.Contains(out, "FROM golang:1.23-alpine") || !strings.Contains(out, "CGO_ENABLED=0 go build") {
		t.Fatalf("unexpected go dockerfile:\n%s", out)
	}
}

func TestComposePortableWithInfraAndExternal(t *testing.T) {
	work := t.TempDir()
	src := t.TempDir()
	p := &domain.Project{
		ID: "abc", SourcePath: src, WorkDir: work, NetworkName: "spool-abc-net",
		Services: []*domain.Service{{
			Name: "web", Path: ".", Language: domain.LangNode, Framework: domain.FrameworkNextJS,
			Port: 3000, HostPort: 3002, DefaultPort: 3000,
		}},
		InfraServices: []*domain.InfraService{
			{Type: domain.InfraPostgres, Name: "postgres", Image: "pgvector/pgvector:pg16", Port: 5432, HostPort: 5433, Mode: domain.InfraModeLocal,
				Healthcheck: domain.HealthcheckSpec{Test: []string{"CMD-SHELL", "pg_isready"}, Interval: "5s", Timeout: "3s", Retries: 5}},
			{Type: domain.InfraRedis, Name: "redis", Image: "redis:7-alpine", Port: 6379, Mode: domain.InfraModeExternal, Provider: "upstash"},
		},
	}
	out, err := GenerateComposeFile(p, ".env")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "5433:5432") {
		t.Fatalf("missing infra host port mapping:\n%s", out)
	}
	if strings.Contains(out, "redis:7-alpine") {
		t.Fatalf("external infra must be excluded:\n%s", out)
	}
	if !strings.Contains(out, "curl -fsS http://localhost:3000/") {
		t.Fatalf("missing app healthcheck:\n%s", out)
	}
	if strings.Contains(out, src) || strings.Contains(out, work) {
		t.Fatalf("compose must not contain absolute paths:\n%s", out)
	}
	if !strings.Contains(out, "Dockerfile.web") {
		t.Fatalf("missing dockerfile reference:\n%s", out)
	}
	if !strings.Contains(out, "condition: service_healthy") {
		t.Fatalf("expected healthy dependency:\n%s", out)
	}
}

func TestWriteOutputs(t *testing.T) {
	work := t.TempDir()
	src := t.TempDir()
	p := &domain.Project{
		ID: "abc", SourcePath: src, WorkDir: work,
		Services: []*domain.Service{{
			Name: "web", Path: ".", Language: domain.LangNode, Framework: domain.FrameworkNextJS,
			PackageManager: domain.PMNpm, StartCmd: "npm run start", Port: 3000, HostPort: 3000,
		}},
	}
	out, err := WriteOutputs(p, work, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "docker-compose.yml")); err != nil {
		t.Fatal("compose not written")
	}
	if _, err := os.Stat(filepath.Join(work, "Dockerfile.web")); err != nil {
		t.Fatal("dockerfile not written")
	}
	if _, err := os.Stat(filepath.Join(src, ".dockerignore")); err != nil {
		t.Fatal(".dockerignore not written to repo root")
	}
	if p.Services[0].DockerfilePath == "" {
		t.Fatal("service DockerfilePath not set")
	}
	if len(out.Files) == 0 {
		t.Fatal("empty output")
	}
}

func TestDockerignore(t *testing.T) {
	c := DockerignoreContent()
	for _, want := range []string{"node_modules", ".env", ".next", "target"} {
		if !strings.Contains(c, want) {
			t.Errorf("dockerignore missing %q", want)
		}
	}
}
