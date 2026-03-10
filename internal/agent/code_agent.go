package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/rahul/mishri/internal/observability"
	"github.com/tmc/langchaingo/llms"
)

// CodeAgent writes a Python or shell script to accomplish data-heavy tasks,
// executes it via the shell tool, and iterates on errors.
// Best for: file parsing, data extraction, calculations, ETL tasks.
type CodeAgent struct {
	model  llms.Model
	worker *WorkerBrain
	logger *observability.Logger
}

func NewCodeAgent(model llms.Model, worker *WorkerBrain, logger *observability.Logger) *CodeAgent {
	return &CodeAgent{model: model, worker: worker, logger: logger}
}

// Run executes the CodeAgent. It delegates to WorkerBrain but with a targeted system
// prompt that instructs the agent to write and execute code. The shell tool handles execution.
func (a *CodeAgent) Run(ctx context.Context, chatID string, agentID int, systemPrompt string, tools []string, parentChatID, parentTaskID string, parentAgentID int, maxIterations int) (string, error) {
	observability.SetStatus(observability.RoleSlave, fmt.Sprintf("[CODE] Agent %d", agentID))
	defer observability.SetStatus(observability.RoleIdle, "")

	log.Printf("[Agent %d][CODE] Starting", agentID)

	// Ensure shell is available — code agents need it
	hasShell := false
	for _, t := range tools {
		if t == "shell" {
			hasShell = true
			break
		}
	}
	if !hasShell {
		tools = append(tools, "shell")
	}

	// Inject code-specific instructions into system prompt
	fullPrompt := systemPrompt + "\n\n" + codeAgentInstructions

	taskMessage := fmt.Sprintf(
		"Execute your task using code. Write a script, run it with the shell tool, observe the output, and iterate if needed.\n\n"+
			"When finished, return a structured report:\n\n%s",
		reportFormatGuide,
	)

	result, err := a.worker.ThinkWithSystemPromptMaxIter(ctx, chatID, parentTaskID, taskMessage, agentID, tools, fullPrompt, maxIterations)
	if err != nil {
		return buildReport("failed", "", "", err.Error(), "Retry with a different script"), nil
	}
	if result == "" {
		return buildReport("failed", "", "", "Agent returned empty response", "Retry"), nil
	}

	if strings.Contains(result, "STATUS:") {
		return result, nil
	}
	return buildReport("success", result, "", "", ""), nil
}

const codeAgentInstructions = `## Code Agent Instructions

You accomplish tasks by writing and executing code. Your workflow:
1. Analyze what data or processing is needed
2. Write a Python script (preferred) or shell script to do it
3. Execute it with the shell tool: {"action": "exec", "command": "python3 -c '...'"} or a temp file
4. Read the output, fix any errors, and re-run as needed
5. Extract the important result from the output

Guidelines:
- Prefer python3 for data parsing/analysis, bash for file ops and simple commands
- Write scripts to a temp file if they're multi-line: use shell to write then execute
- Always print your result to stdout — that is what you'll capture
- If a script fails, read the error carefully and fix it before retrying`
