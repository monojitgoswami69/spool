// Package dashboard implements Spool's three-tab live TUI: logs, env overrides,
// and container controls with stats.
package dashboard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

const (
	tabLogs = iota
	tabEnv
	tabControls
)

var tabNames = []string{"Logs", "Env Overrides", "Controls & Stats"}

type tickMsg time.Time

type dataMsg struct {
	containers []domain.Container
	stats      []domain.ContainerStat
	logs       string
	err        error
}

type actionMsg struct {
	label string
	err   error
}

type editorDoneMsg struct{ err error }

// Model is the dashboard state.
type Model struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mgr     *state.Manager
	lock    *domain.Lock
	compose *docker.Compose

	tab      int
	services []string
	selected int
	reveal   bool

	containers []domain.Container
	stats      []domain.ContainerStat
	logs       string
	logScroll  int
	status     string

	originalEnv map[string]string
	env         map[string]string
	overrides   map[string]string

	width, height int
	program       *tea.Program
	err           error
}

// Run starts the dashboard.
func Run(ctx context.Context, mgr *state.Manager, lock *domain.Lock) error {
	if mgr == nil || lock == nil {
		return fmt.Errorf("dashboard: nil manager or lock")
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()

	env, _ := mgr.LoadEnv()
	overrides, _ := mgr.LoadOverrides()
	m := &Model{
		ctx: cctx, cancel: cancel, mgr: mgr, lock: lock,
		compose:     docker.New(mgr.ComposePath(), mgr.Dir, "spool-"+mgr.ID),
		services:    serviceNames(lock),
		originalEnv: clone(env),
		env:         env,
		overrides:   overrides,
		width:       100, height: 30,
	}
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(cctx))
	m.program = p
	_, err := p.Run()
	return err
}

func serviceNames(lock *domain.Lock) []string {
	var apps, infras []string
	for _, s := range lock.Services {
		apps = append(apps, s.Name)
	}
	for _, i := range lock.Infra {
		infras = append(infras, i.Name)
	}
	sort.Strings(apps)
	sort.Strings(infras)
	return append(apps, infras...)
}

func clone(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.loadCmd(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 6*time.Second)
		defer cancel()
		ps, err := m.compose.Ps(ctx)
		if err != nil {
			return dataMsg{err: err}
		}
		stats, _ := m.compose.Stats(ctx)
		service := ""
		if len(m.services) > 0 {
			service = m.services[m.selected]
		}
		logs, _ := m.compose.Logs(ctx, service, 300)
		return dataMsg{containers: ps, stats: stats, logs: logs}
	}
}

func actionCmd(ctx context.Context, c *docker.Compose, label, action, service string) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		var err error
		switch action {
		case "start":
			_, err = c.Start(cctx, service)
			if err != nil {
				_, err = c.Up(cctx, "--no-deps", service)
			}
		case "stop":
			_, err = c.Stop(cctx, service)
		case "restart":
			_, err = c.Restart(cctx, service)
		case "recreate":
			_, err = c.Recreate(cctx, service)
		}
		return actionMsg{label: label, err: err}
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.loadCmd(), tickCmd())
	case dataMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = ui.ErrorLine(msg.err.Error())
		} else {
			m.err = nil
			m.containers = msg.containers
			m.stats = msg.stats
			m.logs = msg.logs
		}
		return m, nil
	case actionMsg:
		if msg.err != nil {
			m.status = ui.ErrorLine(msg.label + ": " + msg.err.Error())
		} else {
			m.status = ui.SuccessLine(msg.label)
		}
		return m, m.loadCmd()
	case editorDoneMsg:
		m.env, _ = m.mgr.LoadEnv()
		m.originalEnv = clone(m.env)
		m.overrides = map[string]string{}
		m.status = ui.SuccessLine("env saved")
		return m, m.loadCmd()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		m.cancel()
		return m, tea.Quit
	case "tab", "right", "l":
		m.tab = (m.tab + 1) % len(tabNames)
		return m, m.loadCmd()
	case "shift+tab", "left", "h":
		m.tab = (m.tab + len(tabNames) - 1) % len(tabNames)
		return m, m.loadCmd()
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
		return m, m.loadCmd()
	case "down", "j":
		if m.selected < len(m.services)-1 {
			m.selected++
		}
		return m, m.loadCmd()
	case "pgup":
		m.logScroll += 10
		return m, nil
	case "pgdown", "g":
		m.logScroll -= 10
		if m.logScroll < 0 {
			m.logScroll = 0
		}
		return m, nil
	case "v":
		m.reveal = !m.reveal
		return m, nil
	}

	service := ""
	if len(m.services) > 0 {
		service = m.services[m.selected]
	}
	if service == "" {
		return m, nil
	}
	switch msg.String() {
	case "s":
		return m, actionCmd(m.ctx, m.compose, "started "+service, "start", service)
	case "x":
		return m, actionCmd(m.ctx, m.compose, "stopped "+service, "stop", service)
	case "r":
		return m, actionCmd(m.ctx, m.compose, "restarted "+service, "restart", service)
	case "b":
		return m, actionCmd(m.ctx, m.compose, "recreated "+service, "recreate", service)
	case "e":
		return m, m.editEnvCmd()
	}
	return m, nil
}

func (m *Model) editEnvCmd() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	path := m.mgr.EnvPath()
	cmd := exec.Command(editor, path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		_ = m.persistOverrides()
		return editorDoneMsg{err: err}
	})
}

func (m *Model) persistOverrides() error {
	env, err := m.mgr.LoadEnv()
	if err != nil {
		return err
	}
	overrides := map[string]string{}
	for k, v := range env {
		if m.originalEnv[k] != v {
			overrides[k] = v
		}
	}
	for k := range m.originalEnv {
		if _, ok := env[k]; !ok {
			overrides[k] = ""
		}
	}
	m.overrides = overrides
	return m.mgr.SaveOverrides(overrides)
}

// View implements tea.Model.
func (m *Model) View() string {
	var b strings.Builder
	b.WriteString(ui.Banner("v0.1.0") + "\n")
	b.WriteString(m.header() + "\n\n")
	switch m.tab {
	case tabLogs:
		b.WriteString(m.viewLogs())
	case tabEnv:
		b.WriteString(m.viewEnv())
	case tabControls:
		b.WriteString(m.viewControls())
	}
	b.WriteString("\n")
	if m.status != "" {
		b.WriteString(m.status + "\n")
	}
	b.WriteString(m.footer())
	return b.String()
}

func (m *Model) header() string {
	var tabs []string
	for i, name := range tabNames {
		if i == m.tab {
			tabs = append(tabs, ui.TitleStyle.Render("["+name+"]"))
		} else {
			tabs = append(tabs, ui.MutedStyle.Render(" "+name+" "))
		}
	}
	overall := m.overall()
	return fmt.Sprintf("%s  %s  %s", ui.BoldStyle.Render(m.lock.Project), strings.Join(tabs, " "), overall)
}

func (m *Model) overall() string {
	if len(m.containers) == 0 {
		return ui.MutedStyle.Render("○ stopped")
	}
	for _, c := range m.containers {
		if c.State != "running" {
			return ui.ErrorStyle.Render("● degraded")
		}
		if strings.EqualFold(c.Health, "unhealthy") {
			return ui.ErrorStyle.Render("● unhealthy")
		}
	}
	return ui.SuccessStyle.Render("● running")
}

func (m *Model) viewLogs() string {
	service := ""
	if len(m.services) > 0 {
		service = m.services[m.selected]
	}
	header := ui.Section("logs") + "  " + ui.BoldStyle.Render(service)
	lines := strings.Split(strings.TrimRight(m.logs, "\n"), "\n")
	available := m.height - 9
	if available < 5 {
		available = 5
	}
	start := len(lines) - available - m.logScroll
	if start < 0 {
		start = 0
	}
	end := start + available
	if end > len(lines) {
		end = len(lines)
	}
	return header + "\n" + strings.Join(lines[start:end], "\n")
}

func (m *Model) viewEnv() string {
	keys := make([]string, 0, len(m.env))
	for k := range m.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var rows [][]string
	for _, k := range keys {
		val := m.env[k]
		if !m.reveal && envvar.IsSecretName(k) && val != "" {
			val = "••••••••"
		}
		marker := ""
		if _, ok := m.overrides[k]; ok {
			marker = "*"
		}
		rows = append(rows, []string{k, val, marker})
	}
	note := ui.MutedStyle.Render("e edit .env   v reveal secrets   (* = override)")
	return ui.Table([]string{"Key", "Value", ""}, rows, "environment") + "\n" + note
}

func (m *Model) viewControls() string {
	statsByName := map[string]domain.ContainerStat{}
	for _, s := range m.stats {
		statsByName[s.Name] = s
	}
	containerByService := map[string]domain.Container{}
	for _, c := range m.containers {
		containerByService[c.Service] = c
	}
	var rows [][]string
	for i, name := range m.services {
		marker := " "
		if i == m.selected {
			marker = "›"
		}
		c := containerByService[name]
		state := c.State
		health := c.Health
		if state == "" {
			state = "stopped"
		}
		host := ""
		if p, ok := m.lock.PortMap[name]; ok {
			host = fmt.Sprintf(":%d", p)
		}
		cpu, mem := "-", "-"
		for sname, st := range statsByName {
			if strings.Contains(sname, name) {
				cpu = fmt.Sprintf("%.1f%%", st.CPUPerc)
				mem = fmt.Sprintf("%.1f%%", st.MemPerc)
			}
		}
		rows = append(rows, []string{marker, name, ui.StatusBadge(state, health), host, cpu, mem})
	}
	table := ui.Table([]string{"", "Service", "Status", "Host", "CPU", "Mem"}, rows, "containers")
	hints := ui.MutedStyle.Render("s start   x stop   r restart   b recreate   ↑/↓ select")
	return table + "\n" + hints
}

func (m *Model) footer() string {
	return ui.KeyHints([][2]string{
		{"tab", "switch tab"}, {"↑/↓", "select"}, {"s/x/r/b", "start/stop/restart/recreate"},
		{"e", "edit env"}, {"v", "reveal"}, {"q", "quit"},
	})
}
