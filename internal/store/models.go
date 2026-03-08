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
	AgentTypeManager    AgentType = "manager"
)

// Agent represents an autonomous agent spawned by the Manager to complete a phase of work.
// Unlike a Step (atomic one-liner), an Agent receives a full system prompt and runs
// its own internal ReAct/Code/Reflection loop until completion.
type Agent struct {
	ID            int       `json:"id"`
	Type          AgentType `json:"type"`           // "react" | "code" | "reflection" | "manager"
	Goal          string    `json:"goal"`           // Short description of this agent's objective
	SystemPrompt  string    `json:"system_prompt"`  // Full prompt crafted by the Manager
	Tools         []string  `json:"tools"`          // Allowed tools for this agent
	Status        string    `json:"status"`         // pending | running | completed | failed
	Report        string    `json:"report"`         // Structured report returned by the agent
	ParallelGroup int       `json:"parallel_group"` // 0 = sequential (default), N = run with same-group agents
	MaxIterations int       `json:"max_iterations"` // 0 = default (5), N = max ReAct loop iterations
}

// AgentPlan represents a pipeline of autonomous agents spawned for a task.
type AgentPlan struct {
	Agents []Agent `json:"agents"`
}

// EscalationState captures the full state of a Sub-Manager when it pauses
// for user input. This is persisted to SQLite so the Sub-Manager can resume
// after the user responds.
type EscalationState struct {
	ID              int64  `json:"id"`               // Auto-generated DB ID
	ParentChatID    string `json:"parent_chat_id"`   // Original user's chatID
	SubChatID       string `json:"sub_chat_id"`      // Sub-Manager's workspace chatID
	PlanID          int64  `json:"plan_id"`          // Sub-Manager's plan in SQLite
	Goal            string `json:"goal"`             // Original goal given to Sub-Manager
	CompletedAgents string `json:"completed_agents"` // JSON of completed agents with reports
	PendingAgents   string `json:"pending_agents"`   // JSON of agents not yet run
	Question        string `json:"question"`         // What to ask the user
	Options         string `json:"options"`          // JSON array of optional choices
	ParentAgentID   int    `json:"parent_agent_id"` // ID of the manager agent in the parent plan
	ParentTaskID    string `json:"parent_task_id"`  // Task ID of the parent plan
	Status          string `json:"status"`          // "pending" | "answered" | "expired"
}
