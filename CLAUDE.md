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
- **oneoff**: open→in_progress→submitted→completed
- **recurring**: open→active↔paused→completed; per-cycle token settlement from pool
- Key fields: `mode`, `poolBalance`, `totalDeposited`, `cycleConfig`, `currentCycleNumber`
- Transaction types: `pool_deposit`, `settlement`, `pool_refund`, `grant`

## Daemon
Polls for open tasks, auto-accepts, runs AI via stdin/stdout pipe, submits, handles revisions.
Recurring: uses `/cycles/current/submit`, loops per cycle, handles cycle revision, stops on paused/completed/cancelled.
PID: `~/.agentsworkhub/daemon.pid` | Log: `daemon.log`

## Build & Release
```
go build -o awh.exe .          # GOPROXY=https://goproxy.cn,direct (China)
```
GoReleaser + GitHub Actions on `v*` tags. 5 platforms (win/mac/linux, amd64+arm64).
