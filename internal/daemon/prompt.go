package daemon

import (
	"fmt"
	"strings"

	"github.com/agentsworkhub/awh/internal/api"
)

// BuildPrompt constructs the full prompt to send to the AI engine for a task.
// It assembles: system context, task metadata, brief messages, delivery standards,
// and (on revision) the revision request context.
func BuildPrompt(job *api.Job, messages []api.Message, revisionNote string) string {
	var b strings.Builder

	b.WriteString("You are an autonomous AI agent working on the AgentsWorkhub platform.\n")
	b.WriteString("Complete the task below and respond with your final deliverable.\n")
	b.WriteString("Your response will be submitted directly as the task result.\n\n")
	b.WriteString("---\n\n")

	// Task metadata
	b.WriteString(fmt.Sprintf("# Task: %s\n\n", job.Title))

	if job.Description != "" {
		b.WriteString("## Description\n\n")
		b.WriteString(job.Description)
		b.WriteString("\n\n")
	}

	if len(job.Skills) > 0 {
		b.WriteString(fmt.Sprintf("**Required skills:** %s\n\n", strings.Join(job.Skills, ", ")))
	}
	if job.Duration != "" {
		b.WriteString(fmt.Sprintf("**Expected duration:** %s\n\n", job.Duration))
	}

	// Separate messages by type for structured presentation
	var briefMsgs, standardsMsgs, otherMsgs []api.Message
	for _, m := range messages {
		switch m.Type {
		case "brief":
			briefMsgs = append(briefMsgs, m)
		case "standards":
			standardsMsgs = append(standardsMsgs, m)
		default:
			otherMsgs = append(otherMsgs, m)
		}
	}

	if len(briefMsgs) > 0 {
		b.WriteString("## Task Brief\n\n")
		for _, m := range briefMsgs {
			b.WriteString(formatMessage(m))
		}
	}

	if len(standardsMsgs) > 0 {
		b.WriteString("## Delivery Standards\n\n")
		b.WriteString("Your submission MUST meet all of the following standards:\n\n")
		for _, m := range standardsMsgs {
			b.WriteString(formatMessage(m))
		}
	}

	if len(otherMsgs) > 0 {
		b.WriteString("## Additional Context\n\n")
		for _, m := range otherMsgs {
			if m.Type != "delivery" {
				b.WriteString(formatMessage(m))
			}
		}
	}

	// Revision context
	if revisionNote != "" {
		b.WriteString("---\n\n")
		b.WriteString("## ⚠ Revision Request\n\n")
		b.WriteString("Your previous submission was returned for revision. The publisher's feedback:\n\n")
		b.WriteString(revisionNote)
		b.WriteString("\n\n")
		b.WriteString("Please address the feedback above and provide a complete revised submission.\n\n")
	}

	b.WriteString("---\n\n")
	b.WriteString("## Instructions\n\n")
	b.WriteString("- Provide a complete, high-quality deliverable.\n")
	b.WriteString("- If the task requires code, include the full working code.\n")
	b.WriteString("- If the task requires a document or analysis, provide the full content.\n")
	b.WriteString("- Start your response immediately with the deliverable — no preamble needed.\n")

	return b.String()
}

func formatMessage(m api.Message) string {
	var b strings.Builder
	if m.SenderName != "" {
		b.WriteString(fmt.Sprintf("**From %s:**\n", m.SenderName))
	}
	if m.Content != "" {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	if len(m.Attachments) > 0 {
		b.WriteString(fmt.Sprintf("*(%d attachment(s) — download via platform if needed)*\n", len(m.Attachments)))
	}
	b.WriteString("\n")
	return b.String()
}

// ExtractRevisionNote finds the most recent revision_request message content.
func ExtractRevisionNote(messages []api.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type == "revision_request" && messages[i].Content != "" {
			return messages[i].Content
		}
	}
	return ""
}
