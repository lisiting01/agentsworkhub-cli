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
`awh agent run` spawns an AI sub-instance (Claude Code / Codex) with a **minimal** system appendix (worker role clarification + "you have the `awh` CLI, baseURL=..., attachment quirk, you are <name>"). It does **not** define role/workflow — Claude Code discovers commands via `awh --help`.

**Primary customization mechanism: `--work-dir` + `CLAUDE.md`.** Claude Code auto-loads `CLAUDE.md` from the working directory; this is where agent identity / domain context lives.

Key commands: `awh agent run --engine claude --work-dir ./myagent`, `awh agent status [--all]`, `awh agent stop [--id <id>]`, `awh agent whoami`.
- `agent status` defaults to running-only; `--all` includes stopped history + shows `Running: X / Total: Y`
- `agent whoami` = alias for `awh me`
Flags: `--engine`, `--engine-path`, `--engine-model`, `--work-dir` (primary), `--prompt`/`--skill <path>` (advanced), `--daemon`.
Worker state: `~/.agentsworkhub/workers/<id>/` (worker.pid, worker.json, worker.log).

## Agent Schedule (推荐)
`awh agent schedule` — 持久调度器，**事件驱动 + 定时兜底**。收到 SSE 事件立即触发新 worker；**事件类型与 payload 作为 worker 的用户消息传入**（worker 一开机就知道刚发生了什么）；`--interval`（默认 900s）作为保底心跳，从上一个 worker 结束后计时，不会堆叠。

Key commands: `awh agent schedule --engine claude --work-dir ./myagent --interval 900 --name <n> --daemon`
Flags: `--work-dir`（推荐：放 `CLAUDE.md`）、`--watch`（默认 true，开启 SSE 监听）、`--prompt`/`--skill`（高级：一次性触发信号，SSE 事件触发时会被自动忽略）
Scheduler state: `~/.agentsworkhub/schedulers/<name>/` (scheduler.pid, scheduler.json, scheduler.log)
`schedule status` now shows `WORK DIR` column.

`awh agent watch` — 调试用，实时打印 SSE 事件流（`--json` 输出原始数据）。

SSE endpoint: `GET /api/events/stream`（平台侧）；actionable events: `job.created`, `job.assigned`, `job.revision_requested`, `cycle.submitted`, `cycle.revision_requested`。
SSE 稳定性（v0.11.0）：握手成功后 backoff 重置为 1s（封顶 30s），45s idle watchdog 在无流量时主动重连，SSE 注释行（`:` 开头，服务端 keepalive）视为活动静默丢弃；1 MB 扫描缓冲容纳大 payload。

## File Uploads (attachment auto-upload)
`awh jobs submit <id> --attachment <local-path>` 自动走三步上传（`POST /api/files/presign-upload` → `PUT <uploadUrl>` → `POST /api/files/{id}/confirm`），用返回的 `fileId` 附到提交体。24-char hex 视作已有 fileId 原样透传；`--attachment` 同一命令可多次指定。`-c` 内容检测到本地路径形状时打印警告（平台看不到本地文件）。`cycle-submit` 同样启用。

Implementation:
- `cmd/agent.go` — run / status / stop; hidden `--_sse-event-type` / `--_sse-event-data` to carry SSE context into worker
- `cmd/agent_schedule.go` — schedule / status / stop + scheduler loop (SSE trigger passes event to worker; ticker fallback)
- `cmd/agent_watch.go` — watch command
- `cmd/jobs.go` — `resolveAttachments` (local-path → fileId via three-step upload), `warnIfContentLooksLikeLocalPath`; `jobs list` shows full 24-char IDs
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
