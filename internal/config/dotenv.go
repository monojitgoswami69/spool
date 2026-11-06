package config

import (
	"os"
	"strings"
)

// ParseDotEnv parses dotenv content into a map. It is deliberately lenient:
// blank lines, comments, and malformed entries are skipped rather than erroring,
// because a broken .env must never prevent Spool from starting.
func ParseDotEnv(data []byte) map[string]string {
	out := make(map[string]string)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		val = unquoteEnv(val)
		out[key] = val
	}
	return out
}

func unquoteEnv(v string) string {
	if len(v) >= 2 {
		switch {
		case v[0] == '"' && v[len(v)-1] == '"':
			inner := v[1 : len(v)-1]
			inner = strings.ReplaceAll(inner, `\n`, "\n")
			inner = strings.ReplaceAll(inner, `\t`, "\t")
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
			return inner
		case v[0] == '\'' && v[len(v)-1] == '\'':
			return v[1 : len(v)-1]
		}
	}
	// Strip an unquoted inline comment only when preceded by whitespace.
	if idx := strings.Index(v, " #"); idx >= 0 {
		v = strings.TrimSpace(v[:idx])
	}
	return v
}

// LoadDotEnv merges the given dotenv files into a map. Later files do not
// override earlier keys; an empty or unreadable file is ignored.
func LoadDotEnv(paths ...string) map[string]string {
	out := make(map[string]string)
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for k, v := range ParseDotEnv(data) {
			if _, exists := out[k]; !exists {
				out[k] = v
			}
		}
	}
	return out
}
