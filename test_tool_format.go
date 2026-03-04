package main

import (
"context"
"fmt"
"log"
"github.com/rahul/mishri/internal/agent"
"github.com/rahul/mishri/internal/tools"
"github.com/rahul/mishri/pkg/config"
"github.com/tmc/langchaingo/llms/openai"
"github.com/rahul/mishri/internal/store"
"github.com/rahul/mishri/internal/observability"
"github.com/rahul/mishri/internal/governance"
)

func main() {
cfg := config.LoadConfig("config.json")
_, pCfg := cfg.GetDefaultProvider()

opts := []openai.Option{
openai.WithToken(pCfg.APIKey),
openai.WithModel(pCfg.Model),
}
if pCfg.BaseURL != "" {
opts = append(opts, openai.WithBaseURL(pCfg.BaseURL))
}
llm, err := openai.New(opts...)
if err != nil {
log.Fatal(err)
}

registry := tools.NewRegistry()
dynSkills, err := tools.LoadSkills(cfg.App.SkillsDir)
for _, skill := range dynSkills {
registry.Register(skill)
}

history, _ := store.NewHistoryStore(cfg.Memory.Path)
prompts := agent.NewPromptManager("./prompts")
gov := governance.NewDefaultPolicyEngine()
logger := observability.NewLogger()

worker := agent.NewWorkerBrain(llm, registry, history, prompts, gov, logger)

ctx := context.Background()
res, err := worker.Think(ctx, "test_chat", "Get the weather in Khatima.", 1, []string{"get_weather"})
fmt.Printf("Worker Response: %s\n", res)
if err != nil {
fmt.Printf("Error: %v\n", err)
}
}
