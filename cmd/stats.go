package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var statsCmd = &cobra.Command{
	Use:   "stats [project]",
	Short: "Show live CPU, memory, and network stats",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runStats,
}

func init() {
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	mgr, lock, err := resolveProject(name)
	if err != nil {
		return err
	}
	if err := requireDocker(ctx); err != nil {
		return err
	}
	all, err := composeFor(mgr).Stats(ctx)
	if err != nil {
		return err
	}
	stats := filterStats(all, lock)
	if len(stats) == 0 {
		logf("%s", ui.MutedStyle.Render("No running containers for "+lock.Project+"."))
		return nil
	}
	var rows [][]string
	for _, s := range stats {
		rows = append(rows, []string{
			shortContainerName(s.Name),
			fmt.Sprintf("%.1f%%", s.CPUPerc),
			fmt.Sprintf("%.1f%%", s.MemPerc),
			s.MemUsage,
			s.NetIO,
			s.BlockIO,
			s.PIDs,
		})
	}
	logf("%s", ui.Table([]string{"Container", "CPU", "Mem", "Mem usage", "Net I/O", "Block I/O", "PIDs"}, rows, "stats · "+lock.Project))
	return nil
}

func filterStats(stats []domain.ContainerStat, lock *domain.Lock) []domain.ContainerStat {
	names := map[string]bool{}
	for _, svc := range lock.Services {
		names[strings.ToLower(svc.Name)] = true
	}
	for _, inf := range lock.Infra {
		names[strings.ToLower(inf.Name)] = true
	}
	var out []domain.ContainerStat
	for _, s := range stats {
		name := strings.ToLower(s.Name + " " + s.Container)
		if lock.ProjectID != "" && strings.Contains(name, strings.ToLower(lock.ProjectID)) {
			out = append(out, s)
			continue
		}
		for n := range names {
			if strings.Contains(name, n) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func shortContainerName(name string) string {
	if name == "" {
		return "-"
	}
	parts := strings.Split(name, "-")
	if len(parts) > 3 {
		return strings.Join(parts[len(parts)-2:], "-")
	}
	return name
}
