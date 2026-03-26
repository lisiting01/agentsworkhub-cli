package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lisiting01/agentsworkhub-cli/internal/api"
	"github.com/lisiting01/agentsworkhub-cli/internal/config"
)

// Daemon is the main long-running agent loop.
type Daemon struct {
	cfg    *config.Config
	client *api.Client
	engine Engine
	state  *State
	logger *log.Logger
}

// New creates a Daemon from the given config and state.
func New(cfg *config.Config, st *State, logWriter io.Writer) *Daemon {
	logger := log.New(logWriter, "", log.LstdFlags)
	eng := NewEngine(cfg.Daemon.Engine, cfg.Daemon.EnginePath, cfg.Daemon.EngineArgs)
	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	return &Daemon{cfg: cfg, client: client, engine: eng, state: st, logger: logger}
}

// Run starts the main daemon loop. It blocks until the context is cancelled or a signal is received.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Write our own PID
	if err := d.state.WritePID(os.Getpid()); err != nil {
		d.logger.Printf("[WARN] could not write PID file: %v", err)
	}
	defer d.state.ClearPID()
	defer d.state.ClearTask()

	d.logf("Daemon started. Agent: %s | Engine: %s | Poll: %ds | AutoAccept: %v",
		d.cfg.Name, d.cfg.Daemon.Engine,
		d.cfg.Daemon.PollIntervalSecs, d.cfg.Daemon.AutoAccept)

	interval := time.Duration(d.cfg.Daemon.PollIntervalSecs) * time.Second

	// Check if there's a leftover task from a previous run
	if prev, _ := d.state.ReadTask(); prev != nil {
		d.logf("Found leftover task %s (phase: %s) -- attempting to recover", prev.JobID, prev.Phase)
		if err := d.recoverTask(ctx, prev); err != nil {
			d.logf("[WARN] Recovery failed: %v -- will look for new tasks", err)
			_ = d.state.ClearTask()
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logf("Daemon stopping (signal received)")
			return nil
		case <-ticker.C:
			if err := d.poll(ctx); err != nil {
				d.logf("[WARN] poll error: %v", err)
			}
		}
	}
}

// poll looks for open tasks and processes one if auto_accept is enabled.
func (d *Daemon) poll(ctx context.Context) error {
	// Don't look for new tasks if we're already handling one
	if task, _ := d.state.ReadTask(); task != nil {
		d.logf("Still working on task %s (phase: %s) -- skipping poll", task.JobID, task.Phase)
		return nil
	}

	d.logf("Polling for open tasks...")
	result, err := d.client.ListJobs("open", "", 1, 20)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}

	if len(result.Jobs) == 0 {
		d.logf("No open tasks found")
		return nil
	}

	job := d.findMatch(result.Jobs)
	if job == nil {
		d.logf("No matching tasks (skills filter: [%s])", strings.Join(d.cfg.Daemon.SkillsFilter, ", "))
		return nil
	}

	if !d.cfg.Daemon.AutoAccept {
		d.logf("Found task %s (%s) -- auto_accept is off, skipping", job.ID, job.Title)
		return nil
	}

	return d.processJob(ctx, job)
}

// findMatch returns the first job that passes the skills filter.
func (d *Daemon) findMatch(jobs []api.Job) *api.Job {
	filter := d.cfg.Daemon.SkillsFilter
	for i := range jobs {
		j := &jobs[i]
		if j.PublisherName == d.cfg.Name {
			continue // don't accept your own tasks
		}
		if len(filter) == 0 {
			return j
		}
		if skillsMatch(j.Skills, filter) {
			return j
		}
	}
	return nil
}

func skillsMatch(jobSkills, filter []string) bool {
	for _, f := range filter {
		fl := strings.ToLower(f)
		for _, js := range jobSkills {
			if strings.ToLower(js) == fl {
				return true
			}
		}
	}
	return false
}

// processJob runs the full pipeline for a single task.
func (d *Daemon) processJob(ctx context.Context, job *api.Job) error {
	d.logf("Accepting task %s: %s", job.ID, job.Title)

	ts := &TaskStatus{JobID: job.ID, JobTitle: job.Title, Phase: "accepting", StartedAt: time.Now()}
	_ = d.state.WriteTask(ts)

	accepted, err := d.client.AcceptJob(job.ID)
	if err != nil {
		_ = d.state.ClearTask()
		return fmt.Errorf("accept job: %w", err)
	}
	d.logf("Accepted task %s", accepted.ID)

	return d.runTask(ctx, accepted, "")
}

// recoverTask attempts to resume work on a task that was interrupted.
func (d *Daemon) recoverTask(ctx context.Context, ts *TaskStatus) error {
	job, err := d.client.GetJob(ts.JobID)
	if err != nil {
		return fmt.Errorf("get job: %w", err)
	}
	switch job.Status {
	case "in_progress", "revision":
		d.logf("Recovering task %s (status: %s)", job.ID, job.Status)
		revNote := ""
		if job.Status == "revision" {
			msgs, _ := d.client.GetMessages(job.ID, 1, 100)
			if msgs != nil {
				revNote = ExtractRevisionNote(msgs.Messages)
			}
		}
		return d.runTask(ctx, job, revNote)
	case "submitted":
		d.logf("Task %s already submitted -- waiting for feedback", job.ID)
		return d.waitForFeedback(ctx, job)
	default:
		return fmt.Errorf("task in terminal state: %s", job.Status)
	}
}

// runTask fetches messages, builds a prompt, runs the AI engine, and submits.
func (d *Daemon) runTask(ctx context.Context, job *api.Job, revisionNote string) error {
	ts := &TaskStatus{JobID: job.ID, JobTitle: job.Title, Phase: "running_ai", StartedAt: time.Now()}
	_ = d.state.WriteTask(ts)

	d.logf("Fetching messages for task %s...", job.ID)
	msgResp, err := d.client.GetMessages(job.ID, 1, 100)
	if err != nil {
		return fmt.Errorf("get messages: %w", err)
	}

	prompt := BuildPrompt(job, msgResp.Messages, revisionNote)
	d.logf("Running AI engine (%s) for task %s...", d.engine.Name(), job.ID)

	timeout := time.Duration(d.cfg.Daemon.TaskTimeoutMins) * time.Minute
	aiCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workDir := d.cfg.Daemon.WorkDir

	result, err := d.engine.Run(aiCtx, prompt, workDir)
	if err != nil {
		_ = d.notifyError(job.ID, fmt.Sprintf("AI engine error: %v", err))
		return fmt.Errorf("engine run: %w", err)
	}

	d.logf("AI engine finished for task %s (%d chars). Submitting...", job.ID, len(result))

	ts.Phase = "submitting"
	_ = d.state.WriteTask(ts)

	_, err = d.client.SubmitJob(job.ID, api.SubmitRequest{Content: result})
	if err != nil {
		return fmt.Errorf("submit job: %w", err)
	}
	d.logf("Submitted task %s", job.ID)

	return d.waitForFeedback(ctx, job)
}

// waitForFeedback polls for publisher response (complete / revision_request / cancel).
func (d *Daemon) waitForFeedback(ctx context.Context, job *api.Job) error {
	ts := &TaskStatus{JobID: job.ID, JobTitle: job.Title, Phase: "waiting_feedback", StartedAt: time.Now()}
	_ = d.state.WriteTask(ts)

	d.logf("Waiting for publisher feedback on task %s...", job.ID)

	ticker := time.NewTicker(time.Duration(d.cfg.Daemon.PollIntervalSecs) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			updated, err := d.client.GetJob(job.ID)
			if err != nil {
				d.logf("[WARN] get job status: %v", err)
				continue
			}
			switch updated.Status {
			case "completed":
				d.logf("Task %s completed -- tokens released!", job.ID)
				_ = d.state.ClearTask()
				return nil

			case "cancelled":
				d.logf("Task %s was cancelled by publisher", job.ID)
				_ = d.state.ClearTask()
				return nil

			case "revision":
				d.logf("Revision requested for task %s -- re-running AI...", job.ID)
				msgResp, err := d.client.GetMessages(job.ID, 1, 100)
				if err != nil {
					return fmt.Errorf("get messages for revision: %w", err)
				}
				revNote := ExtractRevisionNote(msgResp.Messages)

				ts.Phase = "rerunning"
				_ = d.state.WriteTask(ts)

				return d.runTask(ctx, updated, revNote)

			case "submitted":
				d.logf("Still waiting for feedback on task %s...", job.ID)
			}
		}
	}
}

// notifyError posts a message to the task thread with the error.
func (d *Daemon) notifyError(jobID, msg string) error {
	_, err := d.client.SendMessage(jobID, api.SendMessageRequest{
		Type:    "message",
		Content: fmt.Sprintf("[awh-daemon] Error: %s", msg),
	})
	return err
}

func (d *Daemon) logf(format string, args ...any) {
	d.logger.Printf(format, args...)
}
