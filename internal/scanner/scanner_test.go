package scanner

import (
	"os"
	"path/filepath"
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

func findByPath(svcs []*domain.Service, path string) *domain.Service {
	for _, s := range svcs {
		if s.Path == path {
			return s
		}
	}
	return nil
}

func TestScanNextRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{
		"name": "web",
		"dependencies": {"next": "14.0.0", "react": "18"},
		"engines": {"node": ">=20"},
		"scripts": {"build": "next build", "start": "next start"}
	}`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 {
		t.Fatalf("expected 1 service, got %d", len(svcs))
	}
	s := svcs[0]
	if s.Framework != domain.FrameworkNextJS || s.Language != domain.LangNode || s.Port != 3000 {
		t.Fatalf("unexpected service %+v", s)
	}
	if s.RuntimeVersion != "20" {
		t.Fatalf("runtime version = %q", s.RuntimeVersion)
	}
	if s.BuildCmd == "" || s.StartCmd == "" {
		t.Fatalf("expected build/start commands: %+v", s)
	}
}

func TestScanViteSPA(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"name":"frontend","dependencies":{"vite":"5"},"scripts":{"build":"vite build","dev":"vite"}}`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	s := svcs[0]
	if s.Framework != domain.FrameworkVite || !s.IsSPA || s.Port != 5173 {
		t.Fatalf("unexpected vite service %+v", s)
	}
}

func TestScanNodeMonorepo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"name":"root","private":true,"workspaces":["apps/*"]}`)
	write(t, root, "apps/web/package.json", `{"name":"@acme/web","dependencies":{"next":"14"},"scripts":{"start":"next start","build":"next build"}}`)
	write(t, root, "apps/api/package.json", `{"name":"@acme/api","dependencies":{"express":"4"},"scripts":{"start":"node server.js"}}`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 {
		t.Fatalf("expected 2 services, got %d: %+v", len(svcs), svcs)
	}
	web := findByPath(svcs, "apps/web")
	api := findByPath(svcs, "apps/api")
	if web == nil || api == nil {
		t.Fatalf("missing workspace services: %+v", svcs)
	}
	if web.Name != "acme-web" {
		t.Fatalf("expected sanitized name acme-web, got %q", web.Name)
	}
	if api.Framework != domain.FrameworkExpress {
		t.Fatalf("api framework = %q", api.Framework)
	}
}

func TestScanPythonFastAPI(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pyproject.toml", `[project]
name = "svc"
requires-python = ">=3.11"
dependencies = ["fastapi>=0.100", "uvicorn[standard]>=0.20"]
`)
	write(t, root, "main.py", "from fastapi import FastAPI\napp = FastAPI()\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	s := svcs[0]
	if s.Framework != domain.FrameworkFastAPI || s.Language != domain.LangPython || s.Port != 8000 {
		t.Fatalf("unexpected service %+v", s)
	}
	if s.RuntimeVersion != "3.11" {
		t.Fatalf("runtime version = %q", s.RuntimeVersion)
	}
}

func TestScanPythonWorkerNamedTaskAPIIsStillWeb(t *testing.T) {
	root := t.TempDir()
	write(t, root, "pyproject.toml", `[project]
name = "task-api"
dependencies = ["fastapi"]
`)
	write(t, root, "main.py", "from fastapi import FastAPI\napp = FastAPI()\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	s := svcs[0]
	if s.Framework != domain.FrameworkFastAPI || s.IsWorker {
		t.Fatalf("task-api should remain a web service, got %+v", s)
	}
}

func TestScanGoService(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/orders\n\ngo 1.22\n")
	write(t, root, "main.go", `package main

import "net/http"

func main() {
	http.ListenAndServe(":9090", nil)
}
`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	s := svcs[0]
	if s.Language != domain.LangGo || s.Port != 9090 {
		t.Fatalf("unexpected go service %+v", s)
	}
	if s.BuildCmd != "go build -o app ." {
		t.Fatalf("build cmd = %q", s.BuildCmd)
	}
}

func TestScanGoLibraryIsNotService(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/lib\n\ngo 1.22\n")
	write(t, root, "lib.go", "package lib\n\nfunc Add(a, b int) int { return a + b }\n")
	if _, err := ScanRepository(root); err == nil {
		t.Fatal("expected no services error for a library")
	}
}

func TestScanRustService(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Cargo.toml", `[package]
name = "gateway"
version = "0.1.0"
`)
	write(t, root, "src/main.rs", "use std::net::TcpListener;\nfn main() { TcpListener::bind(\"0.0.0.0:7070\").unwrap(); }\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	s := svcs[0]
	if s.Language != domain.LangRust || s.Port != 7070 {
		t.Fatalf("unexpected rust service %+v", s)
	}
}

func TestScanNoServices(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "hello")
	if _, err := ScanRepository(root); err == nil {
		t.Fatal("expected error")
	}
}

func TestDockerfileExposePortOverride(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"name":"api","dependencies":{"express":"4"},"scripts":{"start":"node server.js"}}`)
	write(t, root, "Dockerfile", "FROM node:20\nEXPOSE 8080\nCMD [\"node\",\"server.js\"]\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].Port != 8080 {
		t.Fatalf("expected EXPOSE override 8080, got %d", svcs[0].Port)
	}
}
