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
	eng := NewEngine(cfg.Patrol.Engine, cfg.Patrol.EnginePath, cfg.Patrol.EngineArgs)
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

	d.logf("Patrol started. Agent: %s | Engine: %s | Poll: %ds | AutoBid: %v",
		d.cfg.Name, d.cfg.Patrol.Engine,
		d.cfg.Patrol.PollIntervalSecs, d.cfg.Patrol.AutoAccept)

	interval := time.Duration(d.cfg.Patrol.PollIntervalSecs) * time.Second

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
			d.logf("Patrol stopping (signal received)")
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
	result, err := d.client.ListJobs("open", "", "", 1, 20)
	if err != nil {
		return fmt.Errorf("list jobs: %w", err)
	}

	if len(result.Jobs) == 0 {
		d.logf("No open tasks found")
		return nil
	}

	job := d.findMatch(result.Jobs)
	if job == nil {
		d.logf("No matching tasks (skills filter: [%s])", strings.Join(d.cfg.Patrol.SkillsFilter, ", "))
		return nil
	}

	if !d.cfg.Patrol.AutoAccept {
		d.logf("Found task %s (%s) -- auto_accept is off, skipping", job.ID, job.Title)
		return nil
	}

	return d.processJob(ctx, job)
}

// findMatch returns the first job that passes the skills filter.
func (d *Daemon) findMatch(jobs []api.Job) *api.Job {
	filter := d.cfg.Patrol.SkillsFilter
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

// processJob places a bid and waits for the publisher to select it.
func (d *Daemon) processJob(ctx context.Context, job *api.Job) error {
	d.logf("Placing bid on task %s: %s", job.ID, job.Title)

	ts := &TaskStatus{JobID: job.ID, JobTitle: job.Title, Phase: "bidding", StartedAt: time.Now()}
	_ = d.state.WriteTask(ts)

	bid, err := d.client.PlaceBid(job.ID, d.cfg.Patrol.BidMessage)
	if err != nil {
		_ = d.state.ClearTask()
		return fmt.Errorf("place bid: %w", err)
	}
	d.logf("Bid placed on task %s (bid: %s)", job.ID, bid.ID)

	return d.waitForSelection(ctx, job, bid.ID)
}

// waitForSelection polls until the bid is selected (or rejected/job cancelled).
func (d *Daemon) waitForSelection(ctx context.Context, job *api.Job, bidID string) error {
	d.logf("Waiting for publisher to select bid on task %s...", job.ID)

	ticker := time.NewTicker(time.Duration(d.cfg.Patrol.PollIntervalSecs) * time.Second)
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
			case "open":
				d.logf("Task %s still open, waiting for selection...", job.ID)

			case "in_progress", "active":
				if updated.ExecutorName == d.cfg.Name {
					d.logf("Bid selected! Starting work on task %s", job.ID)
					return d.runTask(ctx, updated, "")
				}
				d.logf("Task %s assigned to another agent (%s) -- moving on", job.ID, updated.ExecutorName)
				_ = d.state.ClearTask()
				return nil

			case "cancelled":
				d.logf("Task %s was cancelled while bidding", job.ID)
				_ = d.state.ClearTask()
				return nil

			default:
				d.logf("Task %s in unexpected status %s while waiting for selection -- clearing", job.ID, updated.Status)
				_ = d.state.ClearTask()
				return nil
			}
		}
	}
}

// recoverTask attempts to resume work on a task that was interrupted.
func (d *Daemon) recoverTask(ctx context.Context, ts *TaskStatus) error {
	job, err := d.client.GetJob(ts.JobID)
	if err != nil {
		return fmt.Errorf("get job: %w", err)
	}

	// If we were in bidding phase, check if we got selected
	if ts.Phase == "bidding" {
		return d.recoverBidding(ctx, job)
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
	case "active":
		d.logf("Recovering recurring task %s (job active)", job.ID)
		return d.recoverRecurringTask(ctx, job)
	case "paused":
		d.logf("Recurring task %s is paused -- clearing state", job.ID)
		_ = d.state.ClearTask()
		return nil
	default:
		return fmt.Errorf("task in terminal state: %s", job.Status)
	}
}

// recoverBidding resumes the bid-waiting flow after a daemon restart.
func (d *Daemon) recoverBidding(ctx context.Context, job *api.Job) error {
	switch job.Status {
	case "open":
		d.logf("Recovering bidding state for task %s -- still open, resuming wait", job.ID)
		return d.waitForSelection(ctx, job, "")
	case "in_progress", "active":
		if job.ExecutorName == d.cfg.Name {
			d.logf("Bid was selected while offline! Starting work on task %s", job.ID)
			return d.runTask(ctx, job, "")
		}
		d.logf("Task %s assigned to another agent -- clearing", job.ID)
		_ = d.state.ClearTask()
		return nil
	default:
		d.logf("Task %s in status %s after bidding -- clearing", job.ID, job.Status)
		_ = d.state.ClearTask()
		return nil
	}
}

// runTask fetches messages, builds a prompt, runs the AI engine, and submits.
// For recurring jobs, submission goes to the current cycle endpoint.
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

	timeout := time.Duration(d.cfg.Patrol.TaskTimeoutMins) * time.Minute
	aiCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workDir := d.cfg.Patrol.WorkDir

	result, err := d.engine.Run(aiCtx, prompt, workDir)
	if err != nil {
		_ = d.notifyError(job.ID, fmt.Sprintf("AI engine error: %v", err))
		return fmt.Errorf("engine run: %w", err)
	}

	d.logf("AI engine finished for task %s (%d chars). Submitting...", job.ID, len(result))

	ts.Phase = "submitting"
	_ = d.state.WriteTask(ts)

	if job.Mode == "recurring" {
		cycle, err := d.client.SubmitCycle(job.ID, api.SubmitRequest{Content: result})
		if err != nil {
			return fmt.Errorf("submit cycle: %w", err)
		}
		d.logf("Submitted cycle #%d for task %s", cycle.CycleNumber, job.ID)
	} else {
		_, err = d.client.SubmitJob(job.ID, api.SubmitRequest{Content: result})
		if err != nil {
			return fmt.Errorf("submit job: %w", err)
		}
		d.logf("Submitted task %s", job.ID)
	}

	return d.waitForFeedback(ctx, job)
}

// waitForFeedback polls for publisher response (complete / revision_request / cancel).
// For recurring jobs it monitors cycle-level state and loops automatically.
func (d *Daemon) waitForFeedback(ctx context.Context, job *api.Job) error {
	ts := &TaskStatus{JobID: job.ID, JobTitle: job.Title, Phase: "waiting_feedback", StartedAt: time.Now()}
	_ = d.state.WriteTask(ts)

	d.logf("Waiting for publisher feedback on task %s...", job.ID)

	ticker := time.NewTicker(time.Duration(d.cfg.Patrol.PollIntervalSecs) * time.Second)
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

			case "paused":
				d.logf("Recurring task %s paused by publisher -- stopping", job.ID)
				_ = d.state.ClearTask()
				return nil

			case "revision":
				// One-off: job-level revision
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

			case "active":
				// Recurring: check current cycle state
				if err := d.handleActiveCycle(ctx, updated, ts); err != nil {
					return err
				}
			}
		}
	}
}

// handleActiveCycle checks the current cycle for a recurring job and acts accordingly.
func (d *Daemon) handleActiveCycle(ctx context.Context, job *api.Job, ts *TaskStatus) error {
	cycle, err := d.client.GetCurrentCycle(job.ID)
	if err != nil {
		d.logf("[WARN] get current cycle for task %s: %v", job.ID, err)
		return nil
	}
	switch cycle.Status {
	case "submitted":
		d.logf("Cycle #%d for task %s submitted, waiting for publisher...", cycle.CycleNumber, job.ID)

	case "revision":
		d.logf("Cycle #%d revision requested for task %s -- re-running AI...", cycle.CycleNumber, job.ID)
		msgResp, err := d.client.GetMessages(job.ID, 1, 100)
		if err != nil {
			return fmt.Errorf("get messages for cycle revision: %w", err)
		}
		revNote := ExtractRevisionNote(msgResp.Messages)
		ts.Phase = "rerunning"
		_ = d.state.WriteTask(ts)
		return d.runTask(ctx, job, revNote)

	case "completed":
		// Cycle was completed, next cycle auto-created — run the next cycle
		d.logf("Cycle #%d completed for task %s -- starting next cycle", cycle.CycleNumber, job.ID)
		return d.runTask(ctx, job, "")

	case "active":
		// New cycle is ready, run it
		d.logf("New cycle #%d ready for task %s -- running AI", cycle.CycleNumber, job.ID)
		return d.runTask(ctx, job, "")
	}
	return nil
}

// recoverRecurringTask resumes work on an interrupted recurring task.
func (d *Daemon) recoverRecurringTask(ctx context.Context, job *api.Job) error {
	cycle, err := d.client.GetCurrentCycle(job.ID)
	if err != nil {
		return fmt.Errorf("get current cycle: %w", err)
	}
	switch cycle.Status {
	case "active":
		d.logf("Recovering cycle #%d for recurring task %s", cycle.CycleNumber, job.ID)
		return d.runTask(ctx, job, "")
	case "submitted":
		d.logf("Cycle #%d already submitted for task %s -- waiting for feedback", cycle.CycleNumber, job.ID)
		return d.waitForFeedback(ctx, job)
	case "revision":
		d.logf("Cycle #%d revision pending for task %s -- re-running AI", cycle.CycleNumber, job.ID)
		msgResp, err := d.client.GetMessages(job.ID, 1, 100)
		if err != nil {
			return fmt.Errorf("get messages for cycle revision: %w", err)
		}
		revNote := ExtractRevisionNote(msgResp.Messages)
		return d.runTask(ctx, job, revNote)
	default:
		return fmt.Errorf("unexpected cycle status: %s", cycle.Status)
	}
}

// notifyError posts a message to the task thread with the error.
func (d *Daemon) notifyError(jobID, msg string) error {
	_, err := d.client.SendMessage(jobID, api.SendMessageRequest{
		Type:    "message",
		Content: fmt.Sprintf("[awh-patrol] Error: %s", msg),
	})
	return err
}

func (d *Daemon) logf(format string, args ...any) {
	d.logger.Printf(format, args...)
}
