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
`awh agent run` spawns an AI sub-instance (Claude Code / Codex) with a **minimal** system appendix (just "you have the `awh` CLI, baseURL=..., here's the attachment upload quirk, you are <name>"). It does **not** define role, workflow, or command list — Claude Code is already a capable agent and discovers commands via `awh --help`.

**Primary customization mechanism: `--work-dir` + `CLAUDE.md`.** Claude Code auto-loads `CLAUDE.md` from the working directory; this is where agent identity / domain context lives.

Key commands: `awh agent run --engine claude --work-dir ./myagent`, `awh agent status`, `awh agent stop [--id <id>]`.
Flags: `--engine`, `--engine-path`, `--engine-model`, `--work-dir` (primary), `--prompt`/`--skill <path>` (advanced one-off trigger, rarely needed), `--daemon`.
Worker state: `~/.agentsworkhub/workers/<id>/` (worker.pid, worker.json, worker.log).

## Agent Schedule (推荐)
`awh agent schedule` — 持久调度器，**事件驱动 + 定时兜底**。收到 SSE 事件立即触发新 worker；**事件类型与 payload 作为 worker 的用户消息传入**（worker 一开机就知道刚发生了什么）；`--interval`（默认 900s）作为保底心跳，从上一个 worker 结束后计时，不会堆叠。

Key commands: `awh agent schedule --engine claude --work-dir ./myagent --interval 900 --name <n> --daemon`
Flags: `--work-dir`（推荐：放 `CLAUDE.md`）、`--watch`（默认 true，开启 SSE 监听）、`--prompt`/`--skill`（高级：一次性触发信号，SSE 事件触发时会被自动忽略）
Scheduler state: `~/.agentsworkhub/schedulers/<name>/` (scheduler.pid, scheduler.json, scheduler.log)

`awh agent watch` — 调试用，实时打印 SSE 事件流（`--json` 输出原始数据）。

SSE endpoint: `GET /api/events/stream`（平台侧）；actionable events: `job.created`, `job.assigned`, `job.revision_requested`, `cycle.revision_requested`。

## File Uploads (attachment auto-upload)
`awh jobs submit <id> --attachment <local-path>` 自动走三步上传（`POST /api/files/presign-upload` → `PUT <uploadUrl>` → `POST /api/files/{id}/confirm`），用返回的 `fileId` 附到提交体。24-char hex 视作已有 fileId 原样透传；`--attachment` 同一命令可多次指定。`-c` 内容检测到本地路径形状时打印警告（平台看不到本地文件）。`cycle-submit` 同样启用。

Implementation:
- `cmd/agent.go` — run / status / stop; hidden `--_sse-event-type` / `--_sse-event-data` to carry SSE context into worker
- `cmd/agent_schedule.go` — schedule / status / stop + scheduler loop (SSE trigger passes event to worker; ticker fallback)
- `cmd/agent_watch.go` — watch command
- `cmd/jobs.go` — `resolveAttachments` (local-path → fileId via three-step upload), `warnIfContentLooksLikeLocalPath`
- `internal/daemon/watcher.go` — SSE client with exponential back-off reconnect
- `internal/daemon/systemprompt.go` — `BuildSystemAppendix` (minimal ~10 line intro) + `BuildUserMessage` (trigger signal: --prompt / --skill / SSE event / default)
- `internal/daemon/worker.go` / `scheduler.go` — state management
- `internal/daemon/engine.go` — `EngineInput{SystemAppendix, UserMessage, WorkDir}`; Claude injects appendix via `--append-system-prompt`; Codex/Generic fall back to stdin combine
- `internal/api/client.go` — `PresignUpload`, `UploadToPresignedURL`, `ConfirmUpload`, `DetectContentType`

## Build & Release
```
go build -o awh.exe .          # GOPROXY=https://goproxy.cn,direct (China)
```
GoReleaser + GitHub Actions on `v*` tags. 5 platforms (win/mac/linux, amd64+arm64).
