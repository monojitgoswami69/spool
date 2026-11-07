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

var (
	upBuild    bool
	upRecreate bool
)

var upCmd = &cobra.Command{
	Use:     "up [project]",
	Aliases: []string{"start"},
	Short:   "Start a project's stack and wait for health",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runUp,
}

func init() {
	upCmd.Flags().BoolVar(&upBuild, "build", false, "rebuild images before starting")
	upCmd.Flags().BoolVar(&upRecreate, "recreate", false, "force-recreate containers (applies env changes)")
}

func runUp(cmd *cobra.Command, args []string) error {
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
	if upBuild {
		logf("%s", ui.InfoLine("building images"))
		if out, err := compose.Build(ctx, ""); err != nil {
			return err
		} else {
			verbosef("%s", out)
		}
	}
	if upRecreate {
		if out, uerr := compose.Up(ctx, "--force-recreate"); uerr != nil {
			return uerr
		} else {
			verbosef("%s", out)
		}
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
	logf("%s", ui.SuccessLine("stack is healthy"))
	showStatus(ctx, mgr, lock)
	printURLs(lock)
	return nil
}
