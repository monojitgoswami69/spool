// Package fsutil contains small, dependency-free filesystem helpers shared by
// the language scanners.
package fsutil

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	exposeRe = regexp.MustCompile(`(?i)^\s*EXPOSE\s+(\d+)`)
	cmdRe    = regexp.MustCompile(`(?i)^\s*CMD\s+(.+)$`)
	jsonArr  = regexp.MustCompile(`^\[(.*)\]$`)
	quoted   = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)
)

// Exists reports whether path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// IsDir reports whether path is a directory.
func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ReadFile reads a file or returns "" on error.
func ReadFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// FirstExisting returns the first path that exists, or "".
func FirstExisting(paths ...string) string {
	for _, p := range paths {
		if Exists(p) {
			return p
		}
	}
	return ""
}

// DockerfilePath returns <dir>/Dockerfile if present.
func DockerfilePath(dir string) string {
	p := filepath.Join(dir, "Dockerfile")
	if Exists(p) {
		return p
	}
	return ""
}

// DockerfileExpose returns the first EXPOSE port in <dir>/Dockerfile, or 0.
func DockerfileExpose(dir string) int {
	p := DockerfilePath(dir)
	if p == "" {
		return 0
	}
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := exposeRe.FindStringSubmatch(sc.Text()); m != nil {
			var port int
			for _, r := range m[1] {
				port = port*10 + int(r-'0')
			}
			if port > 0 && port <= 65535 {
				return port
			}
		}
	}
	return 0
}

// DockerfileCMD extracts a Dockerfile CMD as a shell string, or "".
func DockerfileCMD(dir string) string {
	p := DockerfilePath(dir)
	if p == "" {
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := cmdRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		val := strings.TrimSpace(m[1])
		if arr := jsonArr.FindStringSubmatch(val); arr != nil {
			var parts []string
			for _, q := range quoted.FindAllStringSubmatch(arr[1], -1) {
				if q[1] != "" {
					parts = append(parts, q[1])
				} else {
					parts = append(parts, q[2])
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
		return val
	}
	return ""
}

// RuntimeVersionFromFile reads a .nvmrc / .python-version / .tool-versions hint.
func RuntimeVersionFromFile(path string) string {
	data := strings.TrimSpace(ReadFile(path))
	if data == "" {
		return ""
	}
	// .tool-versions has lines like "nodejs 20.11.0"; take the first version token.
	fields := strings.Fields(data)
	for _, f := range fields {
		if len(f) > 0 && (f[0] >= '0' && f[0] <= '9') {
			return strings.TrimPrefix(f, "v")
		}
	}
	return ""
}

// MajorVersion returns the first numeric version component found in v, so it
// handles plain versions ("20.11.0"), constraints (">=20"), and ranges ("18.x").
func MajorVersion(v string) string {
	re := regexp.MustCompile(`\d+`)
	return re.FindString(v)
}

// SanitizeName converts an arbitrary package/service name into a docker-safe id.
func SanitizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' || r == '_'
		if r == '@' || r == '/' || r == ' ' || r == '_' {
			ok = false
		}
		if ok {
			b.WriteRune(r)
			lastDash = r == '-'
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "app"
	}
	return out
}
