package healer

import (
	"fmt"
	"regexp"
	"strings"
)

// Failure categories.
const (
	CatCompilation   = "compilation"
	CatDependency    = "dependency"
	CatEnvironment   = "environment"
	CatMissingEnv    = "missing-environment"
	CatNextBuild     = "nextjs-build"
	CatDockerBuild   = "docker-build"
	CatRuntime       = "runtime"
	CatHealthcheck   = "healthcheck"
	CatConfiguration = "configuration"
	CatUnknown       = "unknown"
)

var (
	specificErrorRe = regexp.MustCompile(`(?i)(?:error TS\d+:|Cannot find module|ModuleNotFoundError|ImportError|RuntimeError|Exception|requires a different Python|CRITICAL.*ERROR|No module named|failed to calculate checksum|not found|does not exist|is not defined|is undefined|missing environment variable|failed to collect page data|error occurred prerendering|build error occurred)`)
	genericErrorRe  = regexp.MustCompile(`(?i)(?:failed to solve|returned a non-zero code|exited with code)`)
	stageRe         = regexp.MustCompile(`\[([a-z0-9][a-z0-9_-]*)(?:\s+[a-z0-9_.-]+)?\s+\d+/\d+\]`)
	fileRefRe       = regexp.MustCompile(`([a-zA-Z0-9_][\w/.-]*\.(?:ts|tsx|js|jsx|py|go|rs|java|rb|php))\((\d+)`)
	packageRefRe    = regexp.MustCompile(`(?:Cannot find module|No module named)\s+['"]?([@a-zA-Z0-9_][\w/.-]*)['"]?`)
	envRefRe        = regexp.MustCompile(`(?i)\b([A-Z][A-Z0-9_]{3,})\b[^\n]*(?:missing|not set|not found|required|not provided)`)
	envNotDefinedRe = regexp.MustCompile(`\b([A-Z][A-Z0-9_]{3,})\s+is\s+(?:not\s+defined|undefined)`)
	portRefRe       = regexp.MustCompile(`:(\d{4,5})\b`)
	pythonVersionRe = regexp.MustCompile(`requires a different Python:.*?>=\s*(\d+\.\d+)`)
)

// Failure is a structured analysis of a build/runtime log.
type Failure struct {
	Category    string   `json:"category"`
	Service     string   `json:"service,omitempty"`
	Stage       string   `json:"stage,omitempty"`
	Error       string   `json:"error"`
	RelevantLog string   `json:"relevant_log,omitempty"`
	FileRefs    []string `json:"file_refs,omitempty"`
	PackageRefs []string `json:"package_refs,omitempty"`
	EnvRefs     []string `json:"env_refs,omitempty"`
	PortRefs    []string `json:"port_refs,omitempty"`
	Progression string   `json:"progression,omitempty"`
}

// Analyze extracts a structured failure from raw build output.
func Analyze(output, service string) Failure {
	lines := splitLines(output)
	f := Failure{Service: service}
	f.Error = extractPrimaryError(lines)
	f.RelevantLog = extractRelevantLog(lines)
	f.Stage = extractStage(lines, service)
	f.Category = categorize(f.Error, lines)
	f.FileRefs = uniqMatches(fileRefRe, output, 1)
	f.PackageRefs = uniqMatches(packageRefRe, output, 1)
	f.EnvRefs = append(uniqMatches(envRefRe, output, 1), uniqMatches(envNotDefinedRe, output, 1)...)
	f.EnvRefs = uniq(f.EnvRefs)
	f.PortRefs = uniqMatches(portRefRe, output, 1)
	return f
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func extractPrimaryError(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") && !strings.Contains(line, "error") {
			continue
		}
		if specificErrorRe.MatchString(line) {
			return line
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if genericErrorRe.MatchString(line) {
			return line
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return "unknown error"
}

func extractRelevantLog(lines []string) string {
	idx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if specificErrorRe.MatchString(lines[i]) || genericErrorRe.MatchString(lines[i]) {
			idx = i
			break
		}
	}
	if idx < 0 {
		start := len(lines) - 20
		if start < 0 {
			start = 0
		}
		return strings.Join(lines[start:], "\n")
	}
	start := idx - 5
	if start < 0 {
		start = 0
	}
	end := idx + 10
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start:end], "\n")
}

func extractStage(lines []string, service string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if m := stageRe.FindStringSubmatch(lines[i]); m != nil {
			if service == "" || m[1] == service {
				return m[1]
			}
		}
	}
	return ""
}

func categorize(errLine string, lines []string) string {
	e := strings.ToLower(errLine)
	all := strings.ToLower(strings.Join(lines, "\n"))
	switch {
	case strings.Contains(e, "error ts"):
		return CatCompilation
	case strings.Contains(e, "copy") && (strings.Contains(e, "not found") || strings.Contains(e, "does not exist") || strings.Contains(e, "checksum")):
		return CatConfiguration
	case strings.Contains(e, "checksum") && strings.Contains(e, "not found"):
		return CatConfiguration
	case strings.Contains(e, "cannot find module"), strings.Contains(e, "modulenotfounderror"), strings.Contains(e, "no module named"):
		return CatDependency
	case strings.Contains(e, "requires a different python"), strings.Contains(e, "requires-python"):
		return CatEnvironment
	case strings.Contains(e, "is not defined"), strings.Contains(e, "is undefined"), strings.Contains(e, "missing environment variable"):
		return CatMissingEnv
	case strings.Contains(e, "failed to collect page data"), strings.Contains(e, "error occurred prerendering"), strings.Contains(e, "build error occurred"):
		return CatNextBuild
	case strings.Contains(e, "connection refused"), strings.Contains(e, "could not connect"):
		return CatRuntime
	case strings.Contains(e, "unhealthy"), strings.Contains(e, "healthcheck"):
		return CatHealthcheck
	case strings.Contains(e, "failed to solve"), strings.Contains(e, "returned a non-zero code"), strings.Contains(e, "exited with code"):
		return CatDockerBuild
	}
	switch {
	case strings.Contains(all, "failed to collect page data"), strings.Contains(all, "nextjs"):
		return CatNextBuild
	case strings.Contains(all, "missing environment variable"):
		return CatMissingEnv
	case strings.Contains(all, "error ts"):
		return CatCompilation
	}
	return CatUnknown
}

// Fingerprint is a normalized identity for a failure, used for progression.
func (f Failure) Fingerprint() string {
	e := strings.ToLower(f.Error)
	norm := regexp.MustCompile(`[a-zA-Z0-9_./-]+\(\d+,\d+\)`)
	e = norm.ReplaceAllString(e, "<file>")
	norm = regexp.MustCompile(`#\d+\s+\d+\.\d+\s+`)
	e = norm.ReplaceAllString(e, "")
	norm = regexp.MustCompile(`/(?:[a-zA-Z0-9_.-]+/)+[a-zA-Z0-9_.-]+`)
	e = norm.ReplaceAllString(e, "<path>")
	norm = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
	e = norm.ReplaceAllString(e, "<hash>")
	return f.Category + ":" + strings.TrimSpace(e)
}

// ClassifyProgression compares two failures.
func ClassifyProgression(current, previous *Failure) string {
	if previous == nil {
		return "first"
	}
	if current.Fingerprint() == previous.Fingerprint() {
		return "same_failure"
	}
	if current.Category != previous.Category {
		return "progressed"
	}
	return "new_failure"
}

func uniqMatches(re *regexp.Regexp, s string, group int) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if group < len(m) && m[group] != "" && !seen[m[group]] {
			seen[m[group]] = true
			out = append(out, m[group])
		}
	}
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// PythonVersionFromLog returns a required python X.Y from a log, or "".
func PythonVersionFromLog(log string) string {
	if m := pythonVersionRe.FindStringSubmatch(log); m != nil {
		return m[1]
	}
	return ""
}

// Summary renders a one-line failure description.
func (f Failure) Summary() string {
	return fmt.Sprintf("[%s] %s", f.Category, strings.TrimSpace(f.Error))
}
