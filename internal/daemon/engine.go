package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EngineInput is the payload handed to an AI engine. SystemAppendix is appended
// to the engine's built-in system prompt (e.g. via Claude Code's
// `--append-system-prompt`). UserMessage is the first user turn, delivered on
// stdin. WorkDir becomes the child process working directory; on Claude Code
// this also triggers auto-loading of CLAUDE.md.
type EngineInput struct {
	SystemAppendix string
	UserMessage    string
	WorkDir        string
}

// Engine runs an AI tool with a prompt and returns the result text.
type Engine interface {
	Run(ctx context.Context, in EngineInput) (string, error)
	Name() string
}

// StreamingEngine extends Engine with a method that transparently pipes
// the subprocess stdout to the caller (e.g. os.Stdout) instead of parsing
// the output into a result string. Used by `awh agent run`.
type StreamingEngine interface {
	Engine
	RunStreaming(ctx context.Context, in EngineInput, out io.Writer) (*exec.Cmd, error)
}

// EngineOptions carries engine-specific knobs that are only meaningful for
// some engines (currently only OpenClaw). The zero value is the safe default
// for all engines, so callers that don't care about these may pass an empty
// struct (or use NewEngineSimple).
type EngineOptions struct {
	// OpenClawAgentID is the OpenClaw `--agent <id>` value. Required when
	// engine name is "openclaw".
	OpenClawAgentID string
	// OpenClawSessionID is the OpenClaw `--session-id <id>` value. When
	// non-empty, multiple turns sharing the same session id keep context
	// across invocations (one of the main reasons to choose OpenClaw over
	// Claude Code as the worker container).
	OpenClawSessionID string
	// OpenClawLocal forces `openclaw agent --local` (embedded one-shot),
	// bypassing the gateway daemon. Slower per-turn startup but does not
	// require a running gateway.
	OpenClawLocal bool
	// WorkerDir is the on-disk directory where the engine may stash
	// per-invocation artifacts (e.g. an oversized message payload spilled
	// to disk). When empty, the OS temp dir is used.
	WorkerDir string
}

// NewEngine creates the appropriate engine based on the configured name.
// extraEnv is an optional map of KEY→VALUE pairs injected into the child
// process environment on top of the current process's environment; config
// values take highest priority and can override OS-level env vars.
//
// opts carries engine-specific options; it is consulted only by engines that
// understand it (currently OpenClaw). Other engines treat it as a no-op so
// callers may always pass the same options struct regardless of engine.
func NewEngine(name, path, model string, extraArgs []string, extraEnv map[string]string, opts EngineOptions) Engine {
	switch strings.ToLower(name) {
	case "claude", "claude-code":
		return &ClaudeEngine{path: path, model: model, extraArgs: extraArgs, extraEnv: extraEnv}
	case "codex":
		return &CodexEngine{path: path, extraArgs: extraArgs, extraEnv: extraEnv}
	case "openclaw":
		return &OpenClawEngine{
			path:      path,
			agentID:   opts.OpenClawAgentID,
			sessionID: opts.OpenClawSessionID,
			useLocal:  opts.OpenClawLocal,
			workerDir: opts.WorkerDir,
			extraArgs: extraArgs,
			extraEnv:  extraEnv,
		}
	default:
		return &GenericEngine{path: path, extraArgs: extraArgs, extraEnv: extraEnv}
	}
}

// buildEnv constructs a deduplicated environment slice for a child process.
// Priority (highest last, i.e. last writer wins in the map):
//  1. Current process environment (os.Environ)
//  2. Engine-specific vars (model, git-bash path)
//  3. extraEnv from config — overrides everything above
func buildEnv(model string, gitBashPath string, extraEnv map[string]string) []string {
	envMap := make(map[string]string, len(os.Environ())+len(extraEnv)+2)
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			envMap[k] = v
		}
	}
	if model != "" {
		envMap["ANTHROPIC_MODEL"] = model
	}
	if gitBashPath != "" {
		if _, alreadySet := envMap["CLAUDE_CODE_GIT_BASH_PATH"]; !alreadySet {
			envMap["CLAUDE_CODE_GIT_BASH_PATH"] = gitBashPath
		}
	}
	// Config env takes highest priority.
	for k, v := range extraEnv {
		envMap[k] = v
	}
	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}
	return result
}

// fallbackCombine merges a system appendix and a user message into a single
// stdin payload for engines that do not support native system prompt injection
// (Codex, generic). The appendix is clearly delimited so the model can tell
// context from instruction.
func fallbackCombine(appendix, userMessage string) string {
	appendix = strings.TrimSpace(appendix)
	userMessage = strings.TrimSpace(userMessage)
	if appendix == "" {
		return userMessage
	}
	if userMessage == "" {
		return appendix
	}
	var b strings.Builder
	b.WriteString(appendix)
	b.WriteString("\n\n---\n\n")
	b.WriteString(userMessage)
	return b.String()
}

// --- Claude Code Engine ---
// Claude Code outputs stream-json JSONL. The final result is in:
//
//	{"type":"result","subtype":"success","result":"<text>"}
//
// Invocation: claude --print --output-format stream-json --dangerously-skip-permissions
//   --append-system-prompt "<appendix>"
//
// The --print flag (no argument) enables non-interactive mode and reads the
// user prompt from stdin. The appendix is merged with Claude Code's built-in
// agent system prompt (and any CLAUDE.md in the work directory). The prompt
// is written in a goroutine so large prompts do not deadlock against stdout
// reading on Windows.

type ClaudeEngine struct {
	path      string
	model     string
	extraArgs []string
	extraEnv  map[string]string
}

func (e *ClaudeEngine) Name() string { return "claude" }

func (e *ClaudeEngine) buildArgs(appendix string) []string {
	args := []string{"--print", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}
	if strings.TrimSpace(appendix) != "" {
		args = append(args, "--append-system-prompt", appendix)
	}
	args = append(args, e.extraArgs...)
	return args
}

func (e *ClaudeEngine) Run(ctx context.Context, in EngineInput) (string, error) {
	cmd := newCmd(ctx, e.path, e.buildArgs(in.SystemAppendix), in.WorkDir)
	cmd.Env = buildEnv(e.model, resolveGitBashPath(), e.extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	// Write prompt in a goroutine so reading stdout is not blocked on Windows
	// when the pipe buffer fills up before the process starts reading.
	go func() {
		io.WriteString(stdin, in.UserMessage) //nolint:errcheck
		stdin.Close()
	}()

	result, parseErr := parseClaudeOutput(stdout)
	waitErr := cmd.Wait()

	if parseErr != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr != "" {
			return "", fmt.Errorf("%w (stderr: %s)", parseErr, stderr)
		}
		return "", parseErr
	}
	if waitErr != nil {
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr != "" {
			return result, fmt.Errorf("claude exited with error: %w (stderr: %s)", waitErr, stderr)
		}
	}
	return result, nil
}

// RunStreaming spawns Claude Code and pipes its stream-json output directly to
// out (typically os.Stdout). Returns the running Cmd so the caller can manage
// the process lifecycle. The caller should call cmd.Wait() when done.
func (e *ClaudeEngine) RunStreaming(ctx context.Context, in EngineInput, out io.Writer) (*exec.Cmd, error) {
	cmd := newCmd(ctx, e.path, e.buildArgs(in.SystemAppendix), in.WorkDir)
	cmd.Env = buildEnv(e.model, resolveGitBashPath(), e.extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	cmd.Stdout = out
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	go func() {
		io.WriteString(stdin, in.UserMessage) //nolint:errcheck
		stdin.Close()
	}()

	return cmd, nil
}

// claudeJSONLine represents the JSONL lines Claude Code emits.
type claudeJSONLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Result  string `json:"result"`
	Message *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func parseClaudeOutput(r io.Reader) (string, error) {
	var lastAssistantText string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg claudeJSONLine
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "result":
			if msg.Subtype == "success" && msg.Result != "" {
				return msg.Result, nil
			}
		case "assistant":
			if msg.Message != nil {
				for _, c := range msg.Message.Content {
					if c.Type == "text" && c.Text != "" {
						lastAssistantText = c.Text
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return lastAssistantText, fmt.Errorf("read stdout: %w", err)
	}
	if lastAssistantText != "" {
		return lastAssistantText, nil
	}
	return "", fmt.Errorf("no result found in claude output")
}

// --- Codex Engine ---
// OpenAI Codex CLI also outputs JSONL. Final answer in turn.completed or last text event.
// Codex does not have a dedicated `--append-system-prompt` flag here, so the
// system appendix is folded into the stdin payload as a preamble.

type CodexEngine struct {
	path      string
	extraArgs []string
	extraEnv  map[string]string
}

func (e *CodexEngine) Name() string { return "codex" }

func (e *CodexEngine) Run(ctx context.Context, in EngineInput) (string, error) {
	args := append([]string{"--quiet"}, e.extraArgs...)
	cmd := newCmd(ctx, e.path, args, in.WorkDir)
	cmd.Env = buildEnv("", "", e.extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start codex: %w", err)
	}

	payload := fallbackCombine(in.SystemAppendix, in.UserMessage)
	go func() {
		io.WriteString(stdin, payload) //nolint:errcheck
		stdin.Close()
	}()

	result := parseCodexOutput(stdout)
	_ = cmd.Wait()
	if result == "" {
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr != "" {
			return "", fmt.Errorf("no result from codex (stderr: %s)", stderr)
		}
		return "", fmt.Errorf("no result from codex")
	}
	return result, nil
}

// RunStreaming spawns Codex CLI and pipes its output directly to out.
func (e *CodexEngine) RunStreaming(ctx context.Context, in EngineInput, out io.Writer) (*exec.Cmd, error) {
	args := append([]string{"--quiet"}, e.extraArgs...)
	cmd := newCmd(ctx, e.path, args, in.WorkDir)
	cmd.Env = buildEnv("", "", e.extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	cmd.Stdout = out
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex: %w", err)
	}

	payload := fallbackCombine(in.SystemAppendix, in.UserMessage)
	go func() {
		io.WriteString(stdin, payload) //nolint:errcheck
		stdin.Close()
	}()

	return cmd, nil
}

type codexJSONLine struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Output  string `json:"output"`
}

func parseCodexOutput(r io.Reader) string {
	var last string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg codexJSONLine
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Output != "" {
			last = msg.Output
		} else if msg.Content != "" {
			last = msg.Content
		}
	}
	return last
}

// --- Generic Engine ---
// Runs any command. The system appendix (if any) is prepended to the user
// message in a single stdin payload; all stdout is the result.

type GenericEngine struct {
	path      string
	extraArgs []string
	extraEnv  map[string]string
}

func (e *GenericEngine) Name() string { return e.path }

func (e *GenericEngine) Run(ctx context.Context, in EngineInput) (string, error) {
	cmd := newCmd(ctx, e.path, e.extraArgs, in.WorkDir)
	cmd.Env = buildEnv("", "", e.extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start engine %s: %w", e.path, err)
	}

	payload := fallbackCombine(in.SystemAppendix, in.UserMessage)
	go func() {
		io.WriteString(stdin, payload) //nolint:errcheck
		stdin.Close()
	}()

	out, err := io.ReadAll(stdout)
	_ = cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("read output: %w", err)
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr != "" {
			return "", fmt.Errorf("engine returned empty output (stderr: %s)", stderr)
		}
		return "", fmt.Errorf("engine returned empty output")
	}
	return result, nil
}

// newCmd creates an exec.Cmd with the correct platform flags.
// path and workDir are normalized to handle MSYS/Git Bash Unix-style paths
// on Windows (e.g. /c/Users/... → C:\Users\...).
func newCmd(ctx context.Context, path string, args []string, workDir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, normalizePath(path), args...)
	if workDir != "" {
		cmd.Dir = normalizePath(workDir)
	}
	applyNoWindow(cmd)
	return cmd
}

// --- OpenClaw Engine ---
// OpenClaw (https://docs.openclaw.ai) is a personal-assistant gateway whose
// `agent` command also doubles as a competent agent container: it has bash
// and process tools by default, persistent sessions via `--session-id`, and
// isolated agents via `--agent <id>`. We treat it as a peer to Claude Code
// for awh worker purposes.
//
// Invocation differences vs. Claude Code:
//   - `-m, --message <text>` is a required CLI argument; there is NO stdin.
//     Long messages spill to a temp file and the message text becomes a
//     pointer ("payload at <path>, please Read it").
//   - `--json` returns a single terminal payload object, NOT a stream-json
//     log. We collect stdout in full, then both write the raw JSON and a
//     synthetic `[awh-result]\n<text>` block to the caller's writer for
//     parity with Claude's stream-json viewers.
//   - There is NO `--append-system-prompt`. The system appendix is folded
//     into the user message head (Codex-style).
//
// Default mode is gateway: we expect `openclaw gateway` daemon to be
// running and our invocation just dispatches an RPC. Pass useLocal=true to
// force `openclaw agent --local` (embedded one-shot, slower but
// self-contained).

// openClawMessageInlineLimit caps how big a single --message argument may
// grow before we spill it to disk and pass a pointer instead. Picked well
// below Windows cmd's command-line length ceiling so the entire argv
// (including the message) stays comfortably under the limit even with long
// agent ids and extra args.
const openClawMessageInlineLimit = 4096

type OpenClawEngine struct {
	path      string
	agentID   string
	sessionID string
	useLocal  bool
	workerDir string
	extraArgs []string
	extraEnv  map[string]string
}

func (e *OpenClawEngine) Name() string { return "openclaw" }

// buildArgs constructs the openclaw CLI argv. message is the already-prepared
// --message value (either the inline payload or a "payload at <path>" pointer).
func (e *OpenClawEngine) buildArgs(message string) []string {
	args := []string{"agent", "--json"}
	if e.useLocal {
		args = append(args, "--local")
	}
	if e.agentID != "" {
		args = append(args, "--agent", e.agentID)
	}
	if e.sessionID != "" {
		args = append(args, "--session-id", e.sessionID)
	}
	args = append(args, "--message", message)
	args = append(args, e.extraArgs...)
	return args
}

// prepareMessage folds the system appendix into the user message and, if the
// combined payload exceeds openClawMessageInlineLimit, writes it to disk so
// the actual --message value stays short enough for any platform's
// command-line length limit.
//
// Returns (msgArg, cleanup). cleanup deletes the spill file (if any) and is
// always safe to call.
func (e *OpenClawEngine) prepareMessage(systemAppendix, userMessage string) (string, func()) {
	combined := fallbackCombine(systemAppendix, userMessage)
	if len(combined) <= openClawMessageInlineLimit {
		return combined, func() {}
	}

	dir := e.workerDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		// Spill failed; fall back to inline (the OS will reject if too long
		// and we'll surface the error from the spawn).
		return combined, func() {}
	}

	f, err := os.CreateTemp(dir, "openclaw-message-*.txt")
	if err != nil {
		return combined, func() {}
	}
	if _, err := f.WriteString(combined); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return combined, func() {}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return combined, func() {}
	}

	pointer := fmt.Sprintf(
		"Your full instructions are too large to include inline. The complete payload "+
			"has been written to the local file:\n\n  %s\n\nUse your file-reading tool "+
			"(e.g. Read) to load the entire file before deciding what to do. Treat the "+
			"file contents as the authoritative user message for this turn.",
		filepath.ToSlash(f.Name()),
	)
	return pointer, func() { _ = os.Remove(f.Name()) }
}

// openclawJSONResponse models the terminal --json payload from `openclaw
// agent`. Only fields we actually consume are typed; the rest is ignored.
type openclawJSONResponse struct {
	Payloads []struct {
		Text     string `json:"text"`
		MediaURL string `json:"mediaUrl"`
	} `json:"payloads"`
	Meta           map[string]any `json:"meta"`
	DeliveryStatus map[string]any `json:"deliveryStatus"`
	Error          string         `json:"error"`
	Result         struct {
		Payloads []struct {
			Text     string `json:"text"`
			MediaURL string `json:"mediaUrl"`
		} `json:"payloads"`
	} `json:"result"`
}

// extractOpenClawText pulls the concatenated reply text out of a parsed
// openclaw JSON response, handling both top-level `payloads` (embedded run)
// and `result.payloads` (gateway-backed run).
func extractOpenClawText(resp *openclawJSONResponse) string {
	pick := resp.Payloads
	if len(pick) == 0 {
		pick = resp.Result.Payloads
	}
	if len(pick) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range pick {
		if p.Text == "" {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(p.Text)
	}
	return b.String()
}

func (e *OpenClawEngine) Run(ctx context.Context, in EngineInput) (string, error) {
	msg, cleanup := e.prepareMessage(in.SystemAppendix, in.UserMessage)
	defer cleanup()

	cmd := newCmd(ctx, e.path, e.buildArgs(msg), in.WorkDir)
	cmd.Env = buildEnv("", "", e.extraEnv)

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()

	stdoutText := strings.TrimSpace(stdoutBuf.String())
	stderrText := strings.TrimSpace(stderrBuf.String())

	if stdoutText == "" {
		if runErr != nil {
			if stderrText != "" {
				return "", fmt.Errorf("openclaw exited with no output: %w (stderr: %s)", runErr, stderrText)
			}
			return "", fmt.Errorf("openclaw exited with no output: %w", runErr)
		}
		return "", fmt.Errorf("openclaw produced no output")
	}

	var resp openclawJSONResponse
	if err := json.Unmarshal([]byte(stdoutText), &resp); err != nil {
		// Gateway diagnostics may print extra lines on stdout; try to find
		// the last `{...}` block and parse that.
		if jsonStart := strings.LastIndexByte(stdoutText, '{'); jsonStart >= 0 {
			candidate := stdoutText[jsonStart:]
			if err2 := json.Unmarshal([]byte(candidate), &resp); err2 == nil {
				goto parsed
			}
		}
		if stderrText != "" {
			return "", fmt.Errorf("openclaw output is not valid JSON: %w (stderr: %s, output: %s)",
				err, stderrText, truncateForError(stdoutText))
		}
		return "", fmt.Errorf("openclaw output is not valid JSON: %w (output: %s)",
			err, truncateForError(stdoutText))
	}
parsed:
	if resp.Error != "" {
		return "", fmt.Errorf("openclaw error: %s", resp.Error)
	}

	text := extractOpenClawText(&resp)
	if text == "" {
		// Treat empty payloads as a soft success — surface the raw JSON so
		// the caller can debug, mirroring Claude's behaviour when the
		// model returns no assistant text but the run completed.
		return "", fmt.Errorf("openclaw returned no text payloads (output: %s)", truncateForError(stdoutText))
	}
	if runErr != nil {
		// Non-zero exit but parseable JSON with text — surface the warning
		// to caller without dropping the result.
		if stderrText != "" {
			return text, fmt.Errorf("openclaw exited with error: %w (stderr: %s)", runErr, stderrText)
		}
	}
	return text, nil
}

// RunStreaming starts openclaw in a goroutine, collects the terminal JSON,
// and writes a synthetic stream to out so log viewers built for
// stream-json (Claude Code) still see something useful. The returned cmd is
// already-completed by the time the synthetic events are written; callers
// should still call cmd.Wait() to satisfy the StreamingEngine contract,
// though Wait will return immediately.
func (e *OpenClawEngine) RunStreaming(ctx context.Context, in EngineInput, out io.Writer) (*exec.Cmd, error) {
	msg, cleanup := e.prepareMessage(in.SystemAppendix, in.UserMessage)

	cmd := newCmd(ctx, e.path, e.buildArgs(msg), in.WorkDir)
	cmd.Env = buildEnv("", "", e.extraEnv)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// OpenClaw's progress / diagnostics go to stderr; forward them so the
	// worker.log shows what the engine is doing while the JSON aggregates.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("start openclaw: %w", err)
	}

	// Drain stdout in a goroutine, then synthesize a stream-json-ish view
	// for the writer once we have the full payload. Because OpenClaw
	// returns a single JSON document, we cannot stream incrementally.
	go func() {
		defer cleanup()

		raw, readErr := io.ReadAll(stdout)
		// Always wait so the OS reaps the process and cmd.ProcessState is
		// populated before any caller observes our synthetic output.
		_ = cmd.Wait()

		rawTrimmed := strings.TrimSpace(string(raw))
		if rawTrimmed != "" {
			fmt.Fprintf(out, "[awh-engine] openclaw final json:\n%s\n", rawTrimmed)
		}
		if readErr != nil {
			fmt.Fprintf(out, "[awh-engine] openclaw stdout read error: %v\n", readErr)
		}

		var resp openclawJSONResponse
		if rawTrimmed != "" {
			if err := json.Unmarshal([]byte(rawTrimmed), &resp); err != nil {
				if jsonStart := strings.LastIndexByte(rawTrimmed, '{'); jsonStart >= 0 {
					_ = json.Unmarshal([]byte(rawTrimmed[jsonStart:]), &resp)
				}
			}
		}
		if text := extractOpenClawText(&resp); text != "" {
			fmt.Fprintf(out, "[awh-result]\n%s\n", text)
		}
	}()

	return cmd, nil
}

// truncateForError shortens a string for inclusion in error messages so we
// don't dump multi-MB payloads into logs.
func truncateForError(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}
