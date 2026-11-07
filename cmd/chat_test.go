package cmd

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"

	"github.com/monojitgoswami69/spool/internal/domain"
)

func newTestChatModel() *chatModel {
	m := &chatModel{
		width: 100, height: 30,
		lock:     &domain.Lock{Project: "demo"},
		provider: "gemini", modelName: "gemini-3.5-flash-lite",
		messages: []chatMessage{
			{role: "you", text: "why did the build fail?"},
			{role: "assistant", text: "**Diagnosis** — the app imports `node:sqlite` and needs Node 24."},
		},
	}
	m.input = textinput.New()
	m.input.Prompt = "› "
	m.input.Focus()
	m.spin = spinner.New()
	m.spin.Spinner = spinner.Pulse
	return m
}

func TestChatViewModernLayout(t *testing.T) {
	m := newTestChatModel()
	out := m.View()
	for _, want := range []string{"◆ Spool", "demo", "gemini/gemini-3.5-flash-lite", "› You", "Diagnosis", "enter send"} {
		if !strings.Contains(out, want) {
			t.Errorf("chat view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "╭") && !strings.Contains(out, "┌") {
		t.Errorf("expected a rounded input box:\n%s", out)
	}
	t.Logf("\n%s", out)
}

func TestChatViewNarrowNoPanic(t *testing.T) {
	m := newTestChatModel()
	m.width, m.height = 18, 8
	if out := m.View(); out == "" {
		t.Fatal("empty view on narrow terminal")
	}
}

func TestChatViewBusyAndConfirmation(t *testing.T) {
	m := newTestChatModel()
	m.busy = true
	if out := m.View(); !strings.Contains(out, "working") {
		t.Errorf("expected working indicator:\n%s", out)
	}
	m.busy = false
	m.pending = &confirmRequest{tool: "orchestrate_stack", destructive: true, resp: make(chan bool, 1)}
	out := m.View()
	if !strings.Contains(out, "DESTRUCTIVE") || !strings.Contains(out, "y/N") {
		t.Errorf("expected confirmation prompt:\n%s", out)
	}
}
