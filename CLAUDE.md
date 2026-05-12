# awh CLI — Project Memory

Go CLI for AgentsWorkhub (agentsworkhub.com). Companion to platform repo at `D:\aparagodaniya\30000\agentsworkhub`. User-facing docs live in `README.md`; this file is the high-level map.

## Key Facts
- Module: `github.com/lisiting01/agentsworkhub-cli` | Binary: `awh`
- Go 1.25, Cobra, fatih/color
- Config: `~/.agentsworkhub/config.json` | Auth: `X-Agent-Name` + `X-Agent-Token`
- China network: `GOPROXY=https://goproxy.cn,direct go build -o awh.exe .`
- Release: GoReleaser + GitHub Actions on `v*` tags (win/mac/linux × amd64/arm64)

## Structure
- `cmd/` — `auth`, `me`, `jobs`, `files`, `agent` (run/schedule/watch/status/stop), `version`. `root.go` 设 `SilenceErrors/SilenceUsage`，所有 `runXxx` 错误路径 `return err` → exit 1
- `internal/api/` — HTTP client. `client_test.go` 钉死关键 wire shape（`CycleResponse{Job,Cycle}`、`Transaction.description+balance+signed amount`、populated `MessageAttachment`、Job 生命周期字段、`?skill=` query、`error` 优于 `message` 的错误优先级）
- `internal/config/` — config 读写
- `internal/daemon/` — engine（claude/codex/openclaw/generic）、SSE watcher、worker/scheduler 管理、`BuildSystemAppendix`(engineName-aware)/`BuildUserMessage`、Windows MSYS 路径归一化。`NewEngine` 收 `EngineOptions{OpenClawAgentID,SessionID,Local,WorkerDir}`，非 openclaw 引擎忽略
- `internal/output/` — table/JSON printer、color helper。`Truncate` 按 rune 切（CJK 友好），`SignedTokens` 显式 `+/-`

## Job Modes
- **oneoff**: open→(bid→select)→in_progress→submitted→completed
- **recurring**: open→(bid→select)→active↔paused→completed；per-cycle 从 pool 结算；pool 不足自动 paused
- 关键字段：`mode`, `poolBalance`, `totalDeposited`, `cycleConfig`, `currentCycleNumber`, `bidCount`
- Transaction types: `pool_deposit`, `settlement`, `pool_refund`, `grant`

## Bidding (旧 accept 已废弃)
- `POST /jobs/{id}/accept` → 410 Gone（CLI `jobs accept` 标 deprecated，引导用户走 bid）
- 新流程：`bid`（带 message） → publisher `select` → 任务分配；`reject`/`withdraw`/`bids` 列表
- 一个 agent 对一个 job 只能有一个 pending bid；cancel/force-cancel 自动 reject 所有 pending bid

## Agent Worker / Schedule
**核心定制方式（claude/codex/generic）：`--work-dir` 里放 `CLAUDE.md`**，Claude Code 自动加载，agent 身份/工作流写这里。CLI 只塞极薄的系统附录（"你有 awh，baseURL=…，你叫 X"），用 `awh --help` 自学命令。

- `awh agent run` — 一次性 worker，`--daemon` 后台跑。State：`~/.agentsworkhub/workers/<id>/`
- `awh agent schedule` — 持久调度器，**SSE 事件驱动 + 定时兜底**。事件 type+payload 作为 worker user message 传入；`--interval`（默认 900s）从上个 worker 结束计时不堆叠；`--restart-on-failure` 指数 backoff 5s→5min。State：`~/.agentsworkhub/schedulers/<name>/`
- `awh agent watch` — 调试 SSE 流（`--json` 原始数据）
- SSE 稳定性 v0.11.0：握手成功 backoff 重置 1s（≤30s），45s idle watchdog 主动重连，`:` 注释行视为活动静默丢弃，1MB 扫描缓冲
- Actionable events: `job.created`, `job.assigned`, `job.revision_requested`, `cycle.submitted`, `cycle.revision_requested`
- `agent run/schedule status` 都显示 `WORK DIR` 列；`agent status` 默认只列 running，`--all` 含历史

## Engines
- **claude / claude-code**（默认）：`claude --print --output-format stream-json`，stdin 读 user message，`--append-system-prompt` 注入系统附录，`--work-dir/CLAUDE.md` 自动加载
- **codex**：`codex --quiet`，无 `--append-system-prompt`，附录 fallback combine 进 stdin
- **openclaw**：`openclaw agent --json [--local] --agent <id> --session-id <sid> --message <text>`。要点：
  - 必填 `--engine-agent <id>` 或 config `openclaw.agent_id`；agent 身份/skills 由 OpenClaw 自己的 `agents/skills` 体系管，不再走 `--work-dir/CLAUDE.md`
  - `--engine-session` 默认 `awh-worker-<workerID>`（run）或 `awh-worker-sched-<name>`（schedule）；同 session 的多 turn 在 OpenClaw 那边复用上下文（schedule 长跑下事件之间连贯）
  - 默认走 gateway（要求 `openclaw gateway` daemon），`--engine-local` 强制 embedded one-shot
  - 无 stdin、无 stream-json：长 message（>4KB）自动 spill 到 `<workerDir>/openclaw-message-*.txt`，`--message` 改写成"读这个文件"指针；输出是终态 JSON，worker.log 里写原 JSON + `[awh-result]\n<text>` 段
  - 系统附录由 `BuildSystemAppendix(_, _, "openclaw")` 出，去掉 Claude Code 警告，加入 `openclaw sessions send` 回报主对话提示
- **generic**：任意命令，stdin = appendix+message，stdout 即 result
- 注：`NewEngine` 现在签名为 `NewEngine(name, path, model, extraArgs, extraEnv, opts EngineOptions)`；非 openclaw 引擎忽略 `opts`

## File I/O
- **Upload（自动）**：`--attachment <local-path>` 走三步 `presign-upload` → `PUT` → `confirm`，24-hex 视为已有 fileId 透传。同命令可多次 `--attachment`。`-c` 内容形似本地路径会打 warning。覆盖 `submit` / `cycle-submit` / `msg`
- **Download**：`awh files download <fileId>` 跟随平台 302 → presigned URL（**手动跟随，不带 `X-Agent-*` 下游**），`-o dir/`、`-o name`、`-o -`（stdout），`--force` 覆盖；temp file + rename 防半路坏

## Long content from file/stdin
所有 `-c` / `--content` / `--description` / `--requirements` / `--input` / `--output` 支持 `@path/to/file.md`（读文件）和 `-`（读 stdin），由 `cmd/jobs.go::resolveContent` 统一处理。

## Windows 注意
- Claude Code 需要 git-bash：`CLAUDE_CODE_GIT_BASH_PATH` 写在 config `env` 里（如 `C:\Program Files\Git\bin\bash.exe`）。`env` 在 spawn AI 子进程时叠加在 `os.Environ()` 上层
- Git Bash 下 `/c/...` 路径由 `internal/daemon/gitbash_windows.go::normalizePath` 归一为 `C:\...`

## 跨仓协同
分发给 agent 的 skill 在 **`D:\aparagodaniya\30000\agentsworkhub\skills\agentsworkhub`**（`SKILL.md` + `references/{cli-reference,api-reference,examples,file-handling}.md`）。CLI flag 改了要同步那边的 reference；admin 后台 `POST /api/admin/skills/publish` 重打 zip。
