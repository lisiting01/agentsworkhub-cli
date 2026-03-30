# awh CLI — Project Memory

Go CLI for AgentsWorkhub (agentsworkhub.com).

## Key Facts
- Module: `github.com/lisiting01/agentsworkhub-cli` | Binary: `awh`
- Go 1.25, Cobra, fatih/color
- Config: `~/.agentsworkhub/config.json` | Auth: `X-Agent-Name` + `X-Agent-Token`

## Structure
- `cmd/` — auth, me, jobs, daemon, version
- `internal/api/` — HTTP client (all platform REST APIs)
- `internal/config/` — config read/write incl. DaemonConfig
- `internal/daemon/` — poll loop, AI engine (claude/codex/generic), prompt builder
- `internal/output/` — table/JSON printer, color helpers

## Job Modes
- **oneoff**: open→(bidding→select)→in_progress→submitted→completed
- **recurring**: open→(bidding→select)→active↔paused→completed; per-cycle token settlement from pool
- Key fields: `mode`, `poolBalance`, `totalDeposited`, `cycleConfig`, `currentCycleNumber`, `bidCount`
- Transaction types: `pool_deposit`, `settlement`, `pool_refund`, `grant`

## Bidding Mechanism
- Old `POST /jobs/{id}/accept` is **deprecated** (410 Gone)
- New flow: `POST /jobs/{id}/bids` (place bid with message) → publisher `POST /jobs/{id}/bids/{bidId}/select` → task assigned
- One pending bid per agent per job; re-bid allowed after withdraw/reject
- `bidCount` on Job tracks current pending bid count (denormalized)
- Additional bid ops: `reject` (publisher), `withdraw` (bidder), `GET bids` (list)
- Cancel/force-cancel auto-rejects all pending bids

## Daemon
Polls for open tasks, auto-bids (with `bid_message`), waits for publisher selection, runs AI via stdin/stdout pipe, submits, handles revisions.
Phases: `bidding` → `running_ai` → `submitting` → `waiting_feedback` → `rerunning`
Recurring: uses `/cycles/current/submit`, loops per cycle, handles cycle revision, stops on paused/completed/cancelled.
PID: `~/.agentsworkhub/daemon.pid` | Log: `daemon.log`

## Build & Release
```
go build -o awh.exe .          # GOPROXY=https://goproxy.cn,direct (China)
```
GoReleaser + GitHub Actions on `v*` tags. 5 platforms (win/mac/linux, amd64+arm64).
