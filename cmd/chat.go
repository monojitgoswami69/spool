package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/agent"
	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/graph"
	"github.com/monojitgoswami69/spool/internal/portprobe"
	"github.com/monojitgoswami69/spool/internal/safefs"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/tunnel"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var chatCmd = &cobra.Command{
	Use:   "chat [project]",
	Short: "Conversational DevOps agent with streaming tool execution",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runChat,
}

type chatMessage struct {
	role string
	text string
}

type agentEventMsg struct{ ev agent.Event }
type agentDoneMsg struct {
	res *agent.Result
	err error
}
type slashResultMsg struct {
	text string
	err  error
}
type tunnelStartedMsg struct {
	t   *tunnel.Tunnel
	err error
}
type confirmRequest struct {
	tool        string
	destructive bool
	args        map[string]any
	resp        chan bool
}
type confirmReqMsg struct{ req *confirmRequest }

type chatModel struct {
	ctx     context.Context
	cancel  context.CancelFunc
	program *tea.Program
	ag      *agent.Agent
	mgr     *state.Manager
	lock    *domain.Lock

	input    textinput.Model
	spin     spinner.Model
	messages []chatMessage
	events   []string
	busy     bool
	pending  *confirmRequest
	status   string

	provider, modelName string
	service             string
	client              domain.LLMClient
	width, height       int
	tunnel              *tunnel.Tunnel
}

func (m *chatModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spin.Tick)
}

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = m.chatWidth() - 8
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case agentEventMsg:
		m.events = append(m.events, formatEvent(msg.ev))
		if len(m.events) > 8 {
			m.events = m.events[len(m.events)-8:]
		}
		return m, nil
	case agentDoneMsg:
		m.busy = false
		m.events = nil
		m.pending = nil
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "I hit an error: " + msg.err.Error()})
		} else if msg.res != nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: msg.res.Final})
			if msg.res.Intent != nil {
				if msg.res.Intent.Service != "" {
					m.service = msg.res.Intent.Service
				}
				if (msg.res.Intent.Kind == agent.IntentSwitchProject || msg.res.Intent.Kind == agent.IntentOnboard) && msg.res.Intent.Project != "" {
					if mgr, lock, err := resolveProject(msg.res.Intent.Project); err == nil {
						if ag, aerr := buildChatAgent(m.client, mgr, lock, m.eventHandler(), m.confirmHandler()); aerr == nil {
							if m.ag != nil {
								_ = m.ag.Close()
							}
							m.mgr, m.lock, m.ag = mgr, lock, ag
							m.service = ""
							m.messages = append(m.messages, chatMessage{role: "assistant", text: "Context is now **" + lock.Project + "**."})
						}
					}
				}
			}
		}
		return m, nil
	case slashResultMsg:
		m.busy = false
		text := msg.text
		if msg.err != nil {
			text = "error: " + msg.err.Error()
		}
		m.messages = append(m.messages, chatMessage{role: "assistant", text: text})
		return m, nil
	case tunnelStartedMsg:
		m.busy = false
		if msg.err != nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "tunnel error: " + msg.err.Error()})
			return m, nil
		}
		m.tunnel = msg.t
		m.messages = append(m.messages, chatMessage{role: "assistant", text: "Public preview: " + msg.t.PublicURL()})
		return m, nil
	case confirmReqMsg:
		m.pending = msg.req
		return m, nil
	case tea.KeyMsg:
		if m.pending != nil {
			switch msg.String() {
			case "y", "Y":
				m.pending.resp <- true
				m.pending = nil
				return m, nil
			case "n", "N", "esc":
				m.pending.resp <- false
				m.pending = nil
				return m, nil
			}
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "enter":
			if m.busy {
				return m, nil
			}
			return m.submit()
		}
		if m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *chatModel) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	m.input.SetValue("")
	m.messages = append(m.messages, chatMessage{role: "you", text: text})
	if strings.HasPrefix(text, "/") {
		cmd := m.slash(text)
		return m, cmd
	}
	if m.ag == nil {
		m.messages = append(m.messages, chatMessage{role: "assistant", text: "No agent is available. Configure an LLM provider first."})
		return m, nil
	}
	m.busy = true
	m.status = "classifying intent…"
	ictx := m.intentContext()
	return m, runIntentCmd(m.ctx, m.ag, text, ictx)
}

func runIntentCmd(ctx context.Context, ag *agent.Agent, prompt string, ictx agent.IntentContext) tea.Cmd {
	return func() tea.Msg {
		res, err := ag.RunIntent(ctx, prompt, ictx)
		return agentDoneMsg{res: res, err: err}
	}
}

func (m *chatModel) slash(input string) tea.Cmd {
	fields := strings.Fields(input)
	command := fields[0]
	arg := ""
	if len(fields) > 1 {
		arg = fields[1]
	}
	switch command {
	case "/help":
		m.messages = append(m.messages, chatMessage{role: "assistant", text: strings.Join([]string{
			"Talk naturally, or use slash commands:",
			"· status / what's running    · health",
			"· diagnose / why did it fail  · logs [service]",
			"· explain / topology          · env",
			"· env / set KEY=VALUE         · unset KEY",
			"· heal / fix it               · up / down / restart",
			"· nuke (confirmation required)",
			"· tunnel [port] / preview     · scan <path-or-url>",
			"· read file <path>            · use project <name>",
			"· onboard <git-url-or-path>   · diagnose",
			"",
			"Slash commands: /status /explain /heal /tunnel /nuke /nuke-confirm /clear /help",
		}, "\n")})
		return nil
	case "/clear":
		m.messages = nil
		return nil
	case "/onboard":
		target := strings.TrimSpace(strings.TrimPrefix(input, "/onboard"))
		if target == "" {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "Usage: /onboard <git-url-or-path>"})
			return nil
		}
		if m.ag == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No agent is available."})
			return nil
		}
		m.busy = true
		return runIntentCmd(m.ctx, m.ag, "onboard "+target, m.intentContext())
	case "/diagnose":
		if m.ag == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No agent is available."})
			return nil
		}
		m.busy = true
		return runIntentCmd(m.ctx, m.ag, "diagnose", m.intentContext())
	case "/env":
		if m.ag == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No agent is available."})
			return nil
		}
		m.busy = true
		return runIntentCmd(m.ctx, m.ag, "show environment", m.intentContext())
	case "/status":
		if m.mgr == nil || m.lock == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No project loaded."})
			return nil
		}
		m.busy = true
		return statusSlashCmd(m.ctx, m.mgr, m.lock)
	case "/explain":
		if m.lock == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No project loaded."})
			return nil
		}
		m.messages = append(m.messages, chatMessage{role: "assistant", text: graph.Build(projectFor(m.mgr, m.lock)).Render()})
		return nil
	case "/heal":
		if m.mgr == nil || m.lock == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No project loaded."})
			return nil
		}
		m.busy = true
		return healSlashCmd(m.ctx, m.mgr, m.lock)
	case "/tunnel":
		if m.lock == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No project loaded."})
			return nil
		}
		m.busy = true
		return tunnelSlashCmd(m.ctx, m.lock)
	case "/nuke":
		m.messages = append(m.messages, chatMessage{role: "assistant", text: "Destructive action. Type /nuke-confirm to destroy the project, or /clear to cancel."})
		return nil
	case "/nuke-confirm":
		if m.mgr == nil || m.lock == nil {
			m.messages = append(m.messages, chatMessage{role: "assistant", text: "No project loaded."})
			return nil
		}
		m.busy = true
		return nukeSlashCmd(m.ctx, m.mgr, m.lock)
	default:
		_ = arg
		m.messages = append(m.messages, chatMessage{role: "assistant", text: "Unknown command " + command + ". Try /help."})
		return nil
	}
}

func statusSlashCmd(ctx context.Context, mgr *state.Manager, lock *domain.Lock) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()
		ps, err := composeFor(mgr).Ps(cctx)
		if err != nil {
			return slashResultMsg{err: err}
		}
		byService := map[string]domain.Container{}
		for _, c := range ps {
			byService[c.Service] = c
		}
		var rows [][]string
		for _, svc := range lock.Services {
			c := byService[svc.Name]
			s := c.State
			if s == "" {
				s = "stopped"
			}
			rows = append(rows, []string{svc.Name, ui.StatusBadge(s, c.Health), fmt.Sprintf("%d", lock.PortMap[svc.Name])})
		}
		return slashResultMsg{text: ui.Table([]string{"Service", "Status", "Host"}, rows, "status")}
	}
}

func healSlashCmd(ctx context.Context, mgr *state.Manager, lock *domain.Lock) tea.Cmd {
	return func() tea.Msg {
		client, _ := llmFor(true)
		h, err := healerFor(mgr, lock, client)
		if err != nil {
			return slashResultMsg{err: err}
		}
		res, err := h.Heal(ctx, "", "")
		if res == nil {
			return slashResultMsg{err: err}
		}
		var b strings.Builder
		b.WriteString(res.Summary + "\n")
		for _, a := range res.Attempts {
			fmt.Fprintf(&b, "- attempt %d [%s] %s\n", a.N, a.Kind, a.Summary)
		}
		return slashResultMsg{text: strings.TrimSpace(b.String()), err: err}
	}
}

func tunnelSlashCmd(ctx context.Context, lock *domain.Lock) tea.Cmd {
	return func() tea.Msg {
		port := 0
		for _, svc := range lock.Services {
			if svc.IsWorker {
				continue
			}
			if p := lock.PortMap[svc.Name]; p > 0 {
				port = p
				break
			}
			if svc.HostPort > 0 {
				port = svc.HostPort
				break
			}
		}
		if port == 0 {
			return tunnelStartedMsg{err: fmt.Errorf("no local port available")}
		}
		t, err := tunnel.Start(ctx, port, tunnel.Options{})
		return tunnelStartedMsg{t: t, err: err}
	}
}

func nukeSlashCmd(ctx context.Context, mgr *state.Manager, lock *domain.Lock) tea.Cmd {
	return func() tea.Msg {
		if err := destroyProject(ctx, mgr, lock, false, true); err != nil {
			return slashResultMsg{err: err}
		}
		return slashResultMsg{text: "Destroyed " + lock.Project}
	}
}

func formatEvent(ev agent.Event) string {
	switch ev.Type {
	case "intent":
		return ui.TitleStyle.Render("🎯 " + ev.Message)
	case "tool_progress":
		if ev.Message == "" {
			return ""
		}
		return ui.MutedStyle.Render("· " + ev.Message)
	case "tool_start":
		return ui.TitleStyle.Render("⚡ " + ev.Tool + " " + shortArgs(ev.Args))
	case "tool_end":
		if ev.Success {
			return ui.SuccessStyle.Render("✓ " + ev.Tool + " — " + ev.Message)
		}
		return ui.ErrorStyle.Render("✗ " + ev.Tool + " — " + ev.Message)
	case "permission_denied":
		return ui.WarnStyle.Render("⚠ denied " + ev.Tool)
	case "thinking":
		if ev.Message == "" {
			return ""
		}
		return ui.MutedStyle.Render("… " + ev.Message)
	default:
		return ""
	}
}

func shortArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	s := strings.Join(parts, " ")
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return "(" + s + ")"
}

func (m *chatModel) chatWidth() int {
	width := m.width
	if width <= 0 {
		width = 100
	}
	cw := width - 6
	if cw > 110 {
		cw = 110
	}
	if cw < 24 {
		cw = 24
	}
	return cw
}

func (m *chatModel) View() string {
	width := m.width
	if width < 30 {
		width = 30
	}
	height := m.height
	if height < 12 {
		height = 12
	}
	contentW := m.chatWidth()

	header := ui.TitleStyle.Render("◆ Spool")
	var meta []string
	if m.lock != nil {
		meta = append(meta, ui.BoldStyle.Render(m.lock.Project))
	}
	if m.provider != "" {
		meta = append(meta, ui.MutedStyle.Render(m.provider+"/"+m.modelName))
	}
	if len(meta) > 0 {
		header += ui.MutedStyle.Render("   ") + strings.Join(meta, ui.MutedStyle.Render("  ·  "))
	}
	rule := ui.MutedStyle.Render(strings.Repeat("─", contentW))

	body := m.renderTranscript(contentW)
	if len(m.events) > 0 {
		body = append(body, "")
		for _, e := range m.events {
			if e != "" {
				body = append(body, "  "+e)
			}
		}
	}

	var bottom []string
	if m.pending != nil {
		kind := "mutating"
		style := ui.WarnStyle
		if m.pending.destructive {
			kind = "DESTRUCTIVE"
			style = ui.ErrorStyle
		}
		bottom = append(bottom, style.Render(fmt.Sprintf("⚠ Allow %s (%s)?  [y/N]", m.pending.tool, kind)))
	}
	if m.busy && m.pending == nil {
		bottom = append(bottom, "  "+m.spin.View()+" "+ui.MutedStyle.Render("working…"))
	}

	m.input.Width = contentW - 8
	if m.input.Width < 12 {
		m.input.Width = 12
	}
	inputBox := ui.InputBoxStyle.Copy().Width(contentW - 2).Render(m.input.View())
	bottom = append(bottom, inputBox)
	bottom = append(bottom, ui.MutedStyle.Render("enter send  ·  /help commands  ·  /diagnose  ·  ctrl-c quit"))

	bottomHeight := len(bottom) + 1
	baseHeight := 2 // header + rule
	avail := height - baseHeight - bottomHeight
	if avail < 3 {
		avail = 3
	}
	if len(body) > avail {
		body = body[len(body)-avail:]
	}
	for len(body) < avail {
		body = append(body, "")
	}

	lines := []string{header, rule}
	lines = append(lines, body...)
	lines = append(lines, "")
	lines = append(lines, bottom...)
	content := strings.Join(lines, "\n")
	block := lipgloss.NewStyle().Width(contentW).Render(content)
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, block)
}

func (m *chatModel) renderTranscript(width int) []string {
	var lines []string
	for _, msg := range m.messages {
		if msg.role == "you" {
			lines = append(lines, ui.HighlightStyle.Render("› You"))
			lines = append(lines, indentLines(wrapText(msg.text, width-2), "  ")...)
		} else {
			lines = append(lines, ui.TitleStyle.Render("◆ Spool"))
			lines = append(lines, indentLines(wrapText(ui.Markdown(msg.text), width-2), "  ")...)
		}
		lines = append(lines, "")
	}
	return lines
}

func wrapText(text string, width int) []string {
	if width < 10 {
		width = 10
	}
	return strings.Split(lipgloss.NewStyle().Width(width).Render(text), "\n")
}

func indentLines(lines []string, prefix string) []string {
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return lines
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func runChat(cmd *cobra.Command, args []string) error {
	var mgr *state.Manager
	var lock *domain.Lock
	var err error
	if len(args) > 0 {
		mgr, lock, err = resolveProject(args[0])
		if err != nil {
			return err
		}
	} else if managers, lerr := state.List(); lerr == nil && len(managers) > 0 {
		mgr = managers[0]
		lock, _ = mgr.LoadLock()
	}

	client, lerr := llmFor(true)
	if lerr != nil {
		client = nil
	}
	resolved, rerr := configSummary()
	if rerr != nil {
		resolved = providerSummary{provider: "deterministic", model: "no-llm"}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := &chatModel{
		ctx: cctx, cancel: cancel, mgr: mgr, lock: lock, client: client,
		width: 100, height: 30,
		provider: resolved.provider, modelName: resolved.model,
	}
	m.input = textinput.New()
	m.input.Placeholder = "ask spool to scan, fix, explain, or run something…"
	m.input.CharLimit = 2000
	m.input.Width = 80
	m.input.Placeholder = "Ask Spool anything — status, why it failed, fix it, explain…"
	m.input.Prompt = "› "
	m.input.PromptStyle = ui.TitleStyle
	m.input.Focus()
	m.spin = spinner.New()
	m.spin.Spinner = spinner.Pulse

	ag, err := buildChatAgent(client, mgr, lock, m.eventHandler(), m.confirmHandler())
	if err != nil {
		return err
	}
	m.ag = ag
	defer ag.Close()
	welcome := "I am an agent with real tools: onboard repositories, scan, infer, generate manifests, orchestrate, inspect logs/health, diagnose failures, heal, explain topology, manage env, read repo files, and open tunnels. Ask naturally — I restate the intent before acting. Try `/help`."
	if client == nil {
		welcome = "LLM is not configured, so I am in deterministic intent mode: status, health, diagnose, explain, logs, env, heal, up/down/restart, nuke, scan, tunnel, file reads. Try `/help`."
	}
	m.messages = append(m.messages, chatMessage{role: "assistant", text: welcome})

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(cctx))
	m.program = p
	_, err = p.Run()
	if m.tunnel != nil {
		_ = m.tunnel.Close()
	}
	return err
}

// buildChatAgent constructs an agent bound to the given project.
func buildChatAgent(client domain.LLMClient, mgr *state.Manager, lock *domain.Lock, onEvent func(agent.Event), confirm func(string, bool, map[string]any) bool) (*agent.Agent, error) {
	opts := agent.Options{
		LLM:          client,
		Budget:       agent.DefaultBudget(),
		AutoApprove:  flagYes,
		Allocator:    portprobe.New(),
		EventHandler: onEvent,
		Confirm:      confirm,
		Onboard: func(ctx context.Context, target string, maxServices int, noRun bool, progress func(string)) (*agent.OnboardResult, error) {
			return onboardForAgent(ctx, target, maxServices, noRun, progress, client)
		},
	}
	if mgr != nil {
		opts.WorkJail, _ = safefs.NewJail(mgr.Dir)
		opts.WorkDir = mgr.Dir
		opts.Compose = composeFor(mgr)
		opts.Healer, _ = healerFor(mgr, lock, client)
		opts.Env = chatEnv{mgr: mgr, lock: lock}
		if lock != nil {
			opts.RepoRoot = lock.RepoPath
			opts.Project = projectFor(mgr, lock)
			if lock.RepoPath != "" {
				opts.RepoJail, _ = safefs.NewJail(lock.RepoPath)
			}
		}
	}
	return agent.New(opts)
}

func (m *chatModel) eventHandler() func(agent.Event) {
	return func(ev agent.Event) {
		if m.program != nil {
			m.program.Send(agentEventMsg{ev: ev})
		}
	}
}

func (m *chatModel) confirmHandler() func(string, bool, map[string]any) bool {
	return func(tool string, destructive bool, a map[string]any) bool {
		if flagYes {
			return true
		}
		req := &confirmRequest{tool: tool, destructive: destructive, args: a, resp: make(chan bool, 1)}
		if m.program == nil {
			return false
		}
		m.program.Send(confirmReqMsg{req: req})
		select {
		case ok := <-req.resp:
			return ok
		case <-m.ctx.Done():
			return false
		case <-time.After(90 * time.Second):
			return false
		}
	}
}

func (m *chatModel) projectName() string {
	if m.lock == nil {
		return ""
	}
	return m.lock.Project
}

func (m *chatModel) intentContext() agent.IntentContext {
	ictx := agent.IntentContext{CurrentProject: m.projectName(), CurrentService: m.service}
	managers, err := state.List()
	if err != nil {
		return ictx
	}
	for _, mm := range managers {
		lock, err := mm.LoadLock()
		if err != nil || lock == nil {
			continue
		}
		info := agent.ProjectInfo{ID: mm.ID, Name: lock.Project}
		for _, svc := range lock.Services {
			info.Services = append(info.Services, svc.Name)
		}
		ictx.Projects = append(ictx.Projects, info)
	}
	return ictx
}

// chatEnv adapts project state to the agent's EnvAccess interface.
type chatEnv struct {
	mgr  *state.Manager
	lock *domain.Lock
}

func (e chatEnv) List() (map[string]string, error) { return renderEnv(e.mgr, e.lock) }

func (e chatEnv) Set(key, value string) error {
	overrides, _ := e.mgr.LoadOverrides()
	overrides[key] = value
	if err := e.mgr.SaveOverrides(overrides); err != nil {
		return err
	}
	if err := writeProjectEnv(e.mgr, e.lock); err != nil {
		return err
	}
	applyEnvNow(e.mgr)
	return nil
}

func (e chatEnv) Unset(key string) error {
	overrides, _ := e.mgr.LoadOverrides()
	overrides[key] = ""
	if err := e.mgr.SaveOverrides(overrides); err != nil {
		return err
	}
	if err := writeProjectEnv(e.mgr, e.lock); err != nil {
		return err
	}
	applyEnvNow(e.mgr)
	return nil
}

func (e chatEnv) Path() string { return e.mgr.EnvPath() }

type providerSummary struct {
	provider string
	model    string
}

func configSummary() (providerSummary, error) {
	resolved, err := config.Resolve()
	if err != nil {
		return providerSummary{}, err
	}
	return providerSummary{provider: resolved.Provider, model: resolved.Model}, nil
}
