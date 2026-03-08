package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/rahul/mishri/internal/observability"
)

// AgentRunner is the common interface for all autonomous agent types.
// The Manager dispatches to the correct agent type via this interface.
type AgentRunner interface {
	Run(ctx context.Context, chatID string, agentID int, systemPrompt string, tools []string, parentChatID, parentTaskID string, parentAgentID int) (string, error)
}

// AgentDispatcher holds all registered agent types and dispatches by type string.
type AgentDispatcher struct {
	runners map[string]AgentRunner
}

// NewSimpleDispatcher creates an empty dispatcher. Register agent types with Register().
func NewSimpleDispatcher() *AgentDispatcher {
	return &AgentDispatcher{
		runners: map[string]AgentRunner{},
	}
}

// Register adds an agent type runner.
func (d *AgentDispatcher) Register(agentType string, runner AgentRunner) {
	d.runners[agentType] = runner
}

// Dispatch runs the appropriate agent for the given type.
// Falls back to "react" if the type is unknown.
func (d *AgentDispatcher) Dispatch(ctx context.Context, agentType, chatID string, agentID int, systemPrompt string, tools []string, logger *observability.Logger, parentChatID, parentTaskID string, parentAgentID int) (string, error) {
	runner, ok := d.runners[agentType]
	if !ok {
		log.Printf("[Agent %d] Unknown agent type %q, falling back to react", agentID, agentType)
		runner, ok = d.runners["react"]
		if !ok {
			return "", fmt.Errorf("no agent runners registered")
		}
	}

	log.Printf("[Agent %d] Dispatching to %s agent", agentID, agentType)
	result, err := runner.Run(ctx, chatID, agentID, systemPrompt, tools, parentChatID, parentTaskID, parentAgentID)
	if err != nil {
		return "", err
	}

	// Normalize: if no STATUS: line, wrap in report format
	if !strings.Contains(result, "STATUS:") {
		result = buildReport("success", result, "", "", "")
	}
	return result, nil
}
