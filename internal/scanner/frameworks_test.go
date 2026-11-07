package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func writeRaw(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNodeFrameworkMatrix(t *testing.T) {
	cases := []struct {
		name      string
		pkg       string
		framework domain.Framework
		port      int
		spa       bool
	}{
		{"next", `{"name":"n","dependencies":{"next":"14"},"scripts":{"build":"next build","start":"next start"}}`, domain.FrameworkNextJS, 3000, false},
		{"nuxt", `{"name":"n","dependencies":{"nuxt":"3"},"scripts":{"build":"nuxt build","start":"node .output/server/index.mjs"}}`, domain.FrameworkNuxt, 3000, false},
		{"remix", `{"name":"n","dependencies":{"@remix-run/node":"2"},"scripts":{"start":"remix-serve build"}}`, domain.FrameworkRemix, 3000, false},
		{"sveltekit", `{"name":"n","dependencies":{"@sveltejs/kit":"2"},"scripts":{"start":"node build"}}`, domain.FrameworkSvelteKit, 3000, false},
		{"nestjs", `{"name":"n","dependencies":{"@nestjs/core":"10"},"scripts":{"start:prod":"node dist/main.js","build":"nest build"}}`, domain.FrameworkNestJS, 3000, false},
		{"astro", `{"name":"n","dependencies":{"astro":"4"},"scripts":{"build":"astro build","start":"astro preview"}}`, domain.FrameworkAstro, 4321, false},
		{"vite", `{"name":"n","dependencies":{"vite":"5"},"scripts":{"build":"vite build","dev":"vite"}}`, domain.FrameworkVite, 5173, true},
		{"cra", `{"name":"n","dependencies":{"react-scripts":"5"},"scripts":{"build":"react-scripts build","start":"react-scripts start"}}`, domain.FrameworkCRA, 3000, true},
		{"express", `{"name":"n","dependencies":{"express":"4"},"scripts":{"start":"node server.js"}}`, domain.FrameworkExpress, 3000, false},
		{"fastify", `{"name":"n","dependencies":{"fastify":"4"},"scripts":{"start":"node server.js"}}`, domain.FrameworkFastify, 3000, false},
		{"koa", `{"name":"n","dependencies":{"koa":"2"},"scripts":{"start":"node server.js"}}`, domain.FrameworkKoa, 3000, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeRaw(t, root, "package.json", c.pkg)
			svcs, err := ScanRepository(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(svcs) != 1 {
				t.Fatalf("expected 1 service, got %d", len(svcs))
			}
			s := svcs[0]
			if s.Framework != c.framework {
				t.Fatalf("framework = %q want %q", s.Framework, c.framework)
			}
			if s.Port != c.port {
				t.Fatalf("port = %d want %d", s.Port, c.port)
			}
			if s.IsSPA != c.spa {
				t.Fatalf("spa = %v want %v", s.IsSPA, c.spa)
			}
			if s.StartCmd == "" {
				t.Fatal("missing start command")
			}
		})
	}
}

func TestNodeSourcePortDetection(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "package.json", `{"name":"api","dependencies":{"express":"4"},"scripts":{"start":"node server.js"}}`)
	writeRaw(t, root, "server.js", `const express=require('express');const app=express();app.listen(4000);`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].Port != 4000 {
		t.Fatalf("port = %d want 4000", svcs[0].Port)
	}
}

func TestNodeSourcePortEnvFallback(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "package.json", `{"name":"api","dependencies":{"express":"4"},"scripts":{"start":"node server.js"}}`)
	writeRaw(t, root, "server.js", `const port = process.env.PORT || 5050; app.listen(port);`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].Port != 5050 {
		t.Fatalf("port = %d want 5050", svcs[0].Port)
	}
}

func TestPythonFrameworkMatrix(t *testing.T) {
	cases := []struct {
		name      string
		files     map[string]string
		framework domain.Framework
		port      int
		worker    bool
		startHas  string
	}{
		{
			name: "django",
			files: map[string]string{
				"manage.py":        "#!/usr/bin/env python\n",
				"requirements.txt": "Django==5.0\ngunicorn==21\n",
			},
			framework: domain.FrameworkDjango, port: 8000, startHas: "runserver 0.0.0.0:8000",
		},
		{
			name: "flask-app",
			files: map[string]string{
				"app.py":           "from flask import Flask\napp=Flask(__name__)\n",
				"requirements.txt": "Flask==3.0\n",
			},
			framework: domain.FrameworkFlask, port: 5000, startHas: "flask --app app run",
		},
		{
			name: "flask-wsgi",
			files: map[string]string{
				"wsgi.py":          "from flask import Flask\napp=Flask(__name__)\n",
				"requirements.txt": "Flask==3.0\n",
			},
			framework: domain.FrameworkFlask, port: 5000, startHas: "flask --app wsgi run",
		},
		{
			name: "fastapi",
			files: map[string]string{
				"main.py":          "from fastapi import FastAPI\napp=FastAPI()\n",
				"requirements.txt": "fastapi==0.110\nuvicorn==0.29\n",
			},
			framework: domain.FrameworkFastAPI, port: 8000, startHas: "uvicorn main:app",
		},
		{
			name: "fastapi-nested",
			files: map[string]string{
				"app/main.py":      "from fastapi import FastAPI\napp=FastAPI()\n",
				"requirements.txt": "fastapi>=0.100\nuvicorn>=0.20\n",
			},
			framework: domain.FrameworkFastAPI, port: 8000, startHas: "app.main:app",
		},
		{
			name: "streamlit",
			files: map[string]string{
				"app.py":           "import streamlit as st\n",
				"requirements.txt": "streamlit==1.30\n",
			},
			framework: domain.FrameworkStreamlit, port: 8501, startHas: "streamlit run app.py",
		},
		{
			name: "gradio",
			files: map[string]string{
				"app.py":           "import gradio as gr\n",
				"requirements.txt": "gradio==4.0\n",
			},
			framework: domain.FrameworkGradio, port: 7860, startHas: "python app.py",
		},
		{
			name: "celery-worker",
			files: map[string]string{
				"tasks.py":         "from celery import Celery\napp=Celery('x')\n",
				"requirements.txt": "celery==5.3\nredis==5\n",
			},
			framework: domain.FrameworkCelery, worker: true, startHas: "celery",
		},
		{
			name: "plain-script-worker",
			files: map[string]string{
				"main.py":          "print('hello')\n",
				"requirements.txt": "requests==2.31\n",
			},
			framework: domain.FrameworkGeneric, worker: true,
		},
		{
			name: "aiohttp-web",
			files: map[string]string{
				"main.py":          "from aiohttp import web\n",
				"requirements.txt": "aiohttp==3.9\n",
			},
			framework: domain.FrameworkGeneric, port: 8000, startHas: "python main.py",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range c.files {
				writeRaw(t, root, rel, content)
			}
			svcs, err := ScanRepository(root)
			if err != nil {
				t.Fatal(err)
			}
			s := svcs[0]
			if s.Framework != c.framework {
				t.Fatalf("framework = %q want %q", s.Framework, c.framework)
			}
			if s.IsWorker != c.worker {
				t.Fatalf("worker = %v want %v", s.IsWorker, c.worker)
			}
			if c.port != 0 && s.Port != c.port {
				t.Fatalf("port = %d want %d", s.Port, c.port)
			}
			if c.startHas != "" && !strings.Contains(s.StartCmd, c.startHas) {
				t.Fatalf("start = %q missing %q", s.StartCmd, c.startHas)
			}
		})
	}
}

func TestPythonRequirementsCycleSafe(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "requirements.txt", "-r a.txt\nfastapi\n")
	writeRaw(t, root, "a.txt", "-r b.txt\npydantic\n")
	writeRaw(t, root, "b.txt", "-r a.txt\nuvicorn\n")
	writeRaw(t, root, "main.py", "from fastapi import FastAPI\napp=FastAPI()\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	deps := strings.Join(svcs[0].Dependencies, ",")
	for _, want := range []string{"fastapi", "pydantic", "uvicorn"} {
		if !strings.Contains(deps, want) {
			t.Errorf("deps %q missing %q", deps, want)
		}
	}
}

func TestStaticSiteDetection(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "index.html", "<h1>hello</h1>")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].Language != domain.LangStatic || svcs[0].Port != 80 {
		t.Fatalf("unexpected %+v", svcs)
	}
}

func TestPNPMWorkspaceDoubleStar(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "pnpm-workspace.yaml", "packages:\n  - 'packages/**'\n")
	writeRaw(t, root, "packages/group/web/package.json", `{"name":"web","dependencies":{"next":"14"},"scripts":{"start":"next start"}}`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].Path != "packages/group/web" {
		t.Fatalf("unexpected %+v", svcs)
	}
}

func TestGoAndRustStillDetectedAfterBroadening(t *testing.T) {
	goRoot := t.TempDir()
	writeRaw(t, goRoot, "go.mod", "module example.com/x\n\ngo 1.22\n")
	writeRaw(t, goRoot, "main.go", "package main\nimport \"net/http\"\nfunc main(){ http.ListenAndServe(\":9091\", nil) }\n")
	svcs, err := ScanRepository(goRoot)
	if err != nil || svcs[0].Port != 9091 {
		t.Fatalf("go: %+v %v", svcs, err)
	}

	rustRoot := t.TempDir()
	writeRaw(t, rustRoot, "Cargo.toml", "[package]\nname = \"r\"\nversion = \"0.1.0\"\n")
	writeRaw(t, rustRoot, "src/main.rs", "fn main(){ std::net::TcpListener::bind(\"0.0.0.0:7071\").unwrap(); }\n")
	svcs, err = ScanRepository(rustRoot)
	if err != nil || svcs[0].Port != 7071 {
		t.Fatalf("rust: %+v %v", svcs, err)
	}
}

func TestFlaskNestedPackageLayout(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "requirements.txt", "Flask==3.0\nFlask-SQLAlchemy==3.1\ngunicorn==21\n")
	writeRaw(t, root, "Procfile", "web: gunicorn project.app:app\n")
	writeRaw(t, root, "project/__init__.py", "")
	writeRaw(t, root, "project/app.py", "from flask import Flask\napp=Flask(__name__)\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	s := svcs[0]
	if s.Framework != domain.FrameworkFlask {
		t.Fatalf("framework = %q", s.Framework)
	}
	if !strings.Contains(s.StartCmd, "--app project.app") {
		t.Fatalf("start = %q", s.StartCmd)
	}
}

func TestRootToolingPyprojectIsNotAService(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "pyproject.toml", "[project]\nname=\"tooling\"\ndependencies=[\"ruff\",\"mypy\"]\n")
	writeRaw(t, root, "README.md", "# tooling\n")
	if _, err := ScanRepository(root); err == nil {
		t.Fatal("tooling-only pyproject must not be detected as a service")
	}
}

func TestExamplesDirectoryIgnored(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "pyproject.toml", "[project]\nname=\"lib\"\ndependencies=[\"flask\"]\n")
	writeRaw(t, root, "examples/demo/app.py", "from flask import Flask\napp=Flask(__name__)\n")
	if _, err := ScanRepository(root); err == nil {
		t.Fatal("examples-only project must not yield a service")
	}
}

func TestPythonDotPythonVersion(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "pyproject.toml", "[project]\nname=\"a\"\ndependencies=[\"fastapi\"]\n")
	writeRaw(t, root, "main.py", "from fastapi import FastAPI\napp=FastAPI()\n")
	writeRaw(t, root, ".python-version", "3.12.4\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].RuntimeVersion != "3.12" {
		t.Fatalf("runtime = %q want 3.12", svcs[0].RuntimeVersion)
	}
}

func TestWorkspaceMemberFlag(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "package.json", `{"name":"root","private":true,"workspaces":["apps/*"]}`)
	writeRaw(t, root, "apps/web/package.json", `{"name":"web","dependencies":{"next":"14"},"scripts":{"start":"next start"}}`)
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || !svcs[0].WorkspaceMember {
		t.Fatalf("expected workspace member, got %+v", svcs)
	}
}

func TestDockerfileOnlyService(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "Dockerfile", "FROM nginx:alpine\nEXPOSE 9090\nCMD [\"nginx\",\"-g\",\"daemon off;\"]\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].Language != domain.LangDocker || svcs[0].Port != 9090 {
		t.Fatalf("unexpected %+v", svcs)
	}
}

func TestStaticRootPlusNestedBackend(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "index.html", "<h1>site</h1>")
	writeRaw(t, root, "services/api/pyproject.toml", "[project]\nname=\"api\"\ndependencies=[\"fastapi\"]\n")
	writeRaw(t, root, "services/api/main.py", "from fastapi import FastAPI\napp=FastAPI()\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	var static, api bool
	for _, s := range svcs {
		if s.Language == domain.LangStatic && s.Path == "." {
			static = true
		}
		if s.Path == "services/api" && s.Framework == domain.FrameworkFastAPI {
			api = true
		}
	}
	if !static || !api {
		t.Fatalf("expected static root + backend, got %+v", svcs)
	}
}

func TestCookiecutterTemplateSkipped(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "cookiecutter.json", `{"project_slug":"x"}`)
	writeRaw(t, root, "{{cookiecutter.project_slug}}/manage.py", "#!/usr/bin/env python\n")
	writeRaw(t, root, "{{cookiecutter.project_slug}}/requirements.txt", "Django==5.0\n")
	if _, err := ScanRepository(root); err == nil {
		t.Fatal("cookiecutter template must not be detected as a service")
	}
}

func TestNodeSQLiteForcesNode24(t *testing.T) {
	root := t.TempDir()
	writeRaw(t, root, "package.json", `{"name":"app","dependencies":{"next":"16"},"scripts":{"start":"next start"}}`)
	writeRaw(t, root, "src/db.ts", "import { DatabaseSync } from 'node:sqlite';\nexport const db = new DatabaseSync('x.db');\n")
	svcs, err := ScanRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if svcs[0].RuntimeVersion != "24" {
		t.Fatalf("runtime = %q want 24", svcs[0].RuntimeVersion)
	}
}
