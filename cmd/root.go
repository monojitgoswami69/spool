// Package cmd implements Spool's command-line interface.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/ui"
)

// Version and BuildTime are injected via -ldflags.
var (
	Version   = "v0.3.0"
	BuildTime = "dev"
)

var (
	flagVerbose bool
	flagQuiet   bool
	flagNoColor bool
	flagYes     bool
)

var rootCmd = &cobra.Command{
	Use:           "spool",
	Short:         "Autonomous infrastructure control plane and self-healing DevOps agent",
	Long:          "Spool analyzes any repository, generates a portable container stack, orchestrates it, verifies health, and heals failures with a bounded AI agent.",
	Version:       Version,
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

// Execute runs the root command.
func Execute() {
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if flagNoColor || os.Getenv("NO_COLOR") != "" {
			ui.SetNoColor()
		}
		config.LoadEnvIntoProcess()
	}
	rootCmd.SetVersionTemplate(fmt.Sprintf("Spool %s (built %s)\n", Version, BuildTime))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.ErrorLine(err.Error()))
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "show detailed output")
	rootCmd.PersistentFlags().BoolVarP(&flagQuiet, "quiet", "q", false, "suppress non-error output")
	rootCmd.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "disable ANSI colors")
	rootCmd.PersistentFlags().BoolVar(&flagYes, "yes", false, "auto-approve destructive actions")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(upCmd)
	rootCmd.AddCommand(downCmd)
	rootCmd.AddCommand(nukeCmd)
	rootCmd.AddCommand(healCmd)
	rootCmd.AddCommand(explainCmd)
	rootCmd.AddCommand(dashCmd)
	rootCmd.AddCommand(tunnelCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(doctorCmd)
}
