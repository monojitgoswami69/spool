package cmd

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var configProviders = []string{"gemini", "openai", "anthropic", "groq", "ollama"}

const (
	cfgFieldProvider = iota
	cfgFieldModel
	cfgFieldAPIKey
	cfgFieldBaseURL
	cfgFieldPAT
	cfgFieldSave
	cfgFieldCount
)

type configPanelModel struct {
	cfg     *config.Config
	resolve *config.Resolved

	provider string
	inputs   [4]textinput.Model
	focus    int
	reveal   bool
	status   string
	errText  string
	width    int
	height   int
	saved    bool
}

func newConfigPanelModel(cfg *config.Config, resolved *config.Resolved) *configPanelModel {
	provider := config.NormalizeProvider(firstNonEmptyStr(cfg.LLMProvider, resolvedProvider(resolved), "gemini"))
	model := firstNonEmptyStr(cfg.LLMModel, resolvedModel(resolved), config.DefaultModel(provider))
	apiKey := cfg.LLMAPIKey
	baseURL := firstNonEmptyStr(cfg.LLMBaseURL, resolvedBase(resolved))
	pat := cfg.GitHubPAT

	m := &configPanelModel{
		cfg: cfg, resolve: resolved, provider: provider,
		width: 84, height: 26,
	}
	for i := range m.inputs {
		m.inputs[i] = textinput.New()
		m.inputs[i].Prompt = ""
		m.inputs[i].Width = 46
	}
	m.inputs[0].SetValue(model)
	m.inputs[0].Placeholder = "model id"
	m.inputs[1].SetValue(apiKey)
	m.inputs[1].EchoMode = textinput.EchoPassword
	m.inputs[2].SetValue(baseURL)
	m.inputs[2].Placeholder = "optional base URL"
	m.inputs[3].SetValue(pat)
	m.inputs[3].EchoMode = textinput.EchoPassword
	m.inputs[3].Placeholder = "optional GitHub PAT"
	m.updatePlaceholders()
	m.focusField()
	return m
}

func resolvedProvider(r *config.Resolved) string {
	if r == nil {
		return ""
	}
	return r.Provider
}
func resolvedModel(r *config.Resolved) string {
	if r == nil {
		return ""
	}
	return r.Model
}
func resolvedBase(r *config.Resolved) string {
	if r == nil {
		return ""
	}
	return r.BaseURL
}

func providerEnvKey(provider string) string {
	switch config.NormalizeProvider(provider) {
	case "gemini":
		return "GEMINI_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	default:
		return ""
	}
}

func (m *configPanelModel) updatePlaceholders() {
	if config.NormalizeProvider(m.provider) == "ollama" {
		m.inputs[1].Placeholder = "not required for ollama"
		return
	}
	if m.inputs[1].Value() == "" && m.resolve != nil &&
		config.NormalizeProvider(m.resolve.Provider) == config.NormalizeProvider(m.provider) &&
		m.resolve.APIKey != "" {
		m.inputs[1].Placeholder = "using " + providerEnvKey(m.provider) + " from env/.env"
		return
	}
	m.inputs[1].Placeholder = "required"
}

func (m *configPanelModel) focusField() {
	for i := range m.inputs {
		if m.focus == i+1 {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
}

func (m *configPanelModel) move(delta int) {
	m.focus = (m.focus + delta + cfgFieldCount) % cfgFieldCount
	m.errText = ""
	m.focusField()
}

func (m *configPanelModel) cycleProvider(delta int) {
	idx := 0
	for i, p := range configProviders {
		if p == config.NormalizeProvider(m.provider) {
			idx = i
			break
		}
	}
	old := configProviders[idx]
	idx = (idx + delta + len(configProviders)) % len(configProviders)
	m.provider = configProviders[idx]
	cur := strings.TrimSpace(m.inputs[0].Value())
	if cur == "" || cur == config.DefaultModel(old) {
		m.inputs[0].SetValue(config.DefaultModel(m.provider))
	}
	m.updatePlaceholders()
	m.errText = ""
}

func (m *configPanelModel) toggleReveal() {
	m.reveal = !m.reveal
	mode := textinput.EchoPassword
	if m.reveal {
		mode = textinput.EchoNormal
	}
	m.inputs[1].EchoMode = mode
	m.inputs[3].EchoMode = mode
}

func (m *configPanelModel) save() error {
	if err := persistPanelConfig(m.cfg, m.resolve, m.provider,
		m.inputs[0].Value(), m.inputs[1].Value(), m.inputs[2].Value(), m.inputs[3].Value()); err != nil {
		return err
	}
	m.saved = true
	return nil
}

// persistPanelConfig validates and writes the interactive config values.
func persistPanelConfig(cfg *config.Config, resolved *config.Resolved, provider, model, apiKey, baseURL, pat string) error {
	provider = config.NormalizeProvider(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	apiKey = strings.TrimSpace(apiKey)
	baseURL = strings.TrimSpace(baseURL)
	pat = strings.TrimSpace(pat)

	valid := false
	for _, p := range configProviders {
		if p == provider {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("unknown provider %q", provider)
	}
	if model == "" {
		model = config.DefaultModel(provider)
	}
	if model == "" {
		return fmt.Errorf("a model is required")
	}
	if provider != "ollama" && apiKey == "" {
		// Allow an env/.env key to remain the source of truth.
		if resolved == nil || config.NormalizeProvider(resolved.Provider) != provider || resolved.APIKey == "" {
			return fmt.Errorf("an API key is required for %s (or set %s in env/.env)", provider, providerEnvKey(provider))
		}
	}
	cfg.LLMProvider = provider
	cfg.LLMModel = model
	cfg.LLMAPIKey = apiKey
	cfg.LLMBaseURL = baseURL
	cfg.GitHubPAT = pat
	return config.Save(cfg)
}

func (m *configPanelModel) Init() tea.Cmd { return textinput.Blink }

func (m *configPanelModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}
		if m.focus == cfgFieldProvider {
			switch msg.String() {
			case "left", "h":
				m.cycleProvider(-1)
				return m, nil
			case "right", "l":
				m.cycleProvider(1)
				return m, nil
			}
		}
		if (m.focus == cfgFieldAPIKey || m.focus == cfgFieldPAT) && (msg.String() == "v" || msg.String() == "V") {
			m.toggleReveal()
			return m, nil
		}
		switch msg.String() {
		case "up", "shift+tab":
			m.move(-1)
			return m, nil
		case "down", "tab":
			m.move(1)
			return m, nil
		case "enter":
			if m.focus == cfgFieldSave {
				if err := m.save(); err != nil {
					m.errText = err.Error()
					return m, nil
				}
				return m, tea.Quit
			}
			m.move(1)
			return m, nil
		}
		if m.focus >= cfgFieldModel && m.focus <= cfgFieldPAT {
			var cmd tea.Cmd
			m.inputs[m.focus-1], cmd = m.inputs[m.focus-1].Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *configPanelModel) View() string {
	width := m.width
	if width < 64 {
		width = 64
	}
	contentWidth := 66

	header := ui.TitleStyle.Render("Spool configuration")
	note := ui.MutedStyle.Render("Saved to ~/.spool/config.json. OS env / .env values still take precedence.")

	rows := []string{
		m.row(cfgFieldProvider, "Provider", m.providerValue()),
		m.row(cfgFieldModel, "Model", m.inputs[0].View()),
		m.row(cfgFieldAPIKey, "API key", m.inputs[1].View()),
		m.row(cfgFieldBaseURL, "Base URL", m.inputs[2].View()),
		m.row(cfgFieldPAT, "GitHub PAT", m.inputs[3].View()),
		"",
		m.row(cfgFieldSave, "", m.saveValue()),
	}

	var b strings.Builder
	b.WriteString(header + "\n")
	b.WriteString(note + "\n\n")
	b.WriteString(strings.Join(rows, "\n"))
	b.WriteString("\n\n")
	b.WriteString(ui.MutedStyle.Render("↑/↓ move · ←/→ provider · enter next/save · v reveal secrets · esc cancel"))
	if m.errText != "" {
		b.WriteString("\n" + ui.ErrorStyle.Render(m.errText))
	} else if m.status != "" {
		b.WriteString("\n" + ui.SuccessStyle.Render(m.status))
	}

	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 2).
		Width(contentWidth).
		Render(b.String())
	return lipgloss.Place(width, m.height, lipgloss.Center, lipgloss.Center, panel)
}

func (m *configPanelModel) row(field int, label, value string) string {
	marker := "  "
	if m.focus == field {
		marker = ui.TitleStyle.Render("› ")
	}
	labelStyle := ui.MutedStyle.Width(13)
	if m.focus == field {
		labelStyle = ui.BoldStyle.Width(13)
	}
	return marker + labelStyle.Render(label) + value
}

func (m *configPanelModel) providerValue() string {
	return ui.MutedStyle.Render("‹ ") + ui.BoldStyle.Render(m.provider) + ui.MutedStyle.Render(" ›")
}

func (m *configPanelModel) saveValue() string {
	label := "  Save configuration  "
	if m.focus == cfgFieldSave {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("62")).
			Foreground(lipgloss.Color("230")).
			Padding(0, 1).
			Render(strings.TrimSpace(label))
	}
	return ui.MutedStyle.Render(strings.TrimSpace(label))
}

// runConfigPanel opens the interactive configuration panel.
func runConfigPanel() error {
	if !isInteractive() {
		return fmt.Errorf("interactive config needs a terminal; use `spool config set KEY VALUE`")
	}
	cfg := config.LoadOptional()
	resolved, _ := config.Resolve()
	m := newConfigPanelModel(cfg, resolved)
	program := tea.NewProgram(m, tea.WithAltScreen())
	final, err := program.Run()
	if err != nil {
		return err
	}
	fm, ok := final.(*configPanelModel)
	if !ok || !fm.saved {
		return nil
	}
	path, _ := config.ConfigPath()
	logf("%s", ui.SuccessLine("saved "+path))
	logf("%s", ui.InfoLine("verify with: spool doctor"))
	return nil
}
