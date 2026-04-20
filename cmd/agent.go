package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/lisiting01/agentsworkhub-cli/internal/config"
	"github.com/lisiting01/agentsworkhub-cli/internal/daemon"
	"github.com/lisiting01/agentsworkhub-cli/internal/output"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage AI agent workers (spawn, monitor, stop)",
	Long: `Spawn an AI sub-instance that autonomously operates on the AgentsWorkhub
platform using awh CLI commands. The sub-instance receives your platform
credentials and a comprehensive command reference via its system prompt.

This is the recommended way for an AI agent to run background AWH work
without blocking its main conversation.`,
}

var agentRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Spawn an AI agent worker",
	Long: `Spawn an AI sub-instance (Claude Code, Codex, etc.) with platform context
injected. The worker's JSONL event stream is written to stdout (foreground)
or to a log file (daemon mode).

Examples:
  awh agent run --engine claude --prompt "Find open tasks and complete them"
  awh agent run --engine claude --skill ./my-executor-skill.md
  awh agent run --engine claude --engine-model claude-sonnet-4-20250514 --daemon
  awh agent run --engine codex --prompt "Review submitted tasks"`,
	RunE: runAgentRun,
}

var agentStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show running agent workers",
	RunE:  runAgentStatus,
}

var agentStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop agent worker(s)",
	RunE:  runAgentStop,
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentRunCmd)
	agentCmd.AddCommand(agentStatusCmd)
	agentCmd.AddCommand(agentStopCmd)
	initAgentScheduleCmd()

	agentRunCmd.Flags().String("engine", "claude", "AI engine: claude, codex, generic")
	agentRunCmd.Flags().String("engine-path", "", "Path to AI engine binary (defaults to engine name)")
	agentRunCmd.Flags().String("engine-model", "", "AI model name (e.g. claude-sonnet-4-20250514)")
	agentRunCmd.Flags().StringP("prompt", "p", "", "Mission prompt for the worker")
	agentRunCmd.Flags().String("skill", "", "Path to a skill file (.md) to inject as the mission")
	agentRunCmd.Flags().String("work-dir", "", "Working directory for the AI sub-instance")
	agentRunCmd.Flags().Bool("daemon", false, "Run as a background daemon")

	agentRunCmd.Flags().Bool("_daemonize", false, "internal: run as daemon child")
	agentRunCmd.Flags().String("_worker-id", "", "internal: worker id for daemon child")
	_ = agentRunCmd.Flags().MarkHidden("_daemonize")
	_ = agentRunCmd.Flags().MarkHidden("_worker-id")

	agentStopCmd.Flags().String("id", "", "Worker ID to stop (default: stop all)")
}

func runAgentRun(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return nil
	}

	engineName, _ := cmd.Flags().GetString("engine")
	enginePath, _ := cmd.Flags().GetString("engine-path")
	engineModel, _ := cmd.Flags().GetString("engine-model")
	prompt, _ := cmd.Flags().GetString("prompt")
	skillPath, _ := cmd.Flags().GetString("skill")
	workDir, _ := cmd.Flags().GetString("work-dir")
	daemonMode, _ := cmd.Flags().GetBool("daemon")
	isDaemonChild, _ := cmd.Flags().GetBool("_daemonize")
	workerIDFlag, _ := cmd.Flags().GetString("_worker-id")

	if enginePath == "" {
		enginePath = engineName
	}

	if prompt == "" && skillPath == "" {
		output.Error("Provide --prompt or --skill to give the worker a mission")
		return nil
	}

	var skillContent string
	if skillPath != "" {
		content, err := daemon.LoadSkillFile(skillPath)
		if err != nil {
			output.Error(err.Error())
			return nil
		}
		skillContent = content
	}

	systemPrompt := daemon.BuildAgentSystemPrompt(cfg.Name, cfg.BaseURL, prompt, skillContent)

	eng := daemon.NewEngine(engineName, enginePath, engineModel, nil, cfg.Env)
	streamEng, ok := eng.(daemon.StreamingEngine)
	if !ok {
		output.Error(fmt.Sprintf("Engine %q does not support streaming mode", engineName))
		return nil
	}

	if !isDaemonChild && daemonMode {
		return startAgentDaemon(cmd, cfg, engineName, engineModel, prompt, skillPath)
	}

	// Foreground or daemon-child execution
	workerID := workerIDFlag
	if workerID == "" {
		workerID = daemon.GenerateWorkerID()
	}

	ws, err := daemon.NewWorkerState(workerID)
	if err != nil {
		return fmt.Errorf("create worker state: %w", err)
	}

	info := &daemon.WorkerInfo{
		ID:        workerID,
		PID:       os.Getpid(),
		Engine:    engineName,
		Model:     engineModel,
		Prompt:    prompt,
		SkillFile: skillPath,
		StartedAt: time.Now(),
	}
	if err := ws.WriteInfo(info); err != nil {
		return fmt.Errorf("write worker info: %w", err)
	}
	if err := ws.WritePID(os.Getpid()); err != nil {
		return fmt.Errorf("write worker pid: %w", err)
	}
	defer ws.ClearPID()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var outWriter *os.File
	if isDaemonChild {
		lf, err := ws.OpenLog()
		if err != nil {
			return fmt.Errorf("open worker log: %w", err)
		}
		defer lf.Close()
		outWriter = lf
	}

	var outDest = os.Stdout
	if outWriter != nil {
		outDest = outWriter
	}

	aiCmd, err := streamEng.RunStreaming(ctx, systemPrompt, workDir, outDest)
	if err != nil {
		output.Error(fmt.Sprintf("Failed to start AI engine: %v", err))
		return nil
	}

	info.PID = os.Getpid()
	_ = ws.WriteInfo(info)

	waitErr := aiCmd.Wait()
	if waitErr != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("AI engine exited with error: %w", waitErr)
	}
	return nil
}

func startAgentDaemon(cobraCmd *cobra.Command, cfg *config.Config, engineName, engineModel, prompt, skillPath string) error {
	workerID := daemon.GenerateWorkerID()

	ws, err := daemon.NewWorkerState(workerID)
	if err != nil {
		return fmt.Errorf("create worker state: %w", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	childArgs := []string{"agent", "run", "--_daemonize", "--_worker-id", workerID}
	childArgs = append(childArgs, "--engine", engineName)
	if v, _ := cobraCmd.Flags().GetString("engine-path"); v != "" {
		childArgs = append(childArgs, "--engine-path", v)
	}
	if engineModel != "" {
		childArgs = append(childArgs, "--engine-model", engineModel)
	}
	if prompt != "" {
		childArgs = append(childArgs, "--prompt", prompt)
	}
	if skillPath != "" {
		childArgs = append(childArgs, "--skill", skillPath)
	}
	if v, _ := cobraCmd.Flags().GetString("work-dir"); v != "" {
		childArgs = append(childArgs, "--work-dir", v)
	}
	if baseURLOverride != "" {
		childArgs = append(childArgs, "--base-url", baseURLOverride)
	}

	child := exec.Command(exePath, childArgs...)
	applyDetachAttrs(child)

	logFile, err := ws.OpenLog()
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	child.Stdout = logFile
	child.Stderr = logFile

	if err := child.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	childPID := child.Process.Pid
	child.Process.Release()
	logFile.Close()

	if err := ws.WritePID(childPID); err != nil {
		return fmt.Errorf("write pid: %w", err)
	}
	info := &daemon.WorkerInfo{
		ID:        workerID,
		PID:       childPID,
		Engine:    engineName,
		Model:     engineModel,
		Prompt:    prompt,
		SkillFile: skillPath,
		StartedAt: time.Now(),
	}
	_ = ws.WriteInfo(info)

	time.Sleep(500 * time.Millisecond)
	if alive := processStillAlive(childPID); !alive {
		output.Error("Worker exited immediately. Check logs: " + ws.LogPath())
		return nil
	}

	output.Success(fmt.Sprintf("Agent worker started (ID: %s, PID: %d)", workerID, childPID))
	fmt.Printf("  Engine: %s\n", output.Bold(engineName))
	if engineModel != "" {
		fmt.Printf("  Model:  %s\n", engineModel)
	}
	if prompt != "" {
		truncated := prompt
		if len(truncated) > 80 {
			truncated = truncated[:80] + "..."
		}
		fmt.Printf("  Prompt: %s\n", truncated)
	}
	if skillPath != "" {
		fmt.Printf("  Skill:  %s\n", skillPath)
	}
	fmt.Printf("  Logs:   %s\n", ws.LogPath())
	fmt.Printf("  Stop:   awh agent stop --id %s\n", workerID)
	return nil
}

func runAgentStatus(cmd *cobra.Command, args []string) error {
	workers, err := daemon.ListWorkers()
	if err != nil {
		return err
	}

	if len(workers) == 0 {
		if outputJSON {
			fmt.Println("[]")
			return nil
		}
		fmt.Println(output.Faint("No agent workers found."))
		return nil
	}

	type workerStatus struct {
		ID        string `json:"id"`
		Running   bool   `json:"running"`
		PID       int    `json:"pid"`
		Engine    string `json:"engine"`
		Model     string `json:"model,omitempty"`
		Prompt    string `json:"prompt,omitempty"`
		SkillFile string `json:"skill_file,omitempty"`
		StartedAt string `json:"started_at"`
		LogPath   string `json:"log_path"`
	}

	var statuses []workerStatus
	for _, ws := range workers {
		info, _ := ws.ReadInfo()
		running, pid, _ := ws.IsRunning()
		if info == nil && !running {
			continue
		}
		st := workerStatus{
			Running: running,
			PID:     pid,
			LogPath: ws.LogPath(),
		}
		if info != nil {
			st.ID = info.ID
			st.Engine = info.Engine
			st.Model = info.Model
			st.Prompt = info.Prompt
			st.SkillFile = info.SkillFile
			st.StartedAt = info.StartedAt.Format(time.RFC3339)
		}
		statuses = append(statuses, st)
	}

	if outputJSON {
		return output.JSON(statuses)
	}

	if len(statuses) == 0 {
		fmt.Println(output.Faint("No agent workers found."))
		return nil
	}

	for _, st := range statuses {
		status := output.Green("running")
		if !st.Running {
			status = output.Yellow("stopped")
		}
		fmt.Printf("  [%s] %s  (PID %d)\n", st.ID, status, st.PID)
		fmt.Printf("    Engine:  %s\n", st.Engine)
		if st.Model != "" {
			fmt.Printf("    Model:   %s\n", st.Model)
		}
		if st.Prompt != "" {
			truncated := st.Prompt
			if len(truncated) > 60 {
				truncated = truncated[:60] + "..."
			}
			fmt.Printf("    Prompt:  %s\n", truncated)
		}
		if st.SkillFile != "" {
			fmt.Printf("    Skill:   %s\n", st.SkillFile)
		}
		fmt.Printf("    Started: %s\n", st.StartedAt)
		fmt.Printf("    Logs:    %s\n", st.LogPath)
		fmt.Println()
	}
	return nil
}

func runAgentStop(cmd *cobra.Command, args []string) error {
	targetID, _ := cmd.Flags().GetString("id")

	workers, err := daemon.ListWorkers()
	if err != nil {
		return err
	}

	if len(workers) == 0 {
		output.Warn("No agent workers found")
		return nil
	}

	stopped := 0
	for _, ws := range workers {
		info, _ := ws.ReadInfo()
		id := ""
		if info != nil {
			id = info.ID
		}

		if targetID != "" && id != targetID {
			continue
		}

		running, pid, _ := ws.IsRunning()
		if !running {
			if targetID != "" {
				output.Warn(fmt.Sprintf("Worker %s is not running (stale state)", id))
			}
			_ = ws.Cleanup()
			continue
		}

		proc, err := os.FindProcess(pid)
		if err != nil {
			output.Warn(fmt.Sprintf("Worker %s: find process %d: %v", id, pid, err))
			continue
		}
		if err := terminateProcess(proc); err != nil {
			output.Warn(fmt.Sprintf("Worker %s: terminate PID %d: %v", id, pid, err))
			continue
		}
		_ = ws.ClearPID()
		output.Success(fmt.Sprintf("Worker %s stopped (PID %d)", id, pid))
		stopped++
	}

	if stopped == 0 && targetID != "" {
		output.Warn(fmt.Sprintf("Worker %s not found or not running", targetID))
	} else if stopped == 0 {
		output.Warn("No running workers to stop")
	}
	return nil
}
