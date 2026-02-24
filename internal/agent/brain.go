package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/rahul/mishri/internal/tools"
	"github.com/tmc/langchaingo/llms"
)

// Brain defines the core intelligence interface for the agent.
type Brain interface {
	Think(ctx context.Context, chatID string, input string) (string, error)
}

// Step represents a single sub-task in a broader plan.
type Step struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"` // pending, in_progress, completed, failed
	Result      string `json:"result"`
}

// Plan represents a sequence of steps to fulfill a user request.
type Plan struct {
	Steps []Step `json:"steps"`
}

// WorkerBrain is a ReAct agent that handles individual sub-tasks.
type WorkerBrain struct {
	Model    llms.Model
	Registry *tools.Registry
	History  HistoryStore
	Prompts  *PromptManager
}

type HistoryStore interface {
	AddMessage(chatID string, role string, content string) error
	GetHistory(chatID string, limit int) ([]llms.MessageContent, error)
	ClearTasks(chatID string) error
}

func NewWorkerBrain(model llms.Model, registry *tools.Registry, history HistoryStore, prompts *PromptManager) *WorkerBrain {
	return &WorkerBrain{
		Model:    model,
		Registry: registry,
		History:  history,
		Prompts:  prompts,
	}
}

func (b *WorkerBrain) Think(ctx context.Context, chatID string, input string) (string, error) {
	// Add chatID to context for tools that might need it (like Cron)
	ctx = context.WithValue(ctx, "chatID", chatID)

	// 1. Get Worker Prompt
	systemPrompt, err := b.Prompts.GetWorkerPrompt()
	if err != nil {
		log.Printf("Warning: Failed to load worker prompt: %v", err)
	}

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

	// 3. Prepare tools for the LLM
	var llmTools []llms.Tool
	for _, t := range b.Registry.Tools {
		llmTools = append(llmTools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}

	// 4. Reasoning Loop (ReAct)
	maxSteps := 10
	var finalResponse string

	for i := 0; i < maxSteps; i++ {
		resp, err := b.Model.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			return "", err
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
			tool := b.Registry.Get(tc.FunctionCall.Name)
			var result string

			if tool == nil {
				result = fmt.Sprintf("Error: Tool %s not found", tc.FunctionCall.Name)
			} else {
				log.Printf("[Step %d] Executing tool %s with args: %s", i+1, tool.Name(), tc.FunctionCall.Arguments)
				res, err := tool.Execute(ctx, tc.FunctionCall.Arguments)
				if err != nil {
					res = fmt.Sprintf("Error: %v", err)
				}
				result = res
				log.Printf("[Step %d] Tool %s returned: %s", i+1, tool.Name(), result)
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

// MasterBrain orchestrates multiple WorkerBrain steps.
type MasterBrain struct {
	Model   llms.Model
	Worker  *WorkerBrain
	History HistoryStore
	Prompts *PromptManager
}

func NewMasterBrain(model llms.Model, worker *WorkerBrain, history HistoryStore, prompts *PromptManager) *MasterBrain {
	return &MasterBrain{
		Model:   model,
		Worker:  worker,
		History: history,
		Prompts: prompts,
	}
}

func (b *MasterBrain) Think(ctx context.Context, chatID string, input string) (string, error) {
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
	fullPlannerPrompt := fmt.Sprintf("%s\n\n## Available Tools (Slave Capabilities):\n%s", plannerPrompt, toolsList)

	// 3. Load history (for context)
	history, _ := b.History.GetHistory(chatID, 5)

	// 4. Orchestration Loop
	maxSteps := 15

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

	for stepCount := 0; stepCount < maxSteps; stepCount++ {
		// Ask Master for the current plan state
		plan, rawResponse, isDone, err := b.plan(ctx, orchContext)
		if err != nil {
			return "", fmt.Errorf("planning error: %v", err)
		}

		// If the Master gave a text response instead of a tool call, we are done
		if isDone {
			// Master saves final exchange to history
			b.History.AddMessage(chatID, "human", input)
			b.History.AddMessage(chatID, "ai", rawResponse)
			return rawResponse, nil
		}

		// Find the next step to execute
		var nextStep *Step
		for i := range plan.Steps {
			if plan.Steps[i].Status == "pending" || plan.Steps[i].Status == "failed" {
				nextStep = &plan.Steps[i]
				break
			}
		}

		if nextStep == nil {
			// All steps in current plan are done, but Master didn't give final answer.
			// Ask Master to consolidate.
			orchContext = append(orchContext, llms.MessageContent{
				Role:  llms.ChatMessageTypeHuman,
				Parts: []llms.ContentPart{llms.TextPart("All steps in the plan are completed. Please provide your final response to the user.")},
			})
			continue
		}

		// Execute next step via Worker (Slave)
		log.Printf("[Master] Step %d: %s", nextStep.ID, nextStep.Description)
		nextStep.Status = "in_progress"

		workerResult, err := b.Worker.Think(ctx, chatID, fmt.Sprintf("TASK: %s\n\nCONTEXT: This is a sub-task for the overall request: %s", nextStep.Description, input))
		if err != nil {
			nextStep.Status = "failed"
			nextStep.Result = fmt.Sprintf("Error: %v", err)
		} else {
			nextStep.Status = "completed"
			nextStep.Result = workerResult
		}

		log.Printf("[Master] Step %d result: %s", nextStep.ID, nextStep.Status)

		// Record the execution in the orchestration context
		orchContext = append(orchContext, llms.MessageContent{
			Role: llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{
				llms.TextPart(fmt.Sprintf("Plan updated. Executed step %d.", nextStep.ID)),
			},
		})
		orchContext = append(orchContext, llms.MessageContent{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextPart(fmt.Sprintf("Step %d result: %s\nOutput: %s\n\nPlease update the plan or provide the final answer.", nextStep.ID, nextStep.Status, nextStep.Result)),
			},
		})
	}

	return "I've reached the maximum number of steps for this task. Please try a simpler request.", nil
}

func (b *MasterBrain) plan(ctx context.Context, messages []llms.MessageContent) (*Plan, string, bool, error) {
	// Define the propose_plan tool
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
								},
								"required": []string{"id", "description", "status"},
							},
						},
					},
					"required": []string{"steps"},
				},
			},
		},
	}

	resp, err := b.Model.GenerateContent(ctx, messages, llms.WithTools(plannerTools))
	if err != nil {
		return nil, "", false, err
	}

	choice := resp.Choices[0]

	for _, tc := range choice.ToolCalls {
		if tc.FunctionCall.Name == "propose_plan" {
			var plan Plan
			if err := json.Unmarshal([]byte(tc.FunctionCall.Arguments), &plan); err != nil {
				return nil, "", false, fmt.Errorf("failed to parse propose_plan arguments: %v", err)
			}
			return &plan, "", false, nil
		}
	}

	if choice.Content != "" {
		return nil, choice.Content, true, nil
	}

	return nil, "", false, fmt.Errorf("planner failed to provide a plan or text response")
}
