// Package ui holds Spool's terminal styling primitives.
package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	primary   = lipgloss.Color("#8B5CF6")
	success   = lipgloss.Color("#10B981")
	warning   = lipgloss.Color("#F59E0B")
	errColor  = lipgloss.Color("#EF4444")
	muted     = lipgloss.Color("#6B7280")
	highlight = lipgloss.Color("#60A5FA")

	noColor bool

	TitleStyle     = lipgloss.NewStyle().Bold(true).Foreground(primary)
	BoldStyle      = lipgloss.NewStyle().Bold(true)
	SuccessStyle   = lipgloss.NewStyle().Foreground(success)
	WarnStyle      = lipgloss.NewStyle().Foreground(warning)
	ErrorStyle     = lipgloss.NewStyle().Foreground(errColor)
	MutedStyle     = lipgloss.NewStyle().Foreground(muted)
	LinkStyle      = lipgloss.NewStyle().Foreground(highlight).Underline(true)
	HighlightStyle = lipgloss.NewStyle().Foreground(highlight).Bold(true)
	BoxStyle       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(primary).Padding(0, 1)
	InputBoxStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(primary).Padding(0, 1)
)

// SetNoColor disables color output.
func SetNoColor() {
	noColor = true
	lipgloss.SetColorProfile(0)
	TitleStyle = lipgloss.NewStyle().Bold(true)
	BoldStyle = lipgloss.NewStyle().Bold(true)
	SuccessStyle = lipgloss.NewStyle()
	WarnStyle = lipgloss.NewStyle()
	ErrorStyle = lipgloss.NewStyle()
	MutedStyle = lipgloss.NewStyle()
	LinkStyle = lipgloss.NewStyle()
	HighlightStyle = lipgloss.NewStyle().Bold(true)
	BoxStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1)
	InputBoxStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1)
}

// Banner renders the application banner.
func Banner(version string) string {
	return TitleStyle.Render("Spool") + MutedStyle.Render("  autonomous infrastructure control plane  "+version)
}

// Header renders a command header.
func Header(command string) string {
	if command == "" {
		return ""
	}
	return MutedStyle.Render("spool "+command) + "\n"
}

// Section renders an uppercase section label.
func Section(title string) string {
	return MutedStyle.Bold(true).Render(strings.ToUpper(title))
}

// Step renders a numbered step.
func Step(n, total int, title string) string {
	return fmt.Sprintf("%s %s", MutedStyle.Render(fmt.Sprintf("[%d/%d]", n, total)), BoldStyle.Render(title))
}

// SuccessLine renders a success message.
func SuccessLine(msg string) string { return SuccessStyle.Render("  ✓  " + msg) }

// ErrorLine renders an error message.
func ErrorLine(msg string) string { return ErrorStyle.Render("  ✗  " + msg) }

// WarningLine renders a warning message.
func WarningLine(msg string) string { return WarnStyle.Render("  ⚠  " + msg) }

// InfoLine renders an informational message.
func InfoLine(msg string) string { return MutedStyle.Render("  ℹ  " + msg) }

// URL renders a URL.
func URL(u string) string { return LinkStyle.Render(u) }

// StatusBadge renders a state/health badge.
func StatusBadge(state, health string) string {
	s := strings.ToLower(strings.TrimSpace(state))
	h := strings.ToLower(strings.TrimSpace(health))
	switch {
	case s == "running" && (h == "healthy" || h == ""):
		return SuccessStyle.Render("● " + firstNonEmpty(h, "running"))
	case s == "running" && h == "starting":
		return WarnStyle.Render("● starting")
	case s == "running" && h == "unhealthy":
		return ErrorStyle.Render("● unhealthy")
	case s == "running":
		return SuccessStyle.Render("● running")
	case s == "exited" || s == "dead":
		return ErrorStyle.Render("○ " + s)
	case s == "created" || s == "restarting":
		return WarnStyle.Render("● " + s)
	case s == "":
		return MutedStyle.Render("○ unknown")
	default:
		return MutedStyle.Render("○ " + s)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Table renders a bordered table.
func Table(headers []string, rows [][]string, title string) string {
	if len(rows) == 0 {
		return MutedStyle.Render("(none)")
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, row := range rows {
		for i := range headers {
			if i < len(row) && lipgloss.Width(row[i]) > widths[i] {
				widths[i] = lipgloss.Width(row[i])
			}
		}
	}
	pad := func(s string, w int) string {
		return s + strings.Repeat(" ", w-lipgloss.Width(s))
	}
	var b strings.Builder
	if title != "" {
		b.WriteString(Section(title) + "\n")
	}
	var head []string
	for i, h := range headers {
		head = append(head, MutedStyle.Bold(true).Render(pad(h, widths[i])))
	}
	b.WriteString(strings.Join(head, "  ") + "\n")
	var sep []string
	for _, w := range widths {
		sep = append(sep, strings.Repeat("─", w))
	}
	b.WriteString(MutedStyle.Render(strings.Join(sep, "  ")) + "\n")
	for _, row := range rows {
		var cells []string
		for i := range headers {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			cells = append(cells, pad(cell, widths[i]))
		}
		b.WriteString(strings.Join(cells, "  ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Markdown applies lightweight terminal markdown emphasis.
func Markdown(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "# "):
			lines[i] = TitleStyle.Render(strings.TrimPrefix(trimmed, "# "))
			continue
		case strings.HasPrefix(trimmed, "## "):
			lines[i] = BoldStyle.Render(strings.TrimPrefix(trimmed, "## "))
			continue
		case strings.HasPrefix(trimmed, "- "), strings.HasPrefix(trimmed, "* "):
			lines[i] = "  • " + trimmed[2:]
		}
		lines[i] = renderInline(lines[i])
	}
	return strings.Join(lines, "\n")
}

func renderInline(s string) string {
	// Bold **text**
	for {
		i := strings.Index(s, "**")
		if i < 0 {
			break
		}
		j := strings.Index(s[i+2:], "**")
		if j < 0 {
			break
		}
		j += i + 2
		s = s[:i] + BoldStyle.Render(s[i+2:j]) + s[j+2:]
	}
	// Inline code `text`
	for {
		i := strings.Index(s, "`")
		if i < 0 {
			break
		}
		j := strings.Index(s[i+1:], "`")
		if j < 0 {
			break
		}
		j += i + 1
		s = s[:i] + MutedStyle.Render(s[i+1:j]) + s[j+1:]
	}
	return s
}

// KeyHints renders a footer of key bindings.
func KeyHints(hints [][2]string) string {
	var parts []string
	for _, h := range hints {
		parts = append(parts, BoldStyle.Render(h[0])+" "+MutedStyle.Render(h[1]))
	}
	return MutedStyle.Render(strings.Join(parts, "  ·  "))
}

// Note renders a boxed note.
func Note(title, body string) string {
	return BoxStyle.Render(BoldStyle.Render(title) + "\n" + body)
}
