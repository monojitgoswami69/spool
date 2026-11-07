package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/graph"
	"github.com/monojitgoswami69/spool/internal/httpcheck"
	"github.com/monojitgoswami69/spool/internal/llm"
)

// IntentKind is a stable, testable user intent.
type IntentKind string

const (
	IntentStatus        IntentKind = "status"
	IntentHealth        IntentKind = "health"
	IntentExplain       IntentKind = "explain"
	IntentLogs          IntentKind = "logs"
	IntentEnvList       IntentKind = "env_list"
	IntentEnvSet        IntentKind = "env_set"
	IntentEnvUnset      IntentKind = "env_unset"
	IntentHeal          IntentKind = "heal"
	IntentUp            IntentKind = "up"
	IntentDown          IntentKind = "down"
	IntentRestart       IntentKind = "restart"
	IntentNuke          IntentKind = "nuke"
	IntentScan          IntentKind = "scan"
	IntentTunnel        IntentKind = "tunnel"
	IntentReadFile      IntentKind = "read_file"
	IntentGenerate      IntentKind = "generate"
	IntentSwitchProject IntentKind = "switch_project"
	IntentHelp          IntentKind = "help"
	IntentChat          IntentKind = "chat"
)

// ProjectInfo is the project/service context available to the classifier.
type ProjectInfo struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Services []string `json:"services,omitempty"`
}

// IntentContext is everything the classifier may use to disambiguate.
type IntentContext struct {
	Projects       []ProjectInfo `json:"projects"`
	CurrentProject string        `json:"current_project,omitempty"`
	CurrentService string        `json:"current_service,omitempty"`
}

// Intent is the structured interpretation of a user request.
type Intent struct {
	Kind               IntentKind `json:"kind"`
	Project            string     `json:"project,omitempty"`
	Service            string     `json:"service,omitempty"`
	Key                string     `json:"key,omitempty"`
	Value              string     `json:"value,omitempty"`
	Path               string     `json:"path,omitempty"`
	Port               int        `json:"port,omitempty"`
	Confidence         float64    `json:"confidence"`
	Goal               string     `json:"goal,omitempty"`
	NeedsClarification bool       `json:"needs_clarification,omitempty"`
	Clarification      string     `json:"clarification,omitempty"`
	Source             string     `json:"source,omitempty"`
	Raw                string     `json:"raw,omitempty"`
}

var (
	envSetRe   = regexp.MustCompile(`(?i)(?:set|change|update|export)\s+(?:the\s+)?(?:env(?:ironment)?\s*(?:var(?:iable)?)?\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|to)\s*(.+)`)
	envUnsetRe = regexp.MustCompile(`(?i)(?:unset|remove|clear|delete)\s+(?:the\s+)?(?:env(?:ironment)?\s*(?:var(?:iable)?)?\s*)?([A-Za-z_][A-Za-z0-9_]*)`)
	portRe     = regexp.MustCompile(`(?i)(?:port\s+|:)(\d{2,5})\b`)
	pathRe     = regexp.MustCompile("(?i)(?:file|path)\\s+([^\\s]+)")
)

// ParseIntent deterministically interprets the common DevOps requests. It
// returns Kind=IntentChat with low confidence when it cannot be sure.
func ParseIntent(text string, ictx IntentContext) Intent {
	raw := strings.TrimSpace(text)
	lower := strings.ToLower(raw)
	it := Intent{Raw: raw, Source: "deterministic", Goal: raw}
	it.Project = findProject(lower, ictx)
	if it.Project == "" {
		it.Project = ictx.CurrentProject
	}
	it.Service = findService(lower, ictx, it.Project)
	if it.Service == "" {
		it.Service = ictx.CurrentService
	}

	switch {
	case containsAny(lower, "help", "what can you do", "what do you do", "commands", "how do i use"):
		it.Kind, it.Confidence = IntentHelp, 0.95
	case containsAny(lower, "switch to", "switch project", "use project", "select project", "focus on project", "work on project"):
		it.Kind, it.Confidence = IntentSwitchProject, 0.9
		if it.Project == "" {
			it.NeedsClarification = true
			it.Clarification = "Which project should I switch to?" + availableProjects(ictx)
			it.Confidence = 0.5
		}
	case envSetRe.MatchString(raw):
		m := envSetRe.FindStringSubmatch(raw)
		it.Kind, it.Key, it.Value, it.Confidence = IntentEnvSet, m[1], cleanValue(m[2], it.Project), 0.95
	case envUnsetRe.MatchString(raw):
		m := envUnsetRe.FindStringSubmatch(raw)
		it.Kind, it.Key, it.Confidence = IntentEnvUnset, m[1], 0.9
	case containsAny(lower, "env", "environment", "variable", "secret") && !containsAny(lower, "environment="):
		it.Kind, it.Confidence = IntentEnvList, 0.85
	case containsAny(lower, "nuke", "destroy", "incinerate", "tear down", "teardown", "delete project", "remove project"):
		it.Kind, it.Confidence = IntentNuke, 0.95
	case containsAny(lower, "restart", "reboot"):
		it.Kind, it.Confidence = IntentRestart, 0.9
	case hasWord(lower, "heal") || containsAny(lower, "self-heal", "self heal", "repair", "fix it", "fix the"):
		it.Kind, it.Confidence = IntentHeal, 0.9
	case containsAny(lower, "shut down", "shutdown", "stop the", "stop it", "down", "take down"):
		it.Kind, it.Confidence = IntentDown, 0.9
	case containsAny(lower, "bring up", "boot", "start the", "start it", " up", "up ", "run the", "launch"):
		it.Kind, it.Confidence = IntentUp, 0.85
	case containsAny(lower, "health", "healthy", "unhealthy", "readiness", "probe"):
		it.Kind, it.Confidence = IntentHealth, 0.9
	case containsAny(lower, "status", "state", "is it running", "is it up", "what is running"):
		it.Kind, it.Confidence = IntentStatus, 0.95
	case containsAny(lower, "logs", "log output", "tail", "stdout", "stderr"):
		it.Kind, it.Confidence = IntentLogs, 0.9
	case containsAny(lower, "explain", "topology", "graph", "diagram", "dependencies", "start order", "port map"):
		it.Kind, it.Confidence = IntentExplain, 0.95
	case containsAny(lower, "tunnel", "public url", "public link", "preview", "expose", "share"):
		it.Kind, it.Confidence = IntentTunnel, 0.9
	case containsAny(lower, "scan", "detect", "analyze", "analyse", "inspect repo", "what framework"):
		it.Kind, it.Confidence = IntentScan, 0.9
	case containsAny(lower, "generate manifest", "generate dockerfile", "synthesize", "generate compose"):
		it.Kind, it.Confidence = IntentGenerate, 0.9
	case containsAny(lower, "read file", "cat ", "show file", "open file", "contents of"):
		it.Kind, it.Confidence = IntentReadFile, 0.85
	default:
		it.Kind, it.Confidence = IntentChat, 0.0
	}

	if m := portRe.FindStringSubmatch(raw); m != nil {
		it.Port = atoiSafe(m[1])
	}
	if m := pathRe.FindStringSubmatch(raw); m != nil {
		it.Path = strings.Trim(m[1], `"'`)
	}

	if it.Kind != IntentHelp && it.Kind != IntentScan && it.Kind != IntentChat && it.Kind != IntentSwitchProject {
		if it.Project == "" && len(ictx.Projects) > 1 {
			it.NeedsClarification = true
			it.Clarification = "Which project do you mean?" + availableProjects(ictx)
			it.Confidence = 0.5
		}
	}
	if it.Kind == IntentEnvSet && (it.Key == "" || it.Value == "") {
		it.NeedsClarification = true
		it.Clarification = "Please provide the variable and value, e.g. set JWT_SECRET=abc123."
		it.Confidence = 0.5
	}
	if it.Kind == IntentEnvUnset && it.Key == "" {
		it.NeedsClarification = true
		it.Clarification = "Which variable should I unset?"
		it.Confidence = 0.5
	}
	return it
}

// ClassifyIntent asks the LLM for a strict intent object, falling back to the
// deterministic parser on any error. The result is always validated against the
// supplied context so the model cannot invent projects or services.
func ClassifyIntent(ctx context.Context, client domain.LLMClient, text string, ictx IntentContext) Intent {
	fallback := ParseIntent(text, ictx)
	if client == nil {
		return fallback
	}
	prompt := buildIntentPrompt(text, ictx)
	raw, err := client.Call(ctx, intentSystemPrompt, prompt)
	if err != nil {
		return fallback
	}
	body := llm.ExtractJSON(raw)
	if body == "" {
		return fallback
	}
	var it Intent
	if err := json.Unmarshal([]byte(body), &it); err != nil {
		return fallback
	}
	it.Raw, it.Source = text, "llm"
	if it.Confidence <= 0 {
		it.Confidence = 0.6
	}
	if it.Goal == "" {
		it.Goal = text
	}
	// Validate against the real context.
	if it.Project != "" && !knownProject(it.Project, ictx) {
		it.NeedsClarification = true
		it.Clarification = fmt.Sprintf("I don't see a project %q.%s", it.Project, availableProjects(ictx))
		it.Confidence = 0.4
	}
	if it.Service != "" && !knownService(it.Service, it.Project, ictx) {
		it.NeedsClarification = true
		it.Clarification = fmt.Sprintf("I don't see service %q in %s.", it.Service, it.Project)
		it.Confidence = 0.4
	}
	if it.Project == "" && it.Kind != IntentHelp && it.Kind != IntentScan && it.Kind != IntentChat && it.Kind != IntentSwitchProject {
		if ictx.CurrentProject != "" {
			it.Project = ictx.CurrentProject
		} else if len(ictx.Projects) == 1 {
			it.Project = ictx.Projects[0].Name
		} else if len(ictx.Projects) > 1 {
			it.NeedsClarification = true
			it.Clarification = "Which project do you mean?" + availableProjects(ictx)
			it.Confidence = 0.4
		}
	}
	return it
}

// Describe renders a short, human-readable interpretation for the UI.
func (it Intent) Describe() string {
	target := it.Project
	if it.Service != "" {
		target += "/" + it.Service
	}
	if target != "" {
		target = " · " + target
	}
	switch it.Kind {
	case IntentStatus:
		return "status" + target
	case IntentHealth:
		return "health" + target
	case IntentExplain:
		return "explain topology" + target
	case IntentLogs:
		return "logs" + target
	case IntentEnvList:
		return "list environment" + target
	case IntentEnvSet:
		return "set " + it.Key + target
	case IntentEnvUnset:
		return "unset " + it.Key + target
	case IntentHeal:
		return "heal" + target
	case IntentUp:
		return "start stack" + target
	case IntentDown:
		return "stop stack" + target
	case IntentRestart:
		return "restart" + target
	case IntentNuke:
		return "destroy" + target
	case IntentScan:
		return "scan repository"
	case IntentTunnel:
		return "public tunnel" + target
	case IntentReadFile:
		return "read " + it.Path
	case IntentGenerate:
		return "generate manifests" + target
	case IntentSwitchProject:
		return "switch to " + it.Project
	case IntentHelp:
		return "help"
	default:
		return "general request"
	}
}

// IsReadOnly reports whether the intent never mutates the stack.
func (it Intent) IsReadOnly() bool {
	switch it.Kind {
	case IntentStatus, IntentHealth, IntentExplain, IntentLogs, IntentEnvList, IntentScan, IntentReadFile, IntentHelp:
		return true
	}
	return false
}

// RunIntent interprets and executes a user request. Read-only and simple
// intents are dispatched directly (deterministic, fast); complex or ambiguous
// requests fall back to the tool-using loop.
func (a *Agent) RunIntent(ctx context.Context, text string, ictx IntentContext) (*Result, error) {
	intent := ParseIntent(text, ictx)
	// Use the LLM classifier when deterministic confidence is weak.
	if intent.Confidence < 0.75 || intent.Kind == IntentChat {
		if a.opts.LLM != nil {
			classified := ClassifyIntent(ctx, a.opts.LLM, text, ictx)
			if classified.Confidence >= intent.Confidence || intent.Kind == IntentChat {
				intent = classified
			}
		}
	}
	a.emit(Event{Type: "intent", Message: intent.Describe(), Args: map[string]any{
		"kind": string(intent.Kind), "project": intent.Project, "service": intent.Service,
		"confidence": intent.Confidence, "source": intent.Source,
	}})

	if intent.NeedsClarification || intent.Confidence < 0.55 {
		if intent.Clarification == "" {
			intent.Clarification = "I want to be sure I understand. Which project and action do you mean?" + availableProjects(ictx)
		}
		res := a.resultFor(itentFail(intent), intent)
		return res, nil
	}

	switch intent.Kind {
	case IntentChat:
		if a.opts.LLM == nil {
			res := a.resultFor("I can handle status, health, explain, logs, env, heal, up/down/restart, nuke, scan, tunnel, and file reads deterministically. Configure an LLM for open-ended reasoning.", intent)
			return res, nil
		}
		return a.Run(ctx, SystemPrompt, intentAwarePrompt(text, ictx, intent))
	case IntentSwitchProject:
		res := a.resultFor(fmt.Sprintf("Switching context to %s.", intent.Project), intent)
		return res, nil
	case IntentHelp:
		res := a.resultFor(helpText(), intent)
		return res, nil
	}

	final, err := a.dispatchIntent(ctx, intent)
	res := a.resultFor(final, intent)
	if err != nil {
		res.State = StateFailed
		return res, err
	}
	return res, nil
}

func (a *Agent) resultFor(final string, intent Intent) *Result {
	a.setState(StateDone, "intent complete")
	a.emit(Event{Type: "final", Message: final})
	return &Result{
		Final: final, State: StateDone, Intent: &intent,
		Events: append([]Event{}, a.events...), ToolCalls: a.toolCallCount(), BytesRead: a.bytes,
	}
}

func (a *Agent) dispatchIntent(ctx context.Context, it Intent) (string, error) {
	switch it.Kind {
	case IntentStatus:
		return a.intentStatus(ctx, it)
	case IntentHealth:
		return a.intentHealth(ctx, it)
	case IntentExplain:
		if a.opts.Project == nil {
			return "", domain.E(domain.KindNotFound, "agent.explain", "no project loaded", nil)
		}
		return graph.Build(a.opts.Project).Render(), nil
	case IntentLogs:
		if a.opts.Compose == nil {
			return "", domain.E(domain.KindDocker, "agent.logs", "no compose controller", nil)
		}
		out, err := a.opts.Compose.Logs(ctx, it.Service, 160)
		if err != nil {
			return "", err
		}
		header := "Logs"
		if it.Service != "" {
			header += " for " + it.Service
		}
		return "**" + header + "**\n\n" + strings.TrimSpace(out), nil
	case IntentEnvList:
		return a.intentEnvList(it)
	case IntentEnvSet:
		if a.opts.Env == nil {
			return "", domain.E(domain.KindConfig, "agent.env", "environment access is not configured", nil)
		}
		if err := a.opts.Env.Set(it.Key, it.Value); err != nil {
			return "", err
		}
		return fmt.Sprintf("Set `%s` and updated `.env` (%s). Running stacks are recreated automatically.", it.Key, a.opts.Env.Path()), nil
	case IntentEnvUnset:
		if a.opts.Env == nil {
			return "", domain.E(domain.KindConfig, "agent.env", "environment access is not configured", nil)
		}
		if err := a.opts.Env.Unset(it.Key); err != nil {
			return "", err
		}
		return fmt.Sprintf("Unset `%s` and updated `.env`. Running stacks are recreated automatically.", it.Key), nil
	case IntentScan:
		pathArg := it.Path
		if pathArg == "" {
			pathArg = a.opts.RepoRoot
		}
		return a.invokeTool(ctx, "scan_repository", map[string]any{"path": pathArg})
	case IntentReadFile:
		if it.Path == "" {
			return "Which file should I read?", nil
		}
		return a.invokeTool(ctx, "read_repo_file", map[string]any{"rel_path": it.Path})
	case IntentGenerate:
		return a.invokeTool(ctx, "synthesize_manifests", map[string]any{})
	case IntentHeal:
		return a.invokeTool(ctx, "heal_service", map[string]any{"service": it.Service})
	case IntentUp:
		return a.invokeTool(ctx, "orchestrate_stack", map[string]any{"action": "up"})
	case IntentDown:
		return a.invokeTool(ctx, "orchestrate_stack", map[string]any{"action": "down"})
	case IntentRestart:
		return a.invokeTool(ctx, "orchestrate_stack", map[string]any{"action": "restart"})
	case IntentNuke:
		return a.invokeTool(ctx, "orchestrate_stack", map[string]any{"action": "nuke"})
	case IntentTunnel:
		args := map[string]any{}
		if it.Port > 0 {
			args["port"] = it.Port
		}
		return a.invokeTool(ctx, "start_public_tunnel", args)
	default:
		return "I don't know how to handle that intent yet.", nil
	}
}

func (a *Agent) invokeTool(ctx context.Context, name string, args map[string]any) (string, error) {
	return a.invoke(ctx, name, args)
}

func (a *Agent) intentStatus(ctx context.Context, it Intent) (string, error) {
	if a.opts.Project == nil {
		return "", domain.E(domain.KindNotFound, "agent.status", "no project loaded", nil)
	}
	var ps []domain.Container
	if a.opts.Compose != nil {
		out, err := a.opts.Compose.Ps(ctx)
		if err != nil {
			return "", err
		}
		ps = out
	}
	byService := map[string]domain.Container{}
	for _, c := range ps {
		byService[c.Service] = c
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n", a.opts.Project.Name)
	for _, svc := range a.opts.Project.Services {
		c := byService[svc.Name]
		state := c.State
		if state == "" {
			state = "stopped"
		}
		health := c.Health
		if health == "" {
			health = "—"
		}
		host := 0
		if svc.HostPort > 0 {
			host = svc.HostPort
		}
		fmt.Fprintf(&b, "- **%s** — %s / %s · `%d → %d`\n", svc.Name, state, health, host, svc.Port)
	}
	for _, inf := range a.opts.Project.InfraServices {
		if inf.Mode == domain.InfraModeExternal {
			fmt.Fprintf(&b, "- **%s** — external (%s)\n", inf.Name, inf.Provider)
			continue
		}
		c := byService[inf.Name]
		state := c.State
		if state == "" {
			state = "stopped"
		}
		fmt.Fprintf(&b, "- **%s** — %s · `%d → %d`\n", inf.Name, state, inf.HostPort, inf.Port)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (a *Agent) intentHealth(ctx context.Context, it Intent) (string, error) {
	if a.opts.Project == nil {
		return "", domain.E(domain.KindNotFound, "agent.health", "no project loaded", nil)
	}
	portMap := map[string]int{}
	for _, svc := range a.opts.Project.Services {
		portMap[svc.Name] = svc.HostPort
	}
	results := httpcheck.Services(ctx, a.opts.Project.Services, portMap)
	var b strings.Builder
	fmt.Fprintf(&b, "**Health · %s**\n\n", a.opts.Project.Name)
	if len(results) == 0 {
		b.WriteString("- No web services to probe.\n")
	}
	for _, r := range results {
		mark := "✓"
		if !r.Healthy() {
			mark = "✗"
		}
		fmt.Fprintf(&b, "- %s **%s** — %s (`%s`, HTTP %d)\n", mark, r.Service, r.Status, r.URL, r.Code)
	}
	if a.opts.Compose != nil {
		if ps, err := a.opts.Compose.Ps(ctx); err == nil {
			for _, c := range ps {
				if strings.EqualFold(c.Health, "unhealthy") {
					fmt.Fprintf(&b, "- ✗ **%s** container health is unhealthy\n", c.Service)
				}
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (a *Agent) intentEnvList(it Intent) (string, error) {
	if a.opts.Env == nil {
		return "", domain.E(domain.KindConfig, "agent.env", "environment access is not configured", nil)
	}
	env, err := a.opts.Env.List()
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if !strings.HasSuffix(k, "_") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "**Environment · %s**\n\n", it.Project)
	missing := 0
	for _, k := range keys {
		v := env[k]
		if strings.TrimSpace(v) == "" {
			missing++
			fmt.Fprintf(&b, "- `%s` — **missing**\n", k)
			continue
		}
		if envvar.IsSecretName(k) {
			v = "••••••••"
		}
		fmt.Fprintf(&b, "- `%s` = %s\n", k, v)
	}
	if missing > 0 {
		fmt.Fprintf(&b, "\n%d variable(s) missing. Set one with: `set KEY=VALUE`.", missing)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func itentFail(it Intent) string {
	if it.Clarification != "" {
		return it.Clarification
	}
	return "I need a little more detail."
}

func intentAwarePrompt(text string, ictx IntentContext, it Intent) string {
	ctx, _ := json.MarshalIndent(ictx, "", "  ")
	return fmt.Sprintf(`The user's request was interpreted as: %s
Structured intent: %s
Context:
%s

Resolve the request using tools. If the interpretation is wrong, say so and correct it.`, it.Describe(), mustJSON(it), ctx)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func buildIntentPrompt(text string, ictx IntentContext) string {
	ctx, _ := json.MarshalIndent(ictx, "", "  ")
	return fmt.Sprintf(`Classify this request into exactly one intent.

PROJECT/SERVICE CONTEXT:
%s

REQUEST:
%s

Allowed intents: status, health, explain, logs, env_list, env_set, env_unset, heal, up, down, restart, nuke, scan, tunnel, read_file, generate, switch_project, help, chat.
Rules:
- Use only project names/ids and services from the context. Never invent them.
- If the target is ambiguous or unknown, set needs_clarification=true and ask one short question.
- For env_set extract key and value. For env_unset extract key.
- For tunnel extract port if mentioned.
- For read_file extract a repo-relative path if mentioned.
- confidence is 0..1.
Return JSON only:
{"kind":"status","project":"","service":"","key":"","value":"","path":"","port":0,"confidence":0.9,"goal":"restate the goal","needs_clarification":false,"clarification":""}`, string(ctx), text)
}

const intentSystemPrompt = `You are Spool's intent classifier. You are precise and conservative: when uncertain you ask one clarifying question instead of guessing. Return only valid JSON.`

func helpText() string {
	return strings.Join([]string{
		"**I can do these directly:**",
		"- `status` / `what's running` — container and health state",
		"- `health` — live HTTP readiness probes",
		"- `explain` / `topology` — dependency DAG, ports, start order",
		"- `logs [service]` — recent container logs",
		"- `env` — required variables (secrets masked)",
		"- `set KEY=VALUE` / `unset KEY` — edit and apply env",
		"- `heal` — deterministic + LLM repair",
		"- `up` / `down` / `restart` — orchestrate the stack",
		"- `nuke` — destroy (asks for confirmation)",
		"- `scan <path-or-url>` — detect services",
		"- `tunnel [port]` — public preview URL",
		"- `read file <path>` — jailed file read",
		"- `use project <name>` — switch context",
		"",
		"Ask naturally; I will restate the intent before acting.",
	}, "\n")
}

func hasWord(s, word string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).MatchString(s)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func findProject(lower string, ictx IntentContext) string {
	type cand struct{ name, id string }
	var cands []cand
	for _, p := range ictx.Projects {
		cands = append(cands, cand{p.Name, p.ID})
	}
	sort.Slice(cands, func(i, j int) bool { return len(cands[i].name) > len(cands[j].name) })
	for _, c := range cands {
		if c.name != "" && strings.Contains(lower, strings.ToLower(c.name)) {
			return c.name
		}
		if c.id != "" && strings.Contains(lower, strings.ToLower(c.id)) {
			return c.name
		}
	}
	return ""
}

func findService(lower string, ictx IntentContext, project string) string {
	services := map[string]bool{}
	for _, p := range ictx.Projects {
		if project != "" && !strings.EqualFold(p.Name, project) && !strings.EqualFold(p.ID, project) {
			continue
		}
		for _, s := range p.Services {
			services[s] = true
		}
	}
	var cands []string
	for s := range services {
		cands = append(cands, s)
	}
	sort.Slice(cands, func(i, j int) bool { return len(cands[i]) > len(cands[j]) })
	for _, s := range cands {
		if s != "" && strings.Contains(lower, strings.ToLower(s)) {
			return s
		}
	}
	return ""
}

func knownProject(name string, ictx IntentContext) bool {
	for _, p := range ictx.Projects {
		if strings.EqualFold(p.Name, name) || strings.EqualFold(p.ID, name) {
			return true
		}
	}
	return false
}

func knownService(name, project string, ictx IntentContext) bool {
	for _, p := range ictx.Projects {
		if project != "" && !strings.EqualFold(p.Name, project) && !strings.EqualFold(p.ID, project) {
			continue
		}
		for _, s := range p.Services {
			if strings.EqualFold(s, name) {
				return true
			}
		}
	}
	return false
}

func availableProjects(ictx IntentContext) string {
	if len(ictx.Projects) == 0 {
		return " No projects are initialized yet."
	}
	names := make([]string, 0, len(ictx.Projects))
	for _, p := range ictx.Projects {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return " Available: " + strings.Join(names, ", ") + "."
}

func cleanValue(v, project string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, `"'`)
	low := strings.ToLower(v)
	// Drop trailing natural-language project hints.
	if project != "" {
		for _, marker := range []string{" on " + strings.ToLower(project), " in " + strings.ToLower(project), " for " + strings.ToLower(project)} {
			if i := strings.Index(low, marker); i > 0 {
				v = strings.TrimSpace(v[:i])
				low = strings.ToLower(v)
			}
		}
	}
	for _, marker := range []string{" on project", " in project", " for project"} {
		if i := strings.Index(low, marker); i > 0 {
			v = strings.TrimSpace(v[:i])
		}
	}
	return v
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	if n < 1 || n > 65535 {
		return 0
	}
	return n
}
