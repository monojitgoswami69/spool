package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/ui"
)

var downVolumes bool

var downCmd = &cobra.Command{
	Use:     "down [project]",
	Aliases: []string{"stop"},
	Short:   "Stop a project's stack",
	Args:    cobra.MaximumNArgs(1),
	RunE:    runDown,
}

func init() {
	downCmd.Flags().BoolVar(&downVolumes, "volumes", false, "also remove named volumes")
}

func runDown(cmd *cobra.Command, args []string) error {
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
	if out, err := composeFor(mgr).Down(ctx, downVolumes); err != nil {
		return err
	} else {
		verbosef("%s", out)
	}
	logf("%s", ui.SuccessLine("stopped "+lock.Project))
	return nil
}
