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

// NewEngine creates the appropriate engine based on the configured name.
// extraEnv is an optional map of KEY→VALUE pairs injected into the child
// process environment on top of the current process's environment; config
// values take highest priority and can override OS-level env vars.
func NewEngine(name, path, model string, extraArgs []string, extraEnv map[string]string) Engine {
	switch strings.ToLower(name) {
	case "claude", "claude-code":
		return &ClaudeEngine{path: path, model: model, extraArgs: extraArgs, extraEnv: extraEnv}
	case "codex":
		return &CodexEngine{path: path, extraArgs: extraArgs, extraEnv: extraEnv}
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
func newCmd(ctx context.Context, path string, args []string, workDir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	applyNoWindow(cmd)
	return cmd
}
