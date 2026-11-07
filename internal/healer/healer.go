// Package healer implements Spool's closed-loop build and runtime repair. It
// applies deterministic rules first, then an invariant-guarded LLM repair that
// can only touch generated Dockerfiles and docker-compose.yml inside the work
// directory jail. Every applied patch is recorded with SHA-256 provenance.
package healer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/llm"
	"github.com/monojitgoswami69/spool/internal/safefs"
)

// DefaultMaxTries is the default repair budget.
const DefaultMaxTries = 3

// Healer repairs a generated stack.
type Healer struct {
	Compose       domain.ComposeController
	LLM           domain.LLMClient
	WorkJail      *safefs.Jail
	SourceJail    *safefs.Jail
	Services      []*domain.Service
	PortMap       map[string]int
	MaxTries      int
	Logf          func(format string, args ...any)
	Provenance    *Provenance
	HTTPCheck     func(ctx context.Context, url string) domain.Result
	HealthTimeout time.Duration
	PollInterval  time.Duration
	// RestartGrace is how long a container may stay in "restarting" before it is
	// treated as a crash loop. Defaults to 10s.
	RestartGrace time.Duration
}

// Attempt records one healing iteration.
type Attempt struct {
	N           int           `json:"n"`
	Kind        string        `json:"kind"`
	Service     string        `json:"service,omitempty"`
	Rule        string        `json:"rule,omitempty"`
	Summary     string        `json:"summary,omitempty"`
	Progression string        `json:"progression,omitempty"`
	Duration    time.Duration `json:"duration"`
}

// Result is the outcome of a heal run.
type Result struct {
	Success  bool      `json:"success"`
	Verified bool      `json:"verified"`
	Attempts []Attempt `json:"attempts"`
	Summary  string    `json:"summary"`
	FinalLog string    `json:"final_log,omitempty"`
}

func (h *Healer) snapshotManifests() map[string][]byte {
	out := map[string][]byte{}
	if h.WorkJail == nil {
		return out
	}
	entries, err := os.ReadDir(h.WorkJail.Root())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name != "docker-compose.yml" && !strings.HasPrefix(name, "Dockerfile.") {
			continue
		}
		if data, err := h.WorkJail.ReadFile(name); err == nil {
			out[name] = data
		}
	}
	return out
}

func (h *Healer) restoreManifests(originals map[string][]byte) {
	if h.WorkJail == nil {
		return
	}
	for name, want := range originals {
		cur, err := h.WorkJail.ReadFile(name)
		if err == nil && bytes.Equal(cur, want) {
			continue
		}
		if err := h.WorkJail.WriteFile(name, want, 0o644); err == nil {
			_ = h.Provenance.Record(domain.RepairRecord{
				File: name, OriginalHash: HashBytes(cur), ResultingHash: HashBytes(want),
				Operation: "rollback", Summary: "restored generated manifest after failed heal",
			})
		}
	}
}

func (h *Healer) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

// Heal runs the closed loop. initialOutput may contain a pre-existing build
// failure to avoid rebuilding immediately; failingService may be empty.
func (h *Healer) Heal(ctx context.Context, initialOutput, failingService string) (*Result, error) {
	if h.Compose == nil {
		return nil, domain.E(domain.KindDocker, "healer.Heal", "nil compose controller", nil)
	}
	max := h.MaxTries
	if max <= 0 {
		max = DefaultMaxTries
	}
	res := &Result{}
	originals := h.snapshotManifests()
	rollback := func(r *Result) *Result {
		h.restoreManifests(originals)
		if r != nil {
			r.Summary = strings.TrimSpace(r.Summary + " (healer changes rolled back)")
		}
		return r
	}
	var prev *Failure
	var lastOutput string
	buildErr := errors.New("initial build failure")

	for attempt := 1; attempt <= max; attempt++ {
		start := time.Now()
		if attempt == 1 && strings.TrimSpace(initialOutput) != "" {
			lastOutput = initialOutput
			buildErr = errors.New("initial build failure")
		} else {
			lastOutput, buildErr = h.Compose.Build(ctx, "")
		}

		if buildErr == nil {
			h.logf("✓ build succeeded (attempt %d)", attempt)
			if verr := h.VerifyRuntime(ctx); verr == nil {
				res.Success = true
				res.Verified = true
				res.Summary = "build succeeded and runtime health verified"
				res.FinalLog = lastOutput
				return res, nil
			} else {
				h.logf("⚠ runtime unhealthy: %v", verr)
				svcName := h.resolveService(failingService, "")
				rtLog := lastOutput + "\n" + h.runtimeLog(ctx, svcName)
				f := Analyze(rtLog, svcName)
				if f.Category == CatUnknown {
					f.Category = CatRuntime
					f.Error = verr.Error()
				}
				f.Progression = ClassifyProgression(&f, prev)
				res.Attempts = append(res.Attempts, Attempt{N: attempt, Kind: "runtime", Service: svcName, Summary: f.Summary(), Progression: f.Progression, Duration: time.Since(start)})
				applied, summary, herr := h.repairOnce(ctx, svcName, f, rtLog, true, true)
				if herr != nil {
					res.Summary = "runtime unhealthy and repair failed"
					return rollback(res), herr
				}
				if !applied {
					res.Summary = "runtime unhealthy; no automatic repair found"
					return rollback(res), domain.E(domain.KindHealth, "healer.Heal", verr.Error(), domain.ErrUnfixable)
				}
				h.logf("🔧 %s", summary)
				prev = &f
				continue
			}
		}

		f := Analyze(lastOutput, failingService)
		f.Progression = ClassifyProgression(&f, prev)
		svcName := h.resolveService(failingService, f.Service)
		res.Attempts = append(res.Attempts, Attempt{N: attempt, Kind: "build", Service: svcName, Summary: f.Summary(), Progression: f.Progression, Duration: time.Since(start)})
		h.logf("× build failed (%s): %s", f.Category, f.Error)

		applied, summary, herr := h.repairOnce(ctx, svcName, f, lastOutput, false, !f.Source)
		if herr != nil {
			res.Summary = "build failed and repair errored"
			res.FinalLog = Tail(lastOutput, 80)
			return rollback(res), herr
		}
		if !applied {
			if f.Source {
				res.Summary = "source compilation error (Spool repairs manifests, not application source)"
				res.FinalLog = f.RelevantLog
				return rollback(res), sourceFailureError(f)
			}
			res.Summary = "build failed; no automatic repair applies"
			res.FinalLog = Tail(lastOutput, 80)
			return rollback(res), domain.E(domain.KindDocker, "healer.Heal", f.Summary(), domain.ErrUnfixable)
		}
		h.logf("🔧 %s", summary)
		prev = &f
	}

	// Final verification attempt.
	lastOutput, err := h.Compose.Build(ctx, "")
	if err == nil {
		if verr := h.VerifyRuntime(ctx); verr == nil {
			res.Success = true
			res.Verified = true
			res.Summary = "build succeeded after repair budget"
			res.FinalLog = lastOutput
			return res, nil
		} else {
			res.Summary = "build succeeded but runtime health failed"
			res.FinalLog = Tail(lastOutput, 80)
			return rollback(res), domain.E(domain.KindHealth, "healer.Heal", verr.Error(), nil)
		}
	}
	res.Summary = "build failed after repair budget"
	res.FinalLog = Tail(lastOutput, 80)
	return rollback(res), domain.E(domain.KindDocker, "healer.Heal", res.Summary, err)
}

func (h *Healer) repairOnce(ctx context.Context, svcName string, f Failure, output string, runtime, allowLLM bool) (bool, string, error) {
	dfName, content, err := h.readDockerfile(svcName)
	if err != nil {
		// No Dockerfile to repair: fall through to LLM only if a compose file exists.
		dfName, content = "", ""
	}
	if content != "" {
		newContent, rule, summary, ok := ApplyDeterministic(content, f.RelevantLog)
		if ok {
			if err := h.writeRepair(dfName, newContent, content, f, rule); err != nil {
				return false, "", err
			}
			return true, rule + ": " + summary, nil
		}
	}
	if h.LLM == nil || !allowLLM {
		return false, "", nil
	}
	applied, summary, err := h.llmRepair(ctx, svcName, dfName, f, output, runtime)
	if err != nil {
		return false, "", err
	}
	return applied, summary, nil
}

func (h *Healer) readDockerfile(svcName string) (string, string, error) {
	if h.WorkJail == nil {
		return "", "", domain.E(domain.KindIO, "healer.readDockerfile", "no work jail configured", nil)
	}
	name := ""
	if svcName != "" {
		candidate := "Dockerfile." + svcName
		if h.WorkJail.Exists(candidate) {
			name = candidate
		}
	}
	if name == "" && len(h.Services) == 1 {
		candidate := "Dockerfile." + h.Services[0].Name
		if h.WorkJail.Exists(candidate) {
			name = candidate
		}
	}
	if name == "" {
		return "", "", domain.E(domain.KindNotFound, "healer.readDockerfile", "cannot determine failing Dockerfile", nil)
	}
	data, err := h.WorkJail.ReadFile(name)
	if err != nil {
		return "", "", domain.E(domain.KindIO, "healer.readDockerfile", "read "+name, err)
	}
	return name, string(data), nil
}

func (h *Healer) writeRepair(name, newContent, oldContent string, f Failure, operation string) error {
	if h.WorkJail == nil {
		return domain.E(domain.KindIO, "healer.writeRepair", "no work jail", nil)
	}
	if violations := ValidateReplacement(name, newContent, oldContent); len(violations) > 0 {
		return domain.E(domain.KindValidation, "healer.writeRepair", "invariant violation: "+strings.Join(violations, "; "), nil)
	}
	if err := h.WorkJail.WriteFile(name, []byte(newContent), 0o644); err != nil {
		return domain.E(domain.KindIO, "healer.writeRepair", "write "+name, err)
	}
	return h.Provenance.Record(domain.RepairRecord{
		Service:       f.Service,
		File:          name,
		OriginalHash:  HashBytes([]byte(oldContent)),
		ResultingHash: HashBytes([]byte(newContent)),
		FailureCat:    f.Category,
		Operation:     operation,
		Summary:       f.Summary(),
	})
}

func (h *Healer) resolveService(failing, analyzed string) string {
	for _, name := range []string{analyzed, failing} {
		if name == "" {
			continue
		}
		for _, svc := range h.Services {
			if svc.Name == name {
				return name
			}
		}
		return name
	}
	if len(h.Services) == 1 {
		return h.Services[0].Name
	}
	return ""
}

func (h *Healer) runtimeLog(ctx context.Context, svcName string) string {
	if h.Compose == nil {
		return ""
	}
	out, err := h.Compose.Logs(ctx, svcName, 200)
	if err != nil {
		return ""
	}
	return out
}

// VerifyRuntime brings the stack up and verifies container + HTTP health. It
// never discards health results.
func (h *Healer) VerifyRuntime(ctx context.Context) error {
	if h.Compose == nil {
		return domain.E(domain.KindDocker, "healer.VerifyRuntime", "nil compose", nil)
	}
	if _, err := h.Compose.Up(ctx, "--remove-orphans"); err != nil {
		return err
	}
	timeout := h.HealthTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	interval := h.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	grace := h.RestartGrace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	var last string
	var restartingSince time.Time
	for {
		ps, err := h.Compose.Ps(ctx)
		if err != nil {
			return err
		}
		ready, fatal, restarting, detail := containerHealth(ps)
		last = detail
		if restarting {
			if restartingSince.IsZero() {
				restartingSince = time.Now()
			} else if time.Since(restartingSince) > grace {
				fatal = true
				detail += " (crash loop)"
			}
		} else {
			restartingSince = time.Time{}
		}
		if fatal {
			return domain.E(domain.KindHealth, "healer.VerifyRuntime", "stack failed: "+detail, nil)
		}
		if ready {
			if h.HTTPCheck != nil && len(h.PortMap) > 0 {
				httpOK := true
				for _, svc := range h.Services {
					if svc.IsWorker {
						continue
					}
					hp := h.PortMap[svc.Name]
					if hp == 0 {
						hp = svc.HostPort
					}
					if hp == 0 {
						continue
					}
					r := h.HTTPCheck(ctx, fmt.Sprintf("http://localhost:%d/", hp))
					if !r.Healthy() {
						httpOK = false
						last = fmt.Sprintf("%s http %s: %s", last, svc.Name, r.Status)
						break
					}
				}
				if httpOK {
					return nil
				}
			} else {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return domain.E(domain.KindHealth, "healer.VerifyRuntime", "stack did not become healthy: "+last, nil)
		}
		select {
		case <-ctx.Done():
			return domain.E(domain.KindCancelled, "healer.VerifyRuntime", "cancelled", ctx.Err())
		case <-time.After(interval):
		}
	}
}

func containerHealth(ps []domain.Container) (ready, fatal, restarting bool, detail string) {
	if len(ps) == 0 {
		return false, false, false, "no containers"
	}
	for _, c := range ps {
		if c.State == "exited" || c.State == "dead" {
			return false, true, false, fmt.Sprintf("%s is %s (exit %d)", c.Service, c.State, c.ExitCode)
		}
		if c.State == "restarting" {
			return false, c.ExitCode != 0, true, fmt.Sprintf("%s is restarting (exit %d)", c.Service, c.ExitCode)
		}
		if c.State != "running" {
			return false, false, false, fmt.Sprintf("%s is %s", c.Service, c.State)
		}
		if strings.EqualFold(c.Health, "unhealthy") {
			return false, false, false, fmt.Sprintf("%s is unhealthy", c.Service)
		}
	}
	return true, false, false, "all containers running"
}

// llmRepair performs the invariant-guarded LLM repair with a stale-context guard.
func (h *Healer) llmRepair(ctx context.Context, svcName, dfName string, f Failure, output string, runtime bool) (bool, string, error) {
	if h.LLM == nil || h.WorkJail == nil {
		return false, "", nil
	}
	var df, compose string
	if dfName != "" {
		if b, err := h.WorkJail.ReadFile(dfName); err == nil {
			df = string(b)
		}
	}
	if h.WorkJail.Exists("docker-compose.yml") {
		if b, err := h.WorkJail.ReadFile("docker-compose.yml"); err == nil {
			compose = string(b)
		}
	}
	if df == "" && compose == "" {
		return false, "", nil
	}
	snapshot := map[string]string{dfName: df, "docker-compose.yml": compose}

	files := h.relevantFiles(svcName)
	served := map[string]bool{}
	for round := 1; round <= 3; round++ {
		prompt := h.repairPrompt(svcName, dfName, f, output, df, compose, files, runtime)
		raw, err := h.LLM.Call(ctx, repairSystemPrompt, prompt)
		if err != nil {
			return false, "", err
		}
		plan, perr := ExtractPlan(raw)
		if perr != nil {
			files = append(files, FileEntry{Path: "(parse error)", Content: perr.Error()})
			continue
		}
		if plan.Unfixable {
			return false, "", domain.E(domain.KindLLM, "healer.llmRepair", "model reported failure as unfixable", domain.ErrUnfixable)
		}
		if len(plan.NeedsFiles) > 0 && len(plan.Changes) == 0 && plan.Dockerfile == "" && plan.Compose == "" {
			added := false
			for _, req := range plan.NeedsFiles {
				if served[req] {
					continue
				}
				served[req] = true
				if entry, ok := h.readRelevantFile(req); ok {
					files = append(files, entry)
					added = true
				}
			}
			if !added {
				files = append(files, FileEntry{Path: "(notice)", Content: "All requested files have already been provided. Produce a repair now."})
			}
			continue
		}

		// Stale-context guard: reject if files changed while the model was thinking.
		for name, before := range snapshot {
			if name == "" {
				continue
			}
			after := ""
			if h.WorkJail.Exists(name) {
				if b, err := h.WorkJail.ReadFile(name); err == nil {
					after = string(b)
				}
			}
			if HashBytes([]byte(before)) != HashBytes([]byte(after)) {
				return false, "", domain.E(domain.KindValidation, "healer.llmRepair", "context changed during LLM call; rejecting stale repair", nil)
			}
		}

		applied, summary, err := h.applyPlan(dfName, plan, f)
		if err != nil {
			if domain.IsKind(err, domain.KindValidation) {
				files = append(files, FileEntry{
					Path:    "(validation feedback)",
					Content: err.Error() + "\nUse exactly " + allowedNames(dfName) + " in the \"file\" field and retry with a corrected patch.",
				})
				continue
			}
			return false, "", err
		}
		if applied {
			return true, summary, nil
		}
	}
	return false, "", domain.E(domain.KindLLM, "healer.llmRepair", "model did not produce an applicable repair in 3 rounds", domain.ErrUnfixable)
}

func (h *Healer) applyPlan(dfName string, plan *RepairPlan, f Failure) (bool, string, error) {
	type pending struct {
		file, content string
		old           []byte
		op, summary   string
	}
	var writes []pending
	seen := map[string]bool{}

	add := func(file, content, op, summary string) error {
		file = normalizeRepairFile(file, dfName)
		if !AllowedRepairFile(file) {
			return domain.E(domain.KindValidation, "healer.applyPlan",
				"refusing to modify "+file+" (writable: "+allowedNames(dfName)+")", nil)
		}
		if seen[file] {
			return domain.E(domain.KindValidation, "healer.applyPlan",
				"multiple updates to "+file+"; send one patch per file", nil)
		}
		old, err := h.WorkJail.ReadFile(file)
		if err != nil {
			return domain.E(domain.KindIO, "healer.applyPlan", "read "+file, err)
		}
		if violations := ValidateReplacement(file, content, string(old)); len(violations) > 0 {
			return domain.E(domain.KindValidation, "healer.applyPlan",
				"invariant violation: "+strings.Join(violations, "; "), nil)
		}
		seen[file] = true
		writes = append(writes, pending{file: file, content: content, old: old, op: op, summary: summary})
		return nil
	}

	if strings.TrimSpace(plan.Dockerfile) != "" {
		target := dfName
		if target == "" {
			if len(h.Services) == 1 {
				target = "Dockerfile." + h.Services[0].Name
			} else {
				return false, "", domain.E(domain.KindValidation, "healer.applyPlan", "cannot target Dockerfile replacement", nil)
			}
		}
		if err := add(target, llm.CleanContent(plan.Dockerfile), "llm-full-replace", ""); err != nil {
			return false, "", err
		}
	}

	if strings.TrimSpace(plan.Compose) != "" {
		if err := add("docker-compose.yml", llm.CleanContent(plan.Compose), "llm-full-replace", ""); err != nil {
			return false, "", err
		}
	}

	if len(plan.Changes) > 0 {
		byFile := map[string][]Change{}
		for _, c := range plan.Changes {
			file := normalizeRepairFile(c.File, dfName)
			if !AllowedRepairFile(file) {
				return false, "", domain.E(domain.KindValidation, "healer.applyPlan",
					"refusing to modify "+c.File+" (writable: "+allowedNames(dfName)+")", nil)
			}
			c.File = file
			byFile[file] = append(byFile[file], c)
		}
		names := make([]string, 0, len(byFile))
		for n := range byFile {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			old, err := h.WorkJail.ReadFile(name)
			if err != nil {
				return false, "", domain.E(domain.KindIO, "healer.applyPlan", "read "+name, err)
			}
			content := string(old)
			for _, c := range byFile[name] {
				next, err := ApplyPatch(content, c)
				if err != nil {
					return false, "", domain.E(domain.KindValidation, "healer.applyPlan", name+": "+err.Error(), nil)
				}
				content = next
			}
			if err := add(name, content, "llm-patch", plan.Summary); err != nil {
				return false, "", err
			}
		}
	}

	if len(writes) == 0 {
		return false, "", nil
	}

	// Everything validated; commit the writes atomically per file.
	var summaries []string
	for _, w := range writes {
		if err := h.WorkJail.WriteFile(w.file, []byte(w.content), 0o644); err != nil {
			return false, "", err
		}
		_ = h.Provenance.Record(domain.RepairRecord{
			Service: f.Service, File: w.file,
			OriginalHash: HashBytes(w.old), ResultingHash: HashBytes([]byte(w.content)),
			FailureCat: f.Category, Operation: w.op, Summary: w.summary,
		})
		if w.op == "llm-full-replace" {
			summaries = append(summaries, "replaced "+w.file)
		} else {
			summaries = append(summaries, "patched "+w.file)
		}
	}
	return true, strings.Join(summaries, "; "), nil
}

// normalizeRepairFile maps model-provided names onto the only writable files.
func normalizeRepairFile(name, dfName string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	switch {
	case base == "docker-compose.yml" || base == "docker-compose.yaml":
		return "docker-compose.yml"
	case base == "Dockerfile" && dfName != "":
		return dfName
	case strings.HasPrefix(base, "Dockerfile.") && dfName != "":
		return dfName
	default:
		return base
	}
}

func allowedNames(dfName string) string {
	if dfName == "" {
		return "Dockerfile.<service>, docker-compose.yml"
	}
	return dfName + ", docker-compose.yml"
}

// FileEntry is a file included in the repair context.
type FileEntry struct {
	Path    string
	Content string
}

func (h *Healer) relevantFiles(svcName string) []FileEntry {
	var out []FileEntry
	base := []string{"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock",
		"requirements.txt", "pyproject.toml", "poetry.lock", "uv.lock", "Pipfile",
		"go.mod", "Cargo.toml", "tsconfig.json", "next.config.js", "next.config.mjs", "next.config.ts",
		"vite.config.ts", "vite.config.js", "pnpm-workspace.yaml",
		".env.example", ".env.sample", ".env.template",
		"README.md", "README", "docs/README.md",
		"main.py", "app.py", "server.py", "run.py", "wsgi.py", "asgi.py",
		"index.js", "server.js", "app.js", "main.js", "index.ts", "server.ts", "app.ts", "main.ts"}
	candidates := append([]string{}, base...)
	for _, svc := range h.Services {
		if svcName != "" && svc.Name != svcName {
			continue
		}
		if svc.Path != "" && svc.Path != "." {
			for _, c := range base {
				candidates = append(candidates, path.Join(svc.Path, c))
			}
		}
	}
	seen := map[string]bool{}
	for _, rel := range candidates {
		if seen[rel] {
			continue
		}
		seen[rel] = true
		if entry, ok := h.readRelevantFile(rel); ok {
			out = append(out, entry)
		}
	}
	return out
}

func (h *Healer) readRelevantFile(rel string) (FileEntry, bool) {
	if h.SourceJail == nil {
		return FileEntry{}, false
	}
	if !h.SourceJail.Exists(rel) {
		return FileEntry{}, false
	}
	data, err := h.SourceJail.ReadFileLimited(rel, 16*1024)
	if err != nil {
		return FileEntry{}, false
	}
	return FileEntry{Path: rel, Content: string(data)}, true
}

func (h *Healer) repairPrompt(svcName, dfName string, f Failure, output, df, compose string, files []FileEntry, runtime bool) string {
	var b strings.Builder
	mode := "BUILD"
	if runtime {
		mode = "RUNTIME"
	}
	fmt.Fprintf(&b, "A container stack failed during %s. Repair the generated Dockerfile or docker-compose.yml.\n\n", mode)
	fmt.Fprintf(&b, "FAILING SERVICE: %s\nCATEGORY: %s\nPRIMARY ERROR: %s\n", svcName, f.Category, f.Error)
	fmt.Fprintf(&b, "ALLOWED WRITABLE FILES (use these exact names in \"file\"): %s\n\n", allowedNames(dfName))
	fmt.Fprintf(&b, "RELEVANT LOG:\n%s\n\n", Tail(f.RelevantLog, 60))
	if df != "" {
		fmt.Fprintf(&b, "CURRENT DOCKERFILE:\n%s\n\n", df)
	}
	if compose != "" {
		fmt.Fprintf(&b, "CURRENT DOCKER-COMPOSE.YML:\n%s\n\n", compose)
	}
	for _, fe := range files {
		fmt.Fprintf(&b, "FILE %s:\n%s\n\n", fe.Path, fe.Content)
	}
	b.WriteString("Respond with a single JSON object only: {\"diagnosis\":\"...\",\"summary\":\"...\",\"changes\":[{\"operation\":\"replace_exact|replace_line|insert_before|insert_after|create_file\",\"file\":\"Dockerfile.<svc>|docker-compose.yml\",\"anchor\":\"...\",\"content\":\"...\",\"reason\":\"...\"}],\"unfixable\":false,\"needs_files\":[]}\n")
	b.WriteString("Rules: never disable type checking, never remove a healthcheck, never replace a command with sleep, never invent dependencies, never write outside the generated Dockerfile/compose. Prefer the smallest root-cause patch.\n")
	return b.String()
}

const repairSystemPrompt = `You are Spool's senior build-reliability engineer. You repair generated container manifests, never application source code. You are precise, conservative, and return only valid JSON.`

// Tail returns the last n lines of s.
func sourceFailureError(f Failure) error {
	detail := strings.TrimSpace(f.Error)
	if len(f.FileRefs) > 0 {
		detail = f.FileRefs[0] + ": " + detail
	}
	return domain.E(domain.KindValidation, "healer.Heal",
		"source compilation error (Spool repairs generated manifests, not application source): "+detail+
			". Fix the source, then run `spool heal <project>`.", nil)
}

func Tail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
