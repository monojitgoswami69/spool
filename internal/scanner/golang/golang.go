// Package golang detects Go services from go.mod and package main sources.
package golang

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/scanner/fsutil"
)

var (
	mainPkgRe = regexp.MustCompile(`(?m)^package\s+main\b`)
	// Captures common Go server bind forms.
	portPatterns = []*regexp.Regexp{
		regexp.MustCompile(`ListenAndServe(?:TLS)?\(\s*"[^"]*:(\d{2,5})"`),
		regexp.MustCompile(`Addr:\s*"[^"]*:(\d{2,5})"`),
		regexp.MustCompile(`\.Run\(\s*"[^"]*:(\d{2,5})"`),
		regexp.MustCompile(`net\.JoinHostPort\([^)]*"(\d{2,5})"`),
		regexp.MustCompile(`:(\d{2,5})"\s*\)`),
	}
	httpUsageRe = regexp.MustCompile(`net/http|gin-gonic|labstack/echo|gofiber/fiber|go-chi/chi|gorilla/mux`)
)

// IsGoDir reports whether dir contains a go.mod.
func IsGoDir(dir string) bool { return fsutil.Exists(filepath.Join(dir, "go.mod")) }

// Detect builds a domain.Service for a Go module, or nil when there is no
// buildable main package (i.e. the module is a library).
func Detect(relPath, dir string) *domain.Service {
	mainDir, mainRel := findMain(dir)
	if mainDir == "" {
		return nil
	}
	modName := moduleName(filepath.Join(dir, "go.mod"))
	name := fsutil.SanitizeName(filepath.Base(modName))
	if name == "" || name == "." {
		name = fsutil.SanitizeName(filepath.Base(dir))
	}

	port := detectPort(mainDir)
	svc := &domain.Service{
		Name:           name,
		Path:           cleanRel(relPath),
		Language:       domain.LangGo,
		Framework:      domain.FrameworkGo,
		PackageManager: domain.PMGoMod,
		DefaultPort:    port,
		Port:           port,
		HostPort:       port,
	}
	if mainRel == "." {
		svc.BuildCmd = "go build -o app ."
	} else {
		svc.BuildCmd = "go build -o app ./" + mainRel
	}
	svc.StartCmd = "./app"
	if exposed := fsutil.DockerfileExpose(dir); exposed > 0 {
		svc.DefaultPort = exposed
		svc.Port = exposed
		svc.HostPort = exposed
	}
	return svc
}

func findMain(root string) (dir, rel string) {
	// Prefer the module root when it is package main.
	if hasMainPackage(root) {
		return root, "."
	}
	// Otherwise look one/two levels deep (cmd/<app>).
	var foundDir, foundRel string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "node_modules":
				if path != root {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if foundDir != "" || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if data, err := os.ReadFile(path); err == nil && mainPkgRe.Match(data) {
			d := filepath.Dir(path)
			rel, _ := filepath.Rel(root, d)
			foundDir, foundRel = d, filepath.ToSlash(rel)
			return filepath.SkipAll
		}
		return nil
	})
	return foundDir, foundRel
}

func hasMainPackage(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err == nil && mainPkgRe.Match(data) {
			return true
		}
	}
	return false
}

func moduleName(gomod string) string {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func detectPort(dir string) int {
	port := 8080
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(data)
		if !httpUsageRe.MatchString(text) && !strings.Contains(text, "ListenAndServe") {
			return nil
		}
		for _, re := range portPatterns {
			if m := re.FindStringSubmatch(text); m != nil {
				port = atoi(m[1])
				return filepath.SkipAll
			}
		}
		return nil
	})
	return port
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 || n > 65535 {
		return 8080
	}
	return n
}

func cleanRel(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(rel))
}
