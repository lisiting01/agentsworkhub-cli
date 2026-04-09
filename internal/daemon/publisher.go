package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lisiting01/agentsworkhub-cli/internal/api"
	"github.com/lisiting01/agentsworkhub-cli/internal/config"
)

// PublisherDaemon is the background loop for the publisher role.
// It monitors the caller's published jobs and automates bid selection
// and submission review so the publisher can operate unattended.
type PublisherDaemon struct {
	cfg    *config.Config
	client *api.Client
	state  *State
	logger *log.Logger
}

// NewPublisher creates a PublisherDaemon ready to run.
func NewPublisher(cfg *config.Config, st *State, logWriter io.Writer) *PublisherDaemon {
	logger := log.New(logWriter, "", log.LstdFlags)
	client := api.New(cfg.BaseURL, cfg.Name, cfg.Token)
	return &PublisherDaemon{cfg: cfg, client: client, state: st, logger: logger}
}

// Run starts the publisher daemon loop. It blocks until context is cancelled or
// a signal is received.
func (d *PublisherDaemon) Run(ctx context.Context) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := d.state.WritePID(os.Getpid()); err != nil {
		d.logf("[WARN] could not write PID file: %v", err)
	}
	defer d.state.ClearPID()

	d.logf("Publisher patrol started. Agent: %s | Poll: %ds | AutoSelectBid: %v | AutoComplete: %v | Strategy: %s",
		d.cfg.Name,
		d.cfg.Patrol.PollIntervalSecs,
		d.cfg.Patrol.PublisherAutoSelectBid,
		d.cfg.Patrol.PublisherAutoComplete,
		d.cfg.Patrol.PublisherSelectStrategy,
	)

	interval := time.Duration(d.cfg.Patrol.PollIntervalSecs) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logf("Publisher patrol stopping (signal received)")
			return nil
		case <-ticker.C:
			if err := d.poll(ctx); err != nil {
				d.logf("[WARN] publisher poll error: %v", err)
			}
		}
	}
}

// poll runs one publisher polling cycle:
//  1. Find my open jobs that have pending bids → auto-select if configured.
//  2. Find my submitted jobs → auto-complete if configured.
//  3. Find my active recurring jobs with submitted cycles → auto-complete cycle if configured.
func (d *PublisherDaemon) poll(ctx context.Context) error {
	if d.cfg.Patrol.PublisherAutoSelectBid {
		if err := d.processBids(ctx); err != nil {
			d.logf("[WARN] process bids: %v", err)
		}
	}
	if d.cfg.Patrol.PublisherAutoComplete {
		if err := d.processCompletions(ctx); err != nil {
			d.logf("[WARN] process completions: %v", err)
		}
	}
	return nil
}

// processBids looks for open jobs published by this agent that have pending bids
// and selects the first one according to the configured strategy.
func (d *PublisherDaemon) processBids(ctx context.Context) error {
	d.logf("Checking for open jobs with pending bids...")

	result, err := d.client.MyJobs("publisher", "open", "", 1, 50)
	if err != nil {
		return fmt.Errorf("list my publisher jobs: %w", err)
	}

	selected := 0
	for _, job := range result.Jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if job.BidCount == 0 {
			continue
		}

		bid, err := d.pickBid(job.ID)
		if err != nil {
			d.logf("[WARN] could not pick bid for job %s (%s): %v", job.ID, job.Title, err)
			continue
		}
		if bid == nil {
			continue
		}

		d.logf("Selecting bid %s from %s on job %s (%s)...", bid.ID, bid.BidderName, job.ID, job.Title)
		if _, err := d.client.SelectBid(job.ID, bid.ID); err != nil {
			d.logf("[WARN] select bid %s on job %s: %v", bid.ID, job.ID, err)
			continue
		}
		d.logf("Bid selected: job %s assigned to %s", job.ID, bid.BidderName)
		selected++
	}

	if selected == 0 && len(result.Jobs) > 0 {
		d.logf("No actionable bids found across %d open job(s)", len(result.Jobs))
	}
	return nil
}

// pickBid returns the bid to select for a job based on the configured strategy.
// Returns nil when no pending bid is available.
func (d *PublisherDaemon) pickBid(jobID string) (*api.Bid, error) {
	bids, err := d.client.ListBids(jobID, "pending", 1, 20)
	if err != nil {
		return nil, err
	}
	if len(bids.Bids) == 0 {
		return nil, nil
	}

	// "first" strategy: pick the earliest pending bid (already ordered by createdAt asc from server)
	return &bids.Bids[0], nil
}

// processCompletions looks for submitted one-off jobs and submitted recurring
// cycles owned by this agent and marks them complete.
func (d *PublisherDaemon) processCompletions(ctx context.Context) error {
	if err := d.completeOneOffJobs(ctx); err != nil {
		d.logf("[WARN] complete one-off jobs: %v", err)
	}
	if err := d.completeRecurringCycles(ctx); err != nil {
		d.logf("[WARN] complete recurring cycles: %v", err)
	}
	return nil
}

// completeOneOffJobs finds my one-off jobs in "submitted" status and completes them.
func (d *PublisherDaemon) completeOneOffJobs(ctx context.Context) error {
	d.logf("Checking for submitted one-off jobs to complete...")

	result, err := d.client.MyJobs("publisher", "submitted", "oneoff", 1, 50)
	if err != nil {
		return fmt.Errorf("list submitted one-off jobs: %w", err)
	}

	for _, job := range result.Jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.logf("Auto-completing submitted job %s (%s)...", job.ID, job.Title)
		if _, err := d.client.CompleteJob(job.ID); err != nil {
			d.logf("[WARN] complete job %s: %v", job.ID, err)
			continue
		}
		d.logf("Job %s (%s) completed", job.ID, job.Title)
	}
	return nil
}

// completeRecurringCycles finds my active recurring jobs and completes their
// current cycle if it is in "submitted" status.
func (d *PublisherDaemon) completeRecurringCycles(ctx context.Context) error {
	d.logf("Checking for recurring jobs with submitted cycles...")

	result, err := d.client.MyJobs("publisher", "active", "recurring", 1, 50)
	if err != nil {
		return fmt.Errorf("list active recurring jobs: %w", err)
	}

	for _, job := range result.Jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		cycle, err := d.client.GetCurrentCycle(job.ID)
		if err != nil {
			d.logf("[WARN] get current cycle for job %s: %v", job.ID, err)
			continue
		}
		if cycle.Status != "submitted" {
			continue
		}

		d.logf("Auto-completing cycle #%d for recurring job %s (%s)...", cycle.CycleNumber, job.ID, job.Title)
		if _, err := d.client.CompleteCycle(job.ID); err != nil {
			d.logf("[WARN] complete cycle for job %s: %v", job.ID, err)
			continue
		}
		d.logf("Cycle #%d for job %s (%s) completed", cycle.CycleNumber, job.ID, job.Title)
	}
	return nil
}

func (d *PublisherDaemon) logf(format string, args ...any) {
	d.logger.Printf(format, args...)
}
