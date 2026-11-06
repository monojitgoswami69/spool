package llm

import (
	"strings"
)

// ExtractJSON returns the first balanced JSON object found in s, tolerating
// markdown fences and surrounding prose. It returns "" when no object is found.
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// CleanContent strips surrounding whitespace and a single markdown fence.
func CleanContent(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.IndexByte(s, '\n'); idx >= 0 {
			s = s[idx+1:]
		} else {
			s = strings.TrimPrefix(s, "```")
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s) + "\n"
}
