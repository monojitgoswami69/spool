package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/ui"
)

type doctorCheck struct {
	name   string
	ok     bool
	detail string
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check the local environment and credentials",
	Args:  cobra.NoArgs,
	RunE:  runDoctor,
}

func runDoctor(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	var checks []doctorCheck

	checks = append(checks, pathCheck("git", "git"))
	checks = append(checks, pathCheck("docker", "docker"))
	checks = append(checks, doctorCheck{name: "docker daemon", ok: docker.DaemonRunning(ctx), detail: daemonDetail(ctx)})
	checks = append(checks, doctorCheck{name: "compose v2", ok: composeV2(ctx), detail: composeDetail(ctx)})

	if provider, model, source, err := llmSummary(); err != nil {
		checks = append(checks, doctorCheck{name: "llm provider", ok: false, detail: err.Error()})
	} else {
		checks = append(checks, doctorCheck{name: "llm provider", ok: true, detail: provider + " / " + model + " (" + source + ")"})
	}

	checks = append(checks, pathCheck("cloudflared", "cloudflared"))
	checks = append(checks, pathCheck("ssh (localhost.run fallback)", "ssh"))

	if home, err := config.Home(); err == nil {
		checks = append(checks, doctorCheck{name: "spool home", ok: true, detail: home})
	}

	var rows [][]string
	for _, c := range checks {
		mark := ui.SuccessStyle.Render("✓")
		if !c.ok {
			mark = ui.ErrorStyle.Render("✗")
		}
		rows = append(rows, []string{mark, c.name, c.detail})
	}
	logf("%s", ui.Table([]string{"", "Check", "Detail"}, rows, "environment"))
	if !checks[1].ok || !checks[2].ok {
		logf("%s", ui.WarningLine("Docker is required to run stacks. Install/start Docker, then re-run doctor."))
		return fmt.Errorf("critical environment checks failed: Docker is required")
	}
	return nil
}

func pathCheck(label, bin string) doctorCheck {
	if p, err := exec.LookPath(bin); err == nil {
		return doctorCheck{name: label, ok: true, detail: p}
	}
	return doctorCheck{name: label, ok: false, detail: "not found on PATH"}
}

func composeV2(ctx context.Context) bool {
	return exec.CommandContext(ctx, "docker", "compose", "version").Run() == nil
}

func composeDetail(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "docker", "compose", "version").Output()
	if err != nil {
		return "not available"
	}
	return strings.TrimSpace(string(out))
}

func daemonDetail(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Output()
	if err != nil {
		return "not running"
	}
	return "server " + strings.TrimSpace(string(out))
}

func llmSummary() (provider, model, source string, err error) {
	r, err := config.Resolve()
	if err != nil {
		return "", "", "", err
	}
	return r.Provider, r.Model, r.Source, nil
}
