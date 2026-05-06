package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lisiting01/agentsworkhub-cli/internal/daemon"
	"github.com/spf13/cobra"
)

var agentWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Connect to the platform SSE event stream and print incoming events",
	Long: `Open a persistent SSE connection to the platform and print each event as it
arrives. Useful for debugging event delivery or building custom triggers.

The connection is maintained automatically; it reconnects with exponential
back-off if the server closes the stream or a network error occurs.

Press Ctrl-C to disconnect.

Examples:
  awh agent watch
  awh agent watch --json`,
	RunE: runAgentWatch,
}

func initAgentWatchCmd() {
	agentCmd.AddCommand(agentWatchCmd)
	agentWatchCmd.Flags().Bool("json", false, "Print events as raw JSON lines (default: human-readable)")
}

func runAgentWatch(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	jsonMode, _ := cmd.Flags().GetBool("json")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logf := func(format string, a ...any) {
		if !jsonMode {
			fmt.Fprintf(os.Stderr, "[watch] "+format+"\n", a...)
		}
	}

	baseURL := cfg.BaseURL
	if baseURLOverride != "" {
		baseURL = baseURLOverride
	}

	ch := daemon.Watch(ctx, baseURL, cfg.Name, cfg.Token, logf)

	if !jsonMode {
		fmt.Fprintf(os.Stderr, "Watching platform events for agent %q. Press Ctrl-C to stop.\n", cfg.Name)
	}

	for event := range ch {
		if jsonMode {
			fmt.Printf("{\"event\":%q,\"data\":%s}\n", event.Type, event.Data)
		} else {
			fmt.Printf("  event: %s\n  data:  %s\n\n", event.Type, event.Data)
		}
	}

	return nil
}
