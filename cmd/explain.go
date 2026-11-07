package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/graph"
	"github.com/monojitgoswami69/spool/internal/healer"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var explainCmd = &cobra.Command{
	Use:   "explain [project]",
	Short: "Render the service dependency DAG, port map, and repair provenance",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runExplain,
}

func runExplain(cmd *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	mgr, lock, err := resolveProject(name)
	if err != nil {
		return err
	}
	project := projectFor(mgr, lock)
	g := graph.Build(project)

	logf("%s", ui.TitleStyle.Render("Spool // "+lock.Project))
	logf("")

	logf("%s", ui.Section("applications"))
	var appRows [][]string
	for _, n := range g.Nodes {
		if n.Kind != graph.NodeApp {
			continue
		}
		appRows = append(appRows, []string{n.ID, n.Framework, fmt.Sprintf("%d", n.Port), fmt.Sprintf("%d", n.HostPort)})
	}
	logf("%s", ui.Table([]string{"Service", "Framework", "Container", "Host"}, appRows, ""))

	logf("%s", ui.Section("infrastructure"))
	var infraRows [][]string
	for _, n := range g.Nodes {
		if n.Kind == graph.NodeApp {
			continue
		}
		mode := "local"
		if n.Kind == graph.NodeExternal {
			mode = "external"
			if n.Provider != "" {
				mode += " · " + n.Provider
			}
		}
		host := "-"
		if n.HostPort > 0 {
			host = fmt.Sprintf("%d", n.HostPort)
		}
		infraRows = append(infraRows, []string{n.ID, n.Label, mode, host})
	}
	logf("%s", ui.Table([]string{"Name", "Type", "Mode", "Host"}, infraRows, ""))

	logf("%s", ui.Section("dependencies"))
	if len(g.Edges) == 0 {
		logf("  (none)")
	} else {
		for _, e := range g.Edges {
			line := fmt.Sprintf("  %s %s %s", e.From, ui.MutedStyle.Render("→"), e.To)
			if e.EnvVar != "" {
				line += ui.MutedStyle.Render("  (" + e.EnvVar + ")")
			}
			logf("%s", line)
		}
	}
	logf("")
	logf("%s %s", ui.Section("start order"), strings.Join(g.StartOrder(), " → "))

	// Repair provenance.
	prov := healer.NewProvenance(mgr.RepairsPath())
	records := prov.Records()
	if len(records) > 0 {
		logf("")
		logf("%s", ui.Section("repair provenance"))
		var rows [][]string
		for _, r := range records {
			rows = append(rows, []string{r.File, r.Operation, shortHash(r.OriginalHash), shortHash(r.ResultingHash)})
		}
		logf("%s", ui.Table([]string{"File", "Operation", "Before", "After"}, rows, ""))
	}

	// Required configuration: only variables still unresolved after merging.
	merged, _ := renderEnv(mgr, lock)
	var envRows [][]string
	for _, ev := range lock.Env {
		val := merged[ev.Key]
		if val != "" && !isPlaceholderValue(val) {
			continue
		}
		class := "configuration"
		if envvar.IsSecretName(ev.Key) {
			class = "secret"
		}
		envRows = append(envRows, []string{ev.Key, class, string(ev.Source)})
	}
	sort.Slice(envRows, func(i, j int) bool { return envRows[i][0] < envRows[j][0] })
	if len(envRows) > 0 {
		logf("")
		logf("%s", ui.Section("configuration required"))
		logf("%s", ui.Table([]string{"Variable", "Class", "Source"}, envRows, ""))
		logf("%s", ui.InfoLine("edit with: spool dash "+mgr.ID))
	}
	return nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func isPlaceholderValue(v string) bool {
	lv := strings.ToLower(strings.TrimSpace(v))
	for _, m := range []string{"your_", "your-", "<", "change-me", "changeme", "placeholder", "dummy", "todo"} {
		if strings.Contains(lv, m) {
			return true
		}
	}
	return lv == ""
}
