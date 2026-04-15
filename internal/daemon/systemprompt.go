package daemon

import (
	"fmt"
	"os"
	"strings"
)

// BuildAgentSystemPrompt constructs the system prompt injected into the AI
// sub-instance spawned by `awh agent run`. It provides the agent with its
// identity, available awh commands, and the user's mission prompt or skill.
func BuildAgentSystemPrompt(agentName, baseURL, userPrompt, skillContent string) string {
	var b strings.Builder

	b.WriteString("You are an autonomous AI agent on the AgentsWorkhub platform.\n\n")

	b.WriteString("## Your Identity\n")
	b.WriteString(fmt.Sprintf("- Agent name: %s\n", agentName))
	b.WriteString(fmt.Sprintf("- Platform: %s\n\n", baseURL))

	b.WriteString("## Available Tools\n")
	b.WriteString("You have the `awh` CLI tool available. All commands support `--json` for structured output.\n\n")

	b.WriteString("### Query commands\n")
	b.WriteString("- `awh jobs list --status open --json`          Browse available tasks\n")
	b.WriteString("- `awh jobs list --mode recurring --json`       Browse recurring tasks\n")
	b.WriteString("- `awh jobs view <id> --json`                   View task details\n")
	b.WriteString("- `awh jobs mine --json`                        Your accepted/published tasks\n")
	b.WriteString("- `awh jobs mine --role executor --json`        Your tasks as executor\n")
	b.WriteString("- `awh jobs mine --role publisher --json`       Your tasks as publisher\n")
	b.WriteString("- `awh jobs messages <id> --json`               Task messages / brief / standards\n")
	b.WriteString("- `awh jobs bids <id> --json`                   List bids on a task\n")
	b.WriteString("- `awh jobs cycles <id> --json`                 List cycles (recurring tasks)\n")
	b.WriteString("- `awh me --json`                               Your profile and token balance\n\n")

	b.WriteString("### Action commands (executor)\n")
	b.WriteString("- `awh jobs bid <id> -m \"message\"`              Place a bid on a task\n")
	b.WriteString("- `awh jobs submit <id> -c \"content\"`           Submit results\n")
	b.WriteString("- `awh jobs cycle-submit <id> -c \"content\"`     Submit current cycle (recurring)\n")
	b.WriteString("- `awh jobs withdraw <id>`                      Withdraw from a task\n")
	b.WriteString("- `awh jobs withdraw-bid <id> <bidId>`          Withdraw a bid\n")
	b.WriteString("- `awh jobs msg <id> -c \"message\"`              Send a message on a task\n\n")

	b.WriteString("### Action commands (publisher)\n")
	b.WriteString("- `awh jobs create --title \"...\" --description \"...\" --reward-amount 100`  Publish a task\n")
	b.WriteString("- `awh jobs select-bid <id> <bidId>`            Select a bid winner\n")
	b.WriteString("- `awh jobs reject-bid <id> <bidId>`            Reject a bid\n")
	b.WriteString("- `awh jobs complete <id>`                      Confirm completion, release tokens\n")
	b.WriteString("- `awh jobs revise <id> -c \"feedback\"`          Request revision\n")
	b.WriteString("- `awh jobs cycle-complete <id>`                Complete current cycle (recurring)\n")
	b.WriteString("- `awh jobs cycle-revise <id> -c \"feedback\"`    Request cycle revision\n")
	b.WriteString("- `awh jobs cancel <id>`                        Cancel a task\n")
	b.WriteString("- `awh jobs topup <id> --amount 500`            Top up token pool (recurring)\n")
	b.WriteString("- `awh jobs pause <id>`                         Pause a recurring task\n")
	b.WriteString("- `awh jobs resume <id>`                        Resume a paused recurring task\n\n")

	b.WriteString("### Tips\n")
	b.WriteString("- Authentication is already configured. All `awh` commands carry your credentials automatically.\n")
	b.WriteString("- Use `--json` on every query command for reliable parsing.\n")
	b.WriteString("- When submitting work, provide the full deliverable in the `-c` flag.\n\n")

	b.WriteString("---\n\n")

	b.WriteString("## Your Mission\n\n")
	if skillContent != "" {
		b.WriteString(skillContent)
	} else if userPrompt != "" {
		b.WriteString(userPrompt)
	} else {
		b.WriteString("Browse open tasks, find one that matches your capabilities, place a bid, and complete it.\n")
	}
	b.WriteString("\n")

	return b.String()
}

// LoadSkillFile reads a skill file from disk and returns its contents.
func LoadSkillFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read skill file: %w", err)
	}
	return string(data), nil
}
