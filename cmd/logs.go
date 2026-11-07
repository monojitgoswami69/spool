package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	logFollow bool
	logTail   int
)

var logsCmd = &cobra.Command{
	Use:   "logs [project] [service]",
	Short: "Show or stream container logs",
	Args:  cobra.MaximumNArgs(2),
	RunE:  runLogs,
}

func init() {
	logsCmd.Flags().BoolVarP(&logFollow, "follow", "f", false, "stream logs")
	logsCmd.Flags().IntVarP(&logTail, "tail", "n", 200, "number of lines to show")
}

func runLogs(cmd *cobra.Command, args []string) error {
	name := ""
	service := ""
	if len(args) > 0 {
		name = args[0]
	}
	if len(args) > 1 {
		service = args[1]
	}
	mgr, lock, err := resolveProject(name)
	if err != nil {
		return err
	}
	_ = lock
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	compose := composeFor(mgr)
	if logFollow {
		return compose.Follow(ctx, service, logTail, os.Stdout)
	}
	out, err := compose.Logs(ctx, service, logTail)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}
