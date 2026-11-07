package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

// envPanelMode is what the panel is showing.
type envPanelMode int

const (
	envModeList   envPanelMode = iota // browsing variables
	envModeEdit                       // editing a value
	envModeNewKey                     // typing a new variable name
)

type envPanelModel struct {
	mgr       *state.Manager
	lock      *domain.Lock
	overrides map[string]string
	values    map[string]string

	keys     []string
	cursor   int
	offset   int
	mode     envPanelMode
	editKey  string // key being edited or created
	input    textinput.Model
	reveal   bool
	errText  string
	status   string
	width    int
	height   int
	changed  int
	quitting bool
}

// envPanelKeys builds the sorted, stable variable list shown by the panel:
// every discovered/locked variable plus every override, skipping the
// auto-generated placeholder keys that end in "_".
func envPanelKeys(lock *domain.Lock, overrides map[string]string) []string {
	seen := map[string]bool{}
	for _, ev := range lock.Env {
		seen[ev.Key] = true
	}
	for k := range overrides {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		if strings.HasSuffix(k, "_") {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// applyEnvEdit persists one variable edit the same way `spool env set` does.
func applyEnvEdit(mgr *state.Manager, lock *domain.Lock, overrides map[string]string, key, value string) error {
	overrides[key] = value
	if err := mgr.SaveOverrides(overrides); err != nil {
		return err
	}
	return writeProjectEnv(mgr, lock)
}

// applyEnvUnset clears one variable the same way `spool env unset` does.
func applyEnvUnset(mgr *state.Manager, lock *domain.Lock, overrides map[string]string, key string) error {
	return applyEnvEdit(mgr, lock, overrides, key, "")
}

func newEnvPanelModel(mgr *state.Manager, lock *domain.Lock) (*envPanelModel, error) {
	values, err := renderEnv(mgr, lock)
	if err != nil {
		return nil, err
	}
	overrides, err := mgr.LoadOverrides()
	if err != nil {
		overrides = map[string]string{}
	}
	input := textinput.New()
	input.Prompt = ""
	input.Width = 42
	m := &envPanelModel{
		mgr: mgr, lock: lock, overrides: overrides, values: values,
		keys:  envPanelKeys(lock, overrides),
		width: 84, height: 26,
	}
	m.input = input
	return m, nil
}

func (m *envPanelModel) currentValue(key string) string {
	if v, ok := m.overrides[key]; ok && v != "" {
		return v
	}
	return m.values[key]
}

func (m *envPanelModel) displayValue(key string) string {
	v := m.currentValue(key)
	if v == "" {
		return ""
	}
	if envvar.IsSecretName(key) && !m.reveal {
		return "••••••••"
	}
	return v
}

func (m *envPanelModel) move(delta int) {
	if len(m.keys) == 0 {
		return
	}
	m.cursor = (m.cursor + delta + len(m.keys)) % len(m.keys)
	m.ensureVisible()
	m.errText = ""
}

func (m *envPanelModel) ensureVisible() {
	maxRows := m.maxRows()
	if maxRows <= 0 {
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+maxRows {
		m.offset = m.cursor - maxRows + 1
	}
}

func (m *envPanelModel) maxRows() int {
	rows := m.height - 10
	if rows < 4 {
		rows = 4
	}
	if rows > len(m.keys) {
		rows = len(m.keys)
	}
	return rows
}

func (m *envPanelModel) beginEdit(key string) {
	m.mode = envModeEdit
	m.editKey = key
	m.input.SetValue(m.currentValue(key))
	m.input.Placeholder = "value"
	m.applyEcho(key)
	m.input.Focus()
}

func (m *envPanelModel) beginNewKey() {
	m.mode = envModeNewKey
	m.editKey = ""
	m.input.SetValue("")
	m.input.Placeholder = "KEY_NAME"
	m.input.EchoMode = textinput.EchoNormal
	m.input.Focus()
}

func (m *envPanelModel) applyEcho(key string) {
	if envvar.IsSecretName(key) && !m.reveal {
		m.input.EchoMode = textinput.EchoPassword
	} else {
		m.input.EchoMode = textinput.EchoNormal
	}
}

func (m *envPanelModel) cancelEdit() {
	m.mode = envModeList
	m.editKey = ""
	m.input.Blur()
	m.errText = ""
}

func (m *envPanelModel) commitNewKey() error {
	key := strings.ToUpper(strings.TrimSpace(m.input.Value()))
	if key == "" {
		m.errText = "variable name is empty"
		return nil
	}
	if !envvar.IsValidEnvName(key) {
		m.errText = fmt.Sprintf("%q is not a valid variable name", key)
		return nil
	}
	for i, k := range m.keys {
		if k == key {
			m.cursor = i
			m.ensureVisible()
			m.beginEdit(key)
			return nil
		}
	}
	m.keys = append(m.keys, key)
	sort.Strings(m.keys)
	for i, k := range m.keys {
		if k == key {
			m.cursor = i
		}
	}
	m.ensureVisible()
	m.beginEdit(key)
	return nil
}

func (m *envPanelModel) commitEdit() error {
	value := m.input.Value()
	if err := applyEnvEdit(m.mgr, m.lock, m.overrides, m.editKey, value); err != nil {
		m.errText = err.Error()
		return nil
	}
	m.values[m.editKey] = value
	m.changed++
	m.status = "saved " + m.editKey + "=" + maskValue(m.editKey, value)
	m.cancelEdit()
	return nil
}

func (m *envPanelModel) unsetSelected() error {
	key := m.keys[m.cursor]
	if err := applyEnvUnset(m.mgr, m.lock, m.overrides, key); err != nil {
		m.errText = err.Error()
		return nil
	}
	m.values[key] = ""
	m.changed++
	m.status = "unset " + key
	return nil
}

func (m *envPanelModel) Init() tea.Cmd { return textinput.Blink }

func (m *envPanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.mode == envModeList {
			switch msg.String() {
			case "esc", "q":
				m.quitting = true
				return m, tea.Quit
			case "up", "k":
				m.move(-1)
				return m, nil
			case "down", "j":
				m.move(1)
				return m, nil
			case "v", "V":
				m.reveal = !m.reveal
				m.status = ""
				return m, nil
			case "n", "N":
				m.beginNewKey()
				return m, textinput.Blink
			case "d", "x":
				if len(m.keys) > 0 {
					if err := m.unsetSelected(); err != nil {
						return m, nil
					}
				}
				return m, nil
			case "enter":
				if len(m.keys) > 0 {
					m.beginEdit(m.keys[m.cursor])
					return m, textinput.Blink
				}
				return m, nil
			}
			return m, nil
		}
		// Edit and new-key modes share the input handling.
		switch msg.String() {
		case "esc":
			m.cancelEdit()
			return m, nil
		case "enter":
			if m.mode == envModeNewKey {
				if err := m.commitNewKey(); err != nil {
					m.errText = err.Error()
				}
				return m, nil
			}
			if err := m.commitEdit(); err != nil {
				m.errText = err.Error()
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *envPanelModel) View() string {
	width := m.width
	if width < 64 {
		width = 64
	}
	if m.height < 1 {
		m.height = 26
	}

	var b strings.Builder
	if m.mode == envModeEdit {
		b.WriteString(ui.TitleStyle.Render("Spool environment") + ui.MutedStyle.Render(" · edit variable"))
		b.WriteString("\n\n")
		label := m.editKey
		if envvar.IsSecretName(label) {
			label += " (secret)"
		}
		b.WriteString(ui.BoldStyle.Render(label) + " = " + m.input.View() + "\n\n")
		b.WriteString(ui.MutedStyle.Render("enter save · esc cancel"))
	} else if m.mode == envModeNewKey {
		b.WriteString(ui.TitleStyle.Render("Spool environment") + ui.MutedStyle.Render(" · new variable"))
		b.WriteString("\n\n")
		b.WriteString(ui.BoldStyle.Render("Name") + " = " + m.input.View() + "\n\n")
		b.WriteString(ui.MutedStyle.Render("enter continue to value · esc cancel"))
	} else {
		b.WriteString(ui.TitleStyle.Render("Spool environment") + ui.MutedStyle.Render(" · "+m.lock.Project))
		b.WriteString("\n")
		b.WriteString(ui.MutedStyle.Render("Saved to " + m.mgr.EnvPath() + " as you edit; overrides win over discovered values."))
		b.WriteString("\n\n")
		if len(m.keys) == 0 {
			b.WriteString(ui.MutedStyle.Render("  no variables discovered — press n to add one") + "\n")
		} else {
			maxRows := m.maxRows()
			end := m.offset + maxRows
			if end > len(m.keys) {
				end = len(m.keys)
			}
			for i := m.offset; i < end; i++ {
				b.WriteString(m.row(i) + "\n")
			}
			if len(m.keys) > maxRows {
				b.WriteString(ui.MutedStyle.Render(fmt.Sprintf("  %d-%d of %d variables", m.offset+1, end, len(m.keys))) + "\n")
			}
		}
		b.WriteString("\n")
		b.WriteString(ui.MutedStyle.Render("↑/↓ select · enter edit · n new · d unset · v reveal secrets · esc done"))
	}

	if m.errText != "" {
		b.WriteString("\n" + ui.ErrorStyle.Render(m.errText))
	} else if m.status != "" {
		b.WriteString("\n" + ui.SuccessStyle.Render(m.status))
	}

	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 2).
		Width(66).
		Render(b.String())
	return lipgloss.Place(width, m.height, lipgloss.Center, lipgloss.Center, panel)
}

func (m *envPanelModel) row(i int) string {
	key := m.keys[i]
	marker := "  "
	if m.mode == envModeList && i == m.cursor {
		marker = ui.TitleStyle.Render("› ")
	} else {
		marker = "  "
	}
	keyStyle := ui.MutedStyle.Width(24)
	if i == m.cursor {
		keyStyle = ui.BoldStyle.Width(24)
	}
	val := m.displayValue(key)
	if val == "" {
		val = ui.WarnStyle.Render("missing")
	}
	source := ""
	if v, ok := m.overrides[key]; ok {
		if v == "" {
			source = ui.MutedStyle.Render(" (cleared)")
		} else {
			source = ui.MutedStyle.Render(" (override)")
		}
	}
	return marker + keyStyle.Render(key) + "= " + val + source
}

// runEnvPanel opens the interactive environment editor for a project.
func runEnvPanel(mgr *state.Manager, lock *domain.Lock) error {
	if !isInteractive() {
		return fmt.Errorf("interactive env needs a terminal; use `spool env %s set KEY=VALUE`", mgr.ID)
	}
	m, err := newEnvPanelModel(mgr, lock)
	if err != nil {
		return err
	}
	program := tea.NewProgram(m, tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return err
	}
	fm, ok := final.(*envPanelModel)
	if !ok {
		return nil
	}
	if fm.changed > 0 {
		applyEnvNow(mgr)
		logf("%s", ui.SuccessLine(fmt.Sprintf("updated %d variable(s) in %s", fm.changed, mgr.EnvPath())))
	} else {
		logf("%s", ui.MutedStyle.Render("no changes"))
	}
	return nil
}
