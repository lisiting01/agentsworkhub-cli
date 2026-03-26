package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Engine runs an AI tool with a prompt and returns the result text.
type Engine interface {
	Run(ctx context.Context, prompt string, workDir string) (string, error)
	Name() string
}

// NewEngine creates the appropriate engine based on the configured name.
func NewEngine(name, path string, extraArgs []string) Engine {
	switch strings.ToLower(name) {
	case "claude", "claude-code":
		return &ClaudeEngine{path: path, extraArgs: extraArgs}
	case "codex":
		return &CodexEngine{path: path, extraArgs: extraArgs}
	default:
		return &GenericEngine{path: path, extraArgs: extraArgs}
	}
}

// --- Claude Code Engine ---
// Claude Code outputs JSONL. The final result is in:
//   {"type":"result","subtype":"success","result":"<text>"}
// Large prompts are sent via stdin, not -p args (avoids Windows cmd length limit).

type ClaudeEngine struct {
	path      string
	extraArgs []string
}

func (e *ClaudeEngine) Name() string { return "claude" }

func (e *ClaudeEngine) Run(ctx context.Context, prompt string, workDir string) (string, error) {
	args := append([]string{"--output-format", "json", "--print"}, e.extraArgs...)
	cmd := newCmd(ctx, e.path, args, workDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	// Send prompt via stdin, then close to signal EOF
	if _, err := io.WriteString(stdin, prompt); err != nil {
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("write prompt: %w", err)
	}
	stdin.Close()

	result, err := parseClaudeOutput(stdout)
	_ = cmd.Wait()
	return result, err
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

type CodexEngine struct {
	path      string
	extraArgs []string
}

func (e *CodexEngine) Name() string { return "codex" }

func (e *CodexEngine) Run(ctx context.Context, prompt string, workDir string) (string, error) {
	args := append([]string{"--quiet"}, e.extraArgs...)
	cmd := newCmd(ctx, e.path, args, workDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start codex: %w", err)
	}

	if _, err := io.WriteString(stdin, prompt); err != nil {
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("write prompt: %w", err)
	}
	stdin.Close()

	result := parseCodexOutput(stdout)
	_ = cmd.Wait()
	if result == "" {
		return "", fmt.Errorf("no result from codex")
	}
	return result, nil
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
// Runs any command. Prompt is written to stdin; all stdout is the result.

type GenericEngine struct {
	path      string
	extraArgs []string
}

func (e *GenericEngine) Name() string { return e.path }

func (e *GenericEngine) Run(ctx context.Context, prompt string, workDir string) (string, error) {
	cmd := newCmd(ctx, e.path, e.extraArgs, workDir)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start engine %s: %w", e.path, err)
	}

	if _, err := io.WriteString(stdin, prompt); err != nil {
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("write prompt: %w", err)
	}
	stdin.Close()

	out, err := io.ReadAll(stdout)
	_ = cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("read output: %w", err)
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
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
