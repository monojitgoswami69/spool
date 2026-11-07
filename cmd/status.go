package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var statusCmd = &cobra.Command{
	Use:     "status [project]",
	Aliases: []string{"list"},
	Short:   "Show project or container status",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if len(args) == 0 {
		return listProjects(ctx)
	}
	mgr, lock, err := resolveProject(args[0])
	if err != nil {
		return err
	}
	showStatus(ctx, mgr, lock)
	printURLs(lock)
	return nil
}

func listProjects(ctx context.Context) error {
	managers, err := state.List()
	if err != nil {
		return err
	}
	if len(managers) == 0 {
		logf("%s", ui.MutedStyle.Render("No projects yet. Run `spool init <path-or-url>`."))
		return nil
	}
	canQuery := docker.Available() && docker.DaemonRunning(ctx)
	var rows [][]string
	for _, m := range managers {
		lock, err := m.LoadLock()
		if err != nil {
			continue
		}
		running := "-"
		if canQuery {
			if ps, perr := composeFor(m).Ps(ctx); perr == nil {
				n := 0
				for _, c := range ps {
					if c.State == "running" {
						n++
					}
				}
				running = fmt.Sprintf("%d/%d", n, len(ps))
			}
		}
		rows = append(rows, []string{
			m.ID,
			running,
			fmt.Sprintf("%d", len(lock.Services)),
			fmt.Sprintf("%d", len(lock.Infra)),
			humanTime(lock.LastInit),
		})
	}
	logf("%s", ui.Table([]string{"Project", "Running", "Services", "Infra", "Last init"}, rows, "projects"))
	return nil
}

// showStatus renders a per-service container table.
func showStatus(ctx context.Context, mgr *state.Manager, lock *domain.Lock) {
	ps, err := composeFor(mgr).Ps(ctx)
	if err != nil {
		logf("%s", ui.WarningLine("could not read container status: "+err.Error()))
	}
	byService := map[string]domain.Container{}
	for _, c := range ps {
		byService[c.Service] = c
	}
	var rows [][]string
	for _, svc := range lock.Services {
		c := byService[svc.Name]
		stateText := c.State
		if stateText == "" {
			stateText = "stopped"
		}
		host := ""
		if p, ok := lock.PortMap[svc.Name]; ok {
			host = fmt.Sprintf("%d -> %d", p, svc.Port)
		} else if svc.HostPort > 0 {
			host = fmt.Sprintf("%d -> %d", svc.HostPort, svc.Port)
		}
		rows = append(rows, []string{svc.Name, ui.StatusBadge(stateText, c.Health), string(svc.Framework), host})
	}
	for _, inf := range lock.Infra {
		c := byService[inf.Name]
		stateText := c.State
		if stateText == "" {
			stateText = "stopped"
		}
		mode := "local"
		if inf.Mode == domain.InfraModeExternal {
			mode = "external:" + inf.Provider
		}
		host := ""
		if inf.HostPort > 0 {
			host = fmt.Sprintf("%d -> %d", inf.HostPort, inf.Port)
		}
		rows = append(rows, []string{inf.Name, ui.StatusBadge(stateText, c.Health), mode, host})
	}
	logf("%s", ui.Table([]string{"Service", "Status", "Type", "Host -> Container"}, rows, "status"))
}

func humanTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
