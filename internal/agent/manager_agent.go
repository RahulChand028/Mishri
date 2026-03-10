package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/rahul/mishri/internal/observability"
	"github.com/rahul/mishri/internal/store"
	"github.com/tmc/langchaingo/llms"
)

// ManagerAgent is a Sub-Manager that creates and coordinates its own team of workers.
// It acts like a mini-MasterBrain but can escalate decisions back to the parent MasterBrain.
//
// Flow:
//  1. MasterBrain gives it a goal
//  2. ManagerAgent creates its own team (using LLM planning)
//  3. Dispatches workers sequentially
//  4. If user input is needed, saves state and returns an EscalationResult
//  5. Can be resumed with Resume() after the user responds
type ManagerAgent struct {
	model      llms.Model
	worker     *WorkerBrain
	history    HistoryStore
	prompts    *PromptManager
	logger     *observability.Logger
	dispatcher *AgentDispatcher
}

// EscalationResult is returned when the Sub-Manager needs user input.
// MasterBrain detects this and forwards the question to the user.
type EscalationResult struct {
	EscalationID int64    `json:"escalation_id"`
	Question     string   `json:"question"`
	Options      []string `json:"options,omitempty"`
}

func NewManagerAgent(model llms.Model, worker *WorkerBrain, history HistoryStore, prompts *PromptManager, logger *observability.Logger, dispatcher *AgentDispatcher) *ManagerAgent {
	return &ManagerAgent{
		model:      model,
		worker:     worker,
		history:    history,
		prompts:    prompts,
		logger:     logger,
		dispatcher: dispatcher,
	}
}

// Run implements AgentRunner. This is the entry point when MasterBrain dispatches a manager agent.
func (m *ManagerAgent) Run(ctx context.Context, chatID string, agentID int, systemPrompt string, tools []string, parentChatID, parentTaskID string, parentAgentID int, maxIterations int) (string, error) {
	subChatID := fmt.Sprintf("sub_%s_%d", chatID, agentID)
	goal := systemPrompt // The manager is given the goal as the system prompt by the planner

	log.Printf("[Agent %d][MANAGER] Starting sub-manager for goal: %s", agentID, goal)
	observability.SetStatus(observability.RoleMaster, fmt.Sprintf("[TEAM] Agent %d: %s", agentID, truncate(goal, 60)))
	defer observability.SetStatus(observability.RoleIdle, "")

	return m.execute(ctx, chatID, subChatID, 0, goal, nil, nil, chatID, parentTaskID, agentID, maxIterations)
}

// Resume continues a Sub-Manager's execution after an escalation was answered.
func (m *ManagerAgent) Resume(ctx context.Context, esc *store.EscalationState, answer string) (string, error) {
	log.Printf("[MANAGER] Resuming sub-manager (escalation %d) with answer: %s", esc.ID, truncate(answer, 80))
	observability.SetStatus(observability.RoleMaster, fmt.Sprintf("[TEAM] Resuming: %s", truncate(esc.Goal, 60)))
	defer observability.SetStatus(observability.RoleIdle, "")

	// Parse saved state
	var completedAgents []store.Agent
	if esc.CompletedAgents != "" {
		_ = json.Unmarshal([]byte(esc.CompletedAgents), &completedAgents)
	}

	// Build prior reports from completed agents
	var priorReports []string
	for _, a := range completedAgents {
		priorReports = append(priorReports, fmt.Sprintf("Agent %d (%s): %s", a.ID, a.Type, a.Report))
	}

	// Add the user's answer as a prior report so the planner has it
	priorReports = append(priorReports, fmt.Sprintf("User Decision: %s", answer))

	// Inject the answer into the sub-manager's conversation history
	_ = m.history.AddMessage(esc.SubChatID, "human", fmt.Sprintf("User responded to your escalation: %s", answer))

	history, _ := m.history.GetHistory(esc.SubChatID, 100)

	return m.execute(ctx, esc.ParentChatID, esc.SubChatID, esc.PlanID, esc.Goal, priorReports, history, esc.ParentChatID, esc.ParentTaskID, esc.ParentAgentID, 0)
}

// execute is the core orchestration loop for the Sub-Manager.
// It creates its own team plan, dispatches workers, and handles escalation.
// If existingPlanID > 0, it reuses the plan; otherwise creates a new one.
func (m *ManagerAgent) execute(ctx context.Context, chatID, subChatID string, existingPlanID int64, goal string, priorReports []string, history []llms.MessageContent, realParentChatID, realParentTaskID string, realParentAgentID int, managerMaxIter int) (string, error) {
	// Initialize scratchpad for this sub-team
	scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", subChatID)
	if len(priorReports) == 0 && len(history) == 0 { // Check if it's a fresh run
		// Fresh run — initialize scratchpad
		_ = os.WriteFile(scratchPath, []byte("# Sub-Manager Scratchpad\nGoal: "+goal+"\n"), 0644)
	}
	// Note: scratchpad is NOT defer-removed here because escalation needs it to survive.
	// It's cleaned up when the Sub-Manager finishes (non-escalation exit paths).

	// Save plan or reuse existing
	var planID int64
	if existingPlanID > 0 {
		planID = existingPlanID
	} else {
		planID, _ = m.history.SavePlan(subChatID, goal)
	}
	taskID := fmt.Sprintf("plan_%d", planID)

	// Build the sub-manager's planning prompt
	subManagerPrompt := m.buildSubManagerPrompt(goal)

	// Prepare tool descriptions for the sub-planner
	var toolDescriptions []string
	for _, t := range m.worker.Registry.Tools {
		toolDescriptions = append(toolDescriptions, fmt.Sprintf("- %s: %s", t.Name(), t.Description()))
	}
	toolsList := strings.Join(toolDescriptions, "\n")
	fullPrompt := fmt.Sprintf("%s\n\n## Available Tools for Workers:\n%s", subManagerPrompt, toolsList)

	// Build orchestration context
	orchContext := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(fullPrompt)},
		},
	}

	// Add prior reports context if resuming
	if len(priorReports) > 0 {
		priorContext := "Here is what has been done so far:\n\n" + strings.Join(priorReports, "\n---\n")
		orchContext = append(orchContext, llms.MessageContent{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(priorContext + "\n\nContinue executing your plan. Create agents for the remaining work.")},
		})
	} else {
		orchContext = append(orchContext, llms.MessageContent{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart("Create a team and execute the goal: " + goal)},
		})
	}

	// ---- Sub-Manager Agent Dispatch Loop ----
	if priorReports == nil {
		priorReports = []string{}
	}
	maxIterations := managerMaxIter
	if maxIterations <= 0 {
		maxIterations = 15
	}

	for iteration := 0; iteration < maxIterations; iteration++ {
		select {
		case <-ctx.Done():
			return "Sub-manager task cancelled.", ctx.Err()
		default:
		}

		// Ask sub-manager LLM for plan
		orchContext = trimOrchContext(orchContext, 14)
		agentPlan, rawResponse, isDone, isEscalation, escQuestion, escOptions, planErr := m.plan(ctx, &orchContext, subChatID, taskID, realParentChatID, realParentTaskID, realParentAgentID)

		if planErr != nil {
			os.Remove(scratchPath)
			return buildReport("failed", "", "", fmt.Sprintf("Sub-manager planning error: %v", planErr), ""), nil
		}

		// Sub-manager gave a final answer
		if isDone {
			os.Remove(scratchPath)
			return buildReport("success", rawResponse, "", "", ""), nil
		}

		// Sub-manager wants to escalate to user
		if isEscalation {
			return m.handleEscalation(realParentChatID, realParentTaskID, subChatID, planID, goal, priorReports, escQuestion, escOptions, realParentAgentID)
		}

		if agentPlan == nil {
			os.Remove(scratchPath)
			return buildReport("failed", "", "", "Sub-manager failed to produce a plan", ""), nil
		}

		// Persist plan state
		_ = m.history.SyncPlanAgents(planID, agentPlan.Agents)

		// Find next group of agents to run (supporting parallel execution)
		var agentsToRun []*store.Agent
		firstAgentIdx := -1
		for i := range agentPlan.Agents {
			if agentPlan.Agents[i].Status == "pending" || agentPlan.Agents[i].Status == "" {
				firstAgentIdx = i
				break
			}
		}

		if firstAgentIdx == -1 {
			// All agents done — ask sub-manager for final synthesis
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart("All agents have completed. Synthesize their reports and give the final result as plain text.")},
			})
			continue
		}

		// Group agents with same non-zero parallel_group
		targetGroup := agentPlan.Agents[firstAgentIdx].ParallelGroup
		if targetGroup != 0 {
			for i := range agentPlan.Agents {
				if (agentPlan.Agents[i].Status == "pending" || agentPlan.Agents[i].Status == "") && agentPlan.Agents[i].ParallelGroup == targetGroup {
					agentsToRun = append(agentsToRun, &agentPlan.Agents[i])
				}
			}
		} else {
			agentsToRun = append(agentsToRun, &agentPlan.Agents[firstAgentIdx])
		}

		// Dispatch agents (in parallel if more than one)
		var wg sync.WaitGroup
		results := make(chan struct {
			idx    int
			report string
			err    error
		}, len(agentsToRun))

		for i, agent := range agentsToRun {
			wg.Add(1)
			go func(idx int, a *store.Agent) {
				defer wg.Done()

				iterLimit := a.MaxIterations
				if iterLimit <= 0 {
					iterLimit = 6 // Increase worker default slightly for sub-teams
				}

				// Log start using the Manager's subChatID and its TaskID
				m.logger.LogAgentStart(subChatID, taskID, realParentChatID, realParentTaskID, realParentAgentID, a.ID, string(a.Type), a.Goal)

				workerPrompt := a.SystemPrompt
				if len(priorReports) > 0 {
					priorContext := "\n\n## Prior Reports:\n" + strings.Join(priorReports, "\n---\n")
					if !strings.Contains(workerPrompt, "Prior Reports") {
						workerPrompt += priorContext
					}
				}

				var report string
				var err error
				if m.dispatcher != nil {
					// CRITICAL FIX: Pass subChatID and taskID as parent context so the UI can connect them
					report, err = m.dispatcher.Dispatch(ctx, string(a.Type), subChatID, a.ID, workerPrompt, a.Tools, m.logger, subChatID, taskID, realParentAgentID, iterLimit)
				} else {
					report, err = m.worker.ThinkWithSystemPromptMaxIter(ctx, subChatID, taskID, workerPrompt, a.ID, a.Tools, workerPrompt, iterLimit)
				}
				results <- struct {
					idx    int
					report string
					err    error
				}{idx, report, err}
			}(i, agent)
		}

		wg.Wait()
		close(results)

		// Process results
		for res := range results {
			agent := agentsToRun[res.idx]
			if res.err != nil {
				agent.Status = "failed"
				agent.Report = fmt.Sprintf("Error: %v", res.err)
			} else {
				agent.Status = "completed"
				agent.Report = res.report
				priorReports = append(priorReports, fmt.Sprintf("Agent %d (%s):\n%s", agent.ID, agent.Type, res.report))

				// Persist to scratchpad
				scratchEntry := fmt.Sprintf("\n\n## Worker %d (%s) Report\n%s\n", agent.ID, agent.Type, res.report)
				if f, ferr := os.OpenFile(scratchPath, os.O_APPEND|os.O_WRONLY, 0644); ferr == nil {
					_, _ = f.WriteString(scratchEntry)
					f.Close()
				}
			}
			m.logger.LogAgentEnd(subChatID, taskID, agent.ID, agent.Status, agent.Report)
			_ = m.history.SyncPlanAgents(planID, agentPlan.Agents)

			// Feed result back briefly
			brief := agent.Report
			if len(brief) > 2000 {
				brief = brief[:2000] + "... [truncated]"
			}
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{llms.TextPart(fmt.Sprintf("Worker %d completed.", agent.ID))},
			})
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart(fmt.Sprintf("Worker %d (%s) report [%s]:\n%s\n\nRemaining Goal: %s\n\nIf more workers are needed, call `propose_plan`. If sufficient, provide final response.", agent.ID, agent.Type, agent.Status, brief, goal))},
			})
		}
	}

	os.Remove(scratchPath)
	return buildReport("partial", "", "", "Sub-manager reached maximum iterations", ""), nil
}

// handleEscalation saves the Sub-Manager's state and returns an escalation result.
func (m *ManagerAgent) handleEscalation(parentChatID, parentTaskID, subChatID string, planID int64, goal string, priorReports []string, question string, options []string, parentAgentID int) (string, error) {
	// Build completed agents from prior reports
	var completedAgents []store.Agent
	for i, r := range priorReports {
		completedAgents = append(completedAgents, store.Agent{
			ID:     i + 1,
			Status: "completed",
			Report: r,
		})
	}

	completedJSON, _ := json.Marshal(completedAgents)
	optionsJSON, _ := json.Marshal(options)

	esc := &store.EscalationState{
		ParentChatID:    parentChatID,
		SubChatID:       subChatID,
		PlanID:          planID,
		Goal:            goal,
		CompletedAgents: string(completedJSON),
		PendingAgents:   "[]", // Will be re-planned on resume
		Question:        question,
		Options:         string(optionsJSON),
		ParentAgentID:   parentAgentID,
		Status:          "pending",
	}

	escID, err := m.history.SaveEscalation(esc)
	if err != nil {
		return buildReport("failed", "", "", fmt.Sprintf("Failed to save escalation: %v", err), ""), nil
	}

	log.Printf("[MANAGER] Escalation saved (ID: %d): %s", escID, question)

	// Build the escalation result as a special report
	escResult := EscalationResult{
		EscalationID: escID,
		Question:     question,
		Options:      options,
	}
	escJSON, _ := json.Marshal(escResult)
	return fmt.Sprintf("ESCALATION:%s", string(escJSON)), nil
}

// plan calls the LLM with propose_plan + escalate tools and returns the plan or escalation.
func (m *ManagerAgent) plan(ctx context.Context, messages *[]llms.MessageContent, chatID, taskID, parentChatID, parentTaskID string, parentAgentID int) (*store.AgentPlan, string, bool, bool, string, []string, error) {
	plannerTools := []llms.Tool{
		{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "propose_plan",
				Description: "Submit a plan of workers to execute your goal. Each worker runs to completion and reports back.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agents": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id":             map[string]any{"type": "integer"},
									"type":           map[string]any{"type": "string", "enum": []string{"react", "code", "reflection"}},
									"goal":           map[string]any{"type": "string"},
									"system_prompt":  map[string]any{"type": "string"},
									"tools":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
									"status":         map[string]any{"type": "string", "enum": []string{"pending", "completed", "failed"}},
									"parallel_group": map[string]any{"type": "integer", "description": "Set to same non-zero integer to run agents in parallel."},
									"max_iterations": map[string]any{"type": "integer", "description": "Max ReAct steps. 8-10 for deep research, default 5-6."},
								},
								"required": []string{"id", "type", "goal", "system_prompt"},
							},
						},
					},
					"required": []string{"agents"},
				},
			},
		},
		{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "escalate",
				Description: "Ask the user a question when you need their input to make a decision. Use this when you cannot proceed without user confirmation.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "The question to ask the user",
						},
						"options": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Optional list of choices for the user",
						},
					},
					"required": []string{"question"},
				},
			},
		},
	}

	resp, err := m.model.GenerateContent(ctx, *messages, llms.WithTools(plannerTools))
	if err != nil {
		return nil, "", false, false, "", nil, err
	}

	choice := resp.Choices[0]

	// Add AI message to context
	var assistantParts []llms.ContentPart
	if choice.Content != "" {
		assistantParts = append(assistantParts, llms.TextContent{Text: choice.Content})
		m.logger.LogReasoning(chatID, taskID, 0, choice.Content)
	}
	for _, tc := range choice.ToolCalls {
		assistantParts = append(assistantParts, tc)
	}
	*messages = append(*messages, llms.MessageContent{
		Role:  llms.ChatMessageTypeAI,
		Parts: assistantParts,
	})

	// Handle tool calls
	for _, tc := range choice.ToolCalls {
		if tc.FunctionCall.Name == "propose_plan" {
			var agentPlan store.AgentPlan
			m.logger.LogToolCall(chatID, taskID, 0, "propose_plan", tc.FunctionCall.Arguments)
			if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &agentPlan); err != nil {
				return nil, "", false, false, "", nil, fmt.Errorf("failed to parse propose_plan: %v", err)
			}
			m.logger.LogToolResult(chatID, taskID, 0, "propose_plan", "Plan received.")
			*messages = append(*messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    "Plan received.",
					},
				},
			})

			// Emit plan event for UI
			m.logger.Log(observability.Event{
				Type:          observability.EventTypePlan,
				ChatID:        chatID,
				TaskID:        taskID,
				ParentChatID:  parentChatID,
				ParentTaskID:  parentTaskID,
				ParentAgentID: parentAgentID,
				Data:          &agentPlan,
			})

			return &agentPlan, "", false, false, "", nil, nil
		}

		if tc.FunctionCall.Name == "escalate" {
			var escArgs struct {
				Question string   `json:"question"`
				Options  []string `json:"options"`
			}
			if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &escArgs); err != nil {
				return nil, "", false, false, "", nil, fmt.Errorf("failed to parse escalate args: %v", err)
			}
			m.logger.LogToolCall(chatID, taskID, 0, "escalate", tc.FunctionCall.Arguments)
			// Add tool response to keep context consistent
			*messages = append(*messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    "Escalation sent to user. Waiting for response.",
					},
				},
			})
			m.logger.LogToolResult(chatID, taskID, 0, "escalate", "Escalation sent to user.")
			return nil, "", false, true, escArgs.Question, escArgs.Options, nil
		}
	}

	// No tool calls — this is a final text response
	if choice.Content != "" {
		return nil, choice.Content, true, false, "", nil, nil
	}

	return nil, "", false, false, "", nil, fmt.Errorf("sub-manager planner gave no plan or text response")
}

// buildSubManagerPrompt creates the system prompt for the Sub-Manager's planning LLM.
func (m *ManagerAgent) buildSubManagerPrompt(goal string) string {
	return fmt.Sprintf(`# Sub-Manager

You are a project-level Sub-Manager. Your job is to create and coordinate a team of workers to accomplish the following goal:

**Goal**: %s

## Your Capabilities

1. **propose_plan**: Create a team of workers. Each worker gets a type, goal, system prompt, and tools.
2. **escalate**: Ask the user a question when you need their input (e.g., choosing between options, confirming a decision).

## Worker Types

| Type | Use When |
|------|----------|
| react | Browsing, searching, web navigation, research |
| code | File operations, scripting, data processing |
| reflection | Writing reports, summaries, documentation |

## Rules

1. Create workers using propose_plan. Each worker needs a complete, self-contained system_prompt.
2. Workers run sequentially — each worker's report is available to the next worker.
3. If you need the user to make a decision, use the escalate tool with a clear question and options.
4. After all workers complete, give a final summary as plain text.
5. Do NOT create workers of type "manager" — you cannot nest managers.
6. Feed prior worker reports forward into the next worker's system_prompt.
7. Keep the team small — maximum 5 workers.`, goal)
}

// truncate shortens a string to maxLen characters with "..." suffix.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
