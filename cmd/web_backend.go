package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/monojitgoswami69/spool/internal/agent"
	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/web"
)

type localWebBackend struct {
	client domain.LLMClient
}

func (b *localWebBackend) Projects(ctx context.Context) ([]web.ProjectView, error) {
	managers, err := state.List()
	if err != nil {
		return nil, err
	}
	canDocker := docker.Available() && docker.DaemonRunning(ctx)
	out := make([]web.ProjectView, 0, len(managers))
	for _, m := range managers {
		lock, err := m.LoadLock()
		if err != nil {
			continue
		}
		view := web.ProjectView{
			ID: m.ID, Name: lock.Project, Services: len(lock.Services), Infra: len(lock.Infra),
			URLs: urlStrings(lock), LastInit: lock.LastInit,
		}
		if canDocker {
			if ps, perr := composeFor(m).Ps(ctx); perr == nil {
				unhealthy := false
				view.Total = len(ps)
				for _, c := range ps {
					if c.State == "running" {
						view.Running++
					}
					if strings.EqualFold(c.Health, "unhealthy") {
						unhealthy = true
					}
				}
				view.Healthy = view.Total > 0 && view.Running == view.Total && !unhealthy
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func (b *localWebBackend) Config(ctx context.Context) (web.ConfigView, error) {
	cfg := config.LoadOptional()
	home, _ := config.Home()
	view := web.ConfigView{Provider: cfg.LLMProvider, Model: cfg.LLMModel, Home: home}
	r, err := config.Resolve()
	if err != nil {
		view.Source = "not configured"
		return view, nil
	}
	view.Provider, view.Model, view.Source = r.Provider, r.Model, r.Source
	view.KeyConfigured = r.APIKey != ""
	return view, nil
}

func (b *localWebBackend) Send(ctx context.Context, req web.ChatRequest, emit func(agent.Event)) (*web.Outcome, error) {
	text := strings.TrimSpace(req.Text)
	if handled, final := b.localCommand(ctx, req, text); handled {
		return &web.Outcome{Final: final, Project: req.Project}, nil
	}

	translated := translateSlash(text)
	ictx := intentContext()
	var mgr *state.Manager
	var lock *domain.Lock
	var err error
	if req.Project != "" {
		mgr, lock, err = resolveProject(req.Project)
		if err != nil {
			return nil, err
		}
	} else {
		// Explicit no-project chat: the agent runs project-less. Onboarding,
		// help, and greetings work directly. A turn that names a project binds
		// it for that turn; with exactly one project onboarded, auto-bind it.
		if parsed := agent.ParseIntent(translated, ictx); parsed.Project != "" &&
			parsed.Kind != agent.IntentOnboard && parsed.Kind != agent.IntentGreeting &&
			parsed.Kind != agent.IntentHelp {
			if m, l, rerr := resolveProject(parsed.Project); rerr == nil {
				mgr, lock = m, l
			}
		}
		if mgr == nil {
			managers, lerr := state.List()
			if lerr == nil && len(managers) == 1 {
				mgr, lock, err = resolveProject(managers[0].ID)
				if err != nil {
					return nil, err
				}
			}
		}
	}

	var confirmation *web.Confirmation
	wrapped := func(ev agent.Event) {
		if ev.Type == "permission_denied" {
			confirmation = &web.Confirmation{Tool: ev.Tool, Destructive: ev.Destructive, Args: ev.Args}
		}
		emit(ev)
	}
	confirm := func(tool string, destructive bool, args map[string]any) bool { return req.Confirm }
	ag, err := buildChatAgent(b.client, mgr, lock, wrapped, confirm)
	if err != nil {
		return nil, err
	}
	defer ag.Close()

	if lock != nil {
		ictx.CurrentProject = lock.Project
	}
	res, runErr := ag.RunIntent(ctx, translated, ictx)

	out := &web.Outcome{}
	if res != nil {
		out.Final = res.Final
		out.Intent = res.Intent
		if res.Intent != nil && res.Intent.Project != "" {
			out.Project = res.Intent.Project
		}
	}
	if out.Project == "" && lock != nil {
		out.Project = lock.Project
		out.ProjectID = lock.ProjectID
	}
	if out.Project != "" {
		if _, l, rerr := resolveProject(out.Project); rerr == nil {
			out.ProjectID = l.ProjectID
		}
	}
	if confirmation != nil {
		out.NeedsConfirmation = true
		out.Confirmation = confirmation
	}
	if runErr != nil && strings.TrimSpace(out.Final) == "" {
		out.Final = "Error: " + runErr.Error()
	}
	if strings.TrimSpace(out.Final) == "" {
		out.Final = "No response."
	}
	return out, nil
}

func (b *localWebBackend) localCommand(ctx context.Context, req web.ChatRequest, text string) (bool, string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false, ""
	}
	cmd := strings.ToLower(fields[0])
	rest := strings.TrimSpace(text[len(fields[0]):])
	switch cmd {
	case "/help":
		return true, agent.HelpText() + "\n\n**Slash commands**\n" + slashHelp()
	case "/version":
		return true, "Spool " + Version
	case "/setup":
		return true, "Run `spool setup` in your terminal to enter provider keys securely (masked input)."
	case "/doctor":
		return true, b.doctorSummary(ctx)
	case "/projects":
		projects, err := b.Projects(ctx)
		if err != nil {
			return true, "Error: " + err.Error()
		}
		if len(projects) == 0 {
			return true, "No projects yet. Use `/onboard <git-url-or-path>` to bring one up."
		}
		var bld strings.Builder
		bld.WriteString("**Projects**\n\n")
		for _, p := range projects {
			fmt.Fprintf(&bld, "- `%s` — **%s** · %d/%d running · %d service(s), %d infra\n", p.ID, p.Name, p.Running, p.Total, p.Services, p.Infra)
			for _, u := range p.URLs {
				fmt.Fprintf(&bld, "  - %s\n", u)
			}
		}
		return true, strings.TrimRight(bld.String(), "\n")
	case "/config":
		if strings.HasPrefix(strings.ToLower(rest), "set ") {
			parts := strings.Fields(strings.TrimSpace(rest[4:]))
			if len(parts) < 2 {
				return true, "Usage: `/config set <provider|model|api-key|base-url|github-pat> <value>`"
			}
			if err := applyWebConfig(parts[0], strings.Join(parts[1:], " ")); err != nil {
				return true, "Error: " + err.Error()
			}
			return true, fmt.Sprintf("Set `%s`. Run `/doctor` to verify.", parts[0])
		}
		cfg, err := b.Config(ctx)
		if err != nil {
			return true, "Error: " + err.Error()
		}
		key := "missing"
		if cfg.KeyConfigured {
			key = "configured"
		}
		return true, fmt.Sprintf("**Configuration**\n\n- Provider/model: `%s/%s`\n- API key: %s\n- Source: `%s`\n- Home: `%s`", cfg.Provider, cfg.Model, key, cfg.Source, cfg.Home)
	case "/open":
		_, lock, err := resolveProject(req.Project)
		if err != nil {
			return true, "No project selected."
		}
		urls := urlStrings(lock)
		if len(urls) == 0 {
			return true, "No live endpoints for " + lock.Project + "."
		}
		var bld strings.Builder
		bld.WriteString("**Live endpoints · " + lock.Project + "**\n\n")
		for _, u := range urls {
			fmt.Fprintf(&bld, "- %s\n", u)
		}
		return true, strings.TrimRight(bld.String(), "\n")
	case "/dash", "/nerd":
		return true, "Nerd stats are in the right-hand panel (toggle **Nerd stats**)."
	case "/new", "/clear":
		return true, "Use the **New chat** button."
	}
	return false, ""
}

func (b *localWebBackend) doctorSummary(ctx context.Context) string {
	dockerBin := docker.Available()
	daemon := docker.DaemonRunning(ctx)
	compose := exec.CommandContext(ctx, "docker", "compose", "version").Run() == nil
	cfg, _ := b.Config(ctx)
	key := "missing"
	if cfg.KeyConfigured {
		key = "configured"
	}
	var bld strings.Builder
	bld.WriteString("**Doctor**\n\n")
	fmt.Fprintf(&bld, "- docker: %s\n", yesno(dockerBin))
	fmt.Fprintf(&bld, "- docker daemon: %s\n", yesno(daemon))
	fmt.Fprintf(&bld, "- compose v2: %s\n", yesno(compose))
	fmt.Fprintf(&bld, "- llm: `%s/%s` (%s)\n", cfg.Provider, cfg.Model, key)
	fmt.Fprintf(&bld, "- spool home: `%s`\n", cfg.Home)
	return strings.TrimRight(bld.String(), "\n")
}

func yesno(ok bool) string {
	if ok {
		return "ok"
	}
	return "missing"
}

func applyWebConfig(key, value string) error {
	cfg := config.LoadOptional()
	switch strings.ToLower(key) {
	case "provider":
		cfg.LLMProvider = value
	case "model":
		cfg.LLMModel = value
	case "api-key", "api_key", "apikey":
		cfg.LLMAPIKey = value
	case "base-url", "base_url", "baseurl":
		cfg.LLMBaseURL = value
	case "github-pat", "github_pat", "pat":
		cfg.GitHubPAT = value
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return config.Save(cfg)
}

func translateSlash(text string) string {
	if !strings.HasPrefix(text, "/") {
		return text
	}
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	rest := strings.TrimSpace(text[len(fields[0]):])
	switch cmd {
	case "/status":
		return "status " + rest
	case "/health":
		return "health " + rest
	case "/diagnose":
		return "diagnose " + rest
	case "/explain":
		return "explain " + rest
	case "/logs":
		return "logs " + rest
	case "/heal":
		return "heal " + rest
	case "/up":
		return "start the stack " + rest
	case "/down":
		return "stop the stack " + rest
	case "/restart":
		return "restart the stack " + rest
	case "/nuke":
		return "nuke " + rest
	case "/tunnel":
		if rest == "" {
			return "tunnel"
		}
		return "tunnel port " + rest
	case "/onboard":
		return "onboard " + rest
	case "/scan":
		return "scan " + rest
	case "/ask":
		return rest
	case "/env":
		lower := strings.ToLower(rest)
		switch {
		case strings.HasPrefix(lower, "set "):
			return "set " + rest[4:]
		case strings.HasPrefix(lower, "unset "):
			return rest
		case rest == "":
			return "environment"
		default:
			return "environment " + rest
		}
	default:
		return strings.TrimPrefix(text, "/")
	}
}

func isOnboardRequest(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "onboard")
}

func slashHelp() string {
	return strings.Join([]string{
		"`/status` `/health` `/diagnose` `/explain` `/logs [service]`",
		"`/env` `/env set KEY=VALUE` `/env unset KEY`",
		"`/heal` `/up` `/down` `/restart` `/nuke`",
		"`/tunnel [port]` `/onboard <url-or-path>` `/scan <path-or-url>`",
		"`/projects` `/config` `/config set KEY VALUE` `/open` `/help`",
		"`/doctor` `/version` `/setup`",
	}, "\n")
}
