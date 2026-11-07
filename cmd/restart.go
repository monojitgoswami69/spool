package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/ui"
)

var restartCmd = &cobra.Command{
	Use:     "restart [project]",
	Short:   "Recreate a project's stack and wait for health",
	Aliases: []string{"reboot"},
	Args:    cobra.MaximumNArgs(1),
	RunE:    runRestart,
}

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
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
	printEnvStatus(mgr, lock)
	compose := composeFor(mgr)
	if err := compose.Validate(ctx); err != nil {
		return err
	}
	if out, derr := compose.Down(ctx, false); derr != nil {
		return derr
	} else {
		verbosef("%s", out)
	}
	client, _ := llmFor(true)
	h, err := healerFor(mgr, lock, client)
	if err != nil {
		return err
	}
	if err := h.VerifyRuntime(ctx); err != nil {
		printFailureReport(mgr, lock, nil, err)
		return err
	}
	lock.LastUp = time.Now()
	clearFailureLog(mgr)
	if err := mgr.SaveLock(lock); err != nil {
		return err
	}
	logf("%s", ui.SuccessLine("restarted "+lock.Project))
	showStatus(ctx, mgr, lock)
	printURLs(lock)
	return nil
}
