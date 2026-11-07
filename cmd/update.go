package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/generator"
	"github.com/monojitgoswami69/spool/internal/infra"
	"github.com/monojitgoswami69/spool/internal/portprobe"
	"github.com/monojitgoswami69/spool/internal/reconciler"
	"github.com/monojitgoswami69/spool/internal/scanner"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var (
	updateNoRun   bool
	updateNoAgent bool
)

var updateCmd = &cobra.Command{
	Use:     "update [project]",
	Aliases: []string{"refresh"},
	Short:   "Re-scan a project, regenerate manifests (preserving host ports), and rebuild",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runUpdate,
}

func init() {
	updateCmd.Flags().BoolVar(&updateNoRun, "no-run", false, "regenerate manifests without rebuilding")
	updateCmd.Flags().BoolVar(&updateNoAgent, "no-agent", false, "deterministic healing only")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	mgr, lock, err := resolveProject(name)
	if err != nil {
		return err
	}
	if lock.RepoPath == "" {
		return domain.E(domain.KindValidation, "update", "project has no source path", nil)
	}

	logf("%s", ui.Step(1, 5, "Re-scanning "+lock.RepoPath))
	res, err := scanner.Scan(lock.RepoPath)
	if err != nil {
		return err
	}
	services := res.Services
	if len(services) > 24 {
		services = services[:24]
	}

	logf("%s", ui.Step(2, 5, "Refreshing environment and infrastructure"))
	project := &domain.Project{
		ID: lock.ProjectID, Name: lock.Project, SourcePath: lock.RepoPath, WorkDir: mgr.Dir,
		Services: services, NetworkName: lock.NetworkName,
	}
	if project.NetworkName == "" {
		project.NetworkName = lock.ProjectID + "-net"
	}
	project.Env = mergeDiscoveredEnv(envvar.DiscoverEnvVars(lock.RepoPath), loadSourceEnv(lock.RepoPath, services))

	alloc := portprobe.New()
	// Reserve current app host ports so refresh never moves live URLs.
	for _, hp := range lock.PortMap {
		alloc.Reserve(hp)
	}
	infras, err := infra.Infer(project.Env, dependencySet(services), infra.Options{SourcePath: lock.RepoPath, Allocator: alloc})
	if err != nil {
		return err
	}
	project.InfraServices = infras
	if err := reconciler.ReconcileWithExisting(project.Services, alloc, map[string]string{}, lock.PortMap); err != nil {
		return err
	}

	logf("%s", ui.Step(3, 5, "Regenerating manifests"))
	lock.Services = project.Services
	lock.Infra = infras
	lock.PortMap = portMap(project.Services)
	lock.Env = project.Env
	lock.NetworkName = project.NetworkName
	lock.DetectionHash = state.HashDetection(project.Services)
	if _, err := generator.WriteOutputs(project, mgr.Dir, lock.RepoPath); err != nil {
		return err
	}
	sourceEnv := loadSourceEnv(lock.RepoPath, services)
	existing, _ := mgr.LoadEnv()
	for k, v := range sourceEnv {
		existing[k] = v
	}
	if err := mgr.SaveEnv(existing); err != nil {
		return err
	}
	if err := writeProjectEnv(mgr, lock); err != nil {
		return err
	}
	if err := mgr.SaveLock(lock); err != nil {
		return err
	}
	logf("%s", ui.SuccessLine("state refreshed"))
	printEnvStatus(mgr, lock)
	if docker.Available() {
		if err := composeFor(mgr).Validate(ctx); err != nil {
			return fmt.Errorf("generated compose is invalid: %w", err)
		}
	}

	if updateNoRun {
		logf("%s", ui.Step(4, 5, "Skipping rebuild (--no-run)"))
		logf("%s", ui.Step(5, 5, fmt.Sprintf("Done · %d service(s), %d infra", len(lock.Services), len(lock.Infra))))
		printURLs(lock)
		return nil
	}

	logf("%s", ui.Step(4, 5, "Rebuilding and verifying"))
	if err := requireDocker(ctx); err != nil {
		return err
	}
	var client domain.LLMClient
	if !updateNoAgent {
		if c, lerr := llmFor(true); lerr == nil {
			client = c
		} else {
			logf("%s", ui.WarningLine("LLM unavailable; deterministic repair only"))
		}
	}
	h, err := healerFor(mgr, lock, client)
	if err != nil {
		return err
	}
	h.MaxTries = 3
	healRes, herr := h.Heal(ctx, "", "")
	if herr != nil {
		printFailureReport(mgr, lock, healRes, herr)
		return herr
	}
	lock.LastUp = time.Now()
	clearFailureLog(mgr)
	if err := mgr.SaveLock(lock); err != nil {
		return err
	}
	logf("%s", ui.Step(5, 5, "Updated and healthy"))
	showStatus(ctx, mgr, lock)
	printURLs(lock)
	return nil
}
