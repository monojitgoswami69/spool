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
		Intel:         lock.Intel,
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

// printURLs prints live endpoints, but only for containers that are actually
// running. Stopped services are summarized honestly instead of advertised as
// live, so a stopped stack never claims working URLs.
func printURLs(ctx context.Context, mgr *state.Manager, lock *domain.Lock) {
	pairs := projectURLs(lock)
	if len(pairs) == 0 {
		return
	}
	ps, err := composeFor(mgr).Ps(ctx)
	if err != nil {
		logf("%s", ui.WarningLine("could not query container state; endpoints not shown ("+err.Error()+")"))
		return
	}
	live, stopped := partitionURLs(pairs, ps)
	if len(live) > 0 {
		logf("")
		logf("%s", ui.Section("live endpoints"))
		for _, p := range live {
			logf("  %-16s %s", p.Service, ui.URL(p.URL))
		}
	}
	if len(stopped) > 0 {
		names := make([]string, 0, len(stopped))
		for _, p := range stopped {
			names = append(names, p.Service)
		}
		logf("%s", ui.MutedStyle.Render(fmt.Sprintf("  ○ not running: %s — start with `spool up %s`", strings.Join(names, ", "), mgr.ID)))
	}
}

// partitionURLs splits endpoint candidates into live and stopped based on
// container state; services missing from ps count as stopped.
func partitionURLs(pairs []urlPair, ps []domain.Container) (live, stopped []urlPair) {
	byService := map[string]domain.Container{}
	for _, c := range ps {
		byService[c.Service] = c
	}
	for _, p := range pairs {
		if c, ok := byService[p.Service]; ok && c.State == "running" {
			live = append(live, p)
		} else {
			stopped = append(stopped, p)
		}
	}
	return live, stopped
}

// findContainer returns the container row for a service name.
func findContainer(ps []domain.Container, name string) (domain.Container, bool) {
	for _, c := range ps {
		if c.Service == name {
			return c, true
		}
	}
	return domain.Container{}, false
}

// containerLabel is the short health label used in progress lines.
func containerLabel(c domain.Container) string {
	switch {
	case c.State == "":
		return "stopped"
	case c.State != "running":
		return c.State
	case strings.EqualFold(c.Health, "healthy"):
		return "healthy"
	case strings.EqualFold(c.Health, "unhealthy"):
		return "unhealthy"
	case strings.EqualFold(c.Health, "starting"):
		return "starting"
	default:
		return "running"
	}
}

// waitForHealthy polls compose until every non-worker app service is running
// and its healthcheck (when one is defined) reports healthy, so the status
// table and endpoints printed right after a start reflect reality instead of
// containers that are still "starting". It never fails; on timeout it warns.
func waitForHealthy(ctx context.Context, mgr *state.Manager, lock *domain.Lock, timeout time.Duration) {
	watch := map[string]bool{}
	for _, svc := range lock.Services {
		if svc.IsWorker {
			continue
		}
		if lock.PortMap[svc.Name] > 0 || svc.HostPort > 0 {
			watch[svc.Name] = true
		}
	}
	if len(watch) == 0 {
		return
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	compose := composeFor(mgr)
	deadline := time.Now().Add(timeout)
	last := map[string]string{}
	started := time.Now()
	for {
		ps, err := compose.Ps(ctx)
		if err != nil {
			return
		}
		settled := true
		for name := range watch {
			label := "stopped"
			if c, ok := findContainer(ps, name); ok {
				label = containerLabel(c)
			}
			if last[name] != label {
				last[name] = label
				elapsed := fmt.Sprintf("%.0fs", time.Since(started).Seconds())
				if label == "healthy" || label == "running" {
					logf("%s", ui.SuccessLine(fmt.Sprintf("health check %s: %s (%s)", name, label, elapsed)))
				} else {
					logf("%s", ui.MutedStyle.Render(fmt.Sprintf("  ● health check %s: %s (%s)", name, label, elapsed)))
				}
			}
			if label != "healthy" && label != "running" {
				settled = false
			}
		}
		if settled {
			return
		}
		if time.Now().After(deadline) {
			logf("%s", ui.WarningLine("health checks still settling after "+timeout.String()+"; showing current state"))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
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

func missingEnvKeys(mgr *state.Manager, lock *domain.Lock) []string {
	merged, err := renderEnv(mgr, lock)
	if err != nil {
		return nil
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
	sort.Strings(missing)
	return missing
}

func urlStrings(lock *domain.Lock) []string {
	pairs := projectURLs(lock)
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.URL)
	}
	return out
}

// printEnvStatus surfaces unresolved configuration right after init/update so a
// developer knows exactly what to set.
func printEnvStatus(mgr *state.Manager, lock *domain.Lock) {
	missing := missingEnvKeys(mgr, lock)
	if len(missing) == 0 {
		return
	}
	shown := missing
	extra := ""
	if len(shown) > 10 {
		extra = fmt.Sprintf(" (+%d more)", len(shown)-10)
		shown = shown[:10]
	}
	logf("%s", ui.WarningLine(fmt.Sprintf("%d variable(s) still unset: %s%s", len(missing), strings.Join(shown, ", "), extra)))
	logf("%s", ui.InfoLine(fmt.Sprintf("set them with: spool env %s set KEY=VALUE", mgr.ID)))
}
