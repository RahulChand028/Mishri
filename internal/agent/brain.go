package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/rahul/mishri/internal/governance"
	"github.com/rahul/mishri/internal/observability"
	"github.com/rahul/mishri/internal/store"
	"github.com/rahul/mishri/internal/tools"
	"github.com/tmc/langchaingo/llms"
)

// Brain defines the core intelligence interface for the agent.
type Brain interface {
	Think(ctx context.Context, chatID string, input string) (string, error)
}

// WorkerBrain is a ReAct agent that handles individual sub-tasks.
type WorkerBrain struct {
	Model      llms.Model
	Registry   *tools.Registry
	History    HistoryStore
	Prompts    *PromptManager
	Governance governance.PolicyEngine
	Logger     *observability.Logger
	MaxIter    int // per-call override: 0 = use default (5)
}

type HistoryStore interface {
	AddMessage(chatID string, role string, content string) error
	GetHistory(chatID string, limit int) ([]llms.MessageContent, error)
	ClearTasks(chatID string) error
	SavePlan(chatID string, input string) (int64, error)
	SyncPlanSteps(planID int64, steps []store.Step) error
	SyncPlanAgents(planID int64, agents []store.Agent) error
	RecordCost(chatID string, model string, promptTokens, completionTokens int) error
	SaveEscalation(esc *store.EscalationState) (int64, error)
	LoadEscalation(id int64) (*store.EscalationState, error)
	GetPendingEscalation(parentChatID string) (*store.EscalationState, error)
	ResolveEscalation(id int64) error
}

func NewWorkerBrain(model llms.Model, registry *tools.Registry, history HistoryStore, prompts *PromptManager, gov governance.PolicyEngine, logger *observability.Logger) *WorkerBrain {
	return &WorkerBrain{
		Model:      model,
		Registry:   registry,
		History:    history,
		Prompts:    prompts,
		Governance: gov,
		Logger:     logger,
	}
}

func (b *WorkerBrain) Think(ctx context.Context, chatID, taskID string, input string, stepID int, allowedTools []string) (string, error) {
	observability.SetStatus(observability.RoleSlave, input)
	defer observability.SetStatus(observability.RoleIdle, "")

	// Add chatID to context for tools that might need it (like Cron)
	ctx = context.WithValue(ctx, "chatID", chatID)

	systemPrompt, err := b.Prompts.GetLeanWorkerPrompt()
	if err != nil {
		log.Printf("Warning: Failed to load lean worker prompt: %v", err)
	}

	// Dynamic Template Replacement
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{STEP_ID}}", fmt.Sprintf("%d", stepID))
	systemPrompt = strings.ReplaceAll(systemPrompt, "{{CHAT_ID}}", chatID)

	// 2. Prepare messages (System Prompt + current input)
	var messages []llms.MessageContent
	if systemPrompt != "" {
		messages = append(messages, llms.MessageContent{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{
				llms.TextPart(systemPrompt),
			},
		})
	}

	messages = append(messages, llms.MessageContent{
		Role: llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{
			llms.TextPart(input),
		},
	})

	// 3. Prepare tools for the LLM (Filter by whitelist)
	whitelist := make(map[string]bool)
	for _, t := range allowedTools {
		whitelist[t] = true
	}

	var llmTools []llms.Tool
	for _, t := range b.Registry.Tools {
		if allowedTools != nil && !whitelist[t.Name()] {
			continue // Skip if not whitelisted
		}
		llmTools = append(llmTools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}

	// Bug 5 fix: Always allow reading and writing the scratchpad, but only add each once.
	hasScratchpad := false
	hasWriteScratchpad := false
	for _, t := range llmTools {
		if t.Function != nil && t.Function.Name == "read_scratchpad" {
			hasScratchpad = true
		}
		if t.Function != nil && t.Function.Name == "write_scratchpad" {
			hasWriteScratchpad = true
		}
	}
	if !hasScratchpad {
		llmTools = append(llmTools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "read_scratchpad",
				Description: "Read the current task scratchpad to see details from previous steps.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		})
	}
	if !hasWriteScratchpad {
		llmTools = append(llmTools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "write_scratchpad",
				Description: "Append detailed data to the task scratchpad for future steps to use. Use this to save the FULL, UNTRUNCATED output from your tools (e.g. all search results, all scraped content, all names and values). Do not summarize — write everything.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{
							"type":        "string",
							"description": "The full data to append to the scratchpad. Include all raw details, names, values, and sources.",
						},
					},
					"required": []string{"content"},
				},
			},
		})
	}

	// 4. Reasoning Loop (ReAct)
	maxSteps := b.MaxIter
	if maxSteps <= 0 {
		maxSteps = 5 // default: 5 iterations
	}
	var finalResponse string

	for i := 0; i < maxSteps; i++ {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "Task cancelled.", ctx.Err()
		default:
		}

		// 4.1 Reasoning Loop Timeout (Each turn has a 2-minute limit)
		turnCtx, turnCancel := context.WithTimeout(ctx, 2*time.Minute)
		resp, err := b.Model.GenerateContent(turnCtx, messages, llms.WithTools(llmTools))
		turnCancel()

		if err != nil {
			if turnCtx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("worker reasoning turn timed out")
			}
			return "", err
		}

		// Log LLM interaction
		b.Logger.LogLLM(chatID, taskID, messages, resp.Choices[0].Content, resp.Choices[0].ToolCalls)

		// Track token costs
		if resp.Choices[0].GenerationInfo != nil {
			if usage, ok := resp.Choices[0].GenerationInfo["Usage"].(map[string]any); ok {
				pTokens, _ := usage["PromptTokens"].(int)
				cTokens, _ := usage["CompletionTokens"].(int)
				_ = b.History.RecordCost(chatID, "default", pTokens, cTokens)
				b.Logger.LogCost(chatID, taskID, pTokens, cTokens, "default")
				observability.AddTokens(pTokens, cTokens, "default")
			}
		}

		choice := resp.Choices[0]

		// Add Assistant's message to history
		var assistantParts []llms.ContentPart
		if choice.Content != "" {
			assistantParts = append(assistantParts, llms.TextContent{Text: choice.Content})
			b.Logger.LogReasoning(chatID, taskID, stepID, choice.Content)
		}
		for _, tc := range choice.ToolCalls {
			assistantParts = append(assistantParts, tc)
		}

		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: assistantParts,
		})

		// If no tool calls, this is the final answer
		if len(choice.ToolCalls) == 0 {
			finalResponse = choice.Content
			break
		}

		// Handle Tool Calls (Observe results)
		for _, tc := range choice.ToolCalls {
			var result string
			if tc.FunctionCall.Name == "read_scratchpad" {
				b.Logger.LogToolCall(chatID, taskID, stepID, "read_scratchpad", "")
				scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)
				data, err := os.ReadFile(scratchPath)
				if err != nil {
					result = fmt.Sprintf("Error reading scratchpad: %v", err)
				} else {
					result = string(data)
				}
				b.Logger.LogToolResult(chatID, taskID, stepID, "read_scratchpad", result)
				log.Printf("[%s][Worker %d] read_scratchpad: %d bytes", chatID, i+1, len(result))
			} else if tc.FunctionCall.Name == "escalate" {
				var args store.EscalationState
				b.Logger.LogToolCall(chatID, taskID, stepID, "escalate", tc.FunctionCall.Arguments)
				if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args); err != nil {
					result = fmt.Sprintf("Error parsing escalate args: %v", err)
				} else {
					// Persist escalation to DB
					args.SubChatID = chatID
					args.Status = "pending"
					_, _ = b.History.SaveEscalation(&args)
					result = "Escalation sent to manager. Waiting for response."
				}
				b.Logger.LogToolResult(chatID, taskID, stepID, "escalate", result)
			} else if tc.FunctionCall.Name == "write_scratchpad" {
				var args struct {
					Content string `json:"content"`
				}
				if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args); err != nil {
					result = fmt.Sprintf("Error parsing write_scratchpad args: %v", err)
				} else {
					b.Logger.LogToolCall(chatID, taskID, stepID, "write_scratchpad", args.Content)
					scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)
					f, ferr := os.OpenFile(scratchPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
					if ferr != nil {
						result = fmt.Sprintf("Error opening scratchpad for write: %v", ferr)
					} else {
						f.WriteString("\n#### Worker Data:\n" + args.Content + "\n")
						f.Close()
						result = fmt.Sprintf("Successfully wrote %d bytes to scratchpad.", len(args.Content))
					}
					b.Logger.LogToolResult(chatID, taskID, stepID, "write_scratchpad", result)
				}
				log.Printf("[%s][Worker %d] write_scratchpad: %d bytes", chatID, i+1, len(args.Content))
			} else {
				tool := b.Registry.Get(tc.FunctionCall.Name)
				if tool == nil {
					result = fmt.Sprintf("Error: Tool %s not found", tc.FunctionCall.Name)
				} else {
					b.Logger.LogToolCall(chatID, taskID, stepID, tool.Name(), tc.FunctionCall.Arguments)
					res, err := b.executeWithRetry(ctx, tool, tc.FunctionCall.Arguments, chatID, i+1)
					if err != nil {
						errMsg := fmt.Sprintf("%v", err)
						if len(errMsg) > 200 {
							errMsg = errMsg[:200] + "..."
						}
						log.Printf("[%s][Worker %d] Tool %s failed: %s", chatID, i+1, tool.Name(), errMsg)
						result = fmt.Sprintf("Error: %s", errMsg)
					} else {
						result = res
					}
					b.Logger.LogToolResult(chatID, taskID, stepID, tool.Name(), result)
					log.Printf("[%s][Worker %d] Tool %s done (%d bytes)", chatID, i+1, tool.Name(), len(result))
				}
			}

			// Truncate long tool results to reduce context bloat
			if len(result) > 1500 {
				result = result[:1500] + "\n... [truncated]"
			}

			// Add tool result to messages for the next turn
			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    result,
					},
				},
			})
		}
	}

	if finalResponse == "" {
		finalResponse = "STATUS: partial\nDONE: Reached maximum reasoning steps before completing the task.\nDATA: \nFAILED: Ran out of reasoning steps\nNEXT: Retry with a more focused prompt or simpler sub-task"
	}

	return finalResponse, nil
}

// ThinkWithSystemPrompt is like Think but accepts a fully pre-built system prompt
// instead of loading from the prompt file. Used by CodeAgent and ReflectionAgent
// when the Manager has crafted a custom prompt for the agent.
func (b *WorkerBrain) ThinkWithSystemPrompt(ctx context.Context, chatID, taskID string, input string, agentID int, allowedTools []string, systemPrompt string) (string, error) {
	// Swap out the prompt loader with the pre-built prompt, then delegate.
	original := b.Prompts
	b.Prompts = &PromptManager{Directory: b.Prompts.Directory, overridePrompt: systemPrompt}
	result, err := b.Think(ctx, chatID, taskID, input, agentID, allowedTools)
	b.Prompts = original
	return result, err
}

// ThinkWithSystemPromptMaxIter is like ThinkWithSystemPrompt but also overrides
// the max iteration count. Goroutine-safe: sets and restores MaxIter within
// the call scope.
func (b *WorkerBrain) ThinkWithSystemPromptMaxIter(ctx context.Context, chatID, taskID string, input string, agentID int, allowedTools []string, systemPrompt string, maxIter int) (string, error) {
	origMaxIter := b.MaxIter
	b.MaxIter = maxIter
	result, err := b.ThinkWithSystemPrompt(ctx, chatID, taskID, input, agentID, allowedTools, systemPrompt)
	b.MaxIter = origMaxIter
	return result, err
}
func (b *WorkerBrain) executeWithRetry(ctx context.Context, tool tools.Tool, args string, chatID string, stepIdx int) (string, error) {
	maxRetries := 3
	backoff := 1 * time.Second

	var lastErr error
	var result string

	for i := 0; i < maxRetries; i++ {
		// 4.1 Check Policy Engine
		req := governance.Request{
			Tool:      tool.Name(),
			Arguments: args,
			ChatID:    chatID,
		}
		policyRes, err := b.Governance.Evaluate(ctx, req)
		if err != nil {
			return "", fmt.Errorf("policy evaluation failed: %v", err)
		}
		if policyRes.Effect == governance.EffectDeny {
			return fmt.Sprintf("Policy Error: %s", policyRes.Reason), nil
		}

		// 4.2 Guarded Execution (Timeout)
		toolTimeout := 30 * time.Second
		if tool.Name() == "browser" {
			toolTimeout = 60 * time.Second // Browser needs more time: startup + navigate + render
		}
		toolCtx, toolCancel := context.WithTimeout(ctx, toolTimeout)
		res, err := tool.Execute(toolCtx, args)
		toolCancel()

		if err == nil {
			return res, nil
		}

		lastErr = err
		result = res // In case tool returns a partial result or specific error message

		if toolCtx.Err() == context.DeadlineExceeded {
			result = fmt.Sprintf("Error: Tool %s timed out after %v", tool.Name(), toolTimeout)
			// Don't retry timeouts usually, or maybe just once? Let's skip retry for timeout for now.
			break
		}

		errMsg := fmt.Sprintf("%v", err)
		if len(errMsg) > 200 {
			errMsg = errMsg[:200] + "... [truncated]"
		}
		log.Printf("[%s][Step %d] Tool %s failed (attempt %d/%d): %s. Retrying in %v...", chatID, stepIdx, tool.Name(), i+1, maxRetries, errMsg, backoff)

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	return result, lastErr
}

// trimOrchContext keeps the system prompt (index 0) and the original user request (index 1)
// plus the most recent maxRecent messages, preventing the orchestration context window
// from growing unboundedly.
func trimOrchContext(msgs []llms.MessageContent, maxRecent int) []llms.MessageContent {
	// We want to keep:
	// 1. System Prompt (index 0)
	// 2. Original User Request (index 1 - usually the first human message)
	// 3. Last N messages (maxRecent)

	if len(msgs) <= 2+maxRecent {
		return msgs
	}

	trimmed := make([]llms.MessageContent, 2)
	trimmed[0] = msgs[0] // System
	trimmed[1] = msgs[1] // User Request

	// Append last maxRecent messages
	start := len(msgs) - maxRecent
	if start < 2 {
		start = 2
	}
	trimmed = append(trimmed, msgs[start:]...)
	return trimmed
}

// MasterBrain orchestrates autonomous agents to fulfill a user request.
type MasterBrain struct {
	Model      llms.Model
	Worker     *WorkerBrain
	History    HistoryStore
	Prompts    *PromptManager
	Logger     *observability.Logger
	Dispatcher *AgentDispatcher
	Gateway    GatewaySender // For sending mid-execution messages (escalation)
	Manager    *ManagerAgent // For resuming escalated sub-managers
}

// GatewaySender is a minimal interface for sending messages to the user.
// Implemented by gateway.Messenger.
type GatewaySender interface {
	Send(chatID string, text string) error
}

func NewMasterBrain(model llms.Model, worker *WorkerBrain, history HistoryStore, prompts *PromptManager, logger *observability.Logger, dispatcher *AgentDispatcher) *MasterBrain {
	return &MasterBrain{
		Model:      model,
		Worker:     worker,
		History:    history,
		Prompts:    prompts,
		Logger:     logger,
		Dispatcher: dispatcher,
	}
}

// SetGateway sets the gateway sender for mid-execution user communication.
func (b *MasterBrain) SetGateway(gw GatewaySender) {
	b.Gateway = gw
}

// SetManager sets the ManagerAgent for handling team-based tasks.
func (b *MasterBrain) SetManager(mgr *ManagerAgent) {
	b.Manager = mgr
}

func (b *MasterBrain) Think(ctx context.Context, chatID string, input string) (string, error) {
	observability.SetStatus(observability.RoleMaster, "Planning...")
	observability.ResetSession()
	defer observability.SetStatus(observability.RoleIdle, "")

	// 0. Check for pending escalation — if user is responding to a Sub-Manager question
	if b.Manager != nil {
		esc, err := b.History.GetPendingEscalation(chatID)
		if err == nil && esc != nil {
			// This message is the user's answer to a pending escalation
			log.Printf("[%s] Found pending escalation (ID: %d), resuming sub-manager", chatID, esc.ID)
			_ = b.History.ResolveEscalation(esc.ID)
			report, err := b.Manager.Resume(ctx, esc, input)
			if err != nil {
				return fmt.Sprintf("Sub-manager resume failed: %v", err), nil
			}
			// Check if the resumed sub-manager escalated again
			if strings.HasPrefix(report, "ESCALATION:") {
				return b.handleEscalationReport(chatID, report)
			}
			b.History.AddMessage(chatID, "human", input)
			b.History.AddMessage(chatID, "ai", report)
			return report, nil
		}
	}

	// 1. Get Planner Prompt
	plannerPrompt, err := b.Prompts.GetPlannerPrompt()
	if err != nil {
		return "", fmt.Errorf("failed to load planner prompt: %v", err)
	}

	// 2. Prepare dynamic tool descriptions from the Worker's registry
	var toolDescriptions []string
	for _, t := range b.Worker.Registry.Tools {
		toolDescriptions = append(toolDescriptions, fmt.Sprintf("- %s: %s", t.Name(), t.Description()))
	}

	toolsList := strings.Join(toolDescriptions, "\n")

	// Dynamic Template Replacement for Master
	fullPlannerPrompt := strings.ReplaceAll(plannerPrompt, "{{CHAT_ID}}", chatID)
	fullPlannerPrompt = fmt.Sprintf("%s\n\n## Available Tools (Slave Capabilities):\n%s", fullPlannerPrompt, toolsList)

	// 3. Load history (for context)
	history, _ := b.History.GetHistory(chatID, 10) // Bug 7 fix: increased from 5 to 10

	// Initialize Scratchpad
	scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)
	_ = os.WriteFile(scratchPath, []byte("# Task Scratchpad\nInitial User Request: "+input+"\n"), 0644)
	defer os.Remove(scratchPath)

	// Save plan to history for tracing.
	planID, _ := b.History.SavePlan(chatID, input)
	taskID := fmt.Sprintf("plan_%d", planID)

	// Build the orchestration context: system prompt + chat history + user request.
	orchContext := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(fullPlannerPrompt)},
		},
	}
	orchContext = append(orchContext, history...)
	orchContext = append(orchContext, llms.MessageContent{
		Role:  llms.ChatMessageTypeHuman,
		Parts: []llms.ContentPart{llms.TextPart(input)},
	})

	// ---- Agent Dispatch Loop ----
	var priorReports []string
	maxIterations := 20 // Safety guard — prevents infinite loop if manager stalls.

	for iteration := 0; iteration < maxIterations; iteration++ {
		// Check for cancellation before each iteration.
		select {
		case <-ctx.Done():
			return "Task cancelled.", ctx.Err()
		default:
		}

		// Ask manager for current/updated agent plan.
		// Keep more history turns to avoid loops.
		orchContext = trimOrchContext(orchContext, 16)
		agentPlan, rawResponse, isDone, planErr := b.plan(ctx, &orchContext, chatID, taskID, 0)
		if planErr != nil {
			return "", fmt.Errorf("planning error: %v", planErr)
		}

		// Manager gave a final text answer — we are done.
		if isDone {
			b.History.AddMessage(chatID, "human", input)
			b.History.AddMessage(chatID, "ai", rawResponse)
			return rawResponse, nil
		}

		if agentPlan == nil {
			return "Planner failed to produce a plan.", nil
		}

		// Persist current agent plan state.
		_ = b.History.SyncPlanAgents(planID, agentPlan.Agents)

		// Update dashboard pipeline progress
		completed, failed := 0, 0
		for _, a := range agentPlan.Agents {
			if a.Status == "completed" {
				completed++
			} else if a.Status == "failed" {
				failed++
			}
		}
		observability.SetPipeline(len(agentPlan.Agents), completed, failed)

		b.Logger.Log(observability.Event{
			Type:   observability.EventTypePlan,
			ChatID: chatID,
			TaskID: taskID,
			Data:   agentPlan,
		})

		// Find pending agents to run — parallel-aware.
		// Collect all pending agents with the same ParallelGroup.
		var pendingBatch []*store.Agent
		batchGroup := -1
		for i := range agentPlan.Agents {
			a := &agentPlan.Agents[i]
			if a.Status != "pending" && a.Status != "" {
				continue
			}
			if batchGroup == -1 {
				batchGroup = a.ParallelGroup
			}
			// Group 0 = sequential: only take one agent
			if batchGroup == 0 {
				pendingBatch = append(pendingBatch, a)
				break
			}
			// Group N > 0: take all pending agents with this group
			if a.ParallelGroup == batchGroup {
				pendingBatch = append(pendingBatch, a)
			} else {
				break // different group — stop collecting
			}
		}

		if len(pendingBatch) == 0 {
			// All agents done — push manager to give the final answer.
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart("All agents have completed. Synthesize their reports and give the user a final answer as plain text.")},
			})
			continue
		}

		// --- Dispatch the batch ---
		if len(pendingBatch) == 1 || batchGroup == 0 {
			// Sequential execution (current behavior — single agent at a time)
			nextAgent := pendingBatch[0]
			log.Printf("[%s][Agent %d/%d] Type=%s Goal=%s", taskID, nextAgent.ID, len(agentPlan.Agents), nextAgent.Type, nextAgent.Goal)
			observability.SetStatus(observability.RoleMaster, fmt.Sprintf("Agent %d (%s): %s", nextAgent.ID, nextAgent.Type, nextAgent.Goal))
			observability.SetActiveAgent(nextAgent.ID, string(nextAgent.Type))
			observability.SetParallel(0)

			systemPrompt := nextAgent.SystemPrompt
			if len(priorReports) > 0 {
				priorContext := "\n\n## Prior Context:\n" + strings.Join(priorReports, "\n---\n")
				if !strings.Contains(systemPrompt, "Prior Context") {
					systemPrompt += priorContext
				}
			}

			b.Logger.LogAgentStart(chatID, taskID, "", "", 0, nextAgent.ID, string(nextAgent.Type), nextAgent.Goal)

			var report string
			dispatchPrompt := systemPrompt
			if nextAgent.Type == store.AgentTypeManager {
				dispatchPrompt = nextAgent.Goal
			}
			if b.Dispatcher != nil {
				report, err = b.Dispatcher.Dispatch(ctx, string(nextAgent.Type), chatID, nextAgent.ID, dispatchPrompt, nextAgent.Tools, b.Logger, chatID, taskID, 0)
			} else {
				// Fallback if no dispatcher
				b.Worker.MaxIter = nextAgent.MaxIterations
				report, err = b.Worker.ThinkWithSystemPrompt(ctx, chatID, taskID, dispatchPrompt, nextAgent.ID, nextAgent.Tools, dispatchPrompt)
			}

			// Check for escalation
			if err == nil && strings.HasPrefix(report, "ESCALATION:") {
				nextAgent.Status = "escalated"
				nextAgent.Report = report
				_ = b.History.SyncPlanAgents(planID, agentPlan.Agents)
				result, escErr := b.handleEscalationReport(chatID, report)
				if escErr != nil {
					return fmt.Sprintf("Escalation handling failed: %v", escErr), nil
				}
				b.History.AddMessage(chatID, "human", input)
				b.History.AddMessage(chatID, "ai", result)
				return result, nil
			}

			if err != nil {
				nextAgent.Status = "failed"
				nextAgent.Report = fmt.Sprintf("Error: %v", err)
			} else {
				nextAgent.Status = "completed"
				nextAgent.Report = report
				priorReports = append(priorReports, fmt.Sprintf("Step %d results:\n%s", nextAgent.ID, report))
				scratchEntry := fmt.Sprintf("\n\n## Step %d Report\n%s\n", nextAgent.ID, report)
				if f, ferr := os.OpenFile(scratchPath, os.O_APPEND|os.O_WRONLY, 0644); ferr == nil {
					_, _ = f.WriteString(scratchEntry)
					f.Close()
				}
			}

			_ = b.History.SyncPlanAgents(planID, agentPlan.Agents)
			b.Logger.LogAgentEnd(chatID, taskID, nextAgent.ID, nextAgent.Status, nextAgent.Report)
			log.Printf("[%s][Agent %d] Status=%s", taskID, nextAgent.ID, nextAgent.Status)

			brief := nextAgent.Report
			if len(brief) > 2000 {
				brief = brief[:2000] + "... [truncated]"
			}
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{llms.TextPart("Research step completed.")},
			})
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart(fmt.Sprintf("Step %d [%s]:\n%s\n\nUpdate the plan or give the final answer. IMPORTANT: Do NOT mention agents, steps, or internal processes in your final answer — respond naturally as Mishri.", nextAgent.ID, nextAgent.Status, brief))},
			})
		} else {
			// ---- Parallel execution: run all same-group agents simultaneously ----
			log.Printf("[%s] Running %d agents in parallel (group %d)", taskID, len(pendingBatch), batchGroup)
			observability.SetStatus(observability.RoleMaster, fmt.Sprintf("Running %d agents in parallel", len(pendingBatch)))
			observability.SetParallel(len(pendingBatch))

			type agentResult struct {
				agent  *store.Agent
				report string
				err    error
			}
			resultsCh := make(chan agentResult, len(pendingBatch))

			for _, a := range pendingBatch {
				go func(agent *store.Agent) {
					b.Logger.LogAgentStart(chatID, taskID, "", "", 0, agent.ID, string(agent.Type), agent.Goal)

					sp := agent.SystemPrompt
					if len(priorReports) > 0 {
						priorContext := "\n\n## Prior Context:\n" + strings.Join(priorReports, "\n---\n")
						if !strings.Contains(sp, "Prior Context") {
							sp += priorContext
						}
					}

					var r string
					var e error
					if b.Dispatcher != nil {
						r, e = b.Dispatcher.Dispatch(ctx, string(agent.Type), chatID, agent.ID, sp, agent.Tools, b.Logger, chatID, taskID, 0)
					} else {
						r, e = b.Worker.ThinkWithSystemPromptMaxIter(ctx, chatID, taskID, sp, agent.ID, agent.Tools, sp, agent.MaxIterations)
					}
					resultsCh <- agentResult{agent: agent, report: r, err: e}
				}(a)
			}

			// Collect all results
			var batchSummary strings.Builder
			for range pendingBatch {
				res := <-resultsCh
				if res.err != nil {
					res.agent.Status = "failed"
					res.agent.Report = fmt.Sprintf("Error: %v", res.err)
				} else {
					res.agent.Status = "completed"
					res.agent.Report = res.report
					priorReports = append(priorReports, fmt.Sprintf("Step %d results:\n%s", res.agent.ID, res.report))
					scratchEntry := fmt.Sprintf("\n\n## Step %d Report\n%s\n", res.agent.ID, res.report)
					if f, ferr := os.OpenFile(scratchPath, os.O_APPEND|os.O_WRONLY, 0644); ferr == nil {
						_, _ = f.WriteString(scratchEntry)
						f.Close()
					}
				}
				brief := res.agent.Report
				if len(brief) > 500 {
					brief = brief[:500] + "... [truncated]"
				}
				batchSummary.WriteString(fmt.Sprintf("Step %d [%s]: %s\n---\n", res.agent.ID, res.agent.Status, brief))
				// Log agent end
				b.Logger.LogAgentEnd(chatID, taskID, res.agent.ID, res.agent.Status, res.agent.Report)
				log.Printf("[%s][Agent %d] Status=%s (parallel)", taskID, res.agent.ID, res.agent.Status)
			}

			_ = b.History.SyncPlanAgents(planID, agentPlan.Agents)

			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeAI,
				Parts: []llms.ContentPart{llms.TextPart("Parallel research completed.")},
			})
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart(fmt.Sprintf("Results:\n%s\nUpdate the plan or give the final answer. IMPORTANT: Do NOT mention agents, steps, or internal processes in your final answer — respond naturally as Mishri.", batchSummary.String()))},
			})
		}
	}

	return "I've reached the maximum number of planning iterations. The task may be too complex — please try a simpler request.", nil
}

func (b *MasterBrain) plan(ctx context.Context, messages *[]llms.MessageContent, chatID, taskID string, depth int) (*store.AgentPlan, string, bool, error) {
	if depth > 3 {
		return nil, "", false, fmt.Errorf("master planning exceeded maximum tool recursion depth (3)")
	}
	plannerTools := []llms.Tool{
		{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "propose_plan",
				Description: "Submit or update a plan of autonomous agents to execute the user's task. Each agent runs to completion and reports back.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"agents": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id": map[string]any{
										"type": "integer",
									},
									"type": map[string]any{
										"type": "string",
										"enum": []string{"react", "code", "reflection", "manager"},
									},
									"goal": map[string]any{
										"type": "string",
									},
									"system_prompt": map[string]any{
										"type": "string",
									},
									"tools": map[string]any{
										"type":  "array",
										"items": map[string]any{"type": "string"},
									},
									"status": map[string]any{
										"type": "string",
										"enum": []string{"pending", "completed", "failed"},
									},
									"parallel_group": map[string]any{
										"type":        "integer",
										"description": "Optional. Agents with the same non-zero group run in parallel. Default 0 = sequential.",
									},
									"max_iterations": map[string]any{
										"type":        "integer",
										"description": "Optional. Max ReAct loop iterations for this agent. Default 0 = use system default (5). Set lower (2-3) for simple tasks, higher (8-10) for complex research.",
									},
								},
								"required": []string{"id", "type", "goal", "system_prompt", "tools", "status"},
							},
						},
					},
					"required": []string{"agents"},
				},
			},
		},
	}

	// Planning Turn Timeout (2-minute limit)
	turnCtx, turnCancel := context.WithTimeout(ctx, 2*time.Minute)
	resp, err := b.Model.GenerateContent(turnCtx, *messages, llms.WithTools(plannerTools))
	turnCancel()

	if err != nil {
		if turnCtx.Err() == context.DeadlineExceeded {
			return nil, "", false, fmt.Errorf("master planning turn timed out")
		}
		return nil, "", false, err
	}

	// Log LLM interaction
	b.Logger.LogLLM(chatID, "planning", *messages, resp.Choices[0].Content, resp.Choices[0].ToolCalls)

	// Track token costs
	if resp.Choices[0].GenerationInfo != nil {
		if usage, ok := resp.Choices[0].GenerationInfo["Usage"].(map[string]any); ok {
			pTokens, _ := usage["PromptTokens"].(int)
			cTokens, _ := usage["CompletionTokens"].(int)
			_ = b.History.RecordCost(chatID, "default", pTokens, cTokens)
			b.Logger.LogCost(chatID, "", pTokens, cTokens, "default")
			observability.AddTokens(pTokens, cTokens, "default")
		}
	}

	choice := resp.Choices[0]

	// Add AI message to conversation context
	var assistantParts []llms.ContentPart
	if choice.Content != "" {
		assistantParts = append(assistantParts, llms.TextContent{Text: choice.Content})
	}
	for _, tc := range choice.ToolCalls {
		assistantParts = append(assistantParts, tc)
	}
	*messages = append(*messages, llms.MessageContent{
		Role:  llms.ChatMessageTypeAI,
		Parts: assistantParts,
	})

	// Bug 6 fix: Two-pass tool call handling.
	// Pass 1: respond to all read_scratchpad calls before handling propose_plan.
	hadScratchpadCall := false
	for _, tc := range choice.ToolCalls {
		if tc.FunctionCall.Name == "read_scratchpad" {
			hadScratchpadCall = true
			b.Logger.LogToolCall(chatID, taskID, 0, "read_scratchpad", "")
			scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)
			data, _ := os.ReadFile(scratchPath)
			result := string(data)
			if result == "" {
				result = "Scratchpad is empty or doesn't exist yet."
			}
			b.Logger.LogToolResult(chatID, taskID, 0, "read_scratchpad", result)
			*messages = append(*messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    result,
					},
				},
			})
		}
	}

	// Pass 2: handle propose_plan (takes priority — return immediately if found).
	for _, tc := range choice.ToolCalls {
		if tc.FunctionCall.Name == "propose_plan" {
			var agentPlan store.AgentPlan
			b.Logger.LogToolCall(chatID, taskID, 0, "propose_plan", tc.FunctionCall.Arguments)
			if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &agentPlan); err != nil {
				return nil, "", false, fmt.Errorf("failed to parse propose_plan arguments: %v", err)
			}
			b.Logger.LogToolResult(chatID, taskID, 0, "propose_plan", "Plan received.")
			// Add a synthetic tool response to keep message history consistent.
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
			return &agentPlan, "", false, nil
		}
	}

	// If only read_scratchpad was called, recurse for the next planning turn.
	if hadScratchpadCall {
		return b.plan(ctx, messages, chatID, taskID, depth+1)
	}

	if choice.Content != "" {
		return nil, choice.Content, true, nil
	}

	return nil, "", false, fmt.Errorf("planner failed to provide a plan or text response")
}

// handleEscalationReport parses an ESCALATION: report from a ManagerAgent
// and formats it as a message to the user.
func (b *MasterBrain) handleEscalationReport(chatID string, report string) (string, error) {
	escJSON := strings.TrimPrefix(report, "ESCALATION:")
	var escResult EscalationResult
	if err := json.Unmarshal([]byte(escJSON), &escResult); err != nil {
		return fmt.Sprintf("Sub-manager needs your input but the request was malformed: %s", escJSON), nil
	}

	// Format a user-friendly message
	var msg strings.Builder
	msg.WriteString("🤖 **Team needs your input:**\n\n")
	msg.WriteString(escResult.Question)
	if len(escResult.Options) > 0 {
		msg.WriteString("\n\n**Options:**\n")
		for i, opt := range escResult.Options {
			msg.WriteString(fmt.Sprintf("%d. %s\n", i+1, opt))
		}
		msg.WriteString("\nPlease reply with your choice.")
	}

	return msg.String(), nil
}
