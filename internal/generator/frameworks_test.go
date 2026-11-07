package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func nodeSvc(fw domain.Framework, pm domain.PackageManager, build, start string, port int) *domain.Service {
	return &domain.Service{
		Name: "app", Path: ".", Language: domain.LangNode, Framework: fw,
		PackageManager: pm, BuildCmd: build, StartCmd: start,
		DefaultPort: port, Port: port, HostPort: port,
	}
}

func pySvc(fw domain.Framework, start string, port int) *domain.Service {
	return &domain.Service{
		Name: "api", Path: ".", Language: domain.LangPython, Framework: fw,
		PackageManager: domain.PMPip, StartCmd: start,
		DefaultPort: port, Port: port, HostPort: port,
	}
}

func TestExpressDockerfile(t *testing.T) {
	out := GenerateDockerfile(nodeSvc(domain.FrameworkExpress, domain.PMNpm, "", "npm run start", 3000), Options{})
	for _, want := range []string{"FROM node:20-alpine", "npm ci", "CMD [\"sh\",\"-c\",\"npm run start\"]", "EXPOSE 3000"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestNextDockerfileBuilds(t *testing.T) {
	out := GenerateDockerfile(nodeSvc(domain.FrameworkNextJS, domain.PMPnpm, "pnpm run build", "pnpm run start", 3000), Options{})
	for _, want := range []string{"corepack enable", "pnpm install", "pnpm run build", "pnpm run start"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "|| echo") {
		t.Errorf("build errors must not be swallowed:\n%s", out)
	}
}

func TestFastAPIDockerfile(t *testing.T) {
	out := GenerateDockerfile(pySvc(domain.FrameworkFastAPI, "uvicorn main:app --host 0.0.0.0 --port 8000", 8000), Options{})
	for _, want := range []string{"FROM python:3.11-slim", "uvicorn main:app", "EXPOSE 8000"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "pytorch.org") {
		t.Errorf("unexpected pytorch index:\n%s", out)
	}
}

func TestDjangoDockerfile(t *testing.T) {
	out := GenerateDockerfile(pySvc(domain.FrameworkDjango, "python manage.py runserver 0.0.0.0:8000", 8000), Options{})
	if !strings.Contains(out, "python manage.py runserver 0.0.0.0:8000") {
		t.Fatalf("missing django command:\n%s", out)
	}
}

func TestFlaskDockerfile(t *testing.T) {
	out := GenerateDockerfile(pySvc(domain.FrameworkFlask, "flask --app app run --host=0.0.0.0 --port=5000", 5000), Options{})
	if !strings.Contains(out, "flask --app app run") || !strings.Contains(out, "EXPOSE 5000") {
		t.Fatalf("missing flask command/port:\n%s", out)
	}
}

func TestStaticDockerfile(t *testing.T) {
	svc := &domain.Service{Name: "site", Path: ".", Language: domain.LangStatic, Framework: domain.FrameworkStatic, Port: 80, DefaultPort: 80}
	out := GenerateDockerfile(svc, Options{})
	for _, want := range []string{"FROM nginx:alpine", "/usr/share/nginx/html", "EXPOSE 80"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestRequirementsFilePreferred(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "requirements.txt"), []byte("fastapi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pyproject.toml"), []byte("[project]\nname=\"x\"\ndependencies=[\"fastapi\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := GenerateDockerfile(pySvc(domain.FrameworkFastAPI, "uvicorn main:app", 8000), Options{RepoRoot: repo})
	if !strings.Contains(out, "-r requirements.txt") {
		t.Fatalf("expected requirements install:\n%s", out)
	}
}

func TestTorchIndexOnlyWithTorch(t *testing.T) {
	withTorch := pySvc(domain.FrameworkFastAPI, "uvicorn main:app", 8000)
	withTorch.Dependencies = []string{"torch", "fastapi"}
	out := GenerateDockerfile(withTorch, Options{})
	if !strings.Contains(out, "download.pytorch.org/whl/cpu") {
		t.Fatalf("expected torch index:\n%s", out)
	}
	without := pySvc(domain.FrameworkFastAPI, "uvicorn main:app", 8000)
	without.Dependencies = []string{"fastapi"}
	out = GenerateDockerfile(without, Options{})
	if strings.Contains(out, "pytorch.org") {
		t.Fatalf("unexpected torch index:\n%s", out)
	}
}
