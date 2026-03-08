package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/rahul/mishri/internal/observability"
)

// ReactAgent is a ReAct (Reason + Act) agent that interleaves thinking with tool calls.
// It runs its own internal loop until the task is complete or max iterations are reached.
type ReactAgent struct {
	worker *WorkerBrain
	logger *observability.Logger
}

func NewReactAgent(worker *WorkerBrain, logger *observability.Logger) *ReactAgent {
	return &ReactAgent{worker: worker, logger: logger}
}

// Run executes the agent with the given fully-crafted system prompt and returns
// a structured report in the format: STATUS / DONE / DATA / FAILED / NEXT.
func (a *ReactAgent) Run(ctx context.Context, chatID string, agentID int, systemPrompt string, tools []string, parentChatID, parentTaskID string, parentAgentID int) (string, error) {
	observability.SetStatus(observability.RoleSlave, fmt.Sprintf("[REACT] Agent %d", agentID))
	defer observability.SetStatus(observability.RoleIdle, "")

	log.Printf("[Agent %d][REACT] Starting", agentID)

	// Inject report reminder into the system prompt before passing to workerBrain.
	enrichedPrompt := systemPrompt + "\n\n" + reactReportReminder

	// Include output format instruction as the user message
	taskMessage := fmt.Sprintf(
		"Execute your task as described in your system prompt.\n\n"+
			"When finished, respond with a structured report:\n\n%s",
		reportFormatGuide,
	)

	// Use ThinkWithSystemPrompt so the enriched prompt overrides worker_lean.md
	result, err := a.worker.ThinkWithSystemPrompt(ctx, chatID, parentTaskID, taskMessage, agentID, tools, enrichedPrompt)
	if err != nil {
		return buildReport("failed", "", "", err.Error(), "Retry with a different approach"), nil
	}
	if result == "" {
		return buildReport("failed", "", "", "Agent returned an empty response", "Retry"), nil
	}

	// If it already has a STATUS: line, return as-is (agent formatted it properly).
	if strings.Contains(result, "STATUS:") {
		return result, nil
	}

	// Wrap a plain response into report format.
	return buildReport("success", result, "", "", ""), nil
}

const reactReportReminder = `When you have finished your task, end your response with a structured report:

STATUS: success | partial | failed
DONE: What you accomplished
DATA: Key data, URLs, values, or results collected
FAILED: What didn't work (leave blank if nothing failed)
NEXT: Suggested next action for the manager (leave blank if task is complete)`

const reportFormatGuide = `STATUS: success | partial | failed
DONE: [what was accomplished]
DATA: [key data, URLs, values, or results]
FAILED: [what didn't work]
NEXT: [suggested next action, or blank if complete]`

func buildReport(status, done, data, failed, next string) string {
	return fmt.Sprintf("STATUS: %s\nDONE: %s\nDATA: %s\nFAILED: %s\nNEXT: %s",
		status, done, data, failed, next)
}
