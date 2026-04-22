# awh CLI — Project Memory

Go CLI for AgentsWorkhub (agentsworkhub.com).

## Key Facts
- Module: `github.com/lisiting01/agentsworkhub-cli` | Binary: `awh`
- Go 1.25, Cobra, fatih/color
- Config: `~/.agentsworkhub/config.json` | Auth: `X-Agent-Name` + `X-Agent-Token`

## Structure
- `cmd/` — auth, me, jobs, agent (run/schedule/watch/status/stop), version
- `internal/api/` — HTTP client (all platform REST APIs)
- `internal/config/` — config read/write
- `internal/daemon/` — AI engine (claude/codex/generic), watcher (SSE), worker/scheduler managers, system prompt
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

## Agent Schedule (推荐)
`awh agent schedule` — 持久调度器，**事件驱动 + 定时兜底**。收到 SSE 事件立即触发新 worker；`--interval`（默认 900s）作为保底心跳，从上一个 worker 结束后计时，不会堆叠。

Key commands: `awh agent schedule --engine claude --work-dir ./myagent --interval 900 --name <n> --daemon`
Flags: `--watch`（默认 true，开启 SSE 监听）、`--prompt`/`--skill`（可选，`--work-dir` 内有 `CLAUDE.md` 时无需显式指定）
Scheduler state: `~/.agentsworkhub/schedulers/<name>/` (scheduler.pid, scheduler.json, scheduler.log)

`awh agent watch` — 调试用，实时打印 SSE 事件流（`--json` 输出原始数据）。

SSE endpoint: `GET /api/events/stream`（平台侧）；actionable events: `job.created`, `job.assigned`, `job.revision_requested`, `cycle.revision_requested`。

Implementation:
- `cmd/agent.go` — run / status / stop
- `cmd/agent_schedule.go` — schedule / status / stop + scheduler loop (SSE trigger + ticker fallback)
- `cmd/agent_watch.go` — watch command
- `internal/daemon/watcher.go` — SSE client with exponential back-off reconnect
- `internal/daemon/systemprompt.go` — BuildAgentSystemPrompt
- `internal/daemon/worker.go` / `scheduler.go` — state management
- `internal/daemon/engine.go` — StreamingEngine (ClaudeEngine / CodexEngine)

## Build & Release
```
go build -o awh.exe .          # GOPROXY=https://goproxy.cn,direct (China)
```
GoReleaser + GitHub Actions on `v*` tags. 5 platforms (win/mac/linux, amd64+arm64).
