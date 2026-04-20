package daemon

import (
	"context"
	"encoding/json"
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

// ReviewerDaemon is the background loop for the reviewer role.
// It monitors the caller's submitted jobs, runs an AI engine to evaluate
// the delivery against the brief and standards, then completes or requests
// revision automatically.
type ReviewerDaemon struct {
	cfg    *config.Config
	client *api.Client
	engine Engine
	state  *State
	logger *log.Logger
}

// reviewAction is the JSON structure the AI engine must output.
type reviewAction struct {
	Action   string `json:"action"`   // "complete" or "revise"
	Feedback string `json:"feedback"` // only for "revise"
}

// NewReviewer creates a ReviewerDaemon ready to run.
func NewReviewer(cfg *config.Config, st *State, logWriter io.Writer) *ReviewerDaemon {
	logger := log.New(logWriter, "", log.LstdFlags)
	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	engine := NewEngine(cfg.Patrol.Engine, cfg.Patrol.EnginePath, cfg.Patrol.EngineModel, cfg.Patrol.EngineArgs, cfg.Env)
	return &ReviewerDaemon{cfg: cfg, client: client, engine: engine, state: st, logger: logger}
}

// Run starts the reviewer daemon loop. It blocks until context is cancelled
// or a signal is received.
func (d *ReviewerDaemon) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := d.state.WritePID(os.Getpid()); err != nil {
		d.logf("[WARN] could not write PID file: %v", err)
	}
	defer d.state.ClearPID()

	d.logf("Reviewer patrol started. Agent: %s | Engine: %s | Poll: %ds",
		d.cfg.Name,
		d.engine.Name(),
		d.cfg.Patrol.PollIntervalSecs,
	)
	if len(d.cfg.Patrol.SkillsFilter) > 0 {
		d.logf("Skills filter: %v", d.cfg.Patrol.SkillsFilter)
	}

	interval := time.Duration(d.cfg.Patrol.PollIntervalSecs) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logf("Reviewer patrol stopping (signal received)")
			return nil
		case <-ticker.C:
			if err := d.poll(ctx); err != nil {
				d.logf("[WARN] reviewer poll error: %v", err)
			}
		}
	}
}

// poll runs one reviewer polling cycle:
//  1. Find submitted one-off jobs owned by this agent → review each.
//  2. Find active recurring jobs with submitted cycles → review each cycle.
func (d *ReviewerDaemon) poll(ctx context.Context) error {
	if err := d.reviewOneOffJobs(ctx); err != nil {
		d.logf("[WARN] review one-off jobs: %v", err)
	}
	if err := d.reviewRecurringCycles(ctx); err != nil {
		d.logf("[WARN] review recurring cycles: %v", err)
	}
	return nil
}

// reviewOneOffJobs finds submitted one-off jobs published by this agent
// and runs AI review on each.
func (d *ReviewerDaemon) reviewOneOffJobs(ctx context.Context) error {
	d.logf("Checking for submitted one-off jobs to review...")

	result, err := d.client.MyJobs("publisher", "submitted", "oneoff", 1, 50)
	if err != nil {
		return fmt.Errorf("list submitted one-off jobs: %w", err)
	}

	for _, job := range result.Jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.matchesSkillFilter(job.Skills) {
			continue
		}
		if err := d.reviewJob(ctx, &job); err != nil {
			d.logf("[WARN] review job %s (%s): %v", job.ID, job.Title, err)
		}
	}
	return nil
}

// reviewRecurringCycles finds active recurring jobs with a submitted current
// cycle owned by this agent and runs AI review on each.
func (d *ReviewerDaemon) reviewRecurringCycles(ctx context.Context) error {
	d.logf("Checking for recurring jobs with submitted cycles to review...")

	result, err := d.client.MyJobs("publisher", "active", "recurring", 1, 50)
	if err != nil {
		return fmt.Errorf("list active recurring jobs: %w", err)
	}

	for _, job := range result.Jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.matchesSkillFilter(job.Skills) {
			continue
		}

		cycle, err := d.client.GetCurrentCycle(job.ID)
		if err != nil {
			d.logf("[WARN] get current cycle for job %s: %v", job.ID, err)
			continue
		}
		if cycle.Status != "submitted" {
			continue
		}

		if err := d.reviewCycle(ctx, &job, cycle); err != nil {
			d.logf("[WARN] review cycle #%d for job %s (%s): %v", cycle.CycleNumber, job.ID, job.Title, err)
		}
	}
	return nil
}

// reviewJob fetches messages, builds a review prompt, runs the AI engine,
// and either completes the job or requests a revision.
func (d *ReviewerDaemon) reviewJob(ctx context.Context, job *api.Job) error {
	d.logf("Reviewing submitted job %s (%s)...", job.ID, job.Title)

	messages, err := d.fetchMessages(job.ID)
	if err != nil {
		return fmt.Errorf("fetch messages: %w", err)
	}

	prompt := BuildReviewPrompt(job, messages)
	d.logf("Running AI engine (%s) for review of job %s...", d.engine.Name(), job.ID)

	timeout := time.Duration(d.cfg.Patrol.TaskTimeoutMins) * time.Minute
	aiCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rawOutput, err := d.engine.Run(aiCtx, prompt, d.cfg.Patrol.WorkDir)
	if err != nil {
		return fmt.Errorf("engine run: %w", err)
	}

	action, err := parseReviewAction(rawOutput)
	if err != nil {
		return fmt.Errorf("parse engine output: %w", err)
	}

	switch action.Action {
	case "complete":
		d.logf("AI decision: COMPLETE — completing job %s (%s)", job.ID, job.Title)
		if _, err := d.client.CompleteJob(job.ID); err != nil {
			return fmt.Errorf("complete job: %w", err)
		}
		d.logf("Job %s (%s) completed", job.ID, job.Title)

	case "revise":
		d.logf("AI decision: REVISE — requesting revision on job %s (%s)", job.ID, job.Title)
		if _, err := d.client.RequestRevision(job.ID, api.RevisionRequest{Content: action.Feedback}); err != nil {
			return fmt.Errorf("request revision: %w", err)
		}
		d.logf("Revision requested for job %s (%s): %s", job.ID, job.Title, action.Feedback)

	default:
		return fmt.Errorf("unknown action %q from engine", action.Action)
	}
	return nil
}

// reviewCycle fetches messages, builds a review prompt, runs the AI engine,
// and either completes the cycle or requests a cycle revision.
func (d *ReviewerDaemon) reviewCycle(ctx context.Context, job *api.Job, cycle *api.JobCycle) error {
	d.logf("Reviewing cycle #%d for recurring job %s (%s)...", cycle.CycleNumber, job.ID, job.Title)

	messages, err := d.fetchMessages(job.ID)
	if err != nil {
		return fmt.Errorf("fetch messages: %w", err)
	}

	prompt := BuildReviewPrompt(job, messages)
	d.logf("Running AI engine (%s) for review of cycle #%d job %s...", d.engine.Name(), cycle.CycleNumber, job.ID)

	timeout := time.Duration(d.cfg.Patrol.TaskTimeoutMins) * time.Minute
	aiCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rawOutput, err := d.engine.Run(aiCtx, prompt, d.cfg.Patrol.WorkDir)
	if err != nil {
		return fmt.Errorf("engine run: %w", err)
	}

	action, err := parseReviewAction(rawOutput)
	if err != nil {
		return fmt.Errorf("parse engine output: %w", err)
	}

	switch action.Action {
	case "complete":
		d.logf("AI decision: COMPLETE — completing cycle #%d for job %s (%s)", cycle.CycleNumber, job.ID, job.Title)
		if _, err := d.client.CompleteCycle(job.ID); err != nil {
			return fmt.Errorf("complete cycle: %w", err)
		}
		d.logf("Cycle #%d for job %s (%s) completed", cycle.CycleNumber, job.ID, job.Title)

	case "revise":
		d.logf("AI decision: REVISE — requesting revision for cycle #%d job %s (%s)", cycle.CycleNumber, job.ID, job.Title)
		if _, err := d.client.RequestCycleRevision(job.ID, api.RevisionRequest{Content: action.Feedback}); err != nil {
			return fmt.Errorf("request cycle revision: %w", err)
		}
		d.logf("Revision requested for cycle #%d job %s (%s): %s", cycle.CycleNumber, job.ID, job.Title, action.Feedback)

	default:
		return fmt.Errorf("unknown action %q from engine", action.Action)
	}
	return nil
}

// fetchMessages retrieves all messages for a job (up to 100).
func (d *ReviewerDaemon) fetchMessages(jobID string) ([]api.Message, error) {
	result, err := d.client.GetMessages(jobID, 1, 100)
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

// matchesSkillFilter returns true if no filter is set, or if the job's skills
// overlap with the configured SkillsFilter.
func (d *ReviewerDaemon) matchesSkillFilter(jobSkills []string) bool {
	if len(d.cfg.Patrol.SkillsFilter) == 0 {
		return true
	}
	for _, want := range d.cfg.Patrol.SkillsFilter {
		for _, have := range jobSkills {
			if strings.EqualFold(want, have) {
				return true
			}
		}
	}
	return false
}

// parseReviewAction extracts the JSON action from engine output.
// The engine should output a single JSON line anywhere in its response.
func parseReviewAction(output string) (*reviewAction, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var action reviewAction
		if err := json.Unmarshal([]byte(line), &action); err != nil {
			continue
		}
		if action.Action == "complete" || action.Action == "revise" {
			return &action, nil
		}
	}
	return nil, fmt.Errorf("no valid action JSON found in engine output (expected {\"action\":\"complete\"} or {\"action\":\"revise\",\"feedback\":\"...\"})")
}

func (d *ReviewerDaemon) logf(format string, args ...any) {
	d.logger.Printf(format, args...)
}
