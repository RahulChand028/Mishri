package agent

import (
	"context"
	"fmt"
	"log"

	"github.com/rahul/mishri/internal/tools"
	"github.com/tmc/langchaingo/llms"
)

// Brain defines the core intelligence interface for the agent.
type Brain interface {
	Think(ctx context.Context, chatID string, input string) (string, error)
}

// SimpleBrain is a lightweight implementation of the Brain interface.
type SimpleBrain struct {
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

func NewSimpleBrain(model llms.Model, registry *tools.Registry, history HistoryStore, prompts *PromptManager) *SimpleBrain {
	return &SimpleBrain{
		Model:    model,
		Registry: registry,
		History:  history,
		Prompts:  prompts,
	}
}

func (b *SimpleBrain) Think(ctx context.Context, chatID string, input string) (string, error) {
	// Add chatID to context for tools that might need it (like Cron)
	ctx = context.WithValue(ctx, "chatID", chatID)

	// 1. Get System Prompt
	systemPrompt, err := b.Prompts.GetSystemPrompt()
	if err != nil {
		log.Printf("Warning: Failed to load system prompt: %v", err)
	}

	// 2. Load history
	history, err := b.History.GetHistory(chatID, 10) // Last 10 messages
	if err != nil {
		log.Printf("Error loading history: %v", err)
	}

	// 3. Prepare messages (System Prompt + History + current input)
	var messages []llms.MessageContent
	if systemPrompt != "" {
		messages = append(messages, llms.MessageContent{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{
				llms.TextPart(systemPrompt),
			},
		})
	}

	messages = append(messages, history...)
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

	// 5. Save exchange (Human + AI Final Response) to persistent history
	b.History.AddMessage(chatID, "human", input)
	b.History.AddMessage(chatID, "ai", finalResponse)

	return finalResponse, nil
}
