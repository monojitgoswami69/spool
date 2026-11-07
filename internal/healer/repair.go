package healer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/llm"
	"github.com/monojitgoswami69/spool/internal/safefs"
	"gopkg.in/yaml.v3"
)

// Change is a single structured patch operation requested by the LLM.
type Change struct {
	Operation string `json:"operation"`
	File      string `json:"file"`
	Anchor    string `json:"anchor,omitempty"`
	Content   string `json:"content,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// RepairPlan is the LLM's structured repair proposal.
type RepairPlan struct {
	Diagnosis  string   `json:"diagnosis,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Changes    []Change `json:"changes,omitempty"`
	Dockerfile string   `json:"dockerfile,omitempty"`
	Compose    string   `json:"compose,omitempty"`
	Unfixable  bool     `json:"unfixable,omitempty"`
	NeedsFiles []string `json:"needs_files,omitempty"`
}

// ExtractPlan parses a RepairPlan from raw LLM output.
func ExtractPlan(raw string) (*RepairPlan, error) {
	body := llm.ExtractJSON(raw)
	if body == "" {
		return nil, fmt.Errorf("no JSON object in LLM response")
	}
	var plan RepairPlan
	if err := json.Unmarshal([]byte(body), &plan); err != nil {
		return nil, fmt.Errorf("parse repair plan: %w", err)
	}
	return &plan, nil
}

// AllowedRepairFile reports whether Spool may write name during repair.
func AllowedRepairFile(name string) bool {
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if base == "docker-compose.yml" || base == ".dockerignore" {
		return true
	}
	return strings.HasPrefix(base, "Dockerfile.")
}

// ValidateReplacement enforces the healer's invariants on a full file body.
func ValidateReplacement(file, newContent, original string) []string {
	var violations []string
	lower := strings.ToLower(newContent)
	origLower := strings.ToLower(original)

	for _, token := range []string{"ignorebuilderrors", "ignore_build_errors", "--no-type-check", "typescript.ignorebuilderrors"} {
		if strings.Contains(lower, token) {
			violations = append(violations, "disables type checking: "+token)
		}
	}
	if strings.Contains(lower, "sleep") && (strings.Contains(lower, "cmd") || strings.Contains(lower, "command")) && !strings.Contains(origLower, "sleep") {
		violations = append(violations, "replaces a service command with sleep")
	}
	if strings.HasSuffix(file, "docker-compose.yml") || strings.Contains(file, "compose") {
		if strings.Contains(origLower, "healthcheck") && !strings.Contains(lower, "healthcheck") {
			violations = append(violations, "removes a healthcheck")
		}
		for _, bad := range []string{"context: /", "dockerfile: /", "/users/"} {
			if strings.Contains(lower, bad) {
				violations = append(violations, "compose contains an absolute path: "+bad)
			}
		}
		var newDoc map[string]any
		if err := yaml.Unmarshal([]byte(newContent), &newDoc); err != nil {
			violations = append(violations, "invalid compose YAML: "+err.Error())
		} else {
			var oldDoc map[string]any
			if yaml.Unmarshal([]byte(original), &oldDoc) == nil {
				for name := range composeServiceNames(oldDoc) {
					if !composeServiceNames(newDoc)[name] {
						violations = append(violations, "removes compose service "+name)
					}
				}
			}
		}
	}
	hadInstall := strings.Contains(origLower, "npm install") || strings.Contains(origLower, "npm ci") ||
		strings.Contains(origLower, "pip install") || strings.Contains(origLower, "poetry install") ||
		strings.Contains(origLower, "cargo build") || strings.Contains(origLower, "go build")
	hasInstall := strings.Contains(lower, "npm install") || strings.Contains(lower, "npm ci") ||
		strings.Contains(lower, "pip install") || strings.Contains(lower, "poetry install") ||
		strings.Contains(lower, "cargo build") || strings.Contains(lower, "go build") || strings.Contains(lower, "compileall")
	if hadInstall && !hasInstall {
		violations = append(violations, "removes the dependency build/install step")
	}
	origLines := len(strings.Split(strings.TrimSpace(original), "\n"))
	newLines := len(strings.Split(strings.TrimSpace(newContent), "\n"))
	if newLines > 50 && newLines > origLines+40 {
		violations = append(violations, fmt.Sprintf("change is too broad (%d lines)", newLines))
	}
	return violations
}

// ApplyPatch applies a single structured change to content.
func ApplyPatch(content string, c Change) (string, error) {
	switch c.Operation {
	case "create_file":
		return c.Content, nil
	case "replace_exact":
		if c.Anchor == "" {
			return "", fmt.Errorf("replace_exact requires an anchor")
		}
		count := strings.Count(content, c.Anchor)
		if count != 1 {
			return "", fmt.Errorf("anchor must occur exactly once (found %d)", count)
		}
		return strings.Replace(content, c.Anchor, c.Content, 1), nil
	case "replace_line":
		return replaceLine(content, c.Anchor, c.Content)
	case "insert_before":
		return insertLine(content, c.Anchor, c.Content, true)
	case "insert_after":
		return insertLine(content, c.Anchor, c.Content, false)
	default:
		return "", fmt.Errorf("unsupported operation %q", c.Operation)
	}
}

func replaceLine(content, anchor, replacement string) (string, error) {
	lines := strings.Split(content, "\n")
	idx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == strings.TrimSpace(anchor) {
			if idx >= 0 {
				return "", fmt.Errorf("anchor line is not unique")
			}
			idx = i
		}
	}
	if idx < 0 {
		return "", fmt.Errorf("anchor line not found")
	}
	indent := lines[idx][:len(lines[idx])-len(strings.TrimLeft(lines[idx], " \t"))]
	lines[idx] = indent + replacement
	return strings.Join(lines, "\n"), nil
}

func insertLine(content, anchor, newContent string, before bool) (string, error) {
	lines := strings.Split(content, "\n")
	idx := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == strings.TrimSpace(anchor) {
			if idx >= 0 {
				return "", fmt.Errorf("anchor line is not unique")
			}
			idx = i
		}
	}
	if idx < 0 {
		return "", fmt.Errorf("anchor line not found")
	}
	insert := strings.Split(strings.TrimRight(newContent, "\n"), "\n")
	pos := idx
	if !before {
		pos = idx + 1
	}
	out := make([]string, 0, len(lines)+len(insert))
	out = append(out, lines[:pos]...)
	out = append(out, insert...)
	out = append(out, lines[pos:]...)
	return strings.Join(out, "\n"), nil
}

// HashBytes returns the lowercase hex SHA-256 of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Provenance persists per-patch SHA-256 before/after hashes to repairs.json.
type Provenance struct {
	mu      sync.Mutex
	path    string
	records []domain.RepairRecord
}

// NewProvenance creates/loads a provenance store at path (may be empty for memory-only).
func NewProvenance(path string) *Provenance {
	p := &Provenance{path: path}
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var wrapper struct {
				Repairs []domain.RepairRecord `json:"repairs"`
			}
			if json.Unmarshal(data, &wrapper) == nil {
				p.records = wrapper.Repairs
			}
		}
	}
	return p
}

// Record appends a provenance record and persists it atomically.
func (p *Provenance) Record(rec domain.RepairRecord) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if rec.ID == "" {
		rec.ID = fmt.Sprintf("repair-%d", len(p.records)+1)
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	p.records = append(p.records, rec)
	if p.path == "" {
		return nil
	}
	wrapper := struct {
		Repairs []domain.RepairRecord `json:"repairs"`
	}{Repairs: p.records}
	data, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	return safefs.WriteFileAtomic(p.path, append(data, '\n'), 0o600)
}

// Records returns a copy of the recorded provenance.
func (p *Provenance) Records() []domain.RepairRecord {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]domain.RepairRecord, len(p.records))
	copy(out, p.records)
	return out
}

func composeServiceNames(doc map[string]any) map[string]bool {
	out := map[string]bool{}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return out
	}
	for name := range services {
		out[name] = true
	}
	return out
}
