package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/monojitgoswami69/spool/internal/domain"
)

// GenerateDockerfile renders a hermetic multi-stage Dockerfile for svc.
func GenerateDockerfile(svc *domain.Service, opts Options) string {
	switch svc.Language {
	case domain.LangNode:
		return generateNode(svc, opts)
	case domain.LangPython:
		return generatePython(svc, opts)
	case domain.LangGo:
		return generateGo(svc, opts)
	case domain.LangRust:
		return generateRust(svc, opts)
	case domain.LangStatic:
		return generateStatic(svc)
	default:
		return generateGeneric(svc, opts)
	}
}

func generateNode(svc *domain.Service, opts Options) string {
	if svc.IsSPA {
		return generateNodeSPA(svc, opts)
	}
	image, corepack := nodeBase(svc)
	path := serviceDir(svc)
	workPath := "/app"
	if path != "." {
		workPath = "/app/" + path
	}
	installDir := workPath
	if opts.Monorepo {
		installDir = "/app"
	}
	install := nodeInstall(svc.PackageManager)
	build := strings.TrimSpace(svc.BuildCmd)
	start := strings.TrimSpace(svc.StartCmd)
	if start == "" {
		start = "node index.js"
	}
	port := fmtPort(svc.Port)

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("FROM %s AS base", image)
	w("WORKDIR /app")
	for _, k := range sortedKeysOf(opts.BuildEnv) {
		w("ARG %s", k)
		w("ENV %s=$%s", k, k)
	}
	w("RUN apk add --no-cache libc6-compat curl python3 make g++")
	for _, line := range corepack {
		w("RUN %s", line)
	}
	w("")
	w("FROM base AS deps")
	w("WORKDIR /app")
	w("COPY . .")
	w("RUN cd %s && %s", installDir, install)
	w("")
	w("FROM base AS builder")
	w("WORKDIR /app")
	w("COPY --from=deps /app ./")
	if build != "" {
		w("RUN cd %s && %s", workPath, build)
	}
	w("")
	w("FROM base AS runner")
	w("WORKDIR /app")
	w("ENV NODE_ENV=production")
	w("COPY --from=builder /app ./")
	w("WORKDIR %s", workPath)
	if !svc.IsWorker && svc.Port > 0 {
		w("ENV PORT=%d HOST=0.0.0.0", svc.Port)
		w("EXPOSE %d", port)
	}
	w("CMD %s", jsonArray("sh", "-c", start))
	return b.String()
}

func generateNodeSPA(svc *domain.Service, opts Options) string {
	image, corepack := nodeBase(svc)
	path := serviceDir(svc)
	workPath := "/app"
	if path != "." {
		workPath = "/app/" + path
	}
	installDir := workPath
	if opts.Monorepo {
		installDir = "/app"
	}
	install := nodeInstall(svc.PackageManager)
	build := strings.TrimSpace(svc.BuildCmd)
	if build == "" {
		build = "npm run build"
	}
	outDir := "dist"
	if svc.Framework == domain.FrameworkCRA {
		outDir = "build"
	}
	port := fmtPort(svc.Port)

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("FROM %s AS builder", image)
	w("WORKDIR /app")
	for _, k := range sortedKeysOf(opts.BuildEnv) {
		w("ARG %s", k)
		w("ENV %s=$%s", k, k)
	}
	w("RUN apk add --no-cache libc6-compat curl")
	for _, line := range corepack {
		w("RUN %s", line)
	}
	w("COPY . .")
	w("RUN cd %s && %s", installDir, install)
	w("RUN cd %s && %s", workPath, build)
	w("")
	w("FROM nginx:alpine AS runner")
	w("RUN apk add --no-cache curl")
	w("COPY --from=builder %s/%s /usr/share/nginx/html", workPath, outDir)
	w("RUN printf 'server {\\n listen %d;\\n root /usr/share/nginx/html;\\n index index.html;\\n location / { try_files $uri $uri/ /index.html; }\\n}' > /etc/nginx/conf.d/default.conf", port)
	w("EXPOSE %d", port)
	w("CMD [\"nginx\", \"-g\", \"daemon off;\"]")
	return b.String()
}

func nodeBase(svc *domain.Service) (string, []string) {
	ver := strings.TrimSpace(svc.RuntimeVersion)
	if ver == "" {
		ver = "20"
	}
	if svc.PackageManager == domain.PMBun {
		return "oven/bun:1-alpine", nil
	}
	image := "node:" + ver + "-alpine"
	var setup []string
	switch svc.PackageManager {
	case domain.PMPnpm:
		setup = append(setup, "corepack enable", corepackPrepare("pnpm", svc.PackageManagerVersion))
	case domain.PMYarn:
		setup = append(setup, "corepack enable", corepackPrepare("yarn", svc.PackageManagerVersion))
	}
	return image, setup
}

func corepackPrepare(pm, version string) string {
	if strings.TrimSpace(version) == "" {
		version = "latest"
	}
	return "corepack prepare " + pm + "@" + version + " --activate"
}

func nodeInstall(pm domain.PackageManager) string {
	switch pm {
	case domain.PMPnpm:
		return "pnpm install --frozen-lockfile || pnpm install"
	case domain.PMYarn:
		return "yarn install --immutable || yarn install"
	case domain.PMBun:
		return "bun install --frozen-lockfile || bun install"
	default:
		return "npm ci --no-audit --no-fund || npm install --no-audit --no-fund --legacy-peer-deps"
	}
}

func generatePython(svc *domain.Service, opts Options) string {
	ver := strings.TrimSpace(svc.RuntimeVersion)
	if ver == "" {
		ver = "3.11"
	}
	image := "python:" + ver + "-slim"
	path := serviceDir(svc)
	workPath := "/app"
	if path != "." {
		workPath = "/app/" + path
	}
	start := strings.TrimSpace(svc.StartCmd)
	if start == "" {
		start = "python main.py"
	}
	port := fmtPort(svc.Port)

	torchIndex := ""
	if svc.HasDependency("torch") || svc.HasDependency("pytorch") || svc.HasDependency("torchvision") || svc.HasDependency("torchaudio") {
		torchIndex = " --extra-index-url https://download.pytorch.org/whl/cpu"
	}

	reqFile := firstExistingFile(opts.RepoRoot, path, []string{
		"requirements.txt", "requirements/base.txt", "requirements/prod.txt", "requirements/common.txt",
	})
	hasPyproject := existsFile(opts.RepoRoot, joinRel(path, "pyproject.toml"))
	hasSetup := existsFile(opts.RepoRoot, joinRel(path, "setup.py"))
	pyDeps, pyHasBuild := readPyProjectDeps(opts.RepoRoot, path)

	// Dependency-only pyproject files (no [build-system]) are extremely common
	// in app repos; installing "." would fail. Install the declared
	// dependencies directly instead.
	pipTarget := ""
	switch {
	case reqFile != "":
		pipTarget = " -r " + reqFile
	case hasPyproject && len(pyDeps) > 0 && !pyHasBuild:
		pipTarget = " " + shellJoin(pyDeps)
	case hasPyproject:
		pipTarget = " ."
	case hasSetup:
		pipTarget = " ."
	default:
		pipTarget = " -r requirements.txt"
	}

	var install string
	switch svc.PackageManager {
	case domain.PMUv:
		install = "pip install --upgrade pip uv && uv pip install" + torchIndex + pipTarget
	case domain.PMPoetry:
		install = "pip install --upgrade pip poetry && poetry config virtualenvs.create false && poetry install --no-root --no-interaction"
	case domain.PMPipenv:
		install = "pip install --upgrade pip pipenv && (pipenv install --system --deploy || pipenv install --system)"
	default:
		install = "pip install --upgrade pip && pip install" + torchIndex + pipTarget
	}

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("FROM %s AS builder", image)
	w("WORKDIR /app")
	w("ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 PIP_NO_CACHE_DIR=1")
	w("RUN apt-get update && apt-get install -y --no-install-recommends build-essential libpq-dev curl ca-certificates && rm -rf /var/lib/apt/lists/*")
	w("RUN python -m venv /opt/venv")
	w("ENV PATH=\"/opt/venv/bin:$PATH\"")
	w("COPY . .")
	w("RUN cd %s && %s", workPath, install)
	w("")
	w("FROM %s AS runner", image)
	w("WORKDIR /app")
	w("ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 PATH=\"/opt/venv/bin:$PATH\"")
	w("RUN apt-get update && apt-get install -y --no-install-recommends libpq5 curl ca-certificates && rm -rf /var/lib/apt/lists/*")
	w("COPY --from=builder /opt/venv /opt/venv")
	w("COPY . .")
	w("WORKDIR %s", workPath)
	if !svc.IsWorker && svc.Port > 0 {
		w("ENV PORT=%d HOST=0.0.0.0", svc.Port)
		w("EXPOSE %d", port)
	}
	w("CMD %s", jsonArray("sh", "-c", start))
	return b.String()
}

func generateGo(svc *domain.Service, opts Options) string {
	ver := strings.TrimSpace(svc.RuntimeVersion)
	if ver == "" {
		ver = "1.23"
	}
	path := serviceDir(svc)
	workPath := "/app"
	if path != "." {
		workPath = "/app/" + path
	}
	target := "."
	if strings.Contains(svc.BuildCmd, "./") {
		if idx := strings.Index(svc.BuildCmd, "./"); idx >= 0 {
			target = strings.TrimSpace(svc.BuildCmd[idx:])
		}
	}
	port := fmtPort(svc.Port)

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("FROM golang:%s-alpine AS builder", ver)
	w("WORKDIR /app")
	w("RUN apk add --no-cache git ca-certificates")
	w("COPY . .")
	w("RUN cd %s && CGO_ENABLED=0 go build -trimpath -ldflags=\"-s -w\" -o /out/app %s", workPath, target)
	w("")
	w("FROM alpine:3.20 AS runner")
	w("RUN apk add --no-cache ca-certificates curl")
	w("WORKDIR /app")
	w("COPY --from=builder /out/app /app/app")
	if !svc.IsWorker && svc.Port > 0 {
		w("ENV PORT=%d HOST=0.0.0.0", svc.Port)
		w("EXPOSE %d", port)
	}
	w("CMD [\"/app/app\"]")
	return b.String()
}

func generateRust(svc *domain.Service, opts Options) string {
	ver := strings.TrimSpace(svc.RuntimeVersion)
	if ver == "" {
		ver = "1.83"
	}
	path := serviceDir(svc)
	workPath := "/app"
	if path != "." {
		workPath = "/app/" + path
	}
	bin := strings.TrimSpace(strings.TrimPrefix(svc.StartCmd, "./"))
	if bin == "" {
		bin = svc.Name
	}
	port := fmtPort(svc.Port)

	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("FROM rust:%s-slim AS builder", ver)
	w("WORKDIR /app")
	w("RUN apt-get update && apt-get install -y --no-install-recommends pkg-config libssl-dev && rm -rf /var/lib/apt/lists/*")
	w("COPY . .")
	w("RUN cd %s && cargo build --release", workPath)
	w("")
	w("FROM debian:bookworm-slim AS runner")
	w("RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl libssl3 && rm -rf /var/lib/apt/lists/*")
	w("WORKDIR /app")
	w("COPY --from=builder %s/target/release/%s /app/app", workPath, bin)
	if !svc.IsWorker && svc.Port > 0 {
		w("ENV PORT=%d HOST=0.0.0.0", svc.Port)
		w("EXPOSE %d", port)
	}
	w("CMD [\"/app/app\"]")
	return b.String()
}

func generateGeneric(svc *domain.Service, opts Options) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("FROM alpine:3.20")
	w("RUN apk add --no-cache curl ca-certificates")
	w("WORKDIR /app")
	w("COPY . .")
	if !svc.IsWorker && svc.Port > 0 {
		w("EXPOSE %d", fmtPort(svc.Port))
	}
	start := svc.StartCmd
	if start == "" {
		start = "echo 'no start command detected' && exit 1"
	}
	w("CMD %s", jsonArray("sh", "-c", start))
	return b.String()
}

func jsonArray(parts ...string) string {
	data, _ := json.Marshal(parts)
	return string(data)
}

func joinRel(path, file string) string {
	if path == "." || path == "" {
		return file
	}
	return path + "/" + file
}

func firstExistingFile(root, relDir string, candidates []string) string {
	for _, c := range candidates {
		if existsFile(root, joinRel(relDir, c)) {
			return c
		}
	}
	return ""
}

func readPyProjectDeps(root, relDir string) (deps []string, hasBuildSystem bool) {
	if root == "" {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(joinRel(relDir, "pyproject.toml"))))
	if err != nil {
		return nil, false
	}
	var p struct {
		Project struct {
			Dependencies []string `toml:"dependencies"`
		} `toml:"project"`
		Tool struct {
			Poetry struct {
				Dependencies map[string]any `toml:"dependencies"`
			} `toml:"poetry"`
		} `toml:"tool"`
		BuildSystem struct {
			Requires []string `toml:"requires"`
		} `toml:"build-system"`
	}
	if err := toml.Unmarshal(data, &p); err != nil {
		return nil, false
	}
	for _, d := range p.Project.Dependencies {
		if strings.TrimSpace(d) != "" {
			deps = append(deps, strings.TrimSpace(d))
		}
	}
	for name := range p.Tool.Poetry.Dependencies {
		if !strings.EqualFold(name, "python") && strings.TrimSpace(name) != "" {
			deps = append(deps, name)
		}
	}
	return deps, len(p.BuildSystem.Requires) > 0
}

func shellJoin(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'"'"'`)+"'")
	}
	return strings.Join(quoted, " ")
}

func generateStatic(svc *domain.Service) string {
	port := fmtPort(svc.Port)
	if port == 0 {
		port = 80
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("FROM nginx:alpine AS runner")
	w("RUN apk add --no-cache curl")
	w("COPY . /usr/share/nginx/html")
	w("RUN printf 'server {\\n listen %d;\\n root /usr/share/nginx/html;\\n index index.html;\\n location / { try_files $uri $uri/ /index.html; }\\n}' > /etc/nginx/conf.d/default.conf", port)
	w("EXPOSE %d", port)
	w("CMD [\"nginx\", \"-g\", \"daemon off;\"]")
	return b.String()
}
