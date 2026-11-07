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
	for _, want := range []string{"FROM node:22-alpine", "npm ci", "CMD [\"sh\",\"-c\",\"npm run start\"]", "EXPOSE 3000"} {
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

func TestNestedIndependentNodeUsesOwnContext(t *testing.T) {
	repo := t.TempDir()
	work := t.TempDir()
	p := &domain.Project{
		ID: "demo", SourcePath: repo, WorkDir: work,
		Services: []*domain.Service{
			{Name: "root", Path: ".", Language: domain.LangNode, Framework: domain.FrameworkNextJS, PackageManager: domain.PMNpm, Port: 3000, HostPort: 3000},
			{Name: "nested", Path: "portfolio-v2", Language: domain.LangNode, Framework: domain.FrameworkNextJS, PackageManager: domain.PMNpm, Port: 3000, HostPort: 3000},
		},
	}
	out, err := GenerateWithOptions(p, Options{RepoRoot: repo, WorkDir: work, Monorepo: false})
	if err != nil {
		t.Fatal(err)
	}
	compose := out.Files["docker-compose.yml"]
	if !strings.Contains(compose, "portfolio-v2") {
		t.Fatalf("expected nested service in compose:\n%s", compose)
	}
	ignore := out.Files[".dockerignore"]
	if !strings.Contains(ignore, "portfolio-v2") {
		t.Fatalf("expected nested dir in .dockerignore:\n%s", ignore)
	}
	nested := out.Files["Dockerfile.nested"]
	if strings.Contains(nested, "cd /app/portfolio-v2") || !strings.Contains(nested, "WORKDIR /app") {
		t.Fatalf("nested service should build from its own context:\n%s", nested)
	}
}

func TestWorkspaceNodeKeepsRootContext(t *testing.T) {
	repo := t.TempDir()
	work := t.TempDir()
	p := &domain.Project{
		ID: "demo", SourcePath: repo, WorkDir: work,
		Services: []*domain.Service{
			{Name: "web", Path: "apps/web", Language: domain.LangNode, Framework: domain.FrameworkNextJS, PackageManager: domain.PMPnpm, WorkspaceMember: true, Port: 3000, HostPort: 3000},
		},
	}
	out, err := GenerateWithOptions(p, Options{RepoRoot: repo, WorkDir: work, Monorepo: true})
	if err != nil {
		t.Fatal(err)
	}
	df := out.Files["Dockerfile.web"]
	if !strings.Contains(df, "WORKDIR /app/apps/web") || !strings.Contains(df, "cd /app") {
		t.Fatalf("workspace service should build from repo root:\n%s", df)
	}
	if strings.Contains(out.Files[".dockerignore"], "apps/web") {
		t.Fatalf("workspace member must not be excluded:\n%s", out.Files[".dockerignore"])
	}
}

func TestDockerOnlyManifest(t *testing.T) {
	repo := t.TempDir()
	df := "FROM nginx:alpine\nEXPOSE 9090\nCMD [\"nginx\",\"-g\",\"daemon off;\"]\n"
	if err := os.WriteFile(filepath.Join(repo, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &domain.Service{Name: "site", Path: ".", Language: domain.LangDocker, Framework: domain.FrameworkDocker, Port: 9090, HostPort: 9090}
	if got := GenerateDockerfile(svc, Options{RepoRoot: repo}); got != df {
		t.Fatalf("existing Dockerfile not preserved:\n%s", got)
	}
	compose, err := GenerateComposeFile(&domain.Project{ID: "demo", SourcePath: repo, WorkDir: repo, Services: []*domain.Service{svc}}, ".env")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compose, "healthcheck") {
		t.Fatalf("docker-only app must not get a curl healthcheck:\n%s", compose)
	}
}
