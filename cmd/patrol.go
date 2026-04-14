package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/lisiting01/agentsworkhub-cli/internal/config"
	"github.com/lisiting01/agentsworkhub-cli/internal/daemon"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
)

var patrolCmd = &cobra.Command{
	Use:   "patrol",
	Short: "Manage the patrol mode (background agent)",
	Long: `Patrol mode watches AgentsWorkhub and acts autonomously in the background.

Executor role (default): polls for open tasks, auto-bids, runs AI engine, submits.
Publisher role: monitors your published jobs, auto-selects bids, auto-completes submissions.
Reviewer role: monitors your submitted jobs, runs AI engine to review delivery, completes or requests revision.`,
}

var patrolStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start patrol mode (runs in background)",
	RunE:  runPatrolStart,
}

var patrolStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running patrol",
	RunE:  runPatrolStop,
}

var patrolStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show patrol status and current task",
	RunE:  runPatrolStatus,
}

var patrolLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Print the patrol log",
	RunE:  runPatrolLogs,
}

var patrolConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Show current patrol configuration",
	RunE:  runPatrolConfig,
}

var patrolConfigSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set a patrol config value (key=value ...)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runPatrolConfigSet,
}

func init() {
	rootCmd.AddCommand(patrolCmd)
	patrolCmd.AddCommand(patrolStartCmd)
	patrolCmd.AddCommand(patrolStopCmd)
	patrolCmd.AddCommand(patrolStatusCmd)
	patrolCmd.AddCommand(patrolLogsCmd)
	patrolCmd.AddCommand(patrolConfigCmd)
	patrolConfigCmd.AddCommand(patrolConfigSetCmd)

	patrolStartCmd.Flags().String("role", "executor", "Patrol role: executor, publisher, or reviewer")
	patrolStartCmd.Flags().String("engine", "", "AI engine: claude, codex, generic")
	patrolStartCmd.Flags().String("engine-path", "", "Path to AI engine binary")
	patrolStartCmd.Flags().String("engine-model", "", "AI model name (e.g. claude-sonnet-4-20250514)")
	patrolStartCmd.Flags().Bool("auto-bid", true, "Automatically place a bid on matching tasks (executor only)")
	patrolStartCmd.Flags().Int("poll", 0, "Poll interval in seconds (overrides config)")
	patrolStartCmd.Flags().StringSlice("skills", nil, "Only accept tasks with these skills (executor only, comma-separated)")
	patrolStartCmd.Flags().BoolP("foreground", "f", false, "Run in foreground (for debugging)")
	patrolStartCmd.Flags().Bool("_daemonize", false, "internal: run as background child")
	patrolStartCmd.Flags().MarkHidden("_daemonize")

	// Publisher-role flags
	patrolStartCmd.Flags().Bool("auto-select-bid", false, "Auto-select first bid on your open jobs (publisher only)")
	patrolStartCmd.Flags().Bool("auto-complete", false, "Auto-complete submitted jobs (publisher only)")

	patrolLogsCmd.Flags().BoolP("follow", "f", false, "Follow the log (tail -f style)")
	patrolLogsCmd.Flags().Int("lines", 50, "Number of lines to show")
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

func runPatrolStart(cmd *cobra.Command, args []string) error {
	cfg, st, err := loadStateAndConfig()
	if err != nil {
		return err
	}
	if !cfg.IsLoggedIn() {
		output.Error("Not logged in. Run: awh auth register")
		return nil
	}

	role, _ := cmd.Flags().GetString("role")
	if role != "executor" && role != "publisher" && role != "reviewer" {
		output.Error(fmt.Sprintf("Unknown role %q. Use 'executor', 'publisher', or 'reviewer'", role))
		return nil
	}

	if v, _ := cmd.Flags().GetString("engine"); v != "" {
		cfg.Patrol.Engine = v
	}
	if v, _ := cmd.Flags().GetString("engine-path"); v != "" {
		cfg.Patrol.EnginePath = v
	}
	if v, _ := cmd.Flags().GetString("engine-model"); v != "" {
		cfg.Patrol.EngineModel = v
	}
	if cmd.Flags().Changed("auto-bid") {
		v, _ := cmd.Flags().GetBool("auto-bid")
		cfg.Patrol.AutoBid = v
	}
	if v, _ := cmd.Flags().GetInt("poll"); v > 0 {
		cfg.Patrol.PollIntervalSecs = v
	}
	if v, _ := cmd.Flags().GetStringSlice("skills"); len(v) > 0 {
		cfg.Patrol.SkillsFilter = v
	}
	if cmd.Flags().Changed("auto-select-bid") {
		v, _ := cmd.Flags().GetBool("auto-select-bid")
		cfg.Patrol.PublisherAutoSelectBid = v
	}
	if cmd.Flags().Changed("auto-complete") {
		v, _ := cmd.Flags().GetBool("auto-complete")
		cfg.Patrol.PublisherAutoComplete = v
	}

	running, pid, _ := st.IsRunning()
	if running {
		output.Warn(fmt.Sprintf("Patrol already running (PID %d). Stop it first with: awh patrol stop", pid))
		return nil
	}

	isDaemonChild, _ := cmd.Flags().GetBool("_daemonize")
	foreground, _ := cmd.Flags().GetBool("foreground")

	if !isDaemonChild && !foreground {
		return startPatrolBackground(cmd, st, cfg)
	}

	return runPatrolForeground(cfg, st, role, isDaemonChild)
}

// startPatrolBackground re-execs this binary as a detached background process.
func startPatrolBackground(cobraCmd *cobra.Command, st *daemon.State, cfg *config.Config) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	childArgs := buildChildArgs(cobraCmd)

	child := exec.Command(exePath, childArgs...)
	child.Dir = cfg.Patrol.WorkDir

	logFile, err := st.OpenLog()
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	child.Stdout = logFile
	child.Stderr = logFile

	applyDetachAttrs(child)

	if err := child.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start background process: %w", err)
	}

	childPID := child.Process.Pid
	// Release the child so it's not reaped when we exit
	child.Process.Release()
	logFile.Close()

	time.Sleep(500 * time.Millisecond)
	alive, _, _ := st.IsRunning()
	if !alive {
		aliveByPID := processStillAlive(childPID)
		if !aliveByPID {
			output.Error("Patrol process exited immediately. Check logs: " + st.LogPath())
			return nil
		}
	}

	role, _ := cobraCmd.Flags().GetString("role")

	output.Success(fmt.Sprintf("Patrol [%s] started (PID %d)", role, childPID))
	if role == "publisher" {
		fmt.Printf("  AutoSelectBid: %v\n", cfg.Patrol.PublisherAutoSelectBid)
		fmt.Printf("  AutoComplete:  %v\n", cfg.Patrol.PublisherAutoComplete)
	} else {
		fmt.Printf("  Engine: %s (%s)\n", output.Bold(cfg.Patrol.Engine), cfg.Patrol.EnginePath)
		if cfg.Patrol.EngineModel != "" {
			fmt.Printf("  Model:  %s\n", cfg.Patrol.EngineModel)
		}
	}
	if role == "reviewer" && len(cfg.Patrol.SkillsFilter) > 0 {
		fmt.Printf("  Skills filter: %v\n", cfg.Patrol.SkillsFilter)
	}
	fmt.Printf("  Poll:   every %ds\n", cfg.Patrol.PollIntervalSecs)
	fmt.Printf("  Logs:   %s\n", st.LogPath())
	fmt.Printf("  Stop:   awh patrol stop\n")
	return nil
}

// buildChildArgs rebuilds os.Args for the child, appending --_daemonize.
func buildChildArgs(cobraCmd *cobra.Command) []string {
	var args []string
	args = append(args, "patrol", "start", "--_daemonize")

	if v, _ := cobraCmd.Flags().GetString("role"); v != "" && v != "executor" {
		args = append(args, "--role", v)
	}
	if v, _ := cobraCmd.Flags().GetString("engine"); v != "" {
		args = append(args, "--engine", v)
	}
	if v, _ := cobraCmd.Flags().GetString("engine-path"); v != "" {
		args = append(args, "--engine-path", v)
	}
	if v, _ := cobraCmd.Flags().GetString("engine-model"); v != "" {
		args = append(args, "--engine-model", v)
	}
	if cobraCmd.Flags().Changed("auto-bid") {
		v, _ := cobraCmd.Flags().GetBool("auto-bid")
		args = append(args, fmt.Sprintf("--auto-bid=%v", v))
	}
	if v, _ := cobraCmd.Flags().GetInt("poll"); v > 0 {
		args = append(args, "--poll", fmt.Sprintf("%d", v))
	}
	if v, _ := cobraCmd.Flags().GetStringSlice("skills"); len(v) > 0 {
		for _, s := range v {
			args = append(args, "--skills", s)
		}
	}
	if cobraCmd.Flags().Changed("auto-select-bid") {
		v, _ := cobraCmd.Flags().GetBool("auto-select-bid")
		args = append(args, fmt.Sprintf("--auto-select-bid=%v", v))
	}
	if cobraCmd.Flags().Changed("auto-complete") {
		v, _ := cobraCmd.Flags().GetBool("auto-complete")
		args = append(args, fmt.Sprintf("--auto-complete=%v", v))
	}

	if baseURLOverride != "" {
		args = append(args, "--base-url", baseURLOverride)
	}
	return args
}

// processStillAlive does a quick PID alive check without relying on PID file.
func processStillAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return checkProcessAlive(proc)
}

// runPatrolForeground runs the patrol loop in the current process (child or --foreground).
func runPatrolForeground(cfg *config.Config, st *daemon.State, role string, isDaemonChild bool) error {
	logFile, err := st.OpenLog()
	var logWriter io.Writer
	if err != nil {
		if isDaemonChild {
			logWriter = os.Stdout
		} else {
			output.Warn(fmt.Sprintf("Could not open log file: %v -- logging to stdout only", err))
			logWriter = os.Stdout
		}
	} else {
		defer logFile.Close()
		if isDaemonChild {
			logWriter = logFile
		} else {
			logWriter = io.MultiWriter(os.Stdout, logFile)
		}
	}

	if !isDaemonChild {
		fmt.Printf("%s Starting patrol [%s] (PID %d)\n", output.Cyan("[awh]"), role, os.Getpid())
		if role == "publisher" {
			fmt.Printf("  AutoSelectBid: %v\n", cfg.Patrol.PublisherAutoSelectBid)
			fmt.Printf("  AutoComplete:  %v\n", cfg.Patrol.PublisherAutoComplete)
			fmt.Printf("  Strategy:      %s\n", cfg.Patrol.PublisherSelectStrategy)
		} else {
			fmt.Printf("  Engine:     %s (%s)\n", output.Bold(cfg.Patrol.Engine), cfg.Patrol.EnginePath)
			if cfg.Patrol.EngineModel != "" {
				fmt.Printf("  Model:      %s\n", cfg.Patrol.EngineModel)
			}
			if role == "executor" {
				fmt.Printf("  AutoBid:    %v\n", cfg.Patrol.AutoBid)
			}
			if len(cfg.Patrol.SkillsFilter) > 0 {
				fmt.Printf("  Skills filter: %v\n", cfg.Patrol.SkillsFilter)
			}
		}
		fmt.Printf("  Poll:       every %ds\n", cfg.Patrol.PollIntervalSecs)
		fmt.Printf("  Logs:       %s\n\n", st.LogPath())
		fmt.Printf("Press Ctrl+C to stop.\n\n")
	}

	if role == "publisher" {
		pub := daemon.NewPublisher(cfg, st, logWriter)
		return pub.Run(context.Background())
	}

	if role == "reviewer" {
		rev := daemon.NewReviewer(cfg, st, logWriter)
		return rev.Run(context.Background())
	}

	d := daemon.New(cfg, st, logWriter)
	return d.Run(context.Background())
}

func runPatrolStop(cmd *cobra.Command, args []string) error {
	_, st, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	running, pid, err := st.IsRunning()
	if err != nil {
		return err
	}
	if !running {
		output.Warn("Patrol is not running")
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
	output.Success(fmt.Sprintf("Patrol stopped (PID %d)", pid))
	return nil
}

func runPatrolStatus(cmd *cobra.Command, args []string) error {
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
		fmt.Println(output.Yellow("Patrol: not running"))
		if pid != 0 {
			fmt.Printf("  (stale PID file: %d)\n", pid)
			_ = st.ClearPID()
		}
		return nil
	}

	fmt.Printf("Patrol: %s  (PID %d)\n", output.Green("running"), pid)

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

func runPatrolLogs(cmd *cobra.Command, args []string) error {
	_, st, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	follow, _ := cmd.Flags().GetBool("follow")
	lines, _ := cmd.Flags().GetInt("lines")

	data, err := os.ReadFile(st.LogPath())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println(output.Faint("No log file yet. Start patrol first."))
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

func runPatrolConfig(cmd *cobra.Command, args []string) error {
	cfg, _, err := loadStateAndConfig()
	if err != nil {
		return err
	}

	if outputJSON {
		return output.JSON(cfg.Patrol)
	}

	data, _ := json.MarshalIndent(cfg.Patrol, "", "  ")
	fmt.Println(string(data))
	fmt.Printf("\nEdit: %s\n", output.Faint("~/.agentsworkhub/config.json"))
	return nil
}

func runPatrolConfigSet(cmd *cobra.Command, args []string) error {
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
		if err := applyPatrolConfigKey(&cfg.Patrol, parts[0], parts[1]); err != nil {
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

func applyPatrolConfigKey(d *config.PatrolConfig, key, val string) error {
	switch key {
	case "engine":
		d.Engine = val
	case "engine_path":
		d.EnginePath = val
	case "engine_model":
		d.EngineModel = val
	case "auto_bid":
		d.AutoBid = val == "true" || val == "1"
	case "auto_accept": // deprecated alias for auto_bid
		d.AutoBid = val == "true" || val == "1"
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
	case "bid_message":
		d.BidMessage = val
	case "publisher_auto_select_bid":
		d.PublisherAutoSelectBid = val == "true" || val == "1"
	case "publisher_auto_complete":
		d.PublisherAutoComplete = val == "true" || val == "1"
	case "publisher_select_strategy":
		d.PublisherSelectStrategy = val
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
