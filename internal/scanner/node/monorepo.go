package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type pnpmWorkspace struct {
	Packages []string `yaml:"packages"`
}

// DiscoverWorkspacePackages returns service directories declared by
// pnpm-workspace.yaml and/or package.json workspaces, with ** and * globs
// expanded natively (Go's filepath.Glob does not understand **).
func DiscoverWorkspacePackages(repoRoot string, rootPkg *PackageJSON) []string {
	var patterns []string

	if data, err := os.ReadFile(filepath.Join(repoRoot, "pnpm-workspace.yaml")); err == nil {
		var ws pnpmWorkspace
		if yaml.Unmarshal(data, &ws) == nil {
			patterns = append(patterns, ws.Packages...)
		}
	}

	if rootPkg != nil && len(rootPkg.Workspaces) > 0 {
		patterns = append(patterns, parseWorkspaces(rootPkg.Workspaces)...)
	}

	seen := make(map[string]struct{})
	var out []string
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" || strings.HasPrefix(pat, "!") {
			continue // negation/exclusion patterns are not expanded
		}
		pat = strings.TrimPrefix(pat, "./")
		for _, dir := range expandGlob(repoRoot, pat) {
			rel, err := filepath.Rel(repoRoot, dir)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			if rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			if !feExists(filepath.Join(dir, "package.json")) {
				continue
			}
			if _, dup := seen[rel]; dup {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func parseWorkspaces(raw json.RawMessage) []string {
	// Shape 1: ["packages/*"]
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	// Shape 2: {"packages": ["packages/*"], "nohoist": [...]}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Packages
	}
	return nil
}

var globCache = map[string]*regexp.Regexp{}

func globToRegexp(pattern string) *regexp.Regexp {
	if re, ok := globCache[pattern]; ok {
		return re
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")
	re := regexp.MustCompile(b.String())
	globCache[pattern] = re
	return re
}

func expandGlob(root, pattern string) []string {
	re := globToRegexp(pattern)
	var out []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		base := d.Name()
		if path != root {
			switch base {
			case ".git", "node_modules", "dist", "build", ".next", "target", "vendor", ".venv", "venv", "__pycache__":
				return filepath.SkipDir
			}
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if re.MatchString(rel) {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func feExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
