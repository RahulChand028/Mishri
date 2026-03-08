package observability

import (
	"sync"
	"time"
)

type Role string

const (
	RoleIdle   Role = "IDLE"
	RoleMaster Role = "MASTER"
	RoleSlave  Role = "SLAVE"
)

type SystemStatus struct {
	mu            sync.RWMutex
	CurrentRole   Role
	ActiveTask    string
	LastHeartbeat time.Time

	// Pipeline progress
	TotalAgents     int
	CompletedAgents int
	FailedAgents    int
	ActiveAgentID   int
	ActiveAgentType string

	// Parallel execution
	ParallelCount int // 0 = sequential, >0 = N agents running in parallel

	// Token & Cost tracking (per-session, resets on new task)
	PromptTokens     int
	CompletionTokens int
	TotalCost        float64 // estimated USD cost
	Model            string

	// Timing
	TaskStart time.Time
}

var globalStatus = &SystemStatus{
	CurrentRole:   RoleIdle,
	LastHeartbeat: time.Now(),
}

// SetStatus updates the role and active task label.
func SetStatus(role Role, task string) {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.CurrentRole = role
	globalStatus.ActiveTask = task
}

// GetStatus retrieves a copy of the global system status.
func GetStatus() (Role, string, time.Time) {
	globalStatus.mu.RLock()
	defer globalStatus.mu.RUnlock()
	return globalStatus.CurrentRole, globalStatus.ActiveTask, globalStatus.LastHeartbeat
}

// Heartbeat updates the last heartbeat time.
func Heartbeat() {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.LastHeartbeat = time.Now()
}

// --- Pipeline Progress ---

// SetPipeline updates the agent pipeline state.
func SetPipeline(total, completed, failed int) {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.TotalAgents = total
	globalStatus.CompletedAgents = completed
	globalStatus.FailedAgents = failed
}

// SetActiveAgent sets which agent is currently executing.
func SetActiveAgent(id int, agentType string) {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.ActiveAgentID = id
	globalStatus.ActiveAgentType = agentType
}

// SetParallel marks how many agents are running in parallel (0 = sequential).
func SetParallel(count int) {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.ParallelCount = count
}

// --- Token & Cost Tracking ---

// AddTokens accumulates token usage for the current session.
func AddTokens(prompt, completion int, model string) {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.PromptTokens += prompt
	globalStatus.CompletionTokens += completion
	globalStatus.Model = model
	// Rough cost estimate (adjust rates per model)
	globalStatus.TotalCost += estimateCost(prompt, completion, model)
}

// ResetSession clears per-task counters for a new task.
func ResetSession() {
	globalStatus.mu.Lock()
	defer globalStatus.mu.Unlock()
	globalStatus.TotalAgents = 0
	globalStatus.CompletedAgents = 0
	globalStatus.FailedAgents = 0
	globalStatus.ActiveAgentID = 0
	globalStatus.ActiveAgentType = ""
	globalStatus.ParallelCount = 0
	globalStatus.PromptTokens = 0
	globalStatus.CompletionTokens = 0
	globalStatus.TotalCost = 0
	globalStatus.Model = ""
	globalStatus.TaskStart = time.Now()
}

// GetDashboard returns all dashboard-relevant data in one atomic read.
func GetDashboard() DashboardData {
	globalStatus.mu.RLock()
	defer globalStatus.mu.RUnlock()
	return DashboardData{
		Role:             globalStatus.CurrentRole,
		Task:             globalStatus.ActiveTask,
		LastHeartbeat:    globalStatus.LastHeartbeat,
		TotalAgents:      globalStatus.TotalAgents,
		CompletedAgents:  globalStatus.CompletedAgents,
		FailedAgents:     globalStatus.FailedAgents,
		ActiveAgentID:    globalStatus.ActiveAgentID,
		ActiveAgentType:  globalStatus.ActiveAgentType,
		ParallelCount:    globalStatus.ParallelCount,
		PromptTokens:     globalStatus.PromptTokens,
		CompletionTokens: globalStatus.CompletionTokens,
		TotalCost:        globalStatus.TotalCost,
		Model:            globalStatus.Model,
		TaskStart:        globalStatus.TaskStart,
	}
}

type DashboardData struct {
	Role             Role
	Task             string
	LastHeartbeat    time.Time
	TotalAgents      int
	CompletedAgents  int
	FailedAgents     int
	ActiveAgentID    int
	ActiveAgentType  string
	ParallelCount    int
	PromptTokens     int
	CompletionTokens int
	TotalCost        float64
	Model            string
	TaskStart        time.Time
}

// estimateCost returns approximate USD cost based on model and token counts.
func estimateCost(prompt, completion int, model string) float64 {
	// Default rates (per 1M tokens) — conservative estimates
	promptRate := 0.50    // $0.50 per 1M prompt tokens
	completionRate := 1.5 // $1.50 per 1M completion tokens

	switch {
	case contains(model, "gpt-4"):
		promptRate = 10.0
		completionRate = 30.0
	case contains(model, "gpt-3.5"):
		promptRate = 0.50
		completionRate = 1.50
	case contains(model, "claude"):
		promptRate = 3.0
		completionRate = 15.0
	case contains(model, "gemini"):
		promptRate = 0.35
		completionRate = 1.05
	case contains(model, "deepseek"):
		promptRate = 0.14
		completionRate = 0.28
	}

	cost := (float64(prompt) / 1_000_000 * promptRate) +
		(float64(completion) / 1_000_000 * completionRate)
	return cost
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
