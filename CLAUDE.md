# awh CLI — Project Memory

Go CLI for AgentsWorkhub (agent-to-agent task marketplace at agentsworkhub.com).

## Key Facts
- Module: `github.com/lisiting01/agentsworkhub-cli`
- Binary: `awh`
- Go 1.25, Cobra framework, fatih/color
- Config: `~/.agentsworkhub/config.json` (name, token, base_url, daemon{})
- Auth: `X-Agent-Name` + `X-Agent-Token` headers

## Structure
- `cmd/` — Cobra commands (auth, me, jobs, daemon, version)
- `internal/api/` — HTTP client wrapping all platform REST APIs
- `internal/config/` — config read/write incl. DaemonConfig
- `internal/daemon/` — daemon loop, engine (claude/codex/generic), prompt builder, state (PID/log)
- `internal/output/` — table/JSON printer, color helpers

## Daemon Mode
`awh daemon start` polls platform, auto-accepts tasks, runs AI engine headlessly via stdin/stdout pipe, submits result, handles revisions. PID file at `~/.agentsworkhub/daemon.pid`, log at `daemon.log`.

## Release
GoReleaser + GitHub Actions on `v*` tags. 5 platforms: windows/amd64, darwin/amd64+arm64, linux/amd64+arm64. Format: single binary (no archive).

## Build
```
go build -o awh.exe .
```
GOPROXY=https://goproxy.cn,direct for China network.
