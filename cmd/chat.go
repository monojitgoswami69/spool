package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	messages []chatMessage
	events   []string
	busy     bool
	pending  *confirmRequest
	status   string

	provider, modelName string
	width, height       int
	tunnel              *tunnel.Tunnel
}

func (m *chatModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m *chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
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
	m.status = "thinking…"
	prompt := text
	if m.lock != nil {
		prompt = fmt.Sprintf("Project %s (id %s).\nRequest: %s", m.lock.Project, m.lock.ProjectID, text)
	}
	return m, runAgentCmd(m.ctx, m.ag, prompt)
}

func runAgentCmd(ctx context.Context, ag *agent.Agent, prompt string) tea.Cmd {
	return func() tea.Msg {
		res, err := ag.Run(ctx, agent.SystemPrompt, prompt)
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
			"Slash commands:",
			"/status — container and health status",
			"/explain — dependency topology and port map",
			"/heal — run the deterministic + LLM repair loop",
			"/tunnel — open a public preview tunnel",
			"/nuke then /nuke-confirm — destroy the project",
			"/clear — clear the transcript",
			"/help — this message",
		}, "\n")})
		return nil
	case "/clear":
		m.messages = nil
		return nil
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

func (m *chatModel) View() string {
	var b strings.Builder
	b.WriteString(ui.Banner(Version))
	if m.lock != nil {
		b.WriteString(ui.MutedStyle.Render("  ·  " + m.lock.Project))
	}
	if m.provider != "" {
		b.WriteString(ui.MutedStyle.Render("  ·  " + m.provider + "/" + m.modelName))
	}
	b.WriteString("\n")
	b.WriteString(ui.MutedStyle.Render(strings.Repeat("─", max(20, m.width))) + "\n")

	wrap := lipgloss.NewStyle().Width(max(30, m.width-6))
	var lines []string
	for _, msg := range m.messages {
		switch msg.role {
		case "you":
			lines = append(lines, ui.BoldStyle.Render("You")+"  "+wrap.Render(msg.text))
		default:
			lines = append(lines, ui.SuccessStyle.Render("Spool")+"  "+wrap.Render(ui.Markdown(msg.text)))
		}
	}
	if len(m.events) > 0 {
		lines = append(lines, "")
		for _, e := range m.events {
			if e != "" {
				lines = append(lines, e)
			}
		}
	}
	available := m.height - 8
	if available < 5 {
		available = 5
	}
	joined := strings.Split(strings.Join(lines, "\n"), "\n")
	if len(joined) > available {
		joined = joined[len(joined)-available:]
	}
	b.WriteString(strings.Join(joined, "\n") + "\n\n")

	if m.pending != nil {
		warn := "destructive"
		if !m.pending.destructive {
			warn = "mutating"
		}
		b.WriteString(ui.WarnStyle.Render(fmt.Sprintf("Allow %s (%s tool)? [y/N] ", m.pending.tool, warn)) + "\n")
	} else if m.busy {
		b.WriteString(ui.MutedStyle.Render("working…") + "\n")
	}
	b.WriteString(ui.TitleStyle.Render("› ") + m.input.View() + "\n")
	b.WriteString(ui.KeyHints([][2]string{{"enter", "send"}, {"/help", "commands"}, {"ctrl-c", "quit"}}))
	return b.String()
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

	client, err := llmFor(true)
	if err != nil {
		return fmt.Errorf("`spool chat` requires an LLM provider: %w", err)
	}
	resolved, _ := configSummary()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	m := &chatModel{
		ctx: cctx, cancel: cancel, mgr: mgr, lock: lock,
		width: 100, height: 30,
		provider: resolved.provider, modelName: resolved.model,
	}
	m.input = textinput.New()
	m.input.Placeholder = "ask spool to scan, fix, explain, or run something…"
	m.input.CharLimit = 2000
	m.input.Width = 80
	m.input.Focus()

	opts := agent.Options{
		LLM:         client,
		Budget:      agent.DefaultBudget(),
		AutoApprove: flagYes,
		Allocator:   portprobe.New(),
	}
	if mgr != nil {
		opts.WorkJail, _ = safefs.NewJail(mgr.Dir)
		opts.WorkDir = mgr.Dir
		opts.Compose = composeFor(mgr)
		opts.Healer, _ = healerFor(mgr, lock, client)
		if lock != nil {
			opts.RepoRoot = lock.RepoPath
			opts.Project = projectFor(mgr, lock)
			if lock.RepoPath != "" {
				opts.RepoJail, _ = safefs.NewJail(lock.RepoPath)
			}
		}
	}
	opts.EventHandler = func(ev agent.Event) {
		if m.program != nil {
			m.program.Send(agentEventMsg{ev: ev})
		}
	}
	opts.Confirm = func(tool string, destructive bool, a map[string]any) bool {
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
	ag, err := agent.New(opts)
	if err != nil {
		return err
	}
	m.ag = ag
	defer ag.Close()
	m.messages = append(m.messages, chatMessage{role: "assistant", text: "I can scan repos, infer infrastructure, generate manifests, orchestrate stacks, inspect health, and heal failures. Try `/help` or just describe what you want."})

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(cctx))
	m.program = p
	_, err = p.Run()
	if m.tunnel != nil {
		_ = m.tunnel.Close()
	}
	return err
}

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
