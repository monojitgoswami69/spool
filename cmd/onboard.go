package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/agent"
	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/generator"
	"github.com/monojitgoswami69/spool/internal/healer"
	"github.com/monojitgoswami69/spool/internal/infra"
	"github.com/monojitgoswami69/spool/internal/portprobe"
	"github.com/monojitgoswami69/spool/internal/reconciler"
	"github.com/monojitgoswami69/spool/internal/scanner"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

type onboardOptions struct {
	Target      string
	Name        string
	MaxServices int
	NoRun       bool
	NoAgent     bool
	LLM         domain.LLMClient
	Logf        func(format string, args ...any)
	Progress    func(step, total int, title string)
}

type onboardResult struct {
	Manager      *state.Manager
	Project      *domain.Project
	Lock         *domain.Lock
	Output       *generator.Output
	Heal         *healer.Result
	URLs         []string
	Missing      []string
	ServiceNames []string
	InfraDescs   []string
	Elapsed      time.Duration
	NoRun        bool
	Reused       bool
	Intel        domain.RepoIntel
}

// runOnboarding is the shared end-to-end pipeline used by `spool init` and the
// agent's onboard_repository tool.
func runOnboarding(ctx context.Context, o onboardOptions) (*onboardResult, error) {
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	progress := o.Progress
	if progress == nil {
		progress = func(int, int, string) {}
	}
	start := time.Now()

	// 1. Resolve source.
	progress(1, 8, "Resolving repository source")
	sourcePath, projectName, repoURL, isLocal, err := resolveSource(ctx, o.Target, logf)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(o.Name) != "" {
		projectName = strings.TrimSpace(o.Name)
	}
	logf("%s", ui.SuccessLine(sourcePath))

	// 2. Scan.
	progress(2, 8, "Scanning services")
	res, err := scanner.Scan(sourcePath)
	if err != nil {
		return nil, err
	}
	services := res.Services
	if o.MaxServices > 0 && len(services) > o.MaxServices {
		services = services[:o.MaxServices]
	}
	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, fmt.Sprintf("%s(%s)", s.Name, s.Framework))
	}
	logf("%s", ui.SuccessLine(fmt.Sprintf("%d service(s): %s", len(services), strings.Join(names, ", "))))

	// 3. Project + environment.
	progress(3, 8, "Discovering environment variables")
	identity := sourcePath
	if repoURL != "" {
		identity = repoURL
	}
	id := state.IDFor(projectName, identity)
	mgr, err := state.Ensure(id)
	if err != nil {
		return nil, err
	}
	if len(res.Intel.Dockerfiles) > 0 {
		logf("%s", ui.InfoLine("found existing Dockerfile(s): "+strings.Join(res.Intel.Dockerfiles, ", ")))
	}
	if len(res.Intel.ComposeFiles) > 0 {
		logf("%s", ui.InfoLine("found existing compose file(s): "+strings.Join(res.Intel.ComposeFiles, ", ")+" (Spool generates a managed compose)"))
	}
	project := &domain.Project{
		ID: id, Name: projectName, SourcePath: sourcePath, IsLocal: isLocal,
		WorkDir: mgr.Dir, Services: services, NetworkName: id + "-net",
		Intel: res.Intel, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	project.Env = envvar.DiscoverEnvVars(sourcePath)
	sourceEnv := loadSourceEnv(sourcePath, services)
	project.Env = mergeDiscoveredEnv(project.Env, sourceEnv)
	logf("%s", ui.SuccessLine(fmt.Sprintf("%d variable(s) (%d from repo .env)", len(project.Env), len(sourceEnv))))

	// 4. Infra first (reserves host ports before app allocation).
	progress(4, 8, "Inferring infrastructure")
	alloc := portprobe.New()
	deps := dependencySet(services)
	infras, err := infra.Infer(project.Env, deps, infra.Options{SourcePath: sourcePath, Allocator: alloc})
	if err != nil {
		return nil, err
	}
	project.InfraServices = infras
	if err := reconciler.Reconcile(project.Services, alloc, map[string]string{}); err != nil {
		return nil, err
	}
	logf("%s", ui.SuccessLine(describeInfra(infras)))

	// 5. Generate.
	progress(5, 8, "Generating Dockerfiles and compose")
	lock := &domain.Lock{
		Version: Version, Project: projectName, ProjectID: id, RepoURL: repoURL,
		RepoPath: sourcePath, WorkDir: mgr.Dir, Services: project.Services, Infra: infras,
		PortMap: portMap(project.Services), Env: project.Env, Intel: project.Intel, NetworkName: project.NetworkName,
		DetectionHash: state.HashDetection(project.Services), LastInit: time.Now(),
	}
	out, err := generator.WriteOutputs(project, mgr.Dir, sourcePath)
	if err != nil {
		return nil, err
	}
	logf("%s", ui.SuccessLine(fmt.Sprintf("%d manifest(s) in %s", len(out.Files), mgr.Dir)))
	if out.Reused {
		logf("%s", ui.InfoLine("reusing existing Dockerfile(s) where available"))
	}

	// 6. Persist env/state.
	progress(6, 8, "Writing environment and state")
	if len(sourceEnv) > 0 {
		existing, _ := mgr.LoadEnv()
		for k, v := range sourceEnv {
			existing[k] = v
		}
		if err := mgr.SaveEnv(existing); err != nil {
			return nil, err
		}
	}
	if err := writeProjectEnv(mgr, lock); err != nil {
		return nil, err
	}
	if err := mgr.SaveLock(lock); err != nil {
		return nil, err
	}
	logf("%s", ui.SuccessLine("state persisted"))
	if docker.Available() {
		if err := composeFor(mgr).Validate(ctx); err != nil {
			return nil, fmt.Errorf("generated compose is invalid: %w", err)
		}
	}

	result := &onboardResult{
		Manager: mgr, Project: project, Lock: lock, Output: out,
		Missing: missingEnvKeys(mgr, lock), URLs: urlStrings(lock),
		ServiceNames: names, InfraDescs: describeInfraList(infras), Elapsed: time.Since(start),
		NoRun: o.NoRun, Reused: out.Reused, Intel: res.Intel,
	}

	if o.NoRun {
		progress(7, 8, "Skipping run (--no-run)")
		progress(8, 8, fmt.Sprintf("Ready in %.1fs", time.Since(start).Seconds()))
		return result, nil
	}

	// 7. Build + heal + verify.
	progress(7, 8, "Building and verifying the stack")
	if err := requireDocker(ctx); err != nil {
		return result, err
	}
	client := o.LLM
	if !o.NoAgent && client == nil {
		if c, lerr := llmFor(true); lerr == nil {
			client = c
		} else {
			logf("%s", ui.WarningLine("LLM unavailable ("+lerr.Error()+"); using deterministic repairs only"))
		}
	}
	healOnce := func() (*healer.Result, error) {
		h, err := healerFor(mgr, lock, client)
		if err != nil {
			return nil, err
		}
		h.MaxTries = 3
		return h.Heal(ctx, "", "")
	}
	healRes, herr := healOnce()
	result.Heal = healRes
	if herr != nil && out.Reused {
		logf("%s", ui.WarningLine("existing Dockerfile(s) did not build; regenerating managed Dockerfiles and retrying"))
		fresh, ferr := generator.WriteOutputsOptions(project, mgr.Dir, sourcePath, generator.Options{
			RepoRoot: sourcePath, WorkDir: mgr.Dir, ReuseExisting: false,
		})
		if ferr != nil {
			writeFailureRecord(mgr, healRes)
			return result, ferr
		}
		out = fresh
		result.Output = out
		result.Reused = false
		healRes, herr = healOnce()
		result.Heal = healRes
	}
	result.Reused = out.Reused
	if herr != nil {
		writeFailureRecord(mgr, healRes)
		result.Missing = missingEnvKeys(mgr, lock)
		result.URLs = urlStrings(lock)
		return result, herr
	}
	lock.LastUp = time.Now()
	clearFailureLog(mgr)
	if err := mgr.SaveLock(lock); err != nil {
		return result, err
	}
	result.Missing = missingEnvKeys(mgr, lock)
	result.URLs = urlStrings(lock)
	result.Elapsed = time.Since(start)
	progress(8, 8, fmt.Sprintf("Stack healthy in %.1fs", result.Elapsed.Seconds()))
	return result, nil
}

func describeInfraList(infras []*domain.InfraService) []string {
	out := make([]string, 0, len(infras))
	for _, i := range infras {
		mode := "local"
		if i.Mode == domain.InfraModeExternal {
			mode = "external:" + i.Provider
		}
		out = append(out, fmt.Sprintf("%s(%s)", i.Name, mode))
	}
	return out
}

// onboardForAgent adapts the shared pipeline to the agent tool.
func onboardForAgent(ctx context.Context, target string, maxServices int, noRun bool, progress func(string), client domain.LLMClient) (*agent.OnboardResult, error) {
	res, err := runOnboarding(ctx, onboardOptions{
		Target: target, MaxServices: maxServices, NoRun: noRun,
		NoAgent: client == nil, LLM: client,
		Logf: func(string, ...any) {},
		Progress: func(step, total int, title string) {
			if progress != nil {
				progress(fmt.Sprintf("[%d/%d] %s", step, total, title))
			}
		},
	})
	if res == nil {
		return nil, err
	}
	out := &agent.OnboardResult{
		Project: res.Project.Name, ProjectID: res.Manager.ID,
		Services: res.ServiceNames, Infra: res.InfraDescs,
		URLs: res.URLs, Missing: res.Missing, NoRun: res.NoRun, ReusedDockerfiles: res.Reused,
	}
	if res.Heal != nil {
		out.HealSummary = res.Heal.Summary
	}
	return out, err
}
