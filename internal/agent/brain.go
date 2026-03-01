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
}

type HistoryStore interface {
	AddMessage(chatID string, role string, content string) error
	GetHistory(chatID string, limit int) ([]llms.MessageContent, error)
	ClearTasks(chatID string) error
	SavePlan(chatID string, input string) (int64, error)
	SyncPlanSteps(planID int64, steps []store.Step) error
	RecordCost(chatID string, model string, promptTokens, completionTokens int) error
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

func (b *WorkerBrain) Think(ctx context.Context, chatID string, input string, stepID int, allowedTools []string) (string, error) {
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
	maxSteps := 10
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
		b.Logger.LogLLM(chatID, "", messages, resp.Choices[0].Content, resp.Choices[0].ToolCalls)

		// Track token costs
		if resp.Choices[0].GenerationInfo != nil {
			if usage, ok := resp.Choices[0].GenerationInfo["Usage"].(map[string]any); ok {
				pTokens, _ := usage["PromptTokens"].(int)
				cTokens, _ := usage["CompletionTokens"].(int)
				_ = b.History.RecordCost(chatID, "default", pTokens, cTokens)
				b.Logger.LogCost(chatID, "", pTokens, cTokens, "default")
			}
		}

		choice := resp.Choices[0]

		// Add Assistant's message to history
		var assistantParts []llms.ContentPart
		if choice.Content != "" {
			assistantParts = append(assistantParts, llms.TextContent{Text: choice.Content})
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
				scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)
				data, err := os.ReadFile(scratchPath)
				if err != nil {
					result = fmt.Sprintf("Error reading scratchpad: %v", err)
				} else {
					result = string(data)
				}
				log.Printf("[%s][Worker Reasoning %d] read_scratchpad result: %d bytes", chatID, i+1, len(result))
			} else if tc.FunctionCall.Name == "write_scratchpad" {
				scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)
				var args struct {
					Content string `json:"content"`
				}
				if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args); err != nil {
					result = fmt.Sprintf("Error parsing write_scratchpad args: %v", err)
				} else {
					f, ferr := os.OpenFile(scratchPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
					if ferr != nil {
						result = fmt.Sprintf("Error opening scratchpad for write: %v", ferr)
					} else {
						f.WriteString("\n#### Worker Data:\n" + args.Content + "\n")
						f.Close()
						result = fmt.Sprintf("Successfully wrote %d bytes to scratchpad.", len(args.Content))
					}
				}
				log.Printf("[%s][Worker Reasoning %d] write_scratchpad: %d bytes", chatID, i+1, len(args.Content))
			} else {
				tool := b.Registry.Get(tc.FunctionCall.Name)
				if tool == nil {
					result = fmt.Sprintf("Error: Tool %s not found", tc.FunctionCall.Name)
				} else {
					res, err := b.executeWithRetry(ctx, tool, tc.FunctionCall.Arguments, chatID, i+1)
					if err != nil {
						log.Printf("[%s][Worker Reasoning %d] Tool %s final failure: %v", chatID, i+1, tool.Name(), err)
						result = fmt.Sprintf("Error: %v", err)
					} else {
						result = res
					}
					log.Printf("[%s][Worker Reasoning %d] Tool %s result: %s", chatID, i+1, tool.Name(), result)
				}
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
		finalResponse = "Thinking too much... I've reached the maximum reasoning steps. Please try a simpler request."
	}

	return finalResponse, nil
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
		toolCtx, toolCancel := context.WithTimeout(ctx, 30*time.Second)
		res, err := tool.Execute(toolCtx, args)
		toolCancel()

		if err == nil {
			return res, nil
		}

		lastErr = err
		result = res // In case tool returns a partial result or specific error message

		if toolCtx.Err() == context.DeadlineExceeded {
			result = fmt.Sprintf("Error: Tool %s timed out after 30 seconds", tool.Name())
			// Don't retry timeouts usually, or maybe just once? Let's skip retry for timeout for now.
			break
		}

		log.Printf("[%s][Step %d] Tool %s failed (attempt %d/%d): %v. Retrying in %v...", chatID, stepIdx, tool.Name(), i+1, maxRetries, err, backoff)

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	return result, lastErr
}

// trimOrchContext keeps the system prompt (index 0) plus the most recent maxRecent messages,
// preventing the orchestration context window from growing unboundedly.
func trimOrchContext(msgs []llms.MessageContent, maxRecent int) []llms.MessageContent {
	if len(msgs) <= 1+maxRecent {
		return msgs
	}
	trimmed := make([]llms.MessageContent, 1+maxRecent)
	trimmed[0] = msgs[0]
	copy(trimmed[1:], msgs[len(msgs)-maxRecent:])
	return trimmed
}

// MasterBrain orchestrates multiple WorkerBrain steps.
type MasterBrain struct {
	Model   llms.Model
	Worker  *WorkerBrain
	History HistoryStore
	Prompts *PromptManager
	Logger  *observability.Logger
}

func NewMasterBrain(model llms.Model, worker *WorkerBrain, history HistoryStore, prompts *PromptManager, logger *observability.Logger) *MasterBrain {
	return &MasterBrain{
		Model:   model,
		Worker:  worker,
		History: history,
		Prompts: prompts,
		Logger:  logger,
	}
}

func (b *MasterBrain) Think(ctx context.Context, chatID string, input string) (string, error) {
	observability.SetStatus(observability.RoleMaster, "Planning...")
	defer observability.SetStatus(observability.RoleIdle, "")

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
	// Also add the built-in read_scratchpad tool
	toolDescriptions = append(toolDescriptions, "- read_scratchpad: Read the current task scratchpad to see details from previous steps.")

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

	// 4. Orchestration Loop
	maxSteps := 15

	// Save initial plan to history
	planID, _ := b.History.SavePlan(chatID, input)
	taskID := fmt.Sprintf("plan_%d", planID)

	// stepToolLock locks the tool list for each step ID once it has been set.
	// This prevents the Master from expanding tools for a step it already assigned.
	stepToolLock := make(map[int][]string)

	// Create a local history of the current orchestration
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

	lastCompletedCount := -1
	stuckTurns := 0
	consolidationTurns := 0 // Bug 3 fix: tracks turns where all steps done but Master hasn't given final answer

	for stepCount := 0; stepCount < maxSteps; stepCount++ {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return "Task cancelled.", ctx.Err()
		default:
		}

		// Ask Master for the current plan state
		// Bug 4 fix: Trim orchestration context to prevent unbounded growth.
		// Keep system message (index 0) + last 14 messages.
		orchContext = trimOrchContext(orchContext, 14)

		plan, rawResponse, isDone, err := b.plan(ctx, &orchContext, chatID, 0)
		if err != nil {
			return "", fmt.Errorf("planning error: %v", err)
		}

		// Loop/Deadlock Detection: If completed steps don't increase for 4 turns, it's a deadlock
		if plan != nil {
			completedCount := 0
			for _, s := range plan.Steps {
				if s.Status == "completed" {
					completedCount++
				}
			}

			if completedCount <= lastCompletedCount {
				stuckTurns++
				if stuckTurns >= 4 {
					return "Deadlock detected: The orchestration is not making progress. Multiple turns with no new completed steps. Aborting to prevent infinite loop.", nil
				}
			} else {
				stuckTurns = 0
				lastCompletedCount = completedCount
			}
		}

		// Update persistent plan/trace
		if plan != nil {
			_ = b.History.SyncPlanSteps(planID, plan.Steps)
			b.Logger.Log(observability.Event{
				Type:   observability.EventTypePlan,
				ChatID: chatID,
				TaskID: taskID,
				Data:   plan,
			})
		}

		// If the Master gave a text response instead of a tool call, we are done
		if isDone {
			// Master saves final exchange to history
			b.History.AddMessage(chatID, "human", input)
			b.History.AddMessage(chatID, "ai", rawResponse)
			return rawResponse, nil
		}

		// Find the next step to execute
		var nextStep *store.Step
		for i := range plan.Steps {
			s := &plan.Steps[i]
			if s.Status == "failed" {
				// Step failed — release the tool lock so the Master can assign
				// different tools for a retry re-plan.
				delete(stepToolLock, s.ID)
			} else if s.Status == "pending" {
				// Enforce original tool list if already locked (prevents expanding
				// tools on a re-plan of a still-pending step).
				if locked, ok := stepToolLock[s.ID]; ok {
					s.Tools = locked
				} else if len(s.Tools) > 0 {
					// First time we see this pending step with tools — lock it.
					stepToolLock[s.ID] = s.Tools
				}
			}

			if nextStep == nil && (s.Status == "pending" || s.Status == "failed") {
				nextStep = s
			}
		}

		if nextStep == nil {
			// All steps in current plan are done, but Master didn't give final answer.
			// Bug 3 fix: Abort if Master is repeatedly stuck in consolidation.
			consolidationTurns++
			if consolidationTurns >= 3 {
				return "All steps completed but the planner failed to produce a final answer. Please try again.", nil
			}
			log.Printf("[%s][Master Guard] All steps completed, forcing final answer turn %d (consolidation %d/3)", taskID, stepCount+1, consolidationTurns)
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart("All planned steps are completed. You MUST now provide your final consolidated answer to the user as plain text, NOT as a tool call.")},
			})
			continue
		}

		log.Printf("[%s][Master Step %d] Executing: %s (Tools: %v)", taskID, nextStep.ID, nextStep.Description, nextStep.Tools)
		consolidationTurns = 0 // Reset: Master is actively executing a step, not stuck in consolidation.
		// Bug 2 fix: Do not set "in_progress" — it's a phantom state not in the plan schema.
		observability.SetStatus(observability.RoleMaster, fmt.Sprintf("Step %d: %s", nextStep.ID, nextStep.Description))

		// Management of the Scratchpad
		scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)

		workerResult, err := b.Worker.Think(ctx, chatID, fmt.Sprintf("TASK: %s", nextStep.Description), nextStep.ID, nextStep.Tools)
		if err != nil {
			nextStep.Status = "failed"
			nextStep.Result = fmt.Sprintf("Error: %v", err)
		} else {
			nextStep.Status = "completed"
			nextStep.Result = workerResult
		}

		// Bug 1 fix: Write to scratchpad for BOTH success and failure,
		// so failed step context is available for retry re-plans.
		f, _ := os.OpenFile(scratchPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if f != nil {
			f.WriteString(fmt.Sprintf("\n### Step %d Result (%s):\n%s\n", nextStep.ID, nextStep.Status, nextStep.Result))
			f.Close()
		}

		log.Printf("[%s][Master Step %d] Result received", taskID, nextStep.ID)

		// Record the execution in the orchestration context
		orchContext = append(orchContext, llms.MessageContent{
			Role: llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{
				llms.TextPart(fmt.Sprintf("Plan updated. Executed step %d.", nextStep.ID)),
			},
		})

		// Truncate result for the orchestration context to avoid bloating.
		// Detailed data is in the scratchpad at logs/scratchpad_<chatID>.md
		briefResult := nextStep.Result
		if len(briefResult) > 500 {
			briefResult = briefResult[:500] + "... [truncated, full detail in scratchpad]"
		}

		orchContext = append(orchContext, llms.MessageContent{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextPart(fmt.Sprintf("Step %d result: %s\nOutput: %s\n\nFull details are in the scratchpad. Please update the plan or provide the final answer.", nextStep.ID, nextStep.Status, briefResult)),
			},
		})
	}

	return "I've reached the maximum number of steps for this task. Please try a simpler request.", nil
}

func (b *MasterBrain) plan(ctx context.Context, messages *[]llms.MessageContent, chatID string, depth int) (*store.Plan, string, bool, error) {
	if depth > 3 {
		return nil, "", false, fmt.Errorf("master planning exceeded maximum tool recursion depth (3)")
	}
	plannerTools := []llms.Tool{
		{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "propose_plan",
				Description: "Submit or update a structured plan consisting of multiple steps.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"steps": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"id": map[string]any{
										"type": "integer",
									},
									"description": map[string]any{
										"type": "string",
									},
									"status": map[string]any{
										"type": "string",
										"enum": []string{"pending", "completed", "failed"},
									},
									"tools": map[string]any{
										"type":  "array",
										"items": map[string]any{"type": "string"},
									},
								},
								"required": []string{"id", "description", "status", "tools"},
							},
						},
					},
					"required": []string{"steps"},
				},
			},
		},
		{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        "read_scratchpad",
				Description: "Read the current task scratchpad to see details from previous steps.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
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
			scratchPath := fmt.Sprintf("logs/scratchpad_%s.md", chatID)
			data, _ := os.ReadFile(scratchPath)
			result := string(data)
			if result == "" {
				result = "Scratchpad is empty or doesn't exist yet."
			}
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
			var plan store.Plan
			if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &plan); err != nil {
				return nil, "", false, fmt.Errorf("failed to parse propose_plan arguments: %v", err)
			}
			// Add a synthetic tool response to keep the message history consistent.
			// Without this, any preceding read_scratchpad responses would leave the
			// propose_plan call unanswered, potentially breaking some LLM providers.
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
			return &plan, "", false, nil
		}
	}

	// If only read_scratchpad was called, recurse for the next planning turn.
	if hadScratchpadCall {
		return b.plan(ctx, messages, chatID, depth+1)
	}

	if choice.Content != "" {
		return nil, choice.Content, true, nil
	}

	return nil, "", false, fmt.Errorf("planner failed to provide a plan or text response")
}
