package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/healer"
	"github.com/monojitgoswami69/spool/internal/httpcheck"
	"github.com/monojitgoswami69/spool/internal/infra"
	"github.com/monojitgoswami69/spool/internal/llm"
	"github.com/monojitgoswami69/spool/internal/safefs"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

func logf(format string, args ...any) {
	if flagQuiet {
		return
	}
	fmt.Printf(format+"\n", args...)
}

func verbosef(format string, args ...any) {
	if !flagVerbose || flagQuiet {
		return
	}
	fmt.Printf(ui.MutedStyle.Render(format)+"\n", args...)
}

func resolveProject(name string) (*state.Manager, *domain.Lock, error) {
	return state.Resolve(name)
}

func requireDocker(ctx context.Context) error {
	if !docker.Available() {
		return domain.E(domain.KindDocker, "doctor", "docker is not installed or not on PATH", nil)
	}
	if !docker.DaemonRunning(ctx) {
		return domain.E(domain.KindDocker, "doctor", "the Docker daemon is not running", nil)
	}
	return nil
}

func composeFor(mgr *state.Manager) *docker.Compose {
	return docker.New(mgr.ComposePath(), mgr.Dir, "spool-"+mgr.ID)
}

// projectFor reconstructs the domain.Project from persisted state.
func projectFor(mgr *state.Manager, lock *domain.Lock) *domain.Project {
	return &domain.Project{
		ID:            lock.ProjectID,
		Name:          lock.Project,
		SourcePath:    lock.RepoPath,
		WorkDir:       lock.WorkDir,
		Services:      lock.Services,
		InfraServices: lock.Infra,
		Env:           lock.Env,
		NetworkName:   lock.NetworkName,
		CreatedAt:     lock.LastInit,
		UpdatedAt:     lock.LastUp,
	}
}

// llmFor resolves an LLM client, returning ErrNotConfigured when unavailable.
func llmFor(jsonMode bool) (domain.LLMClient, error) {
	resolved, err := config.Resolve()
	if err != nil {
		return nil, err
	}
	return llm.NewFromResolved(resolved, jsonMode)
}

func healerFor(mgr *state.Manager, lock *domain.Lock, client domain.LLMClient) (*healer.Healer, error) {
	workJail, err := safefs.NewJail(mgr.Dir)
	if err != nil {
		return nil, err
	}
	var srcJail *safefs.Jail
	if lock.RepoPath != "" {
		if j, err := safefs.NewJail(lock.RepoPath); err == nil {
			srcJail = j
		}
	}
	return &healer.Healer{
		Compose:       composeFor(mgr),
		LLM:           client,
		WorkJail:      workJail,
		SourceJail:    srcJail,
		Services:      lock.Services,
		PortMap:       lock.PortMap,
		MaxTries:      3,
		Provenance:    healer.NewProvenance(mgr.RepairsPath()),
		HealthTimeout: 90 * time.Second,
		PollInterval:  2 * time.Second,
		Logf: func(format string, args ...any) {
			if !flagQuiet {
				fmt.Println(ui.MutedStyle.Render(fmt.Sprintf(format, args...)))
			}
		},
		HTTPCheck: func(ctx context.Context, url string) domain.Result {
			return httpcheck.Check(ctx, url, 4*time.Second)
		},
	}, nil
}

// renderEnv rebuilds the project .env from the lock, infra bindings, service
// env, and dashboard overrides. Existing values win over regenerated defaults.
func renderEnv(mgr *state.Manager, lock *domain.Lock) (map[string]string, error) {
	existing, _ := mgr.LoadEnv()
	merged, err := envvar.MergeEnvironment(existing, lock.Env, infra.GenerateEnvBindings(lock.Infra))
	if err != nil {
		return nil, err
	}
	for _, svc := range lock.Services {
		for k, v := range svc.Env {
			merged[k] = v
		}
	}
	overrides, _ := mgr.LoadOverrides()
	for k, v := range overrides {
		merged[k] = v
	}
	return merged, nil
}

func writeProjectEnv(mgr *state.Manager, lock *domain.Lock) error {
	merged, err := renderEnv(mgr, lock)
	if err != nil {
		return err
	}
	return mgr.SaveEnv(merged)
}

type urlPair struct {
	Service string
	URL     string
}

func projectURLs(lock *domain.Lock) []urlPair {
	var out []urlPair
	for _, svc := range lock.Services {
		if svc.IsWorker {
			continue
		}
		host := lock.PortMap[svc.Name]
		if host == 0 {
			host = svc.HostPort
		}
		if host == 0 {
			continue
		}
		out = append(out, urlPair{Service: svc.Name, URL: fmt.Sprintf("http://localhost:%d", host)})
	}
	return out
}

func printURLs(lock *domain.Lock) {
	pairs := projectURLs(lock)
	if len(pairs) == 0 {
		return
	}
	logf("")
	logf("%s", ui.Section("live endpoints"))
	for _, p := range pairs {
		logf("  %-16s %s", p.Service, ui.URL(p.URL))
	}
}

func confirm(prompt string) bool {
	if flagYes {
		return true
	}
	fmt.Print(ui.WarnStyle.Render("? ") + prompt + " [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
func checkDaemon(ctx context.Context) error {
	if !docker.DaemonRunning(ctx) {
		return domain.E(domain.KindDocker, "doctor", "the Docker daemon is not running", nil)
	}
	return nil
}

// printEnvStatus surfaces unresolved configuration right after init/update so a
// developer knows exactly what to set.
func printEnvStatus(mgr *state.Manager, lock *domain.Lock) {
	merged, err := renderEnv(mgr, lock)
	if err != nil {
		return
	}
	var missing []string
	for _, ev := range lock.Env {
		if strings.HasSuffix(ev.Key, "_") {
			continue
		}
		if strings.TrimSpace(merged[ev.Key]) == "" {
			missing = append(missing, ev.Key)
		}
	}
	if len(missing) == 0 {
		return
	}
	sort.Strings(missing)
	shown := missing
	extra := ""
	if len(shown) > 10 {
		extra = fmt.Sprintf(" (+%d more)", len(shown)-10)
		shown = shown[:10]
	}
	logf("%s", ui.WarningLine(fmt.Sprintf("%d variable(s) still unset: %s%s", len(missing), strings.Join(shown, ", "), extra)))
	logf("%s", ui.InfoLine(fmt.Sprintf("set them with: spool env %s set KEY=VALUE", mgr.ID)))
}
