package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/envvar"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var envCmd = &cobra.Command{
	Use:   "env [project] [set KEY=VALUE | unset KEY | list | config]",
	Short: "Show and edit required environment variables",
	Long:  "Show and edit project environment variables. `spool env <project> config` opens an interactive editor for every variable, including adding new ones.",
	Args:  cobra.ArbitraryArgs,
	RunE:  runEnv,
}

func init() {
	rootCmd.AddCommand(envCmd)
}

func runEnv(cmd *cobra.Command, args []string) error {
	project := ""
	action := "list"
	var rest []string
	if len(args) > 0 {
		switch args[0] {
		case "list", "set", "unset", "config":
			action = args[0]
			rest = args[1:]
		default:
			project = args[0]
			if len(args) > 1 {
				action = args[1]
				rest = args[2:]
			}
		}
	}
	mgr, lock, err := resolveProject(project)
	if err != nil {
		return err
	}
	switch action {
	case "list":
		return listEnv(mgr, lock)
	case "config":
		return runEnvPanel(mgr, lock)
	case "set":
		if len(rest) == 0 {
			return fmt.Errorf("usage: spool env %s set KEY=VALUE", mgr.ID)
		}
		key, value, perr := parseKV(rest)
		if perr != nil {
			return perr
		}
		return setEnv(mgr, lock, key, value)
	case "unset":
		if len(rest) == 0 {
			return fmt.Errorf("usage: spool env %s unset KEY", mgr.ID)
		}
		return unsetEnv(mgr, lock, rest[0])
	default:
		return fmt.Errorf("unknown env action %q", action)
	}
}

func parseKV(rest []string) (string, string, error) {
	if len(rest) >= 2 && !strings.Contains(rest[0], "=") {
		return rest[0], rest[1], nil
	}
	parts := strings.SplitN(rest[0], "=", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return "", "", fmt.Errorf("expected KEY=VALUE")
	}
	return strings.TrimSpace(parts[0]), parts[1], nil
}

func listEnv(mgr *state.Manager, lock *domain.Lock) error {
	merged, err := renderEnv(mgr, lock)
	if err != nil {
		return err
	}
	overrides, _ := mgr.LoadOverrides()
	sourceByKey := map[string]string{}
	for _, ev := range lock.Env {
		sourceByKey[ev.Key] = string(ev.Source)
	}
	keys := map[string]bool{}
	for _, ev := range lock.Env {
		keys[ev.Key] = true
	}
	for k := range overrides {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	var rows [][]string
	var missingKeys []string
	missing := 0
	for _, k := range sorted {
		if strings.HasSuffix(k, "_") {
			continue
		}
		val := merged[k]
		source := sourceByKey[k]
		if source == "" {
			source = "override"
		}
		if _, ok := overrides[k]; ok {
			source = "override"
		}
		status := ui.SuccessStyle.Render("set")
		if strings.TrimSpace(val) == "" {
			status = ui.WarnStyle.Render("missing")
			missing++
			missingKeys = append(missingKeys, k)
		}
		rows = append(rows, []string{k, maskValue(k, val), status, source})
	}
	logf("%s", ui.Table([]string{"Variable", "Value", "Status", "Source"}, rows, "environment · "+lock.Project))
	logf("%s", ui.MutedStyle.Render("file: "+mgr.EnvPath()))
	if missing > 0 {
		logf("%s", ui.WarningLine(fmt.Sprintf("%d variable(s) missing. Set one with: spool env %s set KEY=VALUE, or run: spool env %s config", missing, mgr.ID, mgr.ID)))
		if isInteractive() && confirm(fmt.Sprintf("Configure %d missing variable(s) now?", missing)) {
			return configureMissingEnv(mgr, lock, missingKeys)
		}
	} else {
		logf("%s", ui.SuccessLine("all discovered variables are set"))
	}
	return nil
}

// configureMissingEnv prompts for each unresolved variable and applies the
// overrides to the project env (and to a running stack).
func configureMissingEnv(mgr *state.Manager, lock *domain.Lock, keys []string) error {
	reader := bufio.NewReader(os.Stdin)
	overrides, _ := mgr.LoadOverrides()
	updated := 0
	for _, key := range keys {
		if strings.HasSuffix(key, "_") {
			continue
		}
		label := key
		if envvar.IsSecretName(key) {
			label += " (secret)"
		}
		fmt.Printf("  %s = ", ui.BoldStyle.Render(label))
		var val string
		if envvar.IsSecretName(key) {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				continue
			}
			val = strings.TrimSpace(string(b))
		} else {
			line, err := reader.ReadString('\n')
			if err != nil && line == "" {
				continue
			}
			val = strings.TrimSpace(line)
		}
		if val == "" {
			continue
		}
		overrides[key] = val
		updated++
	}
	if updated == 0 {
		logf("%s", ui.MutedStyle.Render("no changes"))
		return nil
	}
	if err := mgr.SaveOverrides(overrides); err != nil {
		return err
	}
	if err := writeProjectEnv(mgr, lock); err != nil {
		return err
	}
	applyEnvNow(mgr)
	logf("%s", ui.SuccessLine(fmt.Sprintf("updated %d variable(s)", updated)))
	return nil
}

func setEnv(mgr *state.Manager, lock *domain.Lock, key, value string) error {
	overrides, _ := mgr.LoadOverrides()
	overrides[key] = value
	if err := mgr.SaveOverrides(overrides); err != nil {
		return err
	}
	if err := writeProjectEnv(mgr, lock); err != nil {
		return err
	}
	applyEnvNow(mgr)
	logf("%s", ui.SuccessLine("set "+key+"="+maskValue(key, value)))
	logf("%s", ui.MutedStyle.Render("written to "+mgr.EnvPath()))
	return nil
}

func unsetEnv(mgr *state.Manager, lock *domain.Lock, key string) error {
	overrides, _ := mgr.LoadOverrides()
	overrides[key] = ""
	if err := mgr.SaveOverrides(overrides); err != nil {
		return err
	}
	if err := writeProjectEnv(mgr, lock); err != nil {
		return err
	}
	applyEnvNow(mgr)
	logf("%s", ui.SuccessLine("unset "+key))
	return nil
}

func applyEnvNow(mgr *state.Manager) {
	if !docker.Available() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := checkDaemon(ctx); err != nil {
		return
	}
	ps, err := composeFor(mgr).Ps(ctx)
	if err != nil {
		return
	}
	running := false
	for _, c := range ps {
		if c.State == "running" {
			running = true
			break
		}
	}
	if !running {
		return
	}
	if _, err := composeFor(mgr).Up(ctx, "--force-recreate"); err != nil {
		logf("%s", ui.WarningLine("env written; restart the stack to apply: "+err.Error()))
		return
	}
	logf("%s", ui.SuccessLine("applied to running stack"))
}

func maskValue(key, value string) string {
	if value != "" && (envvar.IsSecretName(key) || looksLikeCredentialURL(value)) {
		return "••••••••"
	}
	if value == "" {
		return "(empty)"
	}
	return value
}

func looksLikeCredentialURL(v string) bool {
	if !strings.Contains(v, "://") {
		return false
	}
	rest := v[strings.Index(v, "://")+3:]
	return strings.Contains(rest, "@")
}
