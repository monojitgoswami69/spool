package healer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/safefs"
)

var errBuild = errors.New("build failed")

type buildOut struct {
	out string
	err error
}

// fakeCompose implements domain.ComposeController for tests.
type fakeCompose struct {
	outputs []buildOut
	idx     int
	ps      []domain.Container
	upErr   error
	downs   int
}

func (f *fakeCompose) Build(ctx context.Context, service string) (string, error) {
	if f.idx >= len(f.outputs) {
		return "ok", nil
	}
	o := f.outputs[f.idx]
	f.idx++
	return o.out, o.err
}
func (f *fakeCompose) Up(ctx context.Context, extra ...string) (string, error) { return "", f.upErr }
func (f *fakeCompose) Down(ctx context.Context, volumes bool) (string, error) {
	f.downs++
	return "", nil
}
func (f *fakeCompose) Ps(ctx context.Context) ([]domain.Container, error) { return f.ps, nil }
func (f *fakeCompose) Logs(ctx context.Context, service string, tail int) (string, error) {
	return "runtime log", nil
}
func (f *fakeCompose) Stats(ctx context.Context) ([]domain.ContainerStat, error) { return nil, nil }
func (f *fakeCompose) Restart(ctx context.Context, service string) (string, error) {
	return "", nil
}
func (f *fakeCompose) Stop(ctx context.Context, service string) (string, error) { return "", nil }
func (f *fakeCompose) Start(ctx context.Context, service string) (string, error) {
	return "", nil
}
func (f *fakeCompose) Recreate(ctx context.Context, service string) (string, error) {
	return "", nil
}
func (f *fakeCompose) Available() bool { return true }

type fakeLLM struct {
	response string
	calls    int
}

func (f *fakeLLM) Call(ctx context.Context, system, user string) (string, error) {
	f.calls++
	return f.response, nil
}
func (f *fakeLLM) Provider() string { return "fake" }
func (f *fakeLLM) Model() string    { return "fake-1" }

func healthyPS() []domain.Container {
	return []domain.Container{{Service: "api", State: "running", Health: "healthy"}}
}

func TestAnalyzeFailureCategories(t *testing.T) {
	f := Analyze("#10 [api 3/5] RUN npm ci\nnpm error Cannot find module 'left-pad'", "api")
	if f.Category != CatDependency {
		t.Fatalf("category = %q (%s)", f.Category, f.Error)
	}
	f = Analyze("Error: DATABASE_URL is not defined", "api")
	if f.Category != CatMissingEnv {
		t.Fatalf("category = %q", f.Category)
	}
}

func TestFingerprintStable(t *testing.T) {
	a := Analyze("error TS2307: Cannot find module 'x' at /tmp/a/b.ts(1,2)", "")
	b := Analyze("error TS2307: Cannot find module 'x' at /tmp/c/d.ts(9,9)", "")
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatalf("fingerprints differ:\n%s\n%s", a.Fingerprint(), b.Fingerprint())
	}
	if ClassifyProgression(&a, &a) != "same_failure" {
		t.Fatal("expected same_failure")
	}
	if ClassifyProgression(&a, nil) != "first" {
		t.Fatal("expected first")
	}
}

func TestApplyDeterministicLockfile(t *testing.T) {
	content := "FROM node:20-alpine\nRUN npm ci\nCOPY . .\n"
	out, rule, _, ok := ApplyDeterministic(content, "npm ci can only install packages with an existing package-lock.json")
	if !ok || rule != "LockfileDesync" {
		t.Fatalf("rule=%q ok=%v", rule, ok)
	}
	if !strings.Contains(out, "npm install --legacy-peer-deps") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestApplyDeterministicNodeGypAlpine(t *testing.T) {
	content := "FROM node:20-alpine\nRUN npm ci\n"
	out, _, _, ok := ApplyDeterministic(content, "gyp ERR! stack Error: not found: make")
	if !ok || !strings.Contains(out, "apk add --no-cache python3 make g++") {
		t.Fatalf("expected alpine build tools:\n%s", out)
	}
}

func TestApplyDeterministicNodeGypDebian(t *testing.T) {
	content := "FROM node:20-slim\nRUN npm ci\n"
	out, _, _, ok := ApplyDeterministic(content, "g++: not found")
	if !ok || !strings.Contains(out, "apt-get install -y --no-install-recommends python3 make g++") {
		t.Fatalf("expected debian build tools:\n%s", out)
	}
}

func TestApplyDeterministicPythonVersion(t *testing.T) {
	content := "FROM python:3.10-slim\nRUN pip install -r requirements.txt\n"
	out, rule, _, ok := ApplyDeterministic(content, "ERROR: Package requires a different Python: 3.10.13 not in '>=3.12'")
	if !ok || rule != "PythonVersionMismatch" {
		t.Fatalf("rule=%q ok=%v", rule, ok)
	}
	if !strings.Contains(out, "FROM python:3.12-slim") {
		t.Fatalf("unexpected:\n%s", out)
	}
}

func TestApplyDeterministicHeapAfterFrom(t *testing.T) {
	content := "FROM node:20-alpine AS builder\nRUN npm run build\n"
	out, _, _, ok := ApplyDeterministic(content, "FATAL ERROR: JavaScript heap out of memory")
	if !ok {
		t.Fatal("expected heap fix")
	}
	if strings.Index(out, "NODE_OPTIONS") < strings.Index(out, "FROM ") {
		t.Fatalf("ENV inserted before FROM:\n%s", out)
	}
}

func TestApplyDeterministicMissingEnv(t *testing.T) {
	content := "FROM python:3.11-slim\nRUN pip install -r requirements.txt\n"
	out, _, _, ok := ApplyDeterministic(content, "DATABASE_URL is not defined")
	if !ok || !strings.Contains(out, "ENV DATABASE_URL=spool-build-placeholder") {
		t.Fatalf("expected env injection:\n%s", out)
	}
	out2, _, _, ok2 := ApplyDeterministic(content, "API_KEY is not defined")
	if ok2 || strings.Contains(out2, "API_KEY=") {
		t.Fatalf("secret should not be injected:\n%s", out2)
	}
}

func TestValidateReplacementInvariants(t *testing.T) {
	orig := "FROM node:20-alpine\nRUN npm ci\nHEALTHCHECK CMD curl -f http://localhost/\n"
	bad := []string{
		"FROM node:20-alpine\nRUN npm ci\nRUN echo ignoreBuildErrors\nHEALTHCHECK CMD curl -f http://localhost/\n",
		"FROM node:20-alpine\nCMD sleep 1000\nHEALTHCHECK CMD curl -f http://localhost/\n",
	}
	for _, b := range bad {
		if v := ValidateReplacement("Dockerfile.api", b, orig); len(v) == 0 {
			t.Errorf("expected invariant violation for:\n%s", b)
		}
	}
	compose := "services:\n  api:\n    build:\n      context: ..\n    healthcheck:\n      test: x\n"
	if v := ValidateReplacement("docker-compose.yml", "services:\n  api:\n    build:\n      context: /abs\n", compose); len(v) == 0 {
		t.Fatal("expected absolute path violation")
	}
}

func TestApplyPatchOperations(t *testing.T) {
	base := "line1\nline2\nline3\n"
	out, err := ApplyPatch(base, Change{Operation: "replace_exact", Anchor: "line2", Content: "TWO"})
	if err != nil || !strings.Contains(out, "TWO") {
		t.Fatalf("replace_exact: %v %q", err, out)
	}
	out, err = ApplyPatch(base, Change{Operation: "insert_after", Anchor: "line1", Content: "inserted"})
	if err != nil || !strings.Contains(out, "line1\ninserted") {
		t.Fatalf("insert_after: %v %q", err, out)
	}
	out, err = ApplyPatch(base, Change{Operation: "insert_before", Anchor: "line3", Content: "before3"})
	if err != nil || !strings.Contains(out, "before3\nline3") {
		t.Fatalf("insert_before: %v %q", err, out)
	}
	if _, err := ApplyPatch(base, Change{Operation: "replace_exact", Anchor: "missing", Content: "x"}); err == nil {
		t.Fatal("expected missing anchor error")
	}
}

func TestProvenancePersists(t *testing.T) {
	dir := t.TempDir()
	p := NewProvenance(filepath.Join(dir, "repairs.json"))
	if err := p.Record(domain.RepairRecord{File: "Dockerfile.api", OriginalHash: HashBytes([]byte("a")), ResultingHash: HashBytes([]byte("b")), Operation: "test"}); err != nil {
		t.Fatal(err)
	}
	reloaded := NewProvenance(filepath.Join(dir, "repairs.json"))
	if len(reloaded.Records()) != 1 {
		t.Fatalf("expected 1 record, got %d", len(reloaded.Records()))
	}
	rec := reloaded.Records()[0]
	if rec.ID == "" || rec.OriginalHash != HashBytes([]byte("a")) || rec.ResultingHash != HashBytes([]byte("b")) {
		t.Fatalf("unexpected record %+v", rec)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "repairs.json"))
	var wrapper struct {
		Repairs []domain.RepairRecord `json:"repairs"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil || len(wrapper.Repairs) != 1 {
		t.Fatalf("bad persisted JSON: %v", err)
	}
}

func newHealerTest(t *testing.T, content string, compose domain.ComposeController, llmClient domain.LLMClient) *Healer {
	t.Helper()
	work := t.TempDir()
	jail, err := safefs.NewJail(work)
	if err != nil {
		t.Fatal(err)
	}
	if err := jail.WriteFile("Dockerfile.api", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Healer{
		Compose:       compose,
		LLM:           llmClient,
		WorkJail:      jail,
		Services:      []*domain.Service{{Name: "api", Language: domain.LangNode, Framework: domain.FrameworkExpress}},
		MaxTries:      3,
		Provenance:    NewProvenance(filepath.Join(work, "repairs.json")),
		HealthTimeout: 50 * time.Millisecond,
		PollInterval:  5 * time.Millisecond,
	}
}

func TestHealDeterministicLoop(t *testing.T) {
	compose := &fakeCompose{
		outputs: []buildOut{
			{out: "npm ci can only install packages with an existing package-lock.json", err: errBuild},
			{out: "build ok", err: nil},
		},
		ps: healthyPS(),
	}
	h := newHealerTest(t, "FROM node:20-alpine\nRUN npm ci\n", compose, nil)
	res, err := h.Heal(context.Background(), "", "api")
	if err != nil {
		t.Fatalf("heal: %v", err)
	}
	if !res.Success || !res.Verified {
		t.Fatalf("expected verified success: %+v", res)
	}
	out, _ := h.WorkJail.ReadFile("Dockerfile.api")
	if !strings.Contains(string(out), "npm install --legacy-peer-deps") {
		t.Fatalf("deterministic fix not applied:\n%s", out)
	}
	if len(h.Provenance.Records()) != 1 {
		t.Fatal("expected provenance record")
	}
}

func TestHealLLMRepairLoop(t *testing.T) {
	plan := `{"diagnosis":"bad install","summary":"use npm install","changes":[{"operation":"replace_exact","file":"Dockerfile.api","anchor":"RUN npm ci","content":"RUN npm install","reason":"lockfile"}]}`
	llmClient := &fakeLLM{response: plan}
	compose := &fakeCompose{
		outputs: []buildOut{
			{out: "some completely unknown build explosion", err: errBuild},
			{out: "build ok", err: nil},
		},
		ps: healthyPS(),
	}
	h := newHealerTest(t, "FROM node:20-alpine\nRUN npm ci\n", compose, llmClient)
	res, err := h.Heal(context.Background(), "", "api")
	if err != nil {
		t.Fatalf("heal: %v", err)
	}
	if !res.Success || !res.Verified {
		t.Fatalf("expected verified success: %+v", res)
	}
	if llmClient.calls == 0 {
		t.Fatal("expected LLM to be consulted")
	}
	out, _ := h.WorkJail.ReadFile("Dockerfile.api")
	if !strings.Contains(string(out), "RUN npm install") {
		t.Fatalf("LLM patch not applied:\n%s", out)
	}
}

func TestHealRuntimeNeverDiscardsHealth(t *testing.T) {
	compose := &fakeCompose{
		outputs: []buildOut{{out: "build ok", err: nil}},
		ps:      []domain.Container{{Service: "api", State: "exited", ExitCode: 1}},
	}
	h := newHealerTest(t, "FROM node:20-alpine\nRUN npm ci\n", compose, nil)
	h.HealthTimeout = 30 * time.Millisecond
	h.PollInterval = 5 * time.Millisecond
	res, err := h.Heal(context.Background(), "", "api")
	if err == nil {
		t.Fatal("expected health error, not false success")
	}
	if res.Success || res.Verified {
		t.Fatalf("must not report success: %+v", res)
	}
}

func TestVerifyRuntimeUnhealthy(t *testing.T) {
	compose := &fakeCompose{ps: []domain.Container{{Service: "api", State: "running", Health: "unhealthy"}}}
	h := newHealerTest(t, "FROM node:20-alpine\n", compose, nil)
	h.HealthTimeout = 30 * time.Millisecond
	h.PollInterval = 5 * time.Millisecond
	if err := h.VerifyRuntime(context.Background()); err == nil {
		t.Fatal("expected unhealthy error")
	}
}

func TestTail(t *testing.T) {
	if got := Tail("a\nb\nc", 2); got != "b\nc" {
		t.Fatalf("got %q", got)
	}
	if got := Tail("", 2); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestVerifyRuntimeFailsFastOnExitedContainer(t *testing.T) {
	compose := &fakeCompose{ps: []domain.Container{{Service: "api", State: "exited", ExitCode: 1}}}
	h := newHealerTest(t, "FROM python:3.11-slim\n", compose, nil)
	h.HealthTimeout = 10 * time.Second // must not wait this long
	h.PollInterval = 100 * time.Millisecond
	start := time.Now()
	if err := h.VerifyRuntime(context.Background()); err == nil {
		t.Fatal("expected exited container to fail")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("exited container took %s to fail", elapsed)
	}
}

func TestApplyPlanNormalizesGenericDockerfileName(t *testing.T) {
	work := t.TempDir()
	jail, err := safefs.NewJail(work)
	if err != nil {
		t.Fatal(err)
	}
	if err := jail.WriteFile("Dockerfile.api", []byte("FROM node:20-alpine\nRUN npm ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Healer{WorkJail: jail, Provenance: NewProvenance(""), Services: []*domain.Service{{Name: "api"}}}
	plan := &RepairPlan{Changes: []Change{{Operation: "replace_exact", File: "Dockerfile", Anchor: "RUN npm ci", Content: "RUN npm install"}}}
	applied, _, err := h.applyPlan("Dockerfile.api", plan, Failure{})
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v", applied, err)
	}
	got, _ := jail.ReadFile("Dockerfile.api")
	if !strings.Contains(string(got), "RUN npm install") {
		t.Fatalf("patch not applied:\n%s", got)
	}
}

type scriptedHealLLM struct {
	responses []string
	i         int
	calls     int
}

func (s *scriptedHealLLM) Call(ctx context.Context, system, user string) (string, error) {
	s.calls++
	if s.i >= len(s.responses) {
		return s.responses[len(s.responses)-1], nil
	}
	r := s.responses[s.i]
	s.i++
	return r, nil
}
func (s *scriptedHealLLM) Provider() string { return "scripted" }
func (s *scriptedHealLLM) Model() string    { return "scripted" }

func TestLLMRepairRetriesAfterValidationFeedback(t *testing.T) {
	work := t.TempDir()
	jail, err := safefs.NewJail(work)
	if err != nil {
		t.Fatal(err)
	}
	if err := jail.WriteFile("Dockerfile.api", []byte("FROM node:20-alpine\nRUN npm ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	llmClient := &scriptedHealLLM{responses: []string{
		`{"changes":[{"operation":"replace_exact","file":"main.py","anchor":"x","content":"y"}]}`,
		`{"changes":[{"operation":"replace_exact","file":"Dockerfile","anchor":"RUN npm ci","content":"RUN npm install"}],"summary":"fix install"}`,
	}}
	h := &Healer{
		LLM: llmClient, WorkJail: jail, Provenance: NewProvenance(""),
		Services: []*domain.Service{{Name: "api"}},
	}
	applied, summary, err := h.llmRepair(context.Background(), "api", "Dockerfile.api",
		Failure{Category: CatDockerBuild, Error: "boom"}, "boom", false)
	if err != nil || !applied {
		t.Fatalf("applied=%v err=%v summary=%q", applied, err, summary)
	}
	if llmClient.calls != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", llmClient.calls)
	}
	got, _ := jail.ReadFile("Dockerfile.api")
	if !strings.Contains(string(got), "RUN npm install") {
		t.Fatalf("patch not applied:\n%s", got)
	}
}

func TestVerifyRuntimeCrashLoopFailsFast(t *testing.T) {
	compose := &fakeCompose{ps: []domain.Container{{Service: "api", State: "restarting", ExitCode: 0}}}
	h := newHealerTest(t, "FROM python:3.11-slim\n", compose, nil)
	h.HealthTimeout = 10 * time.Second
	h.PollInterval = 20 * time.Millisecond
	h.RestartGrace = 150 * time.Millisecond
	start := time.Now()
	if err := h.VerifyRuntime(context.Background()); err == nil {
		t.Fatal("expected crash loop to fail")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("crash loop took %s to fail", elapsed)
	}
}
