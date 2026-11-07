// Package scanner discovers deployable services across Node (including
// monorepos), Python, Go, and Rust. Detection is manifest-driven and
// deterministic: results are returned in a stable order.
package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/scanner/fsutil"
	"github.com/monojitgoswami69/spool/internal/scanner/golang"
	"github.com/monojitgoswami69/spool/internal/scanner/node"
	"github.com/monojitgoswami69/spool/internal/scanner/python"
	"github.com/monojitgoswami69/spool/internal/scanner/rust"
)

// Result is the outcome of a scan.
type Result struct {
	Root      string            `json:"root"`
	Services  []*domain.Service `json:"services"`
	Languages []domain.Language `json:"languages"`
}

var groupingDirs = map[string]bool{"services": true, "apps": true, "packages": true}

var ignoredDirs = map[string]bool{
	"node_modules": true, ".git": true, ".next": true, ".nuxt": true, ".svelte-kit": true,
	"dist": true, "build": true, "out": true, "vendor": true, "legacy": true,
	"public": true, "static": true, "assets": true, "docs": true, "documentation": true,
	"test": true, "tests": true, "__tests__": true, "scripts": true,
	"coverage": true, "venv": true, ".venv": true, "__pycache__": true,
	"target": true, "bin": true, "obj": true, ".idea": true, ".vscode": true,
	"db-scripts": true, "demo-data": true, "contracts": true, "hooks": true,
	"types": true, "components": true, "migrations": true, ".spool": true, "cookiecutter": true,
	"examples": true, "example": true, "samples": true, "sample": true,
	"demo": true, "demos": true, "tutorials": true, "tutorial": true,
	"benchmarks": true, "benchmark": true, "playground": true,
	"fixtures": true, "fixture": true, "e2e": true,
}

// ScanRepository returns all detected services, or an error when none are found.
func ScanRepository(root string) ([]*domain.Service, error) {
	res, err := Scan(root)
	if err != nil {
		return nil, err
	}
	return res.Services, nil
}

// Scan returns all detected services plus the detected language set.
func Scan(root string) (*Result, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, domain.E(domain.KindValidation, "scanner.Scan", "abs path", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, domain.E(domain.KindNotFound, "scanner.Scan", "not a directory: "+root, err)
	}

	seen := make(map[string]bool)
	var services []*domain.Service
	add := func(s *domain.Service) {
		if s == nil {
			return
		}
		key := s.Path + "|" + string(s.Language)
		if seen[key] {
			return
		}
		seen[key] = true
		services = append(services, s)
	}

	// Root manifests. Workspace discovery also works from pnpm-workspace.yaml
	// alone (some monorepos omit a root package.json).
	rootPkg, rootPkgErr := node.ParsePackageJSON(abs)
	if rootPkgErr == nil || fsutil.Exists(filepath.Join(abs, "pnpm-workspace.yaml")) {
		for _, rel := range node.DiscoverWorkspacePackages(abs, rootPkg) {
			sub := filepath.Join(abs, filepath.FromSlash(rel))
			subPkg, err := node.ParsePackageJSON(sub)
			if err != nil || !node.IsRunnableService(subPkg) {
				continue
			}
			pm, ver := node.DetectPackageManager(sub, subPkg)
			if pm == domain.PMNpm && !node.HasLockfile(sub) && rootPkg != nil {
				pm, ver = node.DetectPackageManager(abs, rootPkg)
			}
			svc := node.Detect(rel, sub, subPkg, pm)
			svc.PackageManagerVersion = ver
			svc.WorkspaceMember = true
			add(svc)
		}
	}
	if rootPkgErr == nil && node.IsRunnableService(rootPkg) {
		pm, ver := node.DetectPackageManager(abs, rootPkg)
		svc := node.Detect(".", abs, rootPkg, pm)
		svc.PackageManagerVersion = ver
		add(svc)
	}
	if python.IsPythonDir(abs) {
		pyproj, _ := python.ParsePyProject(abs)
		pm := python.DetectPackageManager(abs, pyproj)
		add(python.Detect(".", abs, pyproj, pm))
	}
	if golang.IsGoDir(abs) {
		add(golang.Detect(".", abs))
	}
	if rust.IsRustDir(abs) {
		add(rust.Detect(".", abs))
	}

	scanDir := func(rel string) {
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." || seen[rel+"|"+string(domain.LangNode)] && seen[rel+"|"+string(domain.LangPython)] {
			return
		}
		full := filepath.Join(abs, filepath.FromSlash(rel))
		if pkg, err := node.ParsePackageJSON(full); err == nil && node.IsRunnableService(pkg) {
			pm, ver := node.DetectPackageManager(full, pkg)
			svc := node.Detect(rel, full, pkg, pm)
			svc.PackageManagerVersion = ver
			add(svc)
		}
		if python.IsPythonDir(full) {
			pyproj, _ := python.ParsePyProject(full)
			pm := python.DetectPackageManager(full, pyproj)
			add(python.Detect(rel, full, pyproj, pm))
		}
		if golang.IsGoDir(full) {
			add(golang.Detect(rel, full))
		}
		if rust.IsRustDir(full) {
			add(rust.Detect(rel, full))
		}
	}

	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, domain.E(domain.KindIO, "scanner.Scan", "read dir", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if skipDirName(name) {
			continue
		}
		if groupingDirs[name] {
			children, err := os.ReadDir(filepath.Join(abs, name))
			if err != nil {
				continue
			}
			sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
			for _, c := range children {
				if !c.IsDir() || skipDirName(c.Name()) {
					continue
				}
				scanDir(name + "/" + c.Name())
			}
			continue
		}
		scanDir(name)
	}

	hasRoot := false
	for _, svc := range services {
		if svc.Path == "." {
			hasRoot = true
			break
		}
	}
	if !hasRoot {
		if s := detectDockerfile(abs); s != nil {
			add(s)
		} else if s := detectStatic(abs); s != nil {
			add(s)
		}
	}
	if len(services) == 0 {
		return nil, domain.E(domain.KindNotFound, "scanner.Scan",
			fmt.Sprintf("no supported services detected in %s. Spool looks for package.json (Node/Next/Vite/Express), pyproject.toml/requirements.txt/Pipfile (FastAPI/Flask/Django), go.mod, Cargo.toml, an index.html static site, or a root Dockerfile. Add a manifest or Dockerfile, or run `spool doctor` to verify the environment.", abs), nil)
	}

	sort.Slice(services, func(i, j int) bool {
		if services[i].Path != services[j].Path {
			return services[i].Path < services[j].Path
		}
		return services[i].Language < services[j].Language
	})
	normalizeNames(services)
	ensureUniquePaths(services)

	langSeen := map[domain.Language]bool{}
	var langs []domain.Language
	for _, s := range services {
		if !langSeen[s.Language] {
			langSeen[s.Language] = true
			langs = append(langs, s.Language)
		}
	}
	sort.Slice(langs, func(i, j int) bool { return langs[i] < langs[j] })

	return &Result{Root: abs, Services: services, Languages: langs}, nil
}

func normalizeNames(services []*domain.Service) {
	used := make(map[string]int)
	for _, s := range services {
		base := s.Name
		if strings.TrimSpace(base) == "" {
			base = "app"
		}
		if count, ok := used[base]; ok {
			count++
			used[base] = count
			s.Name = fmt.Sprintf("%s-%d", base, count)
			continue
		}
		used[base] = 1
		s.Name = base
	}
}

// ensureUniquePaths guarantees each service has a distinct Path, which compose
// service keys rely on (they are derived from Name, but paths help humans).
func ensureUniquePaths(services []*domain.Service) {
	_ = services // paths may legitimately repeat across languages; names are unique
}

// detectStatic recognizes a plain static site served from the repository root.
func detectStatic(root string) *domain.Service {
	if !fsutil.Exists(filepath.Join(root, "index.html")) {
		return nil
	}
	base := filepath.Base(root)
	if len(base) >= 9 && base[len(base)-9] == '-' && isHex8(base[len(base)-8:]) {
		base = base[:len(base)-9]
	}
	name := fsutil.SanitizeName(base)
	if name == "" {
		name = "site"
	}
	return &domain.Service{
		Name:        name,
		Path:        ".",
		Language:    domain.LangStatic,
		Framework:   domain.FrameworkStatic,
		DefaultPort: 80,
		Port:        80,
		HostPort:    80,
		StartCmd:    "",
	}
}

func skipDirName(name string) bool {
	if strings.HasPrefix(name, ".") || ignoredDirs[strings.ToLower(name)] {
		return true
	}
	if strings.Contains(name, "{{") || strings.Contains(name, "}}") {
		return true
	}
	return strings.Contains(strings.ToLower(name), "cookiecutter")
}

// detectDockerfile recognizes a repo that only ships a root Dockerfile.
func detectDockerfile(root string) *domain.Service {
	if !fsutil.Exists(filepath.Join(root, "Dockerfile")) {
		return nil
	}
	port := fsutil.DockerfileExpose(root)
	if port == 0 {
		port = 8080
	}
	name := fsutil.SanitizeName(filepath.Base(root))
	if name == "" || name == "." {
		name = "app"
	}
	return &domain.Service{
		Name: name, Path: ".", Language: domain.LangDocker, Framework: domain.FrameworkDocker,
		DefaultPort: port, Port: port, HostPort: port,
	}
}

func isHex8(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
