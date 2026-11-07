// Package node detects Node.js/TypeScript services from package.json manifests.
package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/scanner/fsutil"
)

// PackageJSON is the subset of package.json Spool cares about.
type PackageJSON struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Type             string            `json:"type"`
	Private          bool              `json:"private"`
	Scripts          map[string]string `json:"scripts"`
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
	OptionalDeps     map[string]string `json:"optionalDependencies"`
	PackageManager   string            `json:"packageManager"`
	Workspaces       json.RawMessage   `json:"workspaces"`
	Engines          map[string]string `json:"engines"`
	Main             string            `json:"main"`
	Bin              json.RawMessage   `json:"bin"`
}

// HasDependency reports whether name appears in any dependency map.
func (p *PackageJSON) HasDependency(name string) bool {
	for _, m := range []map[string]string{p.Dependencies, p.DevDependencies, p.PeerDependencies, p.OptionalDeps} {
		if _, ok := m[name]; ok {
			return true
		}
	}
	return false
}

// DependencyNames returns the sorted union of dependency names.
func (p *PackageJSON) DependencyNames() []string {
	seen := make(map[string]struct{})
	for _, m := range []map[string]string{p.Dependencies, p.DevDependencies, p.PeerDependencies, p.OptionalDeps} {
		for k := range m {
			seen[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParsePackageJSON reads <dir>/package.json.
func ParsePackageJSON(dir string) (*PackageJSON, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var p PackageJSON
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	return &p, nil
}

// DetectPackageManager resolves the package manager and optional pinned version.
func DetectPackageManager(dir string, pkg *PackageJSON) (domain.PackageManager, string) {
	if pkg != nil && strings.TrimSpace(pkg.PackageManager) != "" {
		pm := strings.TrimSpace(pkg.PackageManager)
		name := pm
		ver := ""
		if i := strings.Index(pm, "@"); i > 0 {
			name = pm[:i]
			ver = pm[i+1:]
			if j := strings.IndexByte(ver, '+'); j >= 0 {
				ver = ver[:j]
			}
		}
		switch domain.PackageManager(strings.ToLower(name)) {
		case domain.PMNpm, domain.PMPnpm, domain.PMYarn, domain.PMBun:
			return domain.PackageManager(strings.ToLower(name)), ver
		}
	}
	lockfiles := []struct {
		file string
		pm   domain.PackageManager
	}{
		{"pnpm-lock.yaml", domain.PMPnpm},
		{"yarn.lock", domain.PMYarn},
		{"bun.lockb", domain.PMBun},
		{"bun.lock", domain.PMBun},
		{"package-lock.json", domain.PMNpm},
		{"npm-shrinkwrap.json", domain.PMNpm},
	}
	for _, l := range lockfiles {
		if fsutil.Exists(filepath.Join(dir, l.file)) {
			return l.pm, ""
		}
	}
	return domain.PMNpm, ""
}

func runScript(pm domain.PackageManager, script string) string {
	switch pm {
	case domain.PMPnpm, domain.PMYarn, domain.PMBun:
		return string(pm) + " run " + script
	default:
		return "npm run " + script
	}
}

// HasLockfile reports whether a Node lockfile exists in dir.
func HasLockfile(dir string) bool {
	for _, f := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "npm-shrinkwrap.json"} {
		if fsutil.Exists(filepath.Join(dir, f)) {
			return true
		}
	}
	return false
}

// Detect builds a domain.Service from a parsed package.json. relPath is the
// service directory relative to the repo root ("." for the root).
func Detect(relPath, dir string, pkg *PackageJSON, pm domain.PackageManager) *domain.Service {
	name := fsutil.SanitizeName(pkg.Name)
	if strings.TrimSpace(pkg.Name) == "" {
		if relPath == "." || relPath == "" {
			name = "web"
		} else {
			name = fsutil.SanitizeName(filepath.Base(relPath))
		}
	}

	svc := &domain.Service{
		Name:           name,
		Path:           cleanRel(relPath),
		Language:       domain.LangNode,
		PackageManager: pm,
		DefaultPort:    3000,
		Port:           3000,
		HostPort:       3000,
		Dependencies:   pkg.DependencyNames(),
	}
	svc.RuntimeVersion = fsutil.MajorVersion(firstNonEmpty(pkg.Engines["node"], fsutil.RuntimeVersionFromFile(filepath.Join(dir, ".nvmrc"))))

	isWorker := isWorkerName(name, relPath)
	framework := detectFramework(pkg)
	svc.Framework = framework

	switch framework {
	case domain.FrameworkNextJS:
		svc.DefaultPort = 3000
		if hasScript(pkg, "start") {
			svc.StartCmd = runScript(pm, "start")
		} else {
			svc.StartCmd = runScript(pm, "dev") + " -- -p 3000"
		}
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	case domain.FrameworkNuxt:
		svc.DefaultPort = 3000
		svc.StartCmd = pickScript(pm, pkg, "start", "preview", "dev")
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	case domain.FrameworkRemix, domain.FrameworkSvelteKit:
		svc.DefaultPort = 3000
		svc.StartCmd = pickScript(pm, pkg, "start", "preview", "dev")
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	case domain.FrameworkNestJS:
		svc.DefaultPort = 3000
		switch {
		case hasScript(pkg, "start:prod"):
			svc.StartCmd = runScript(pm, "start:prod")
		case hasScript(pkg, "start"):
			svc.StartCmd = runScript(pm, "start")
		default:
			svc.StartCmd = "node dist/main.js"
		}
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	case domain.FrameworkVite:
		svc.DefaultPort = 5173
		svc.IsSPA = true
		svc.StartCmd = runScript(pm, "dev") + " -- --host 0.0.0.0 --port 5173"
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	case domain.FrameworkCRA:
		svc.DefaultPort = 3000
		svc.IsSPA = true
		svc.StartCmd = pickScript(pm, pkg, "start", "dev")
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	case domain.FrameworkAstro:
		svc.DefaultPort = 4321
		if hasScript(pkg, "start") {
			svc.StartCmd = runScript(pm, "start")
		} else {
			svc.StartCmd = runScript(pm, "dev") + " -- --host 0.0.0.0 --port 4321"
		}
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	case domain.FrameworkExpress, domain.FrameworkFastify, domain.FrameworkKoa:
		svc.DefaultPort = 3000
		svc.StartCmd = pickScript(pm, pkg, "start", "serve", "dev")
		if svc.StartCmd == "" {
			svc.StartCmd = nodeEntryCmd(pkg)
		}
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	default:
		svc.DefaultPort = 3000
		svc.StartCmd = pickScript(pm, pkg, "start", "serve", "dev")
		if svc.StartCmd == "" {
			svc.StartCmd = nodeEntryCmd(pkg)
		}
		if hasScript(pkg, "build") {
			svc.BuildCmd = runScript(pm, "build")
		}
	}

	// For plain Node HTTP servers, honour a port hardcoded in source so health
	// probes target the right container port.
	if !isWorker && fsutil.DockerfileExpose(dir) == 0 {
		switch svc.Framework {
		case domain.FrameworkExpress, domain.FrameworkFastify, domain.FrameworkKoa, domain.FrameworkGeneric:
			if p := detectSourcePort(dir); p > 0 {
				svc.DefaultPort = p
			}
		}
	}

	if isWorker {
		svc.IsWorker = true
		svc.DefaultPort = 0
		svc.Port = 0
		svc.HostPort = 0
	} else {
		svc.Port = svc.DefaultPort
		svc.HostPort = svc.DefaultPort
	}

	// Honour an explicit Dockerfile EXPOSE only after framework defaults are set,
	// fixing the prior generation where framework branches clobbered it.
	if exposed := fsutil.DockerfileExpose(dir); exposed > 0 && !svc.IsWorker {
		svc.DefaultPort = exposed
		svc.Port = exposed
		svc.HostPort = exposed
	}

	return svc
}

var nodePortPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)process\.env\.PORT\s*\|\|\s*(\d{2,5})`),
	regexp.MustCompile(`(?i)\.listen\(\s*(\d{2,5})`),
	regexp.MustCompile(`(?i)listen\(\s*(\d{2,5})`),
	regexp.MustCompile(`(?i)(?:localhost|127\.0\.0\.1):(\d{2,5})`),
}

func detectSourcePort(dir string) int {
	best := 0
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", "dist", "build", ".next", ".nuxt", ".svelte-kit", "coverage":
				if path != dir {
					return filepath.SkipDir
				}
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		for _, re := range nodePortPatterns {
			if m := re.FindStringSubmatch(text); m != nil {
				if n := atoiPort(m[1]); n > 0 && best == 0 {
					best = n
				}
			}
		}
		return nil
	})
	return best
}

func atoiPort(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	if n < 1024 || n > 65535 {
		return 0
	}
	return n
}

func detectFramework(pkg *PackageJSON) domain.Framework {
	switch {
	case pkg.HasDependency("next"):
		return domain.FrameworkNextJS
	case pkg.HasDependency("nuxt"), pkg.HasDependency("nuxt3"):
		return domain.FrameworkNuxt
	case pkg.HasDependency("@remix-run/node"), pkg.HasDependency("@remix-run/react"):
		return domain.FrameworkRemix
	case pkg.HasDependency("@sveltejs/kit"):
		return domain.FrameworkSvelteKit
	case pkg.HasDependency("@nestjs/core"):
		return domain.FrameworkNestJS
	case pkg.HasDependency("astro"):
		return domain.FrameworkAstro
	case pkg.HasDependency("vite"):
		return domain.FrameworkVite
	case pkg.HasDependency("react-scripts"):
		return domain.FrameworkCRA
	case pkg.HasDependency("express"):
		return domain.FrameworkExpress
	case pkg.HasDependency("fastify"):
		return domain.FrameworkFastify
	case pkg.HasDependency("koa"):
		return domain.FrameworkKoa
	default:
		return domain.FrameworkGeneric
	}
}

func hasScript(pkg *PackageJSON, name string) bool {
	_, ok := pkg.Scripts[name]
	return ok
}

func pickScript(pm domain.PackageManager, pkg *PackageJSON, names ...string) string {
	for _, n := range names {
		if hasScript(pkg, n) {
			return runScript(pm, n)
		}
	}
	return ""
}

func nodeEntryCmd(pkg *PackageJSON) string {
	if strings.TrimSpace(pkg.Main) != "" {
		return "node " + strings.TrimSpace(pkg.Main)
	}
	return "node index.js"
}

func isWorkerName(name, rel string) bool {
	hay := strings.ToLower(name + "/" + rel)
	for _, kw := range []string{"worker", "consumer", "processor", "cron", "queue", "job"} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

func cleanRel(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// IsRunnableService reports whether a workspace package should be treated as a
// deployable service rather than a library.
func IsRunnableService(pkg *PackageJSON) bool {
	if pkg == nil {
		return false
	}
	for _, dep := range []string{
		"next", "nuxt", "nuxt3", "@remix-run/node", "@sveltejs/kit", "@nestjs/core",
		"vite", "astro", "react-scripts", "express", "fastify", "koa",
		"@hapi/hapi", "hapi", "restify", "polka", "sails", "loopback",
	} {
		if pkg.HasDependency(dep) {
			return true
		}
	}
	for _, script := range []string{"start", "serve"} {
		if _, ok := pkg.Scripts[script]; ok {
			return true
		}
	}
	return false
}
