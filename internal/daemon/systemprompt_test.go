package daemon

import (
	"strings"
	"testing"
)

func TestBuildSystemAppendix_ClaudeIncludesAgentToolWarning(t *testing.T) {
	got := BuildSystemAppendix("alice", "https://example.com", "claude")
	if !strings.Contains(got, "Claude Code's built-in Agent tool") {
		t.Errorf("expected Claude-specific Agent tool warning, got:\n%s", got)
	}
	if strings.Contains(got, "OpenClaw") {
		t.Errorf("Claude appendix should not mention OpenClaw, got:\n%s", got)
	}
	if !strings.Contains(got, "alice") {
		t.Errorf("expected agent name in appendix, got:\n%s", got)
	}
	if !strings.Contains(got, "https://example.com") {
		t.Errorf("expected baseURL in appendix, got:\n%s", got)
	}
}

func TestBuildSystemAppendix_ClaudeCodeAlias(t *testing.T) {
	got := BuildSystemAppendix("a", "u", "claude-code")
	if !strings.Contains(got, "Claude Code's built-in Agent tool") {
		t.Errorf("claude-code alias should behave like claude, got:\n%s", got)
	}
}

func TestBuildSystemAppendix_DefaultsToClaude(t *testing.T) {
	// Empty engine name → backward-compat default to claude branch.
	got := BuildSystemAppendix("a", "u", "")
	if !strings.Contains(got, "Claude Code's built-in Agent tool") {
		t.Errorf("empty engineName should default to claude, got:\n%s", got)
	}
}

func TestBuildSystemAppendix_OpenClawIncludesReportBackHints(t *testing.T) {
	got := BuildSystemAppendix("ops-bot", "https://api.test", "openclaw")
	if !strings.Contains(got, "OpenClaw") {
		t.Errorf("expected OpenClaw-specific guidance, got:\n%s", got)
	}
	// Both real reporting paths must be mentioned so the agent can pick
	// the one that fits the user's main conversation channel.
	if !strings.Contains(got, "openclaw message send") {
		t.Errorf("expected `openclaw message send` hint, got:\n%s", got)
	}
	if !strings.Contains(got, "openclaw agent --session-id") {
		t.Errorf("expected `openclaw agent --session-id` hint, got:\n%s", got)
	}
	if strings.Contains(got, "Claude Code's built-in Agent tool") {
		t.Errorf("openclaw appendix should not include Claude Code warning, got:\n%s", got)
	}
	if !strings.Contains(got, "ops-bot") {
		t.Errorf("expected agent name, got:\n%s", got)
	}
}

func TestBuildSystemAppendix_GenericOmitsBothEngineHints(t *testing.T) {
	// codex / generic: no engine-specific extra block, but core awh CLI
	// guidance must still be present.
	got := BuildSystemAppendix("a", "u", "codex")
	if strings.Contains(got, "Claude Code's built-in Agent tool") {
		t.Errorf("codex appendix should not include Claude warning, got:\n%s", got)
	}
	if strings.Contains(got, "OpenClaw") {
		t.Errorf("codex appendix should not include OpenClaw hints, got:\n%s", got)
	}
	if !strings.Contains(got, "awh") {
		t.Errorf("codex appendix should still introduce the awh CLI, got:\n%s", got)
	}
	if !strings.Contains(got, "AgentsWorkhub") {
		t.Errorf("codex appendix should still describe the platform, got:\n%s", got)
	}
}

func TestBuildSystemAppendix_CaseInsensitiveEngineName(t *testing.T) {
	got := BuildSystemAppendix("a", "u", "OPENCLAW")
	if !strings.Contains(got, "OpenClaw") {
		t.Errorf("engine name match should be case-insensitive, got:\n%s", got)
	}
}
