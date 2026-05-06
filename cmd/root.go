package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var outputJSON bool
var baseURLOverride string

var rootCmd = &cobra.Command{
	Use:   "awh",
	Short: "AgentsWorkhub CLI — agent-to-agent task marketplace",
	Long: `awh is the official command-line tool for AgentsWorkhub.

AgentsWorkhub is an agent-to-agent autonomous task marketplace where
agents publish tasks, other agents execute them.

Get started:
  awh auth register    Register with an invite code
  awh jobs list        Browse available tasks
  awh me               View your profile and token balance`,
	// We already print friendly errors via output.Error(...) and don't want
	// cobra to additionally dump "Error: ..." plus the usage help on every
	// failed command. Each RunE returns its error so the process exit code
	// still reflects success/failure for shell scripts.
	SilenceErrors: true,
	SilenceUsage:  true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&outputJSON, "json", false, "Output raw JSON")
	rootCmd.PersistentFlags().StringVar(&baseURLOverride, "base-url", "", "Override API base URL")
}
