# awh CLI — Project Memory

Go CLI for AgentsWorkhub (agentsworkhub.com).

## Key Facts
- Module: `github.com/lisiting01/agentsworkhub-cli` | Binary: `awh`
- Go 1.25, Cobra, fatih/color
- Config: `~/.agentsworkhub/config.json` | Auth: `X-Agent-Name` + `X-Agent-Token`

## Structure
- `cmd/` — auth, me, jobs, agent, patrol (legacy 巡逻模式), version
- `internal/api/` — HTTP client (all platform REST APIs)
- `internal/config/` — config read/write incl. PatrolConfig
- `internal/daemon/` — AI engine (claude/codex/generic), prompt builder, worker manager, system prompt
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

## Agent Worker (recommended)
`awh agent run` spawns an AI sub-instance (Claude Code / Codex) with platform credentials and command reference injected as a system prompt. The sub-instance autonomously uses `awh` CLI commands to operate on the platform.

Key commands: `awh agent run --engine claude --prompt "..."`, `awh agent status`, `awh agent stop [--id <id>]`.
Flags: `--engine`, `--engine-path`, `--engine-model`, `--prompt`, `--skill <path>`, `--work-dir`, `--daemon`.
Worker state: `~/.agentsworkhub/workers/<id>/` (worker.pid, worker.json, worker.log).

## Agent Schedule
`awh agent schedule` is a lightweight persistent scheduler (no AI) that repeatedly spawns fresh `awh agent run` instances. `--interval` counts from completion of the last worker, avoiding stacking.

Key commands: `awh agent schedule --engine claude --skill ./ops.md --interval 120 --name <n> --daemon`, `awh agent schedule status`, `awh agent schedule stop [--name <n>] [--force]`.
Scheduler state: `~/.agentsworkhub/schedulers/<name>/` (scheduler.pid, scheduler.json, scheduler.log).

Implementation:
- `cmd/agent.go` — run / status / stop commands
- `cmd/agent_schedule.go` — schedule / status / stop commands + scheduler loop
- `internal/daemon/systemprompt.go` — BuildAgentSystemPrompt (auth context + command list + mission)
- `internal/daemon/worker.go` — WorkerState, WorkerInfo, ListWorkers
- `internal/daemon/scheduler.go` — SchedulerState, SchedulerInfo, ListSchedulers
- `internal/daemon/engine.go` — StreamingEngine interface, RunStreaming on ClaudeEngine / CodexEngine

## Patrol (巡逻模式) [legacy]
Three roles: **executor** (default), **publisher**, **reviewer**.
`awh patrol start` — self-daemonizes. `stop` / `status` / `logs` / `config` for management. `--foreground` for debugging.

**Executor**: polls open tasks, auto-bids (`auto_bid`, `bid_message`), waits for selection, runs AI via stdin/stdout, submits, handles revisions.
Phases: `bidding` → `running_ai` → `submitting` → `waiting_feedback` → `rerunning`
Recurring: `/cycles/current/submit`, loops per cycle, stops on paused/completed/cancelled.

**Publisher**: `awh patrol start --role publisher [--auto-select-bid] [--auto-complete]`
Polls own open jobs → auto-selects first pending bid; polls submitted jobs/cycles → auto-completes.
Implemented in `internal/daemon/publisher.go`.

**Reviewer**: `awh patrol start --role reviewer --engine claude [--skills ...]`
Polls own submitted jobs/cycles → fetches brief+standards+delivery messages → runs AI engine → parses `{"action":"complete"|"revise","feedback":"..."}` → calls complete or request-revision.
Implemented in `internal/daemon/reviewer.go`. Uses `BuildReviewPrompt` in `prompt.go`.

Config keys: `auto_bid` (renamed from `auto_accept`, old key migrated on load), `engine_model` (sets `ANTHROPIC_MODEL` env for ClaudeEngine), `publisher_auto_select_bid`, `publisher_auto_complete`, `publisher_select_strategy`.
PID: `~/.agentsworkhub/patrol.pid` | Log: `patrol.log` | Config key: `patrol`

## Build & Release
```
go build -o awh.exe .          # GOPROXY=https://goproxy.cn,direct (China)
```
GoReleaser + GitHub Actions on `v*` tags. 5 platforms (win/mac/linux, amd64+arm64).
