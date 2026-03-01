package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/rahul/mishri/internal/observability"
	"github.com/tmc/langchaingo/llms"
)

// ReflectionAgent produces high-quality written output by running a draft → critique → revise cycle.
// Best for: writing reports, summaries, emails, documentation.
type ReflectionAgent struct {
	model  llms.Model
	worker *WorkerBrain
	logger *observability.Logger
}

func NewReflectionAgent(model llms.Model, worker *WorkerBrain, logger *observability.Logger) *ReflectionAgent {
	return &ReflectionAgent{model: model, worker: worker, logger: logger}
}

// Run executes the three-phase reflection loop: draft → critique → revise.
// Each phase is a separate LLM call; tools are available during drafting only.
func (a *ReflectionAgent) Run(ctx context.Context, chatID string, agentID int, systemPrompt string, tools []string) (string, error) {
	observability.SetStatus(observability.RoleSlave, fmt.Sprintf("[REFLECT] Agent %d", agentID))
	defer observability.SetStatus(observability.RoleIdle, "")

	log.Printf("[Agent %d][REFLECT] Starting draft phase", agentID)

	// --- Phase 1: Draft ---
	draftPrompt := systemPrompt + "\n\n" + reflectionDraftInstructions
	draftMessage := "Produce an initial draft of your output. Use any available tools to gather information first, then write the draft."

	draft, err := a.worker.ThinkWithSystemPrompt(ctx, chatID, draftMessage, agentID, tools, draftPrompt)
	if err != nil || draft == "" {
		return buildReport("failed", "", "", fmt.Sprintf("Draft phase failed: %v", err), "Retry"), nil
	}

	log.Printf("[Agent %d][REFLECT] Draft complete, starting critique phase", agentID)

	// --- Phase 2: Critique (no tool access) ---
	critiqueMessages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{
				llms.TextPart("You are a critical reviewer. Your job is to identify weaknesses in the draft below and suggest specific improvements. Be concise and direct."),
			},
		},
		{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextPart(fmt.Sprintf("Review this draft and list its weaknesses and how to fix them:\n\n%s", draft)),
			},
		},
	}

	critiqueResp, err := a.model.GenerateContent(ctx, critiqueMessages)
	if err != nil || len(critiqueResp.Choices) == 0 {
		// If critique fails, return the draft as-is
		log.Printf("[Agent %d][REFLECT] Critique phase failed, returning draft", agentID)
		return buildReport("success", draft, "", "Critique phase failed, draft returned as-is", ""), nil
	}
	critique := critiqueResp.Choices[0].Content

	log.Printf("[Agent %d][REFLECT] Critique done, starting revision phase", agentID)

	// --- Phase 3: Revise ---
	reviseMessages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{
				llms.TextPart(systemPrompt),
			},
		},
		{
			Role: llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{
				llms.TextPart(fmt.Sprintf(
					"Here is your original draft:\n\n%s\n\n---\nHere is a critique of the draft:\n\n%s\n\n---\n"+
						"Now write a revised, improved version that addresses all the critique points. Return only the final output, no meta-commentary.",
					draft, critique,
				)),
			},
		},
	}

	reviseResp, err := a.model.GenerateContent(ctx, reviseMessages)
	if err != nil || len(reviseResp.Choices) == 0 {
		log.Printf("[Agent %d][REFLECT] Revision failed, returning draft", agentID)
		return buildReport("partial", draft, "", "Revision phase failed", ""), nil
	}
	revised := reviseResp.Choices[0].Content

	if strings.Contains(revised, "STATUS:") {
		return revised, nil
	}
	return buildReport("success", revised, "", "", ""), nil
}

const reflectionDraftInstructions = `## Reflection Agent — Draft Phase

Your task is to produce a first draft of the requested output.
- Use your available tools (if any) to gather facts and data you need first
- Then write a comprehensive initial draft
- Do not self-censor — write everything you think belongs in the output
- The draft will be reviewed and improved in subsequent phases`
