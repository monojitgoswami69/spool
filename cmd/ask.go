package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/monojitgoswami69/spool/internal/agent"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var askCmd = &cobra.Command{
	Use:   "ask [project] <request>",
	Short: "Run one intent-aware request and print the result",
	Long: `Interpret a natural-language request, print the structured intent, execute it,
and print the result. Mutating intents require --yes or an interactive confirmation.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAsk,
}

func init() {
	rootCmd.AddCommand(askCmd)
}

func runAsk(cmd *cobra.Command, args []string) error {
	project := ""
	request := strings.Join(args, " ")
	if len(args) >= 2 {
		if _, _, err := resolveProject(args[0]); err == nil {
			project = args[0]
			request = strings.Join(args[1:], " ")
		}
	}
	mgr, lock, err := resolveProject(project)
	if err != nil {
		if errors.Is(err, domain.ErrNoProjects) && strings.Contains(strings.ToLower(request), "onboard") {
			// The agent can onboard a repo from a completely clean slate.
			mgr, lock = nil, nil
		} else if errors.Is(err, domain.ErrNoProjects) {
			return fmt.Errorf("no projects initialized; ask me to `onboard <path-or-url>` or run `spool init`")
		} else {
			return err
		}
	}

	client, _ := llmFor(true)
	ag, err := buildChatAgent(client, mgr, lock, func(ev agent.Event) {
		switch ev.Type {
		case "intent":
			logf("%s", ui.TitleStyle.Render("🎯 "+ev.Message))
		case "tool_start":
			if flagVerbose {
				logf("%s", ui.MutedStyle.Render("⚡ "+ev.Tool))
			}
		case "tool_progress":
			logf("%s", ui.MutedStyle.Render("· "+ev.Message))
		case "permission_denied":
			logf("%s", ui.WarningLine("denied "+ev.Tool))
		}
	}, func(tool string, destructive bool, a map[string]any) bool {
		if flagYes {
			return true
		}
		if !isInteractive() {
			logf("%s", ui.WarningLine("mutating action "+tool+" requires --yes in non-interactive mode"))
			return false
		}
		label := tool
		if destructive {
			label += " (DESTRUCTIVE)"
		}
		return confirm("Run " + label + "?")
	})
	if err != nil {
		return err
	}
	defer ag.Close()

	ictx := intentContext()
	if lock != nil {
		ictx.CurrentProject = lock.Project
	}
	res, err := ag.RunIntent(context.Background(), request, ictx)
	if res != nil && strings.TrimSpace(res.Final) != "" {
		fmt.Println(ui.Markdown(res.Final))
	}
	if err == nil && res != nil && res.State == agent.StateFailed {
		return fmt.Errorf("%s", strings.TrimSpace(res.Final))
	}
	return err
}

// intentContext builds classifier context from all initialized projects.
func intentContext() agent.IntentContext {
	var ictx agent.IntentContext
	managers, err := state.List()
	if err != nil {
		return ictx
	}
	for _, m := range managers {
		lock, err := m.LoadLock()
		if err != nil || lock == nil {
			continue
		}
		info := agent.ProjectInfo{ID: m.ID, Name: lock.Project}
		for _, svc := range lock.Services {
			info.Services = append(info.Services, svc.Name)
		}
		ictx.Projects = append(ictx.Projects, info)
	}
	return ictx
}

func isInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
