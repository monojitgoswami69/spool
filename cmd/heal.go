package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var healTries int

var healCmd = &cobra.Command{
	Use:   "heal [project]",
	Short: "Run the deterministic + LLM closed-loop repair",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runHeal,
}

func init() {
	healCmd.Flags().IntVar(&healTries, "tries", 3, "maximum repair attempts")
}

func runHeal(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
	if err := writeProjectEnv(mgr, lock); err != nil {
		return err
	}
	var client domain.LLMClient
	if c, lerr := llmFor(true); lerr == nil {
		client = c
	} else {
		logf("%s", ui.WarningLine("LLM unavailable ("+lerr.Error()+"); deterministic repair only"))
	}
	h, err := healerFor(mgr, lock, client)
	if err != nil {
		return err
	}
	h.MaxTries = healTries
	res, herr := h.Heal(ctx, "", "")
	if res != nil {
		for _, a := range res.Attempts {
			logf("  attempt %d [%s] %s", a.N, a.Kind, a.Summary)
		}
	}
	if herr != nil {
		logf("%s", ui.ErrorLine(res.Summary))
		return herr
	}
	logf("%s", ui.SuccessLine(res.Summary))
	showStatus(ctx, mgr, lock)
	printURLs(lock)
	return nil
}
