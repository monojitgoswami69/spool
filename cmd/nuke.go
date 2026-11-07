package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/docker"
	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/git"
	"github.com/monojitgoswami69/spool/internal/safefs"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var (
	nukeKeepVolumes bool
	nukeForce       bool
)

var nukeCmd = &cobra.Command{
	Use:     "nuke <project>",
	Aliases: []string{"incinerate", "remove"},
	Short:   "Destroy a project: containers, volumes, clone, and state",
	Args:    cobra.ExactArgs(1),
	RunE:    runNuke,
}

func init() {
	nukeCmd.Flags().BoolVar(&nukeKeepVolumes, "keep-volumes", false, "preserve named volumes")
	nukeCmd.Flags().BoolVar(&nukeForce, "force", false, "delete files even if Docker cleanup fails")
}

func runNuke(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	mgr, lock, err := resolveProject(args[0])
	if err != nil {
		return err
	}
	logf("%s", ui.WarningLine("This will remove containers, the generated workspace, and (for remote repos) the clone for "+lock.Project))
	if !nukeKeepVolumes {
		logf("%s", ui.ErrorLine("named volumes will also be removed (destructive)"))
	}
	if !confirm("Destroy project " + lock.Project + "?") {
		logf("aborted")
		return nil
	}
	if err := destroyProject(ctx, mgr, lock, nukeKeepVolumes, nukeForce); err != nil {
		return err
	}
	logf("%s", ui.SuccessLine("destroyed "+lock.Project))
	return nil
}

// destroyProject tears down containers and (for remote repos) the clone, then
// deletes state. It refuses to delete files when Docker cleanup fails unless
// force is set, so a failed teardown never leaves orphaned containers.
func destroyProject(ctx context.Context, mgr *state.Manager, lock *domain.Lock, keepVolumes, force bool) error {
	if docker.Available() {
		if err := checkDaemon(ctx); err != nil {
			logf("%s", ui.WarningLine("Docker unavailable: "+err.Error()))
			if !force {
				return fmt.Errorf("docker cleanup failed; refusing to delete files without --force")
			}
		} else if out, derr := composeFor(mgr).Down(ctx, !keepVolumes); derr != nil {
			logf("%s", ui.WarningLine("compose down failed: "+derr.Error()))
			verbosef("%s", out)
			if !force {
				return fmt.Errorf("docker cleanup failed; refusing to delete files without --force")
			}
		}
	} else if !force {
		return fmt.Errorf("docker is not installed; refusing to delete files without --force")
	}

	if lock.RepoURL != "" {
		if parsed, perr := git.ParseURL(lock.RepoURL); perr == nil {
			if reposDir, derr := config.ReposDir(); derr == nil {
				cloneDir := filepath.Join(reposDir, state.IDFor(parsed.Repo, parsed.Clone))
				if rerr := safefs.SafeRemoveAll(cloneDir, reposDir); rerr != nil {
					logf("%s", ui.WarningLine("clone removal skipped: "+rerr.Error()))
				}
			}
		}
	}
	return mgr.Delete()
}
