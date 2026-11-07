package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/generator"
	"github.com/monojitgoswami69/spool/internal/git"
	"github.com/monojitgoswami69/spool/internal/infra"
	"github.com/monojitgoswami69/spool/internal/portprobe"
	"github.com/monojitgoswami69/spool/internal/reconciler"
	"github.com/monojitgoswami69/spool/internal/safefs"
	"github.com/monojitgoswami69/spool/internal/scanner"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var (
	initNoRun   bool
	initNoAgent bool
	initName    string
	initMaxSvc  int
)

var initCmd = &cobra.Command{
	Use:   "init [path-or-repo-url]",
	Short: "Analyze a repository, generate a stack, and bring it up",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runInit,
}

func init() {
	initCmd.Flags().BoolVar(&initNoRun, "no-run", false, "generate manifests without starting containers")
	initCmd.Flags().BoolVar(&initNoAgent, "no-agent", false, "use deterministic healing only (no LLM)")
	initCmd.Flags().StringVar(&initName, "name", "", "override the project name")
	initCmd.Flags().IntVar(&initMaxSvc, "max-services", 12, "maximum number of services to include")
}

func runInit(cmd *cobra.Command, args []string) error {
	start := time.Now()
	if !flagQuiet {
		fmt.Println(ui.Banner(Version))
		fmt.Println()
	}
	target := "."
	if len(args) > 0 {
		target = args[0]
	}

	ctx := context.Background()

	// 1. Resolve source.
	logf("%s", ui.Step(1, 8, "Resolving repository source"))
	sourcePath, projectName, repoURL, isLocal, err := resolveSource(ctx, target)
	if err != nil {
		return err
	}
	logf("%s", ui.SuccessLine(sourcePath))

	// 2. Scan.
	logf("%s", ui.Step(2, 8, "Scanning services"))
	res, err := scanner.Scan(sourcePath)
	if err != nil {
		return err
	}
	services := res.Services
	if initMaxSvc > 0 && len(services) > initMaxSvc {
		services = services[:initMaxSvc]
	}
	names := make([]string, 0, len(services))
	for _, s := range services {
		names = append(names, fmt.Sprintf("%s(%s)", s.Name, s.Framework))
	}
	logf("%s", ui.SuccessLine(fmt.Sprintf("%d service(s): %s", len(services), strings.Join(names, ", "))))

	// 3. Prepare project + allocator.
	logf("%s", ui.Step(3, 8, "Discovering environment variables"))
	identity := sourcePath
	if repoURL != "" {
		identity = repoURL
	}
	id := state.IDFor(projectName, identity)
	mgr, err := state.Ensure(id)
	if err != nil {
		return err
	}
	project := &domain.Project{
		ID: id, Name: projectName, SourcePath: sourcePath, IsLocal: isLocal,
		WorkDir: mgr.Dir, Services: services, NetworkName: id + "-net",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	project.Env = envvar.DiscoverEnvVars(sourcePath)
	sourceEnv := loadSourceEnv(sourcePath, services)
	project.Env = mergeDiscoveredEnv(project.Env, sourceEnv)
	logf("%s", ui.SuccessLine(fmt.Sprintf("%d variable(s) (%d from repo .env)", len(project.Env), len(sourceEnv))))

	// 4. Infer infra FIRST so its host ports are reserved before app ports.
	logf("%s", ui.Step(4, 8, "Inferring infrastructure"))
	alloc := portprobe.New()
	deps := dependencySet(services)
	infras, err := infra.Infer(project.Env, deps, infra.Options{SourcePath: sourcePath, Allocator: alloc})
	if err != nil {
		return err
	}
	project.InfraServices = infras
	if err := reconciler.Reconcile(project.Services, alloc, map[string]string{}); err != nil {
		return err
	}
	logf("%s", ui.SuccessLine(describeInfra(infras)))

	// 5. Generate manifests.
	logf("%s", ui.Step(5, 8, "Generating Dockerfiles and compose"))
	lock := &domain.Lock{
		Version: Version, Project: projectName, ProjectID: id, RepoURL: repoURL,
		RepoPath: sourcePath, WorkDir: mgr.Dir, Services: project.Services, Infra: infras,
		PortMap: portMap(project.Services), Env: project.Env, NetworkName: project.NetworkName,
		DetectionHash: state.HashDetection(project.Services), LastInit: time.Now(),
	}
	out, err := generator.WriteOutputs(project, mgr.Dir, sourcePath)
	if err != nil {
		return err
	}
	logf("%s", ui.SuccessLine(fmt.Sprintf("%d manifest(s) in %s", len(out.Files), mgr.Dir)))

	// 6. Write .env and lock.
	logf("%s", ui.Step(6, 8, "Writing environment and state"))
	if len(sourceEnv) > 0 {
		existing, _ := mgr.LoadEnv()
		for k, v := range sourceEnv {
			existing[k] = v
		}
		if err := mgr.SaveEnv(existing); err != nil {
			return err
		}
	}
	if err := writeProjectEnv(mgr, lock); err != nil {
		return err
	}
	if err := mgr.SaveLock(lock); err != nil {
		return err
	}
	logf("%s", ui.SuccessLine("state persisted"))

	if initNoRun {
		logf("%s", ui.Step(7, 8, "Skipping run (--no-run)"))
		logf("%s", ui.Step(8, 8, fmt.Sprintf("Ready in %.1fs", time.Since(start).Seconds())))
		logf("")
		logf("Next: %s", ui.BoldStyle.Render("spool up "+mgr.ID))
		return nil
	}

	// 7. Orchestrate + heal (closed loop; health is never discarded).
	logf("%s", ui.Step(7, 8, "Building and verifying the stack"))
	if err := requireDocker(ctx); err != nil {
		return err
	}
	var client domain.LLMClient
	if !initNoAgent {
		if c, lerr := llmFor(true); lerr == nil {
			client = c
			verbosef("LLM repair enabled")
		} else {
			logf("%s", ui.WarningLine("LLM unavailable ("+lerr.Error()+"); using deterministic repairs only"))
		}
	}
	h, err := healerFor(mgr, lock, client)
	if err != nil {
		return err
	}
	h.MaxTries = 3
	healRes, err := h.Heal(ctx, "", "")
	if err != nil {
		if healRes != nil {
			for _, a := range healRes.Attempts {
				logf("  attempt %d [%s] %s", a.N, a.Kind, a.Summary)
			}
			logf("%s", ui.ErrorLine(healRes.Summary))
		}
		return err
	}
	lock.LastUp = time.Now()
	if err := mgr.SaveLock(lock); err != nil {
		return err
	}

	// 8. Report.
	logf("%s", ui.Step(8, 8, fmt.Sprintf("Stack healthy in %.1fs", time.Since(start).Seconds())))
	logf("")
	showStatus(ctx, mgr, lock)
	printURLs(lock)
	logf("")
	logf("%s", ui.InfoLine(fmt.Sprintf("project %s · state %s", mgr.ID, mgr.Dir)))
	return nil
}

func resolveSource(ctx context.Context, target string) (sourcePath, name, repoURL string, isLocal bool, err error) {
	if git.IsRemote(target) {
		parsed, perr := git.ParseURL(target)
		if perr != nil {
			return "", "", "", false, perr
		}
		reposDir, derr := config.Dir("repos")
		if derr != nil {
			return "", "", "", false, derr
		}
		id := state.IDFor(parsed.Repo, parsed.Clone)
		dest := filepath.Join(reposDir, id)
		if entries, rerr := os.ReadDir(dest); rerr != nil || len(entries) == 0 {
			if err := safefs.EnsureDir(dest, 0o755); err != nil {
				return "", "", "", false, err
			}
			logf("%s", ui.InfoLine("cloning "+parsed.Clone))
			if err := git.Clone(ctx, target, dest, config.GitHubToken(), parsed.Branch); err != nil {
				return "", "", "", false, err
			}
		} else {
			logf("%s", ui.InfoLine("reusing clone at "+dest))
		}
		root := dest
		if parsed.Subdir != "" {
			sub := filepath.Join(dest, filepath.FromSlash(parsed.Subdir))
			ok, serr := safefs.IsSubPath(dest, sub)
			if serr != nil || !ok {
				return "", "", "", false, domain.E(domain.KindUnsafePath, "init.resolveSource", "subdirectory escapes the clone", nil)
			}
			root = sub
		}
		return root, parsed.Repo, parsed.Clone, false, nil
	}
	abs, aerr := filepath.Abs(target)
	if aerr != nil {
		return "", "", "", false, aerr
	}
	info, serr := os.Stat(abs)
	if serr != nil || !info.IsDir() {
		return "", "", "", false, domain.E(domain.KindNotFound, "init.resolveSource", "not a directory: "+target, serr)
	}
	return abs, filepath.Base(abs), "", true, nil
}

func dependencySet(services []*domain.Service) map[string]bool {
	deps := map[string]bool{}
	for _, s := range services {
		for _, d := range s.Dependencies {
			deps[strings.ToLower(d)] = true
		}
	}
	return deps
}

func describeInfra(infras []*domain.InfraService) string {
	if len(infras) == 0 {
		return "no backing services inferred"
	}
	parts := make([]string, 0, len(infras))
	for _, i := range infras {
		mode := "local"
		if i.Mode == domain.InfraModeExternal {
			mode = "external:" + i.Provider
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", i.Name, mode))
	}
	sort.Strings(parts)
	return "inferred: " + strings.Join(parts, ", ")
}

func portMap(services []*domain.Service) map[string]int {
	out := map[string]int{}
	for _, s := range services {
		if s.HostPort > 0 {
			out[s.Name] = s.HostPort
		}
	}
	return out
}

// loadSourceEnv reads the repository's own .env files (including the user's
// real .env) so generated stacks inherit required configuration. Values from
// more specific files win, and only non-empty values are imported.
func loadSourceEnv(root string, services []*domain.Service) map[string]string {
	out := map[string]string{}
	names := []string{".env.example", ".env.sample", ".env.template", ".env", ".env.local", ".env.development.local", ".env.production.local"}
	dirs := []string{root}
	for _, svc := range services {
		if svc.Path != "" && svc.Path != "." {
			dirs = append(dirs, filepath.Join(root, filepath.FromSlash(svc.Path)))
		}
	}
	for _, dir := range dirs {
		for _, name := range names {
			m, err := envvar.ParseEnvFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			for k, v := range m {
				if strings.TrimSpace(v) != "" {
					out[k] = v
				}
			}
		}
	}
	return out
}

// mergeDiscoveredEnv adds repo .env values as user-sourced variables.
func mergeDiscoveredEnv(discovered []*domain.EnvVar, source map[string]string) []*domain.EnvVar {
	seen := map[string]bool{}
	for _, ev := range discovered {
		seen[ev.Key] = true
		if v, ok := source[ev.Key]; ok && strings.TrimSpace(v) != "" {
			ev.Value = v
			ev.Default = v
			ev.Required = false
		}
	}
	keys := make([]string, 0, len(source))
	for k := range source {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		discovered = append(discovered, &domain.EnvVar{Key: k, Value: source[k], Default: source[k], Source: domain.EnvSourceUser})
	}
	sort.Slice(discovered, func(i, j int) bool { return discovered[i].Key < discovered[j].Key })
	return discovered
}
