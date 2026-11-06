package healer

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/monojitgoswami69/spool/internal/envvar"
)

var (
	ansiRe           = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	pythonFromRe     = regexp.MustCompile(`(?m)^FROM\s+python:(\d+\.\d+)(?:\.\d+)?-slim`)
	requiresPythonRe = regexp.MustCompile(`(?i)(?:requires a different Python|Requires-Python)[^\n]*?>=\s*(\d+\.\d+)`)
	missingEnvRe     = regexp.MustCompile(`(?i)([A-Z][A-Z0-9_]{3,})\s+(?:is\s+)?(?:not\s+defined|undefined|not set)`)
	missingEnvRe2    = regexp.MustCompile(`(?i)missing environment variable[:\s]+([A-Z][A-Z0-9_]{3,})`)
)

// CleanLog strips ANSI escape sequences and carriage returns.
func CleanLog(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return strings.TrimSpace(s)
}

type deterministicRule struct {
	name    string
	matches func(log string) bool
	apply   func(content, log string) (string, string, bool)
}

// ApplyDeterministic applies the first matching deterministic repair. It returns
// the new content, rule name, human summary, and whether a change was made.
func ApplyDeterministic(content, errorLog string) (string, string, string, bool) {
	log := CleanLog(errorLog)
	for _, rule := range ruleSet() {
		if !rule.matches(log) {
			continue
		}
		out, summary, ok := rule.apply(content, log)
		if ok && out != content {
			return out, rule.name, summary, true
		}
	}
	return content, "", "", false
}

func ruleSet() []deterministicRule {
	return []deterministicRule{
		{
			name: "LockfileDesync",
			matches: func(log string) bool {
				for _, m := range []string{
					"npm ci can only install", "code EUSAGE", "npm ERR! Invalid: lock file",
					"Frozen lockfile was violated", "ERR_PNPM_LOCKFILE_MISSING_DEPENDENCY",
				} {
					if strings.Contains(log, m) {
						return true
					}
				}
				return false
			},
			apply: func(content, log string) (string, string, bool) {
				if strings.Contains(content, "npm ci") {
					return strings.Replace(content, "npm ci", "npm install --legacy-peer-deps", 1), "npm ci -> npm install --legacy-peer-deps", true
				}
				if strings.Contains(content, "--frozen-lockfile") {
					return strings.ReplaceAll(content, " --frozen-lockfile", ""), "removed --frozen-lockfile", true
				}
				return content, "", false
			},
		},
		{
			name: "YarnBerryImmutable",
			matches: func(log string) bool {
				return strings.Contains(log, "Unknown option: --frozen-lockfile")
			},
			apply: func(content, log string) (string, string, bool) {
				if !strings.Contains(content, "yarn install --frozen-lockfile") {
					return content, "", false
				}
				return strings.ReplaceAll(content, "yarn install --frozen-lockfile", "yarn install --immutable"), "yarn --frozen-lockfile -> --immutable", true
			},
		},
		{
			name: "PnpmIgnoredBuilds",
			matches: func(log string) bool {
				return strings.Contains(log, "ERR_PNPM_IGNORED_BUILDS") || strings.Contains(log, "Ignored build scripts")
			},
			apply: func(content, log string) (string, string, bool) {
				if !strings.Contains(content, "pnpm install") || strings.Contains(content, "--dangerously-allow-all-builds") {
					return content, "", false
				}
				return strings.ReplaceAll(content, "pnpm install", "pnpm install --dangerously-allow-all-builds"), "enabled pnpm build scripts", true
			},
		},
		{
			name: "NodeGypBuildTools",
			matches: func(log string) bool {
				for _, m := range []string{"node-gyp rebuild", "gyp ERR!", "make: not found", "g++: not found"} {
					if strings.Contains(log, m) {
						return true
					}
				}
				return false
			},
			apply: func(content, log string) (string, string, bool) {
				if strings.Contains(content, "build-essential") || strings.Contains(content, "make g++") || strings.Contains(content, "make gcc") {
					return content, "", false
				}
				switch {
				case isAlpineBase(content):
					return insertAfterFirstFrom(content, "RUN apk add --no-cache python3 make g++ gcc libc6-compat"), "installed alpine build tools", true
				case isDebianBase(content):
					return insertAfterFirstFrom(content, "RUN apt-get update && apt-get install -y --no-install-recommends python3 make g++ gcc build-essential && rm -rf /var/lib/apt/lists/*"), "installed debian build tools", true
				default:
					return content, "", false
				}
			},
		},
		{
			name: "PythonLibpqDev",
			matches: func(log string) bool {
				if strings.Contains(log, "/bin/sh: apt-get: not found") {
					return false
				}
				for _, m := range []string{"pg_config executable not found", "Error: pg_config", "fatal error: libpq-fe.h"} {
					if strings.Contains(log, m) {
						return true
					}
				}
				return false
			},
			apply: func(content, log string) (string, string, bool) {
				if !strings.Contains(strings.ToLower(content), "python") {
					return content, "", false
				}
				if strings.Contains(content, "libpq-dev") || strings.Contains(content, "postgresql-dev") {
					return content, "", false
				}
				switch {
				case isAlpineBase(content):
					return insertAfterFirstFrom(content, "RUN apk add --no-cache postgresql-dev gcc musl-dev"), "installed alpine libpq headers", true
				case isDebianBase(content):
					return insertAfterFirstFrom(content, "RUN apt-get update && apt-get install -y --no-install-recommends build-essential libpq-dev && rm -rf /var/lib/apt/lists/*"), "installed debian libpq headers", true
				default:
					return content, "", false
				}
			},
		},
		{
			name: "PythonVersionMismatch",
			matches: func(log string) bool {
				return requiresPythonRe.MatchString(log)
			},
			apply: func(content, log string) (string, string, bool) {
				m := requiresPythonRe.FindStringSubmatch(log)
				if m == nil {
					return content, "", false
				}
				required := m[1]
				cur := pythonFromRe.FindStringSubmatch(content)
				if cur == nil {
					return content, "", false
				}
				if versionGTE(cur[1], required) {
					return content, "", false
				}
				out := pythonFromRe.ReplaceAllString(content, "FROM python:"+required+"-slim")
				return out, "python base bumped to " + required, true
			},
		},
		{
			name: "NodeHeapOOM",
			matches: func(log string) bool {
				return strings.Contains(log, "JavaScript heap out of memory") || strings.Contains(log, "Allocation failed - JavaScript heap out of memory")
			},
			apply: func(content, log string) (string, string, bool) {
				if strings.Contains(content, "max-old-space-size") {
					return content, "", false
				}
				return insertAfterFirstFrom(content, `ENV NODE_OPTIONS="--max-old-space-size=4096"`), "raised Node heap limit", true
			},
		},
		{
			name: "MissingBuildEnv",
			matches: func(log string) bool {
				return missingEnvRe.MatchString(log) || missingEnvRe2.MatchString(log)
			},
			apply: func(content, log string) (string, string, bool) {
				names := map[string]bool{}
				for _, re := range []*regexp.Regexp{missingEnvRe, missingEnvRe2} {
					for _, m := range re.FindAllStringSubmatch(log, -1) {
						if !envvar.IsSecretName(m[1]) {
							names[m[1]] = true
						}
					}
				}
				if len(names) == 0 {
					return content, "", false
				}
				var injected []string
				out := content
				for _, name := range sortedKeys(names) {
					if strings.Contains(out, "ENV "+name+"=") {
						continue
					}
					out = insertBeforeFirstRun(out, "ENV "+name+"=spool-build-placeholder")
					injected = append(injected, name)
				}
				if len(injected) == 0 {
					return content, "", false
				}
				return out, "injected build-time env: " + strings.Join(injected, ", "), true
			},
		},
	}
}

func firstFrom(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), "FROM ") {
			return strings.ToLower(line)
		}
	}
	return ""
}

func isAlpineBase(content string) bool {
	from := firstFrom(content)
	return strings.Contains(from, "alpine")
}

func isDebianBase(content string) bool {
	from := firstFrom(content)
	if from == "" {
		return false
	}
	if isAlpineBase(content) {
		return false
	}
	return strings.Contains(from, "debian") || strings.Contains(from, "ubuntu") || strings.Contains(from, "slim") ||
		strings.Contains(from, "python:") || strings.Contains(from, "node:") || strings.Contains(from, "rust:")
}

func insertAfterFirstFrom(content, line string) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(l)), "FROM ") {
			out := append([]string{}, lines[:i+1]...)
			out = append(out, line)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n")
		}
	}
	return content
}

func insertBeforeFirstRun(content, line string) string {
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(l)), "RUN ") {
			out := append([]string{}, lines[:i]...)
			out = append(out, line)
			out = append(out, lines[i:]...)
			return strings.Join(out, "\n")
		}
	}
	return insertAfterFirstFrom(content, line)
}

func versionGTE(have, want string) bool {
	hp, wp := parseVer(have), parseVer(want)
	if hp[0] != wp[0] {
		return hp[0] > wp[0]
	}
	return hp[1] >= wp[1]
}

func parseVer(v string) [2]int {
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d.%d", &major, &minor); err != nil {
		return [2]int{0, 0}
	}
	return [2]int{major, minor}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ErrNoDeterministicFix is returned by callers that require a rule match.
var ErrNoDeterministicFix = errors.New("no deterministic repair applies")
