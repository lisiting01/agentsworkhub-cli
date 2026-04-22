package daemon

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WatcherEvent is a single event received from the platform SSE stream.
type WatcherEvent struct {
	// Type is the SSE event name, e.g. "job.created", "job.assigned",
	// "job.revision_requested", "cycle.submitted", "cycle.revision_requested".
	Type string
	// Data is the raw JSON payload from the "data:" field.
	Data string
}

// actionableEvents is the set of event types that should trigger an immediate
// agent spawn. "connected" and "job.completed" are informational only.
var actionableEvents = map[string]bool{
	"job.created":             true,
	"job.assigned":            true,
	"job.revision_requested":  true,
	"cycle.submitted":         true,
	"cycle.revision_requested": true,
}

// Watch connects to the platform SSE stream and returns a channel of events.
// The connection is maintained automatically; on error it reconnects with
// exponential back-off (1 s → 2 s → 4 s → … capped at 60 s).
//
// Only actionable events are sent to the returned channel; informational
// events (connected, job.completed, etc.) are silently discarded.
//
// The channel is closed when ctx is cancelled.
func Watch(ctx context.Context, baseURL, agentName, agentToken string, logf func(string, ...any)) <-chan WatcherEvent {
	ch := make(chan WatcherEvent, 16)

	go func() {
		defer close(ch)
		backoff := time.Second
		const maxBackoff = 60 * time.Second

		for {
			if ctx.Err() != nil {
				return
			}

			err := streamOnce(ctx, baseURL, agentName, agentToken, ch, logf)
			if ctx.Err() != nil {
				return
			}

			if err != nil {
				logf("SSE connection error: %v — reconnecting in %s", err, backoff)
			} else {
				logf("SSE connection closed — reconnecting in %s", backoff)
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}

			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}()

	return ch
}

// streamOnce opens one SSE connection and reads until it closes or ctx is done.
func streamOnce(ctx context.Context, baseURL, agentName, agentToken string, ch chan<- WatcherEvent, logf func(string, ...any)) error {
	url := strings.TrimRight(baseURL, "/") + "/api/events/stream"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Agent-Name", agentName)
	req.Header.Set("X-Agent-Token", agentToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{} // no timeout — connection stays open intentionally
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	logf("SSE stream connected")

	// Parse the SSE text/event-stream line-by-line.
	// SSE format:
	//   event: <name>
	//   data: <json>
	//   (blank line = end of event)
	var (
		eventType string
		dataLine  string
	)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLine = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			// Blank line signals end of an event.
			if eventType != "" && actionableEvents[eventType] {
				select {
				case ch <- WatcherEvent{Type: eventType, Data: dataLine}:
				case <-ctx.Done():
					return nil
				default:
					// Channel full — drop the event; the fallback scheduler will catch it.
				}
			}
			eventType = ""
			dataLine = ""
		}
	}

	return scanner.Err()
}
