package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/dashboard"
)

var dashCmd = &cobra.Command{
	Use:   "dash [project]",
	Short: "Open the live three-tab dashboard",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runDash,
}

func runDash(cmd *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	mgr, lock, err := resolveProject(name)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return dashboard.Run(ctx, mgr, lock)
}
