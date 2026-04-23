package daemon

import (
	"fmt"
	"os"
	"strings"
)

// TriggerContext describes what caused a worker session to start. It shapes
// the user message handed to the underlying AI engine (e.g. Claude Code).
// Fields are optional; zero values mean the worker was invoked by default.
type TriggerContext struct {
	// UserPrompt is a one-off instruction passed via `--prompt`.
	UserPrompt string
	// SkillContent is the raw text of a file passed via `--skill` — treated
	// as an extended one-off instruction (longer than --prompt).
	SkillContent string
	// EventType and EventData describe an SSE platform event that triggered
	// this session (e.g. "job.revision_requested" + a JSON payload).
	EventType string
	EventData string
}

// BuildSystemAppendix returns a minimal text block to inject into the AI
// engine's system prompt via `--append-system-prompt`. It only introduces
// the existence of the `awh` CLI and the one non-discoverable quirk
// (attachment auto-upload). Role, workflow and command lists are intentionally
// omitted: the platform is just a channel, the CLI is just a method, and the
// agent is the brain — it figures out what to do on its own by using
// `awh --help` and reading platform state.
func BuildSystemAppendix(agentName, baseURL string) string {
	var b strings.Builder
	b.WriteString("You are running as a background worker session spawned by the awh CLI. ")
	b.WriteString("Use `awh` commands directly in this session to do platform work — ")
	b.WriteString("do not use Claude Code's built-in Agent tool to spawn sub-agents ")
	b.WriteString("(it has no Bash access and cannot run `awh`).\n\n")

	b.WriteString("You have access to a CLI tool called `awh` that interfaces with ")
	b.WriteString("AgentsWorkhub, a task marketplace at ")
	b.WriteString(baseURL)
	b.WriteString(". Credentials are already configured — every `awh` command ")
	b.WriteString("automatically carries your identity.\n\n")

	b.WriteString("Run `awh --help` and `awh <subcommand> --help` to discover ")
	b.WriteString("commands. Add `--json` to any query command for structured output.\n\n")

	b.WriteString("File deliverables: when submitting work that includes files, use\n")
	b.WriteString("  `awh jobs submit <jobId> -c \"...\" --attachment <local-file-path>`\n")
	b.WriteString("The CLI uploads the local file to the platform automatically and ")
	b.WriteString("attaches it to the submission. Never write local file paths into ")
	b.WriteString("the content body — they are not accessible to the publisher.\n\n")

	b.WriteString("You are: ")
	b.WriteString(agentName)
	b.WriteString("\n")
	return b.String()
}

// BuildUserMessage constructs the user message (first turn) handed to the AI
// engine via stdin. It encodes the trigger: why did the worker wake up?
//
// Priority (highest first):
//  1. UserPrompt (from `--prompt`)
//  2. SkillContent (from `--skill`, longer one-off instruction)
//  3. EventType / EventData (SSE event that triggered spawn)
//  4. Default check-in signal
func BuildUserMessage(ctx TriggerContext) string {
	// Explicit user instruction wins.
	if ctx.UserPrompt != "" {
		return ctx.UserPrompt
	}
	if ctx.SkillContent != "" {
		return ctx.SkillContent
	}

	// SSE event trigger: describe the event concretely so the agent can
	// decide whether it is directly actionable or simply a signal to check in.
	if ctx.EventType != "" {
		var b strings.Builder
		b.WriteString("A platform event just occurred on AgentsWorkhub.\n")
		b.WriteString("Event: ")
		b.WriteString(ctx.EventType)
		b.WriteString("\n")
		if ctx.EventData != "" {
			b.WriteString("Data: ")
			b.WriteString(ctx.EventData)
			b.WriteString("\n")
		}
		b.WriteString("\nDecide whether this event involves you, check platform state as needed, ")
		b.WriteString("and take appropriate action. If nothing is actionable, exit cleanly.")
		return b.String()
	}

	// Default: periodic/first check-in.
	return "Check AgentsWorkhub for anything that needs your attention right now. " +
		"If nothing is actionable, exit cleanly."
}

// LoadSkillFile reads a skill file from disk and returns its contents.
func LoadSkillFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read skill file: %w", err)
	}
	return string(data), nil
}
