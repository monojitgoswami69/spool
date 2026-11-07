package cmd

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/monojitgoswami69/spool/internal/config"
)

func TestPersistPanelConfigAllowsEnvKey(t *testing.T) {
	t.Setenv("SPOOL_HOME", t.TempDir())
	cfg := config.LoadOptional()
	resolved := &config.Resolved{Provider: "gemini", APIKey: "env-key"}
	if err := persistPanelConfig(cfg, resolved, "gemini", "", "", "https://example.test", "pat-123"); err != nil {
		t.Fatal(err)
	}
	loaded := config.LoadOptional()
	if loaded.LLMProvider != "gemini" || loaded.LLMModel != config.DefaultModel("gemini") {
		t.Fatalf("unexpected config: %+v", loaded)
	}
	if loaded.LLMBaseURL != "https://example.test" || loaded.GitHubPAT != "pat-123" {
		t.Fatalf("unexpected config: %+v", loaded)
	}
}

func TestPersistPanelConfigRequiresKey(t *testing.T) {
	t.Setenv("SPOOL_HOME", t.TempDir())
	cfg := config.LoadOptional()
	resolved := &config.Resolved{Provider: "gemini", APIKey: "env-key"}
	if err := persistPanelConfig(cfg, resolved, "openai", "", "", "", ""); err == nil {
		t.Fatal("expected key requirement for openai")
	}
	if err := persistPanelConfig(cfg, resolved, "nope", "m", "k", "", ""); err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestConfigPanelProviderCycle(t *testing.T) {
	m := newConfigPanelModel(&config.Config{LLMProvider: "gemini", LLMModel: config.DefaultModel("gemini")}, nil)
	m.cycleProvider(1)
	if m.provider != "openai" {
		t.Fatalf("provider = %q", m.provider)
	}
	if got := m.inputs[0].Value(); got != config.DefaultModel("openai") {
		t.Fatalf("model = %q", got)
	}
	if m.View() == "" {
		t.Fatal("empty config panel view")
	}
}

func TestConfigPanelSaveViaUpdate(t *testing.T) {
	t.Setenv("SPOOL_HOME", t.TempDir())
	cfg := config.LoadOptional()
	resolved := &config.Resolved{Provider: "gemini", APIKey: "env-key"}
	m := newConfigPanelModel(cfg, resolved)
	m.provider = "gemini"
	m.inputs[0].SetValue("gemini-2.5-pro")
	m.inputs[1].SetValue("")
	m.focus = cfgFieldSave
	final, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	fm := final.(*configPanelModel)
	if cmd == nil {
		t.Fatal("expected quit command after save")
	}
	if !fm.saved || fm.errText != "" {
		t.Fatalf("saved=%v err=%q", fm.saved, fm.errText)
	}
	loaded := config.LoadOptional()
	if loaded.LLMProvider != "gemini" || loaded.LLMModel != "gemini-2.5-pro" {
		t.Fatalf("unexpected config: %+v", loaded)
	}
}

func TestConfigPanelSaveShowsValidationError(t *testing.T) {
	t.Setenv("SPOOL_HOME", t.TempDir())
	cfg := config.LoadOptional()
	m := newConfigPanelModel(cfg, &config.Resolved{Provider: "gemini", APIKey: "env-key"})
	m.provider = "openai"
	m.inputs[0].SetValue("gpt-4o-mini")
	m.inputs[1].SetValue("")
	m.focus = cfgFieldSave
	final, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	fm := final.(*configPanelModel)
	if fm.saved || fm.errText == "" {
		t.Fatalf("expected validation error, saved=%v err=%q", fm.saved, fm.errText)
	}
	if !strings.Contains(fm.View(), "API key is required") {
		t.Fatalf("error not rendered:\n%s", fm.View())
	}
}
