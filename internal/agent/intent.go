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
	"github.com/monojitgoswami69/spool/internal/healer"
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
	IntentDiagnose      IntentKind = "diagnose"
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
	IntentOnboard       IntentKind = "onboard"
	IntentSwitchProject IntentKind = "switch_project"
	IntentHelp          IntentKind = "help"
	IntentGreeting      IntentKind = "greeting"
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
	Target             string     `json:"target,omitempty"`
	Port               int        `json:"port,omitempty"`
	NoRun              bool       `json:"no_run,omitempty"`
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
	if it.Service == "" && ictx.CurrentService != "" && containsAny(lower, " it", "it ", "that service", "this service", "that one") {
		it.Service = ictx.CurrentService
	}

	switch {
	case isGreeting(lower):
		// Pure greetings ("hi", "what's up") must never reach the tool loop or
		// the fragile " up" substring match below.
		it.Kind, it.Confidence = IntentGreeting, 0.99
	case containsAny(lower, "is it up", "is it running", "is the app up", "is everything up", "what is running", "what's running", "whats running"):
		it.Kind, it.Confidence = IntentStatus, 0.95
	case containsAny(lower, "help", "what can you do", "what do you do", "commands", "how do i use"):
		it.Kind, it.Confidence = IntentHelp, 0.95
	case containsAny(lower, "onboard", "set up repo", "setup repo", "init repo", "bootstrap", "bring in repo", "add repo", "onboard repo"):
		it.Kind, it.Confidence = IntentOnboard, 0.92
		it.Target = extractTarget(raw)
		it.NoRun = containsAny(lower, "no-run", "no run", "generate only", "don't run", "dont run", "without running")
		if it.Target == "" {
			it.NeedsClarification = true
			it.Clarification = "Which repository should I onboard? Give me a Git URL or a local path."
			it.Confidence = 0.5
		}
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
	case containsAny(lower, "diagnose", "why did", "why is", "why does", "what failed", "what went wrong", "root cause", "debug"):
		it.Kind, it.Confidence = IntentDiagnose, 0.92
	case hasWord(lower, "heal") || containsAny(lower, "self-heal", "self heal", "repair", "fix it", "fix the"):
		it.Kind, it.Confidence = IntentHeal, 0.9
	case containsAny(lower, "shut down", "shutdown", "stop the", "stop it", "down", "take down", "take it down", "shut it down", "kill it"):
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

	if it.Kind != IntentHelp && it.Kind != IntentScan && it.Kind != IntentChat && it.Kind != IntentSwitchProject && it.Kind != IntentOnboard && it.Kind != IntentGreeting {
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

// isGreeting reports whether a message is a pure greeting or small talk —
// short, no actionable content — so the agent can answer directly instead of
// burning LLM tool-loop cycles on "hello".
func isGreeting(lower string) bool {
	s := strings.TrimRight(strings.TrimSpace(lower), "!?., ")
	switch s {
	case "what's up", "whats up", "wassup", "sup", "hi spool", "hello spool", "hey spool":
		return true
	}
	words := strings.Fields(s)
	if len(words) == 0 || len(words) > 3 {
		return false
	}
	for _, w := range words {
		switch w {
		case "hi", "hello", "hey", "yo", "howdy", "hola", "greetings", "thanks", "thank",
			"you", "spool", "there", "all", "good", "morning", "afternoon", "evening", "friends":
		default:
			return false
		}
	}
	return true
}

// greetingReply renders the deterministic small-talk answer with light project
// context. No tools, no LLM.
func (a *Agent) greetingReply(ictx IntentContext) string {
	var b strings.Builder
	b.WriteString("Hey! I'm Spool, your infrastructure agent.")
	switch {
	case ictx.CurrentProject != "":
		fmt.Fprintf(&b, " I'm watching **%s** — ask me for status, health, logs, topology, or env changes; say \"heal\" if something looks off.", ictx.CurrentProject)
	case len(ictx.Projects) == 1:
		fmt.Fprintf(&b, " I'm watching **%s** — ask me for status, health, logs, topology, or env changes.", ictx.Projects[0].Name)
	case len(ictx.Projects) > 1:
		fmt.Fprintf(&b, " %d projects are onboarded — ask for status, or say \"switch to <project>\" to focus one.", len(ictx.Projects))
	default:
		b.WriteString(" Nothing is onboarded yet — say \"onboard <git-url-or-path>\" and I'll scan, containerize, and verify it.")
	}
	return b.String()
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
	if it.Project == "" && it.Kind != IntentHelp && it.Kind != IntentScan && it.Kind != IntentChat && it.Kind != IntentSwitchProject && it.Kind != IntentOnboard && it.Kind != IntentGreeting {
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
	case IntentDiagnose:
		return "diagnose" + target
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
	case IntentOnboard:
		return "onboard " + it.Target
	case IntentSwitchProject:
		return "switch to " + it.Project
	case IntentHelp:
		return "help"
	case IntentGreeting:
		return "greeting"
	default:
		return "general request"
	}
}

// IsReadOnly reports whether the intent never mutates the stack.
func (it Intent) IsReadOnly() bool {
	switch it.Kind {
	case IntentStatus, IntentHealth, IntentExplain, IntentLogs, IntentDiagnose, IntentEnvList, IntentScan, IntentReadFile, IntentHelp:
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

	// Project-less sessions answer clearly instead of erroring inside a tool.
	if a.opts.Project == nil && len(ictx.Projects) == 0 {
		runsProjectLess := intent.Kind == IntentChat || intent.Kind == IntentHelp ||
			intent.Kind == IntentGreeting || intent.Kind == IntentOnboard || intent.Kind == IntentSwitchProject
		if !runsProjectLess && !(intent.Kind == IntentScan && intent.Path != "") {
			res := a.resultFor("No projects are onboarded yet. Use `/onboard <git-url-or-path>` (or `spool init`) to bring one up, and I'll manage it from there.", intent)
			return res, nil
		}
	}

	switch intent.Kind {
	case IntentChat:
		if a.opts.LLM == nil {
			res := a.resultFor("I can handle status, health, explain, logs, env, heal, up/down/restart, nuke, scan, tunnel, and file reads deterministically. Configure an LLM for open-ended reasoning.", intent)
			return res, nil
		}
		return a.Run(ctx, SystemPrompt, intentAwarePrompt(text, ictx, intent))
	case IntentOnboard:
		return a.runOnboardIntent(ctx, intent)
	case IntentSwitchProject:
		res := a.resultFor(fmt.Sprintf("Switching context to %s.", intent.Project), intent)
		return res, nil
	case IntentHelp:
		res := a.resultFor(helpText(), intent)
		return res, nil
	case IntentGreeting:
		res := a.resultFor(a.greetingReply(ictx), intent)
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
	case IntentDiagnose:
		return a.intentDiagnose(ctx, it)
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

func extractTarget(raw string) string {
	if m := regexp.MustCompile(`(?:https?://|git@)\S+`).FindString(raw); m != "" {
		return strings.Trim(m, `"'`)
	}
	for _, f := range strings.Fields(raw) {
		f = strings.Trim(f, `"'`)
		if strings.HasPrefix(f, "-") {
			continue
		}
		if strings.HasPrefix(f, "/") || strings.HasPrefix(f, "./") || strings.HasPrefix(f, "../") {
			return f
		}
		if strings.HasPrefix(f, "github.com/") {
			return "https://" + f
		}
		if strings.Count(f, "/") == 1 && !strings.Contains(f, "=") && !strings.Contains(f, ".") {
			return "https://github.com/" + f
		}
		if strings.Contains(f, "/") && (strings.Contains(f, "fixture") || strings.HasPrefix(f, "test/") || strings.Contains(f, ".")) {
			return f
		}
	}
	return ""
}

func (a *Agent) runOnboardIntent(ctx context.Context, it Intent) (*Result, error) {
	if a.opts.Onboard == nil {
		res := a.resultFor("Onboarding is not available in this session.", it)
		res.State = StateFailed
		return res, nil
	}
	args := map[string]any{"target": it.Target, "no_run": it.NoRun}
	if !a.opts.AutoApprove {
		if a.opts.Confirm == nil || !a.opts.Confirm("onboard_repository", false, args) {
			a.emit(Event{Type: "permission_denied", Tool: "onboard_repository", Args: args, Message: "onboarding denied"})
			res := a.resultFor("Onboarding was not approved.", it)
			res.State = StateFailed
			return res, nil
		}
	}
	a.emit(Event{Type: "tool_start", Tool: "onboard_repository", Args: args})
	out, err := a.opts.Onboard(ctx, it.Target, 0, it.NoRun, func(msg string) {
		a.emit(Event{Type: "tool_progress", Message: msg})
	})
	if err != nil {
		msg := "Onboarding failed: " + err.Error()
		if out != nil {
			if out.Project != "" {
				it.Project = out.Project
				msg = "Onboarding failed for **" + out.Project + "**: " + err.Error()
			}
			if len(out.URLs) > 0 {
				msg += "\n\nPartial URLs: " + strings.Join(out.URLs, ", ")
			}
			if len(out.Missing) > 0 {
				msg += "\n\nMissing env: `" + strings.Join(out.Missing, "`, `") + "`"
			}
		}
		a.emit(Event{Type: "tool_end", Tool: "onboard_repository", Success: false, Message: err.Error()})
		res := a.resultFor(msg, it)
		res.State = StateFailed
		return res, nil
	}
	it.Project = out.Project
	a.emit(Event{Type: "tool_end", Tool: "onboard_repository", Success: true, Message: out.Project})
	return a.resultFor(formatOnboard(out), it), nil
}

func formatOnboard(out *OnboardResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Onboarded %s** (`%s`)\n", out.Project, out.ProjectID)
	if out.NoRun {
		b.WriteString("\nGeneration complete (containers were not started).\n")
	}
	if len(out.URLs) > 0 {
		b.WriteString("\n**Live endpoints**\n")
		for _, u := range out.URLs {
			fmt.Fprintf(&b, "- %s\n", u)
		}
	}
	if len(out.Services) > 0 {
		fmt.Fprintf(&b, "\n**Services**: %s\n", strings.Join(out.Services, ", "))
	}
	if len(out.Infra) > 0 {
		fmt.Fprintf(&b, "**Infrastructure**: %s\n", strings.Join(out.Infra, ", "))
	}
	if out.ReusedDockerfiles {
		b.WriteString("**Build**: reused existing Dockerfile(s)\n")
	}
	if len(out.Missing) > 0 {
		fmt.Fprintf(&b, "\n**Missing env**: `%s`\n", strings.Join(out.Missing, "`, `"))
		fmt.Fprintf(&b, "Set them with `spool env %s set KEY=VALUE`.\n", out.ProjectID)
	}
	if strings.TrimSpace(out.HealSummary) != "" {
		fmt.Fprintf(&b, "\n**Heal**: %s\n", out.HealSummary)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *Agent) intentDiagnose(ctx context.Context, it Intent) (string, error) {
	if a.opts.Project == nil || a.opts.Compose == nil {
		return "", domain.E(domain.KindNotFound, "agent.diagnose", "no project or compose controller loaded", nil)
	}
	logs, _ := a.opts.Compose.Logs(ctx, it.Service, 300)
	ps, _ := a.opts.Compose.Ps(ctx)
	f := healer.Analyze(logs, it.Service)
	evidenceSource := ""
	persistedRaw := ""
	if strings.TrimSpace(logs) == "" || f.Category == healer.CatUnknown {
		if a.opts.WorkJail != nil {
			if data, err := a.opts.WorkJail.ReadFile("last-failure.log"); err == nil && len(data) > 0 {
				persisted := healer.Analyze(string(data), it.Service)
				if persisted.Category != healer.CatUnknown || strings.TrimSpace(logs) == "" {
					f = persisted
					persistedRaw = string(data)
					evidenceSource = "last recorded build failure"
				}
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**Diagnosis · %s**\n\n", a.opts.Project.Name)
	fmt.Fprintf(&b, "- Category: `%s`\n", f.Category)
	fmt.Fprintf(&b, "- Primary error: %s\n", healer.CleanError(f.Error))
	if f.Stage != "" {
		fmt.Fprintf(&b, "- Failing stage/service: `%s`\n", f.Stage)
	}
	if len(f.FileRefs) > 0 {
		fmt.Fprintf(&b, "- Files: `%s`\n", strings.Join(f.FileRefs, "`, `"))
	}
	if len(f.EnvRefs) > 0 {
		fmt.Fprintf(&b, "- Env refs: `%s`\n", strings.Join(f.EnvRefs, "`, `"))
	}
	fmt.Fprintf(&b, "- Likely cause: %s\n", diagnoseCause(f.Category))
	if evidenceSource != "" {
		fmt.Fprintf(&b, "- Evidence source: %s\n", evidenceSource)
	}

	for _, c := range ps {
		if c.State != "running" || strings.EqualFold(c.Health, "unhealthy") {
			fmt.Fprintf(&b, "- Container **%s**: %s / %s (exit %d)\n", c.Service, c.State, c.Health, c.ExitCode)
		}
	}

	evidence := strings.TrimSpace(f.RelevantLog)
	if evidenceSource != "" {
		var attempts []string
		for _, line := range strings.Split(persistedRaw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "attempt ") {
				attempts = append(attempts, line)
			}
		}
		if len(attempts) > 0 {
			evidence = strings.Join(attempts, "\n")
		}
	}
	if evidence == "" {
		evidence = strings.TrimSpace(healer.Tail(logs, 12))
	}
	if evidence != "" {
		fmt.Fprintf(&b, "\n**Evidence**\n")
		for _, line := range strings.Split(healer.Tail(evidence, 12), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}

	id := a.opts.Project.ID
	if id == "" {
		id = a.opts.Project.Name
	}
	fmt.Fprintf(&b, "\n**Next steps**\n")
	fmt.Fprintf(&b, "- `spool heal %s`\n", id)
	fmt.Fprintf(&b, "- `spool logs %s%s`\n", id, serviceSuffix(it.Service))
	fmt.Fprintf(&b, "- `spool env %s`\n", id)
	return strings.TrimRight(b.String(), "\n"), nil
}

func serviceSuffix(service string) string {
	if service == "" {
		return ""
	}
	return " " + service
}

func diagnoseCause(category string) string {
	switch category {
	case healer.CatDependency:
		return "a dependency is missing or cannot be resolved inside the container"
	case healer.CatEnvironment:
		return "the runtime language/tool version does not match what the app requires"
	case healer.CatMissingEnv:
		return "a required environment variable is not set"
	case healer.CatCompilation:
		return "application source does not compile (Spool repairs manifests, not source)"
	case healer.CatNextBuild:
		return "Next.js failed while building or collecting page data"
	case healer.CatDockerBuild:
		return "the container image build failed"
	case healer.CatRuntime:
		return "the container started but the application failed at runtime"
	case healer.CatHealthcheck:
		return "the container healthcheck did not pass"
	case healer.CatConfiguration:
		return "a build/configuration file references a missing path or value"
	default:
		return "the root cause is not obvious from the log tail; inspect the evidence below"
	}
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

Allowed intents: status, health, explain, logs, env_list, env_set, env_unset, heal, up, down, restart, nuke, scan, tunnel, read_file, generate, onboard, switch_project, help, chat.
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
		"- `diagnose` / `why did it fail` — root-cause analysis with evidence",
		"- `env` — required variables (secrets masked)",
		"- `set KEY=VALUE` / `unset KEY` — edit and apply env",
		"- `heal` — deterministic + LLM repair",
		"- `up` / `down` / `restart` — orchestrate the stack",
		"- `nuke` — destroy (asks for confirmation)",
		"- `scan <path-or-url>` — detect services",
		"- `tunnel [port]` — public preview URL",
		"- `read file <path>` — jailed file read",
		"- `use project <name>` — switch context",
		"- `onboard <path-or-url>` — clone, scan, generate, run, heal, and report live URLs",
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

// HelpText returns the agent's user-facing command capabilities.
func HelpText() string { return helpText() }
