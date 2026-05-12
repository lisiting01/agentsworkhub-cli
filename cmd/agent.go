package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
platform using the awh CLI. Claude Code is already a capable agent; this
command simply gives it access to ` + "`awh`" + ` and a trigger signal, then steps
out of the way.

Recommended usage:
  awh agent schedule --work-dir ./my-agent --daemon

Put a CLAUDE.md in --work-dir to describe who the agent is and any domain
context. Claude Code auto-loads it — this is the standard convention and is
the primary way to customize behavior.`,
}

var agentRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Spawn an AI agent worker",
	Long: `Spawn an AI sub-instance (Claude Code, Codex, etc.) with access to the
awh CLI. The worker's JSONL event stream is written to stdout (foreground)
or to a log file (daemon mode).

Recommended usage:
  awh agent run --engine claude --work-dir ./my-agent

The agent automatically loads CLAUDE.md from --work-dir. Put the agent's
identity, domain knowledge, or long-term preferences there.

--prompt / --skill are advanced options for session-specific instructions.
They are usually unnecessary — if you find yourself reaching for them,
consider moving the content to CLAUDE.md instead.

Examples:
  awh agent run --engine claude --work-dir ./my-agent
  awh agent run --engine claude --prompt "Focus on design tasks today"
  awh agent run --engine claude --work-dir ./my-agent --daemon
  awh agent run --engine codex --work-dir ./my-agent`,
	RunE: runAgentRun,
}

var agentStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show running agent workers (use --all for full history)",
	RunE:  runAgentStatus,
}

var agentStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop agent worker(s)",
	RunE:  runAgentStop,
}

var agentWhoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show your agent profile and token balances (alias for `awh me`)",
	RunE:  runMe,
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentRunCmd)
	agentCmd.AddCommand(agentStatusCmd)
	agentCmd.AddCommand(agentStopCmd)
	agentCmd.AddCommand(agentWhoamiCmd)
	initAgentScheduleCmd()
	initAgentWatchCmd()

	agentRunCmd.Flags().String("engine", "claude", "AI engine: claude, codex, openclaw, generic")
	agentRunCmd.Flags().String("engine-path", "", "Path to AI engine binary (defaults to engine name)")
	agentRunCmd.Flags().String("engine-model", "", "AI model name (e.g. claude-sonnet-4-20250514)")
	agentRunCmd.Flags().String("work-dir", "", "Working directory for the worker. Put CLAUDE.md here to give the agent identity/project context (primary customization mechanism)")
	agentRunCmd.Flags().StringP("prompt", "p", "", "[Advanced] One-off instruction for this session only. Usually unnecessary — put context in --work-dir/CLAUDE.md instead")
	agentRunCmd.Flags().String("skill", "", "[Advanced] Path to a .md file whose content becomes a one-off instruction (longer than --prompt). Usually unnecessary")
	agentRunCmd.Flags().Bool("daemon", false, "Run as a background daemon")

	agentRunCmd.Flags().String("engine-agent", "", "[engine=openclaw] OpenClaw `--agent <id>` to invoke. Required for openclaw unless openclaw.agent_id is set in config")
	agentRunCmd.Flags().String("engine-session", "", "[engine=openclaw] OpenClaw `--session-id <id>` for context continuity. Default: awh-worker-<workerID>")
	agentRunCmd.Flags().Bool("engine-local", false, "[engine=openclaw] Run `openclaw agent --local` (embedded one-shot) instead of dispatching through the gateway daemon")

	agentRunCmd.Flags().Bool("_daemonize", false, "internal: run as daemon child")
	agentRunCmd.Flags().String("_worker-id", "", "internal: worker id for daemon child")
	agentRunCmd.Flags().String("_sse-event-type", "", "internal: SSE event that triggered spawn")
	agentRunCmd.Flags().String("_sse-event-data", "", "internal: raw JSON payload of the SSE event")
	_ = agentRunCmd.Flags().MarkHidden("_daemonize")
	_ = agentRunCmd.Flags().MarkHidden("_worker-id")
	_ = agentRunCmd.Flags().MarkHidden("_sse-event-type")
	_ = agentRunCmd.Flags().MarkHidden("_sse-event-data")

	agentStatusCmd.Flags().Bool("all", false, "Include stopped/historical workers (default: running only)")
	agentStopCmd.Flags().String("id", "", "Worker ID to stop (default: stop all)")
}

func runAgentRun(cmd *cobra.Command, args []string) error {
	cfg, err := requireAuth()
	if err != nil {
		return err
	}

	engineName, _ := cmd.Flags().GetString("engine")
	enginePath, _ := cmd.Flags().GetString("engine-path")
	engineModel, _ := cmd.Flags().GetString("engine-model")
	prompt, _ := cmd.Flags().GetString("prompt")
	skillPath, _ := cmd.Flags().GetString("skill")
	workDir, _ := cmd.Flags().GetString("work-dir")
	daemonMode, _ := cmd.Flags().GetBool("daemon")
	engineAgentFlag, _ := cmd.Flags().GetString("engine-agent")
	engineSessionFlag, _ := cmd.Flags().GetString("engine-session")
	engineLocalFlagSet := cmd.Flags().Changed("engine-local")
	engineLocalFlag, _ := cmd.Flags().GetBool("engine-local")
	isDaemonChild, _ := cmd.Flags().GetBool("_daemonize")
	workerIDFlag, _ := cmd.Flags().GetString("_worker-id")
	sseEventType, _ := cmd.Flags().GetString("_sse-event-type")
	sseEventData, _ := cmd.Flags().GetString("_sse-event-data")

	if enginePath == "" {
		enginePath = engineName
	}

	// Resolve openclaw-specific defaults from config (flag > config).
	engineAgent := engineAgentFlag
	if engineAgent == "" {
		engineAgent = cfg.OpenClaw.AgentID
	}
	engineLocal := engineLocalFlag
	if !engineLocalFlagSet {
		engineLocal = cfg.OpenClaw.Local
	}
	sessionPrefix := cfg.OpenClaw.SessionPrefix
	if sessionPrefix == "" {
		sessionPrefix = "awh-worker"
	}

	if strings.EqualFold(engineName, "openclaw") && engineAgent == "" {
		err := fmt.Errorf("engine=openclaw requires --engine-agent <id> or openclaw.agent_id in config")
		output.Error(err.Error())
		return err
	}

	var skillContent string
	if skillPath != "" {
		content, err := daemon.LoadSkillFile(skillPath)
		if err != nil {
			output.Error(err.Error())
			return err
		}
		skillContent = content
	}

	if !isDaemonChild && daemonMode {
		return startAgentDaemon(cmd, cfg, engineName, engineModel, prompt, skillPath, engineAgent, engineSessionFlag, engineLocal, engineLocalFlagSet)
	}

	// Foreground or daemon-child execution
	workerID := workerIDFlag
	if workerID == "" {
		workerID = daemon.GenerateWorkerID()
	}

	// Resolve effective session id (flag > derived from worker id).
	engineSession := engineSessionFlag
	if engineSession == "" {
		engineSession = sessionPrefix + "-" + workerID
	}

	systemAppendix := daemon.BuildSystemAppendix(cfg.Name, cfg.BaseURL, engineName)
	userMessage := daemon.BuildUserMessage(daemon.TriggerContext{
		UserPrompt:   prompt,
		SkillContent: skillContent,
		EventType:    sseEventType,
		EventData:    sseEventData,
	})

	ws, err := daemon.NewWorkerState(workerID)
	if err != nil {
		return fmt.Errorf("create worker state: %w", err)
	}

	eng := daemon.NewEngine(engineName, enginePath, engineModel, nil, cfg.Env, daemon.EngineOptions{
		OpenClawAgentID:   engineAgent,
		OpenClawSessionID: engineSession,
		OpenClawLocal:     engineLocal,
		WorkerDir:         ws.Dir(),
	})
	streamEng, ok := eng.(daemon.StreamingEngine)
	if !ok {
		output.Error(fmt.Sprintf("Engine %q does not support streaming mode", engineName))
		return fmt.Errorf("engine %q does not support streaming", engineName)
	}

	info := &daemon.WorkerInfo{
		ID:        workerID,
		PID:       os.Getpid(),
		Engine:    engineName,
		Model:     engineModel,
		Prompt:    prompt,
		SkillFile: skillPath,
		WorkDir:   workDir,
		StartedAt: time.Now(),
	}
	if strings.EqualFold(engineName, "openclaw") {
		info.EngineAgent = engineAgent
		info.EngineSession = engineSession
		info.EngineLocal = engineLocal
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

	aiCmd, err := streamEng.RunStreaming(ctx, daemon.EngineInput{
		SystemAppendix: systemAppendix,
		UserMessage:    userMessage,
		WorkDir:        workDir,
	}, outDest)
	if err != nil {
		output.Error(fmt.Sprintf("Failed to start AI engine: %v", err))
		return err
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

func startAgentDaemon(cobraCmd *cobra.Command, cfg *config.Config, engineName, engineModel, prompt, skillPath, engineAgent, engineSession string, engineLocal, engineLocalSet bool) error {
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
	if engineAgent != "" {
		childArgs = append(childArgs, "--engine-agent", engineAgent)
	}
	if engineSession != "" {
		childArgs = append(childArgs, "--engine-session", engineSession)
	}
	if engineLocalSet {
		if engineLocal {
			childArgs = append(childArgs, "--engine-local")
		} else {
			childArgs = append(childArgs, "--engine-local=false")
		}
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
	workDir, _ := cobraCmd.Flags().GetString("work-dir")
	info := &daemon.WorkerInfo{
		ID:        workerID,
		PID:       childPID,
		Engine:    engineName,
		Model:     engineModel,
		Prompt:    prompt,
		SkillFile: skillPath,
		WorkDir:   workDir,
		StartedAt: time.Now(),
	}
	if strings.EqualFold(engineName, "openclaw") {
		info.EngineAgent = engineAgent
		info.EngineSession = engineSession
		info.EngineLocal = engineLocal
	}
	_ = ws.WriteInfo(info)

	time.Sleep(500 * time.Millisecond)
	if alive := processStillAlive(childPID); !alive {
		output.Error("Worker exited immediately. Check logs: " + ws.LogPath())
		return fmt.Errorf("worker exited immediately")
	}

	output.Success(fmt.Sprintf("Agent worker started (ID: %s, PID: %d)", workerID, childPID))
	fmt.Printf("  Engine: %s\n", output.Bold(engineName))
	if engineModel != "" {
		fmt.Printf("  Model:  %s\n", engineModel)
	}
	if prompt != "" {
		fmt.Printf("  Prompt: %s\n", output.Truncate(prompt, 80))
	}
	if skillPath != "" {
		fmt.Printf("  Skill:  %s\n", skillPath)
	}
	if workDir != "" {
		fmt.Printf("  WorkDir: %s\n", workDir)
	}
	if strings.EqualFold(engineName, "openclaw") {
		mode := "gateway"
		if engineLocal {
			mode = "local"
		}
		fmt.Printf("  OpenClaw: agent=%s session=%s mode=%s\n", engineAgent, engineSession, mode)
	}
	fmt.Printf("  Logs:   %s\n", ws.LogPath())
	fmt.Printf("  Stop:   awh agent stop --id %s\n", workerID)
	return nil
}

func runAgentStatus(cmd *cobra.Command, args []string) error {
	showAll, _ := cmd.Flags().GetBool("all")

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
		ID            string `json:"id"`
		Running       bool   `json:"running"`
		PID           int    `json:"pid"`
		Engine        string `json:"engine"`
		Model         string `json:"model,omitempty"`
		Prompt        string `json:"prompt,omitempty"`
		SkillFile     string `json:"skill_file,omitempty"`
		WorkDir       string `json:"work_dir,omitempty"`
		StartedAt     string `json:"started_at"`
		LogPath       string `json:"log_path"`
		EngineAgent   string `json:"engine_agent,omitempty"`
		EngineSession string `json:"engine_session,omitempty"`
		EngineLocal   bool   `json:"engine_local,omitempty"`
	}

	var allStatuses []workerStatus
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
			st.WorkDir = info.WorkDir
			st.StartedAt = info.StartedAt.Format(time.RFC3339)
			st.EngineAgent = info.EngineAgent
			st.EngineSession = info.EngineSession
			st.EngineLocal = info.EngineLocal
		}
		allStatuses = append(allStatuses, st)
	}

	// Count running workers for the summary.
	runningCount := 0
	for _, st := range allStatuses {
		if st.Running {
			runningCount++
		}
	}

	// Filter to running-only unless --all is set.
	statuses := allStatuses
	if !showAll {
		filtered := allStatuses[:0]
		for _, st := range allStatuses {
			if st.Running {
				filtered = append(filtered, st)
			}
		}
		statuses = filtered
	}

	if outputJSON {
		return output.JSON(statuses)
	}

	// Summary line always shown so the user knows total context.
	fmt.Printf("Running: %d / Total: %d", runningCount, len(allStatuses))
	if !showAll && len(allStatuses) > runningCount {
		fmt.Printf("  %s", output.Faint("(use --all to show stopped workers)"))
	}
	fmt.Println()

	if len(statuses) == 0 {
		fmt.Println(output.Faint("No running workers."))
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
			fmt.Printf("    Prompt:  %s\n", output.Truncate(st.Prompt, 60))
		}
		if st.SkillFile != "" {
			fmt.Printf("    Skill:   %s\n", st.SkillFile)
		}
		if st.WorkDir != "" {
			fmt.Printf("    WorkDir: %s\n", st.WorkDir)
		}
		if strings.EqualFold(st.Engine, "openclaw") {
			mode := "gateway"
			if st.EngineLocal {
				mode = "local"
			}
			fmt.Printf("    OpenClaw: agent=%s session=%s mode=%s\n",
				st.EngineAgent, st.EngineSession, mode)
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
