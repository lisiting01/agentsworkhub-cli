package daemon

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
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
// agent spawn. "connected", keepalive comments, and "job.completed" are
// informational only.
var actionableEvents = map[string]bool{
	"job.created":              true,
	"job.assigned":             true,
	"job.revision_requested":   true,
	"cycle.submitted":          true,
	"cycle.revision_requested": true,
}

// Backoff and watchdog tuning for the SSE reconnect loop. The server emits a
// keepalive comment every 15 s, so 45 s of total silence unambiguously means
// the connection is dead (be it a proxy FIN we haven't noticed yet, or the
// keepalive itself being blocked). 30 s max backoff keeps event latency bounded
// even in the worst case where we hit a sustained outage.
const (
	sseInitialBackoff = 1 * time.Second
	sseMaxBackoff     = 30 * time.Second
	sseIdleTimeout    = 45 * time.Second
)

// Watch connects to the platform SSE stream and returns a channel of events.
// The connection is maintained automatically; on error it reconnects with
// exponential back-off (1 s → 2 s → 4 s → … capped at 30 s). Backoff resets to
// 1 s after every successful connection attempt, so a transient disconnect
// never compounds into 30-second reconnect storms.
//
// Only actionable events are sent to the returned channel; informational
// events (connected, keepalives, job.completed, etc.) are silently discarded.
//
// The channel is closed when ctx is cancelled.
func Watch(ctx context.Context, baseURL, agentName, agentToken string, logf func(string, ...any)) <-chan WatcherEvent {
	ch := make(chan WatcherEvent, 16)

	go func() {
		defer close(ch)
		backoff := sseInitialBackoff

		for {
			if ctx.Err() != nil {
				return
			}

			connected, err := streamOnce(ctx, baseURL, agentName, agentToken, ch, logf)
			if ctx.Err() != nil {
				return
			}

			// If we successfully connected at least once this round, the
			// disconnect was likely a proxy/server idle timeout and the next
			// reconnect should happen promptly. Reset backoff to avoid the
			// pathological case where one long-lived but eventually-dropped
			// connection leaves us in a 30 s reconnect loop forever.
			if connected {
				backoff = sseInitialBackoff
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

			if !connected {
				backoff *= 2
				if backoff > sseMaxBackoff {
					backoff = sseMaxBackoff
				}
			}
		}
	}()

	return ch
}

// streamOnce opens one SSE connection and reads until it closes, the idle
// watchdog fires, or ctx is done. The returned bool indicates whether the
// underlying HTTP request completed its handshake (HTTP 200 received), which
// the caller uses to decide whether to reset backoff.
func streamOnce(parent context.Context, baseURL, agentName, agentToken string, ch chan<- WatcherEvent, logf func(string, ...any)) (connected bool, err error) {
	url := strings.TrimRight(baseURL, "/") + "/api/events/stream"

	// Per-attempt cancel context lets the idle watchdog abort the HTTP request
	// when the server goes silent for longer than sseIdleTimeout.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Agent-Name", agentName)
	req.Header.Set("X-Agent-Token", agentToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{} // no client-wide timeout — we manage idle timeout per-read
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	connected = true
	logf("SSE stream connected")

	// Idle watchdog: every activity bumps lastActivity; a ticker periodically
	// checks how long it's been since the last read and cancels the context if
	// the gap exceeds sseIdleTimeout. Keepalive comments from the server count
	// as activity, so in a healthy connection this never fires.
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())

	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(sseIdleTimeout / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				last := time.Unix(0, lastActivity.Load())
				if now.Sub(last) > sseIdleTimeout {
					logf("SSE idle %s with no server traffic — forcing reconnect", now.Sub(last).Round(time.Second))
					cancel()
					return
				}
			}
		}
	}()
	defer func() { <-watchdogDone }()

	// Parse the SSE text/event-stream line-by-line.
	// SSE format:
	//   event: <name>
	//   data: <json>
	//   (blank line = end of event)
	//   : <comment>    ← server keepalives; we just treat them as activity
	var (
		eventType string
		dataLine  string
	)

	scanner := bufio.NewScanner(resp.Body)
	// Raise the line buffer ceiling — SSE data payloads for job events can
	// comfortably exceed the 64 KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		lastActivity.Store(time.Now().UnixNano())
		line := scanner.Text()

		switch {
		case strings.HasPrefix(line, ":"):
			// SSE comment line (keepalive). Activity already recorded; drop it.
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
					return connected, nil
				default:
					// Channel full — drop the event; the fallback scheduler will catch it.
				}
			}
			eventType = ""
			dataLine = ""
		}
	}

	return connected, scanner.Err()
}
