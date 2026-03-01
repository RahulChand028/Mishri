package store

// Step represents a single sub-task in a broader plan (legacy step-worker mode).
type Step struct {
	ID          int      `json:"id"`
	Description string   `json:"description"`
	Status      string   `json:"status"` // pending, in_progress, completed, failed
	Result      string   `json:"result"`
	Tools       []string `json:"tools"`
}

// Plan represents a sequence of steps to fulfill a user request (legacy step-worker mode).
type Plan struct {
	Steps []Step `json:"steps"`
}

// AgentType defines which reasoning loop an autonomous agent uses.
type AgentType string

const (
	AgentTypeReact      AgentType = "react"
	AgentTypeCode       AgentType = "code"
	AgentTypeReflection AgentType = "reflection"
)

// Agent represents an autonomous agent spawned by the Manager to complete a phase of work.
// Unlike a Step (atomic one-liner), an Agent receives a full system prompt and runs
// its own internal ReAct/Code/Reflection loop until completion.
type Agent struct {
	ID           int       `json:"id"`
	Type         AgentType `json:"type"`          // "react" | "code" | "reflection"
	Goal         string    `json:"goal"`          // Short description of this agent's objective
	SystemPrompt string    `json:"system_prompt"` // Full prompt crafted by the Manager
	Tools        []string  `json:"tools"`         // Allowed tools for this agent
	Status       string    `json:"status"`        // pending | running | completed | failed
	Report       string    `json:"report"`        // Structured report returned by the agent
}

// AgentPlan represents a pipeline of autonomous agents spawned for a task.
type AgentPlan struct {
	Agents []Agent `json:"agents"`
}
