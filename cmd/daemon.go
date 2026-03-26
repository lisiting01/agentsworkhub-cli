package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lisiting01/agentsworkhub-cli/internal/config"
	"github.com/lisiting01/agentsworkhub-cli/internal/daemon"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the background agent daemon",
	Long: `The daemon watches AgentsWorkhub for open tasks, automatically accepts
matching ones, runs your local AI engine (Claude Code, Codex, etc.) headlessly
to complete the work, and submits results -- all without touching your main AI session.`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon (runs in foreground; background with your shell tools)",
	RunE:  runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running daemon",
	RunE:  runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status and current task",
	RunE:  runDaemonStatus,
}

var daemonLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Print the daemon log",
	RunE:  runDaemonLogs,
}

var daemonConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show current daemon configuration",
	RunE:  runDaemonConfig,
}

var daemonConfigSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a daemon config value (key=value ...)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runDaemonConfigSet,
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonStatusCmd)
	daemonCmd.AddCommand(daemonLogsCmd)
	daemonCmd.AddCommand(daemonConfigCmd)
	daemonConfigCmd.AddCommand(daemonConfigSetCmd)

	daemonStartCmd.Flags().String("engine", "", "AI engine: claude, codex, generic")
	daemonStartCmd.Flags().String("engine-path", "", "Path to AI engine binary")
	daemonStartCmd.Flags().Bool("auto-accept", true, "Automatically accept matching tasks")
	daemonStartCmd.Flags().Int("poll", 0, "Poll interval in seconds (overrides config)")
	daemonStartCmd.Flags().StringSlice("skills", nil, "Only accept tasks with these skills (comma-separated)")

	daemonLogsCmd.Flags().BoolP("follow", "f", false, "Follow the log (tail -f style)")
	daemonLogsCmd.Flags().Int("lines", 50, "Number of lines to show")
}

func loadStateAndConfig() (*config.Config, *daemon.State, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	if baseURLOverride != "" {
		cfg.BaseURL = baseURLOverride
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, err
	}
	configDir := home + "/.agentsworkhub"
	st := daemon.NewState(configDir)
	return cfg, st, nil
}

func runDaemonStart(cmd *cobra.Command, args []string) error {
	cfg, st, err := loadStateAndConfig()
	if err != nil {
		return err
	}
	if !cfg.IsLoggedIn() {
		output.Error("Not logged in. Run: awh auth register")
		return nil
	}

	if v, _ := cmd.Flags().GetString("engine"); v != "" {
		cfg.Daemon.Engine = v
	}
	if v, _ := cmd.Flags().GetString("engine-path"); v != "" {
		cfg.Daemon.EnginePath = v
	}
	if cmd.Flags().Changed("auto-accept") {
		v, _ := cmd.Flags().GetBool("auto-accept")
		cfg.Daemon.AutoAccept = v
	}
	if v, _ := cmd.Flags().GetInt("poll"); v > 0 {
		cfg.Daemon.PollIntervalSecs = v
	}
	if v, _ := cmd.Flags().GetStringSlice("skills"); len(v) > 0 {
		cfg.Daemon.SkillsFilter = v
	}

	running, pid, _ := st.IsRunning()
	if running {
		output.Warn(fmt.Sprintf("Daemon already running (PID %d). Stop it first with: awh daemon stop", pid))
		return nil
	}

	logFile, err := st.OpenLog()
	var logWriter io.Writer
	if err != nil {
		output.Warn(fmt.Sprintf("Could not open log file: %v -- logging to stdout only", err))
		logWriter = os.Stdout
	} else {
		defer logFile.Close()
		logWriter = io.MultiWriter(os.Stdout, logFile)
	}

	fmt.Printf("%s Starting daemon (PID %d)\n", output.Cyan("[awh]"), os.Getpid())
	fmt.Printf("  Engine:     %s (%s)\n", output.Bold(cfg.Daemon.Engine), cfg.Daemon.EnginePath)
	fmt.Printf("  Poll:       every %ds\n", cfg.Daemon.PollIntervalSecs)
	fmt.Printf("  AutoAccept: %v\n", cfg.Daemon.AutoAccept)
	if len(cfg.Daemon.SkillsFilter) > 0 {
		fmt.Printf("  Skills filter: %v\n", cfg.Daemon.SkillsFilter)
	}
	fmt.Printf("  Logs:       %s\n\n", st.LogPath())
	fmt.Printf("Press Ctrl+C to stop.\n\n")

	d := daemon.New(cfg, st, logWriter)
	return d.Run(context.Background())
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	_, st, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	running, pid, err := st.IsRunning()
	if err != nil {
		return err
	}
	if !running {
		output.Warn("Daemon is not running")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}
	if err := terminateProcess(proc); err != nil {
		return fmt.Errorf("terminate: %w", err)
	}
	_ = st.ClearPID()
	output.Success(fmt.Sprintf("Daemon (PID %d) stopped", pid))
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	_, st, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	running, pid, err := st.IsRunning()
	if err != nil {
		return err
	}

	if outputJSON {
		task, _ := st.ReadTask()
		return output.JSON(map[string]any{
			"running": running,
			"pid":     pid,
			"task":    task,
		})
	}

	if !running {
		fmt.Println(output.Yellow("Daemon: not running"))
		if pid != 0 {
			fmt.Printf("  (stale PID file: %d)\n", pid)
			_ = st.ClearPID()
		}
		return nil
	}

	fmt.Printf("Daemon: %s  (PID %d)\n", output.Green("running"), pid)

	task, err := st.ReadTask()
	if err != nil || task == nil {
		fmt.Println("  Current task: none (polling)")
		return nil
	}

	elapsed := time.Since(task.StartedAt).Round(time.Second)
	output.KeyValue([][2]string{
		{"Current task", output.Bold(task.JobTitle)},
		{"Job ID", task.JobID},
		{"Phase", output.StatusColor(task.Phase)},
		{"Started", task.StartedAt.Format("15:04:05")},
		{"Elapsed", elapsed.String()},
	})
	return nil
}

func runDaemonLogs(cmd *cobra.Command, args []string) error {
	_, st, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	follow, _ := cmd.Flags().GetBool("follow")
	lines, _ := cmd.Flags().GetInt("lines")

	data, err := os.ReadFile(st.LogPath())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println(output.Faint("No log file yet. Start the daemon first."))
			return nil
		}
		return err
	}

	allLines := splitLines(string(data))
	start := 0
	if len(allLines) > lines {
		start = len(allLines) - lines
	}
	for _, l := range allLines[start:] {
		fmt.Println(l)
	}

	if follow {
		fmt.Println(output.Faint("--- following (Ctrl+C to stop) ---"))
		size := int64(len(data))
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			f, err := os.Open(st.LogPath())
			if err != nil {
				continue
			}
			info, _ := f.Stat()
			if info.Size() > size {
				f.Seek(size, io.SeekStart)
				buf := make([]byte, info.Size()-size)
				n, _ := f.Read(buf)
				if n > 0 {
					fmt.Print(string(buf[:n]))
					size = info.Size()
				}
			}
			f.Close()
		}
	}
	return nil
}

func runDaemonConfig(cmd *cobra.Command, args []string) error {
	cfg, _, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	if outputJSON {
		return output.JSON(cfg.Daemon)
	}

	data, _ := json.MarshalIndent(cfg.Daemon, "", "  ")
	fmt.Println(string(data))
	fmt.Printf("\nEdit: %s\n", output.Faint("~/.agentsworkhub/config.json"))
	return nil
}

func runDaemonConfigSet(cmd *cobra.Command, args []string) error {
	cfg, _, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	for _, kv := range args {
		parts := splitKV(kv)
		if len(parts) != 2 {
			output.Warn(fmt.Sprintf("Skipping invalid key=value: %s", kv))
			continue
		}
		if err := applyDaemonConfigKey(&cfg.Daemon, parts[0], parts[1]); err != nil {
			output.Warn(fmt.Sprintf("%s: %v", kv, err))
		} else {
			fmt.Printf("  set %s = %s\n", output.Bold(parts[0]), parts[1])
		}
	}

	if err := config.Save(cfg); err != nil {
		return err
	}
	output.Success("Config saved")
	return nil
}

func applyDaemonConfigKey(d *config.DaemonConfig, key, val string) error {
	switch key {
	case "engine":
		d.Engine = val
	case "engine_path":
		d.EnginePath = val
	case "auto_accept":
		d.AutoAccept = val == "true" || val == "1"
	case "poll_interval_secs":
		n := 0
		fmt.Sscanf(val, "%d", &n)
		if n > 0 {
			d.PollIntervalSecs = n
		}
	case "task_timeout_mins":
		n := 0
		fmt.Sscanf(val, "%d", &n)
		if n > 0 {
			d.TaskTimeoutMins = n
		}
	case "work_dir":
		d.WorkDir = val
	default:
		return fmt.Errorf("unknown config key")
	}
	return nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for _, l := range splitStr(s, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func splitStr(s, sep string) []string {
	var out []string
	for {
		i := indexStr(s, sep)
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i])
		s = s[i+len(sep):]
	}
	return out
}

func indexStr(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func splitKV(s string) []string {
	for i, c := range s {
		if c == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
