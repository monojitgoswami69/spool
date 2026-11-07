package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/generator"
	"github.com/monojitgoswami69/spool/internal/git"
	"github.com/monojitgoswami69/spool/internal/graph"
	"github.com/monojitgoswami69/spool/internal/httpcheck"
	"github.com/monojitgoswami69/spool/internal/infra"
	"github.com/monojitgoswami69/spool/internal/portprobe"
	"github.com/monojitgoswami69/spool/internal/scanner"
	"github.com/monojitgoswami69/spool/internal/tunnel"
)

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func (a *Agent) registerDefaultTools() error {
	tools := []Tool{
		{
			Name:        "scan_repository",
			Description: "Detect languages, frameworks, monorepo workspaces, services, and ports from a local path or Git URL.",
			Parameters:  objectSchema(map[string]any{"path": strProp("Local path or Git URL to scan")}, "path"),
			Handler:     a.toolScanRepository,
		},
		{
			Name:        "infer_infrastructure",
			Description: "Infer required backing services (Postgres, Redis, MySQL, Mongo, Kafka, MinIO, RabbitMQ, Elasticsearch) and allocate conflict-free host ports.",
			Parameters:  objectSchema(map[string]any{}),
			Mutating:    true,
			Handler:     a.toolInferInfrastructure,
		},
		{
			Name:        "synthesize_manifests",
			Description: "Generate Dockerfiles, docker-compose.yml, and .dockerignore for the current project.",
			Parameters:  objectSchema(map[string]any{}),
			Mutating:    true,
			Handler:     a.toolSynthesizeManifests,
		},
		{
			Name:        "orchestrate_stack",
			Description: "Run a stack action: build, up, down, restart, or nuke (down with volumes).",
			Parameters: objectSchema(map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"build", "up", "down", "restart", "nuke"}, "description": "Action to perform"},
			}, "action"),
			Mutating: true,
			Handler:  a.toolOrchestrateStack,
		},
		{
			Name:        "diagnose_failure",
			Description: "Analyze recent logs and container state to explain what failed, why, and what to do next.",
			Parameters: objectSchema(map[string]any{
				"service": strProp("Optional service name"),
			}),
			Handler: a.toolDiagnoseFailure,
		},
		{
			Name:        "inspect_logs_and_health",
			Description: "Fetch container logs, states, and HTTP readiness for a service.",
			Parameters: objectSchema(map[string]any{
				"service": strProp("Optional service name"),
			}),
			Handler: a.toolInspectLogsAndHealth,
		},
		{
			Name:        "onboard_repository",
			Description: "Clone/accept a repo, scan services, infer infra, generate manifests, run, heal, verify, and return live URLs.",
			Parameters: objectSchema(map[string]any{
				"target":       strProp("Git URL or local repository path"),
				"max_services": intProp("Optional maximum services"),
				"no_run":       map[string]any{"type": "boolean", "description": "Generate manifests without starting"},
			}, "target"),
			Mutating: true,
			Timeout:  20 * time.Minute,
			Handler:  a.toolOnboardRepository,
		},
		{
			Name:        "heal_service",
			Description: "Run the deterministic + invariant-guarded LLM repair loop for the stack or a specific service.",
			Parameters: objectSchema(map[string]any{
				"service": strProp("Optional service name"),
			}),
			Mutating: true,
			Handler:  a.toolHealService,
		},
		{
			Name:        "inspect_repository",
			Description: "Return README excerpt, docs, Makefile, existing Dockerfiles/compose files, scripts, and detected services for the current project.",
			Parameters:  objectSchema(map[string]any{}),
			Handler:     a.toolInspectRepository,
		},
		{
			Name:        "read_repo_file",
			Description: "Read a file inside the repository. The path is strictly jailed to the repo root.",
			Parameters: objectSchema(map[string]any{
				"rel_path": strProp("Repository-relative file path"),
				"limit":    intProp("Maximum bytes to return (default 65536, max 262144)"),
			}, "rel_path"),
			Handler: a.toolReadRepoFile,
		},
		{
			Name:        "explain_topology",
			Description: "Render the service/infrastructure dependency graph, port map, and start order.",
			Parameters:  objectSchema(map[string]any{}),
			Handler:     a.toolExplainTopology,
		},
		{
			Name:        "start_public_tunnel",
			Description: "Start a public preview tunnel (cloudflared or localhost.run) for a local port.",
			Parameters: objectSchema(map[string]any{
				"port": intProp("Local port to expose; defaults to the first web service host port"),
			}),
			Mutating: true,
			Handler:  a.toolStartPublicTunnel,
		},
	}
	for _, t := range tools {
		if err := a.registry.Register(t); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) toolScanRepository(ctx context.Context, args map[string]any) (string, error) {
	target := argString(args, "path")
	if target == "" {
		target = "."
	}
	root := target
	if git.IsRemote(target) {
		repos, err := config.Dir("repos")
		if err != nil {
			return "", err
		}
		dir, err := os.MkdirTemp(repos, "scan-*")
		if err != nil {
			return "", err
		}
		if err := git.Clone(ctx, target, dir, config.GitHubToken(), ""); err != nil {
			return "", err
		}
		root = dir
	} else if !filepath.IsAbs(target) {
		root = filepath.Join(a.opts.RepoRoot, target)
	}
	res, err := scanner.Scan(root)
	if err != nil {
		return "", err
	}
	type svcView struct {
		Name               string `json:"name"`
		Path               string `json:"path"`
		Language           string `json:"language"`
		Framework          string `json:"framework"`
		Port               int    `json:"port"`
		HostPort           int    `json:"host_port"`
		IsWorker           bool   `json:"is_worker"`
		Manager            string `json:"package_manager"`
		ExistingDockerfile string `json:"existing_dockerfile,omitempty"`
		BuildContext       string `json:"build_context,omitempty"`
	}
	views := make([]svcView, 0, len(res.Services))
	for _, s := range res.Services {
		views = append(views, svcView{s.Name, s.Path, string(s.Language), string(s.Framework), s.Port, s.HostPort, s.IsWorker, string(s.PackageManager), s.ExistingDockerfile, s.BuildContext})
	}
	payload := map[string]any{"root": res.Root, "languages": res.Languages, "services": views, "intel": res.Intel}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return string(b), nil
}

func (a *Agent) toolInferInfrastructure(ctx context.Context, args map[string]any) (string, error) {
	p := a.opts.Project
	if p == nil {
		return "", fmt.Errorf("no project is loaded")
	}
	deps := map[string]bool{}
	for _, s := range p.Services {
		for _, d := range s.Dependencies {
			deps[strings.ToLower(d)] = true
		}
	}
	alloc := a.opts.Allocator
	if alloc == nil {
		alloc = portprobe.New()
	}
	infras, err := infra.Infer(p.Env, deps, infra.Options{SourcePath: a.opts.RepoRoot, Allocator: alloc})
	if err != nil {
		return "", err
	}
	p.InfraServices = infras
	b, _ := json.MarshalIndent(infras, "", "  ")
	return string(b), nil
}

func (a *Agent) toolSynthesizeManifests(ctx context.Context, args map[string]any) (string, error) {
	p := a.opts.Project
	if p == nil {
		return "", fmt.Errorf("no project is loaded")
	}
	out, err := generator.WriteOutputs(p, a.opts.WorkDir, a.opts.RepoRoot)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(out.Files))
	for n := range out.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	return "generated: " + strings.Join(names, ", "), nil
}

func (a *Agent) toolOrchestrateStack(ctx context.Context, args map[string]any) (string, error) {
	if a.opts.Compose == nil {
		return "", fmt.Errorf("no compose controller configured")
	}
	action := strings.ToLower(argString(args, "action"))
	switch action {
	case "build":
		return a.opts.Compose.Build(ctx, "")
	case "up":
		return a.opts.Compose.Up(ctx)
	case "down":
		return a.opts.Compose.Down(ctx, false)
	case "restart":
		return a.opts.Compose.Restart(ctx, "")
	case "nuke":
		return a.opts.Compose.Down(ctx, true)
	default:
		return "", fmt.Errorf("unknown action %q (expected build, up, down, restart, nuke)", action)
	}
}

func (a *Agent) toolDiagnoseFailure(ctx context.Context, args map[string]any) (string, error) {
	service := argString(args, "service")
	return a.intentDiagnose(ctx, Intent{Kind: IntentDiagnose, Service: service, Project: a.projectName()})
}

func (a *Agent) toolInspectLogsAndHealth(ctx context.Context, args map[string]any) (string, error) {
	if a.opts.Compose == nil {
		return "", fmt.Errorf("no compose controller configured")
	}
	service := argString(args, "service")
	logs, logErr := a.opts.Compose.Logs(ctx, service, 200)
	if logErr != nil {
		return "", logErr
	}
	ps, err := a.opts.Compose.Ps(ctx)
	if err != nil {
		return "", err
	}
	var httpResults any
	if a.opts.Project != nil {
		portMap := map[string]int{}
		for _, s := range a.opts.Project.Services {
			portMap[s.Name] = s.HostPort
		}
		httpResults = httpcheck.Services(ctx, a.opts.Project.Services, portMap)
	}
	payload := map[string]any{"logs": logs, "containers": ps, "http": httpResults}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return string(b), nil
}

func (a *Agent) toolOnboardRepository(ctx context.Context, args map[string]any) (string, error) {
	if a.opts.Onboard == nil {
		return "", fmt.Errorf("onboarding is not available in this session")
	}
	target := argString(args, "target")
	if target == "" {
		target = argString(args, "path")
	}
	if target == "" {
		return "", fmt.Errorf("target is required")
	}
	res, err := a.opts.Onboard(ctx, target, argInt(args, "max_services", 0), argBool(args, "no_run", false), func(msg string) {
		a.emit(Event{Type: "tool_progress", Message: msg})
	})
	if res == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("onboarding returned no result")
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return "", fmt.Errorf("onboarding failed: %w (project=%s urls=%v)", err, res.Project, res.URLs)
	}
	return string(b), nil
}

func (a *Agent) toolHealService(ctx context.Context, args map[string]any) (string, error) {
	if a.opts.Healer == nil {
		return "", fmt.Errorf("no healer configured")
	}
	service := argString(args, "service")
	res, err := a.opts.Healer.Heal(ctx, "", service)
	if err != nil {
		if res != nil {
			return "", fmt.Errorf("%v (attempts=%d, summary=%s)", err, len(res.Attempts), res.Summary)
		}
		return "", err
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	return string(b), nil
}

func (a *Agent) toolInspectRepository(ctx context.Context, args map[string]any) (string, error) {
	if a.opts.Project == nil {
		return "", fmt.Errorf("no project is loaded")
	}
	payload := map[string]any{
		"project":    a.opts.Project.Name,
		"project_id": a.opts.Project.ID,
		"services":   a.opts.Project.Services,
		"infra":      a.opts.Project.InfraServices,
		"intel":      a.opts.Project.Intel,
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return string(b), nil
}

func (a *Agent) toolReadRepoFile(ctx context.Context, args map[string]any) (string, error) {
	if a.opts.RepoJail == nil {
		return "", fmt.Errorf("repository jail is not configured")
	}
	rel := argString(args, "rel_path")
	if rel == "" {
		rel = argString(args, "path")
	}
	if rel == "" {
		return "", fmt.Errorf("rel_path is required")
	}
	limit := argInt(args, "limit", 64*1024)
	if limit <= 0 || limit > 256*1024 {
		limit = 256 * 1024
	}
	data, err := a.opts.RepoJail.ReadFileLimited(rel, int64(limit))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (a *Agent) toolExplainTopology(ctx context.Context, args map[string]any) (string, error) {
	if a.opts.Project == nil {
		return "", fmt.Errorf("no project is loaded")
	}
	return graph.Build(a.opts.Project).Render(), nil
}

func (a *Agent) toolStartPublicTunnel(ctx context.Context, args map[string]any) (string, error) {
	port := argInt(args, "port", 0)
	if port == 0 && a.opts.Project != nil {
		for _, s := range a.opts.Project.Services {
			if !s.IsWorker && s.HostPort > 0 {
				port = s.HostPort
				break
			}
		}
	}
	if port == 0 {
		return "", fmt.Errorf("no port specified and no web service host port is available")
	}
	if a.tunnel != nil {
		_ = a.tunnel.Close()
	}
	t, err := tunnel.Start(ctx, port, tunnel.Options{})
	if err != nil {
		return "", err
	}
	a.tunnel = t
	return t.PublicURL(), nil
}

func (a *Agent) projectName() string {
	if a.opts.Project == nil {
		return ""
	}
	return a.opts.Project.Name
}
