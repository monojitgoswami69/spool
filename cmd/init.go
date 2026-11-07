package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/git"
	"github.com/monojitgoswami69/spool/internal/safefs"
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
	if !flagQuiet {
		fmt.Println(ui.Banner(Version))
		fmt.Println()
	}
	target := "."
	if len(args) > 0 {
		target = args[0]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := runOnboarding(ctx, onboardOptions{
		Target: target, Name: initName, MaxServices: initMaxSvc,
		NoRun: initNoRun, NoAgent: initNoAgent,
		Logf: logf,
		Progress: func(step, total int, title string) {
			logf("%s", ui.Step(step, total, title))
		},
	})
	if err != nil {
		if res != nil {
			printFailureReport(res.Manager, res.Lock, res.Heal, err)
		}
		return err
	}

	printEnvStatus(res.Manager, res.Lock)
	if initNoRun {
		logf("")
		logf("Next: %s", ui.BoldStyle.Render("spool up "+res.Manager.ID))
		return nil
	}
	logf("")
	showStatus(ctx, res.Manager, res.Lock)
	printURLs(res.Lock)
	logf("")
	logf("%s", ui.InfoLine(fmt.Sprintf("project %s · state %s", res.Manager.ID, res.Manager.Dir)))
	return nil
}

func resolveSource(ctx context.Context, target string, logf func(format string, args ...any)) (sourcePath, name, repoURL string, isLocal bool, err error) {
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
