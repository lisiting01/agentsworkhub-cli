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
	// ID is the SSE `id:` field associated with this event, used for
	// Last-Event-ID replay on reconnect. Empty for events without an id.
	ID string
}

// actionableEvents is the set of event types that should trigger an immediate
// agent spawn. "connected", keepalive comments, and "job.completed" are
// informational only — the executor already knows the job is done.
//
// Coverage rationale (kept in sync with the platform's broadcast() call sites
// in agentsworkhub/app/api/jobs/**):
//   - Executor-facing:   job.assigned, job.revision_requested, cycle.revision_requested,
//                        bid.rejected, job.cancelled, job.paused, job.resumed
//   - Publisher-facing:  bid.created, bid.withdrawn, job.submitted,
//                        cycle.submitted, cycle.completed, job.withdrawn
//   - Open market:       job.created
//   - Bidirectional:     job.message
var actionableEvents = map[string]bool{
	// Open market — every connected agent.
	"job.created": true,

	// Executor-facing: I'm being told to do something.
	"job.assigned":             true,
	"job.revision_requested":   true,
	"cycle.revision_requested": true,
	"bid.rejected":             true,
	"job.cancelled":            true,
	"job.paused":               true,
	"job.resumed":              true,

	// Publisher-facing: someone is interacting with my job and I should react.
	"bid.created":     true,
	"bid.withdrawn":   true,
	"job.submitted":   true,
	"cycle.submitted": true,
	"cycle.completed": true,
	"job.withdrawn":   true,

	// Bidirectional thread message — both sides may need to look.
	"job.message": true,
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
// On reconnect, Watch sends the most recently observed event ID via the
// `Last-Event-ID` header so the server can replay any events that fired
// during the disconnect window. The cursor is reset whenever the server's
// `connected` event reports a different bootEpoch (server restart).
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

		// SSE replay cursor. Updated as we observe `id:` fields on the wire.
		// Persisted across reconnects within the same process; reset to ""
		// whenever the server reports a new bootEpoch.
		var lastEventID string
		var serverBootEpoch string

		for {
			if ctx.Err() != nil {
				return
			}

			connected, newEpoch, lastID, err := streamOnce(ctx, baseURL, agentName, agentToken, ch, logf, lastEventID, serverBootEpoch)
			if ctx.Err() != nil {
				return
			}

			// Track the server identity and the latest seen event ID so the
			// next reconnect can ask for replay starting from where we left
			// off — but only if the epoch is consistent. A new epoch means
			// the server restarted and its in-memory replay buffer is gone;
			// holding onto a stale cursor would mismatch and silently skip
			// real events.
			if newEpoch != "" {
				if serverBootEpoch != "" && newEpoch != serverBootEpoch {
					logf("SSE server bootEpoch changed (%s → %s) — discarding replay cursor", serverBootEpoch, newEpoch)
					lastEventID = ""
				}
				serverBootEpoch = newEpoch
			}
			if lastID != "" {
				lastEventID = lastID
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
// watchdog fires, or ctx is done.
//
// On every exit it emits a one-line structured `SSE summary` log capturing
// the connection's vital stats (duration, event/keepalive counts, last
// activity timing, disconnect reason). This makes it possible to diagnose
// "why does SSE keep reconnecting?" by grepping the scheduler log:
//   - reason=eof + short last_activity_ago → server-side close (likely a
//     reverse proxy maxDuration / idle timeout)
//   - reason=idle_watchdog + keepalives_received=0 → reverse proxy is
//     buffering the response body (e.g. nginx proxy_buffering on) so our
//     keepalives never reach us
//   - duration clustering at a fixed value (60s, 100s, 300s) → an
//     intermediate hop is enforcing that ceiling
//   - reason=scanner_error → underlying TCP/HTTP error mid-stream
//
// Returns:
//   - connected: whether the HTTP handshake completed (200 received). Used
//     by the caller to decide whether to reset backoff.
//   - bootEpoch: the server's reported bootEpoch from the `connected` hello
//     event. Empty if we didn't receive one.
//   - lastID: the most recent SSE `id:` field we observed during this stream.
//     The caller uses this to populate Last-Event-ID on the next reconnect.
//   - err: any underlying read/connect error.
func streamOnce(
	parent context.Context,
	baseURL, agentName, agentToken string,
	ch chan<- WatcherEvent,
	logf func(string, ...any),
	priorEventID string,
	priorBootEpoch string,
) (connected bool, bootEpoch string, lastID string, err error) {
	url := strings.TrimRight(baseURL, "/") + "/api/events/stream"

	// Per-attempt cancel context lets the idle watchdog abort the HTTP request
	// when the server goes silent for longer than sseIdleTimeout.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// Stats accumulated for the summary log emitted on exit.
	startedAt := time.Now()
	var (
		eventsReceived     int
		keepalivesReceived int
		commentsReceived   int
		replayedReceived   int
		// Set to true exactly when our own watchdog cancels ctx due to idle.
		// Distinguishes idle timeout from upstream-driven cancellations.
		watchdogFired atomic.Bool
		// Tracks whether we are currently inside a `: replay-start` /
		// `: replay-end` block so per-event counters can be classified.
		insideReplayBlock bool
	)
	lastID = priorEventID

	// Idle activity tracker — also referenced by the summary so the reader
	// can see "how long was it since the last byte from the server when we
	// gave up?". Initialised to startedAt so a connection that produces
	// nothing at all reports a clean elapsed gap.
	var lastActivity atomic.Int64
	lastActivity.Store(startedAt.UnixNano())

	defer func() {
		// Classify the disconnect reason for the structured summary.
		var reason string
		switch {
		case parent.Err() != nil:
			reason = "ctx_cancelled"
		case watchdogFired.Load():
			reason = "idle_watchdog"
		case !connected:
			// Pre-handshake failure — err carries the detail.
			if err != nil {
				reason = fmt.Sprintf("handshake_error: %v", err)
			} else {
				reason = "handshake_error"
			}
		case err != nil:
			reason = fmt.Sprintf("scanner_error: %v", err)
		default:
			reason = "eof"
		}

		duration := time.Since(startedAt)
		lastActivityAgo := time.Since(time.Unix(0, lastActivity.Load()))

		logf(
			"SSE summary | duration=%s events=%d keepalives=%d comments=%d "+
				"replayed=%d last_activity_ago=%s reason=%s last_id=%s epoch=%s",
			duration.Round(100*time.Millisecond),
			eventsReceived,
			keepalivesReceived,
			commentsReceived,
			replayedReceived,
			lastActivityAgo.Round(100*time.Millisecond),
			reason,
			shortID(lastID),
			shortID(bootEpoch),
		)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, "", priorEventID, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("X-Agent-Name", agentName)
	req.Header.Set("X-Agent-Token", agentToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	// Some HTTP intermediaries (rare CDN/proxy combos) buffer gzip-encoded
	// SSE payloads until a full block is filled, breaking the per-event
	// flush contract. Forcing identity encoding eliminates that risk; SSE
	// is plain text and tiny per-event, so the bandwidth cost is trivial.
	req.Header.Set("Accept-Encoding", "identity")
	if priorEventID != "" {
		// EventSource-style replay cursor. The server replays events newer
		// than this ID — provided priorBootEpoch is still the current epoch,
		// which the server validates internally.
		req.Header.Set("Last-Event-ID", priorEventID)
	}

	client := &http.Client{} // no client-wide timeout — we manage idle timeout per-read
	resp, err := client.Do(req)
	if err != nil {
		return false, "", priorEventID, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", priorEventID, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	connected = true
	if priorEventID != "" {
		logf("SSE stream connected (resuming after %s)", shortID(priorEventID))
	} else {
		logf("SSE stream connected")
	}

	// Idle watchdog: a ticker periodically checks how long it's been since
	// the last read and cancels the context if the gap exceeds
	// sseIdleTimeout. Keepalive comments from the server count as activity,
	// so in a healthy connection this never fires.
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
					watchdogFired.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	defer func() { <-watchdogDone }()

	// Parse the SSE text/event-stream line-by-line.
	// SSE format:
	//   id: <opaque>
	//   event: <name>
	//   data: <json>
	//   (blank line = end of event)
	//   : <comment>    ← server keepalives + replay markers;
	//                    treated as activity but never dispatched
	var (
		eventType string
		dataLine  string
		eventID   string
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
			// SSE comment line. Activity already recorded above; classify
			// for diagnostic counters and otherwise drop. The server emits
			// `: keepalive <ts>` every 15s and `: replay-start count=N` /
			// `: replay-end` to bracket replayed events on reconnect.
			commentsReceived++
			body := strings.TrimSpace(line[1:])
			switch {
			case strings.HasPrefix(body, "keepalive"):
				keepalivesReceived++
			case strings.HasPrefix(body, "replay-start"):
				insideReplayBlock = true
			case strings.HasPrefix(body, "replay-end"):
				insideReplayBlock = false
			}
		case strings.HasPrefix(line, "id:"):
			eventID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLine = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			// Blank line signals end of an event. Always advance lastID for
			// any event with an id field — even informational ones — so that
			// reconnect resumes from the correct point in the stream.
			if eventID != "" {
				lastID = eventID
			}

			// The first frame is always `connected`; capture bootEpoch from
			// its data payload so the caller can detect server restarts.
			// We use a tiny scan rather than json.Unmarshal to avoid pulling
			// in a dependency for a one-shot lookup.
			if eventType == "connected" {
				if be := extractBootEpoch(dataLine); be != "" {
					bootEpoch = be
				}
			} else if eventType != "" {
				if insideReplayBlock {
					replayedReceived++
				} else {
					eventsReceived++
				}
				if actionableEvents[eventType] {
					select {
					case ch <- WatcherEvent{Type: eventType, Data: dataLine, ID: eventID}:
					case <-ctx.Done():
						return connected, bootEpoch, lastID, nil
					default:
						// Channel full — drop the event; the fallback scheduler will catch it.
						logf("SSE channel full, dropping %s event (id=%s)", eventType, shortID(eventID))
					}
				}
			}
			eventType = ""
			dataLine = ""
			eventID = ""
		}
	}

	return connected, bootEpoch, lastID, scanner.Err()
}

// extractBootEpoch pulls the bootEpoch value out of a `connected` event's
// JSON payload without using a JSON decoder. The payload is small and
// well-known: {"agentId":"...","bootEpoch":"..."}.
func extractBootEpoch(jsonData string) string {
	const key = `"bootEpoch":"`
	i := strings.Index(jsonData, key)
	if i < 0 {
		return ""
	}
	rest := jsonData[i+len(key):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// shortID truncates a long event ID for log readability without losing
// uniqueness within a session.
func shortID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:8] + "…" + id[len(id)-6:]
}
