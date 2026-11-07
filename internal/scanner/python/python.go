// Package python detects Python services from pyproject.toml, requirements.txt,
// Pipfile and Django's manage.py.
package python

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/scanner/fsutil"
	"github.com/pelletier/go-toml/v2"
)

// PyProject is the subset of pyproject.toml Spool reads (PEP 621 + Poetry).
type PyProject struct {
	Project struct {
		Name           string              `toml:"name"`
		Dependencies   []string            `toml:"dependencies"`
		OptionalDeps   map[string][]string `toml:"optional-dependencies"`
		Scripts        map[string]string   `toml:"scripts"`
		RequiresPython string              `toml:"requires-python"`
	} `toml:"project"`
	Tool struct {
		Poetry struct {
			Name         string                 `toml:"name"`
			Dependencies map[string]interface{} `toml:"dependencies"`
			Scripts      map[string]string      `toml:"scripts"`
		} `toml:"poetry"`
	} `toml:"tool"`
	BuildSystem struct {
		Requires     []string `toml:"requires"`
		BuildBackend string   `toml:"build-backend"`
	} `toml:"build-system"`
}

// ParsePyProject parses <dir>/pyproject.toml.
func ParsePyProject(dir string) (*PyProject, error) {
	data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil {
		return nil, err
	}
	var p PyProject
	if err := toml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse pyproject.toml: %w", err)
	}
	return &p, nil
}

var (
	reqNameRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]*)`)
	includeRe = regexp.MustCompile(`^(?:-r|--requirement)\s*[=\s]?\s*(.+)$`)
	fastAPIRe = regexp.MustCompile(`(?m)\bFastAPI\s*\(`)
	flaskRe   = regexp.MustCompile(`(?m)\bFlask\s*\(`)
)

// ParseRequirements parses a requirements file, recursively following -r
// includes with cycle detection.
func ParseRequirements(dir, filename string) []string {
	seen := make(map[string]bool)
	var deps []string
	var parse func(file string)
	parse = func(file string) {
		path := file
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, file)
		}
		clean := filepath.Clean(path)
		if seen[clean] {
			return
		}
		seen[clean] = true

		f, err := os.Open(clean)
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(strings.TrimSuffix(sc.Text(), "\r"))
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.HasPrefix(line, "-e ") || strings.HasPrefix(line, "--editable") {
				continue
			}
			if m := includeRe.FindStringSubmatch(line); m != nil {
				inc := strings.TrimSpace(m[1])
				if !filepath.IsAbs(inc) {
					inc = filepath.Join(filepath.Dir(clean), inc)
				}
				parse(inc)
				continue
			}
			if strings.HasPrefix(line, "-") {
				continue // other pip options (index-url, find-links, ...)
			}
			// Drop environment markers and extras.
			if i := strings.IndexAny(line, ";[ "); i > 0 {
				line = line[:i]
			}
			if i := strings.Index(line, ">"); i > 0 {
				line = line[:i]
			}
			if i := strings.Index(line, "<"); i > 0 {
				line = line[:i]
			}
			if i := strings.Index(line, "="); i > 0 {
				line = line[:i]
			}
			if i := strings.Index(line, "~"); i > 0 {
				line = line[:i]
			}
			if m := reqNameRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
				deps = append(deps, strings.ToLower(m[1]))
			}
		}
	}
	parse(filename)
	sort.Strings(deps)
	return deps
}

// DetectPackageManager resolves the Python dependency manager.
func DetectPackageManager(dir string, pyproj *PyProject) domain.PackageManager {
	switch {
	case fsutil.Exists(filepath.Join(dir, "uv.lock")):
		return domain.PMUv
	case fsutil.Exists(filepath.Join(dir, "poetry.lock")):
		return domain.PMPoetry
	case pyproj != nil && pyproj.Tool.Poetry.Name != "":
		return domain.PMPoetry
	case fsutil.Exists(filepath.Join(dir, "Pipfile")):
		return domain.PMPipenv
	default:
		return domain.PMPip
	}
}

// CollectDependencies returns the lowercased dependency name set.
func CollectDependencies(dir string, pyproj *PyProject) map[string]bool {
	deps := make(map[string]bool)
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return
		}
		deps[name] = true
	}
	if pyproj != nil {
		for _, d := range pyproj.Project.Dependencies {
			if m := reqNameRe.FindStringSubmatch(strings.TrimSpace(d)); m != nil {
				add(m[1])
			}
		}
		for _, group := range pyproj.Project.OptionalDeps {
			for _, d := range group {
				if m := reqNameRe.FindStringSubmatch(strings.TrimSpace(d)); m != nil {
					add(m[1])
				}
			}
		}
		for name := range pyproj.Tool.Poetry.Dependencies {
			if strings.EqualFold(name, "python") {
				continue
			}
			add(name)
		}
	}
	for _, file := range []string{"requirements.txt", "requirements/base.txt", "requirements/prod.txt", "requirements/common.txt"} {
		for _, d := range ParseRequirements(dir, file) {
			add(d)
		}
	}
	return deps
}

// Detect builds a domain.Service for a Python directory.
func Detect(relPath, dir string, pyproj *PyProject, pm domain.PackageManager) *domain.Service {
	deps := CollectDependencies(dir, pyproj)
	name := ""
	if pyproj != nil {
		name = firstNonEmpty(pyproj.Project.Name, pyproj.Tool.Poetry.Name)
	}
	if strings.TrimSpace(name) == "" {
		if relPath == "." || relPath == "" {
			name = "api"
		} else {
			name = filepath.Base(relPath)
		}
	}
	name = fsutil.SanitizeName(name)

	depList := make([]string, 0, len(deps))
	for d := range deps {
		depList = append(depList, d)
	}
	sort.Strings(depList)

	svc := &domain.Service{
		Name:           name,
		Path:           cleanRel(relPath),
		Language:       domain.LangPython,
		PackageManager: pm,
		DefaultPort:    8000,
		Port:           8000,
		HostPort:       8000,
		Dependencies:   depList,
	}
	if pyproj != nil {
		svc.RuntimeVersion = pythonMajorMinor(pyproj.Project.RequiresPython)
		if svc.RuntimeVersion == "" {
			svc.RuntimeVersion = pythonMajorMinor(poetryPython(pyproj))
		}
	}
	if svc.RuntimeVersion == "" {
		svc.RuntimeVersion = fsutil.MajorVersion(fsutil.RuntimeVersionFromFile(filepath.Join(dir, ".python-version")))
	}

	hasManage := fsutil.Exists(filepath.Join(dir, "manage.py"))
	exposed := fsutil.DockerfileExpose(dir)
	dockerCmd := fsutil.DockerfileCMD(dir)
	flaskApp := findFlaskApp(dir)
	entryOK := hasPyEntry(dir) || flaskApp != "" || exposed > 0 || strings.TrimSpace(dockerCmd) != ""
	isWeb := hasManage || deps["django"] || deps["fastapi"] || deps["flask"] || deps["streamlit"] || deps["gradio"] || hasWebLib(deps)

	// Web framework detection MUST precede worker heuristics so that a service
	// named e.g. "task-api" is still detected as FastAPI. A dependency alone is
	// not enough: root-level tooling pyprojects and libraries must not become
	// services, so we require a plausible entry point, EXPOSE, or Dockerfile CMD.
	switch {
	case hasManage || (deps["django"] && entryOK):
		svc.Framework = domain.FrameworkDjango
		svc.DefaultPort = 8000
		svc.StartCmd = firstNonEmpty(dockerCmd, "python manage.py runserver 0.0.0.0:8000")
	case deps["fastapi"] && (findFastAPIEntry(dir) != "" || entryOK):
		svc.Framework = domain.FrameworkFastAPI
		svc.DefaultPort = 8000
		entry := findFastAPIEntry(dir)
		if entry == "" {
			entry = findASGIEntry(dir)
		}
		svc.StartCmd = firstNonEmpty(dockerCmd, fmt.Sprintf("uvicorn %s --host 0.0.0.0 --port 8000", entry))
	case deps["flask"] && entryOK:
		svc.Framework = domain.FrameworkFlask
		svc.DefaultPort = 5000
		appFlag := ""
		switch {
		case flaskApp != "":
			appFlag = " --app " + flaskApp
		case fsutil.Exists(filepath.Join(dir, "wsgi.py")):
			appFlag = " --app wsgi"
		case fsutil.Exists(filepath.Join(dir, "app.py")):
			appFlag = " --app app"
		}
		svc.StartCmd = firstNonEmpty(dockerCmd, "flask"+appFlag+" run --host=0.0.0.0 --port=5000")
	case deps["streamlit"] && entryOK:
		svc.Framework = domain.FrameworkStreamlit
		svc.DefaultPort = 8501
		entry := firstPyEntry(dir, "app.py", "main.py")
		svc.StartCmd = firstNonEmpty(dockerCmd, fmt.Sprintf("streamlit run %s --server.port 8501 --server.address 0.0.0.0", entry))
	case deps["gradio"] && entryOK:
		svc.Framework = domain.FrameworkGradio
		svc.DefaultPort = 7860
		entry := firstPyEntry(dir, "app.py", "main.py")
		svc.StartCmd = firstNonEmpty(dockerCmd, "python "+entry)
	case deps["celery"] && !isWeb && hasCeleryEntry(dir):
		svc.Framework = domain.FrameworkCelery
		svc.IsWorker = true
		svc.DefaultPort = 0
		svc.StartCmd = firstNonEmpty(dockerCmd, "celery -A app worker --loglevel=info")
	case !isWeb && isWorkerName(name, relPath) && entryOK:
		svc.Framework = domain.FrameworkGeneric
		svc.IsWorker = true
		svc.DefaultPort = 0
		svc.StartCmd = firstNonEmpty(dockerCmd, "python "+firstPyEntry(dir, "main.py", "app.py", "worker.py", "run.py"))
	default:
		if !entryOK {
			return nil
		}
		svc.Framework = domain.FrameworkGeneric
		if deps["uvicorn"] || deps["gunicorn"] || deps["hypercorn"] || deps["starlette"] {
			svc.DefaultPort = 8000
			svc.StartCmd = firstNonEmpty(dockerCmd, "uvicorn "+findASGIEntry(dir)+" --host 0.0.0.0 --port 8000")
		} else if hasWebLib(deps) || exposed > 0 {
			svc.DefaultPort = 8000
			svc.StartCmd = firstNonEmpty(dockerCmd, "python "+firstPyEntry(dir, "main.py", "app.py", "server.py", "run.py"))
		} else {
			// A plain Python script is not a web service; run it as a worker so
			// we never attach a doomed HTTP healthcheck.
			svc.IsWorker = true
			svc.DefaultPort = 0
			svc.StartCmd = firstNonEmpty(dockerCmd, "python "+firstPyEntry(dir, "main.py", "app.py", "server.py", "run.py"))
		}
	}

	if exposed > 0 && !svc.IsWorker {
		svc.DefaultPort = exposed
	}
	svc.Port = svc.DefaultPort
	if svc.IsWorker {
		svc.Port = 0
		svc.HostPort = 0
	} else {
		svc.HostPort = svc.DefaultPort
	}
	return svc
}

var flaskSkipDirs = map[string]bool{
	"venv": true, ".venv": true, "__pycache__": true, ".git": true, "node_modules": true,
	"tests": true, "test": true, "examples": true, "example": true, "docs": true,
	"migrations": true, "alembic": true, "build": true, "dist": true,
}

// findFlaskApp returns the importable module that defines a Flask app (e.g.
// "project.app"), supporting the common root-requirements + package layout.
func findFlaskApp(dir string) string {
	candidates := []struct{ file, module string }{
		{"app.py", "app"},
		{"wsgi.py", "wsgi"},
		{"application.py", "application"},
		{"main.py", "main"},
		{"src/app.py", "src.app"},
		{"src/main.py", "src.main"},
		{"app/app.py", "app.app"},
		{"app/main.py", "app.main"},
	}
	for _, c := range candidates {
		p := filepath.Join(dir, filepath.FromSlash(c.file))
		if data, err := os.ReadFile(p); err == nil && flaskRe.Match(data) {
			return c.module
		}
	}
	var found string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, "/")
		if d.IsDir() {
			if flaskSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if len(parts) >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if len(parts) > 2 || !strings.HasSuffix(path, ".py") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil || !flaskRe.Match(data) {
			return nil
		}
		module := strings.TrimSuffix(rel, ".py")
		module = strings.ReplaceAll(module, "/", ".")
		found = module
		return filepath.SkipAll
	})
	return found
}

func hasPyEntry(dir string) bool {
	for _, c := range []string{
		"main.py", "app.py", "server.py", "run.py", "asgi.py", "wsgi.py", "manage.py",
		"app/main.py", "src/main.py", "src/app.py", "src/server.py", "cmd/main.py",
	} {
		if fsutil.Exists(filepath.Join(dir, filepath.FromSlash(c))) {
			return true
		}
	}
	return false
}

func hasCeleryEntry(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "venv", ".venv", "__pycache__", ".git", "node_modules":
				if path != dir {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(path, ".py") {
			if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), "Celery(") {
				found = true
			}
		}
		return nil
	})
	return found
}

func hasWebLib(deps map[string]bool) bool {
	for _, d := range []string{
		"aiohttp", "tornado", "bottle", "sanic", "quart", "falcon", "pyramid",
		"uvicorn", "gunicorn", "hypercorn", "starlette", "werkzeug",
	} {
		if deps[d] {
			return true
		}
	}
	return false
}

func findASGIEntry(dir string) string {
	if fsutil.Exists(filepath.Join(dir, "main.py")) {
		return "main:app"
	}
	if fsutil.Exists(filepath.Join(dir, "app", "main.py")) {
		return "app.main:app"
	}
	if fsutil.Exists(filepath.Join(dir, "src", "main.py")) {
		return "src.main:app"
	}
	return "main:app"
}

func findFastAPIEntry(dir string) string {
	candidates := []struct {
		file, entry string
	}{
		{"main.py", "main:app"},
		{"app/main.py", "app.main:app"},
		{"src/main.py", "src.main:app"},
		{"api.py", "api:app"},
		{"app.py", "app:app"},
	}
	for _, c := range candidates {
		p := filepath.Join(dir, filepath.FromSlash(c.file))
		if data, err := os.ReadFile(p); err == nil && fastAPIRe.Match(data) {
			return c.entry
		}
	}
	// Fall back to any non-test .py file containing FastAPI(.
	var found string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() {
			if flaskSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".py") || strings.HasSuffix(path, "_test.py") || strings.HasPrefix(filepath.Base(path), "test_") {
			return nil
		}
		if data, err := os.ReadFile(path); err == nil && fastAPIRe.Match(data) {
			rel, _ := filepath.Rel(dir, path)
			mod := strings.TrimSuffix(filepath.ToSlash(rel), ".py")
			mod = strings.ReplaceAll(mod, "/", ".")
			found = mod + ":app"
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func firstPyEntry(dir string, names ...string) string {
	for _, n := range names {
		if fsutil.Exists(filepath.Join(dir, n)) {
			return n
		}
	}
	return "main.py"
}

func isWorkerName(name, rel string) bool {
	hay := strings.ToLower(name + "/" + rel)
	for _, kw := range []string{"worker", "consumer", "processor", "cron", "ingestion", "task"} {
		if strings.Contains(hay, kw) {
			return true
		}
	}
	return false
}

func pythonMajorMinor(v string) string {
	v = strings.TrimSpace(v)
	re := regexp.MustCompile(`(\d+)\.(\d+)`)
	if m := re.FindStringSubmatch(v); m != nil {
		return m[1] + "." + m[2]
	}
	return ""
}

func poetryPython(p *PyProject) string {
	if p == nil {
		return ""
	}
	if v, ok := p.Tool.Poetry.Dependencies["python"]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
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

// IsPythonDir reports whether dir looks like a Python project.
func IsPythonDir(dir string) bool {
	for _, f := range []string{"pyproject.toml", "requirements.txt", "Pipfile", "manage.py", "setup.py"} {
		if fsutil.Exists(filepath.Join(dir, f)) {
			return true
		}
	}
	return false
}
