package main

import (
	"context"
	"log"

	"github.com/rahul/mishri/internal/agent"
	"github.com/rahul/mishri/internal/gateway"
	"github.com/rahul/mishri/internal/store"
	"github.com/rahul/mishri/internal/tools"
	"github.com/rahul/mishri/pkg/config"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

func main() {
	cfg := config.LoadConfig("config.json")

	tgCfg, ok := cfg.GetTelegramConfig()
	if !ok {
		log.Fatal("Telegram gateway is not enabled or token is missing")
	}

	// Initialize Tools
	registry := tools.NewRegistry()

	searchTool, err := tools.NewSearchTool()
	if err != nil {
		log.Printf("Warning: Failed to initialize search tool: %v", err)
	} else {
		registry.Register(searchTool)
	}

	fsTool := tools.NewFilesystemTool(cfg.App.Workspace)
	registry.Register(fsTool)

	scraperTool := tools.NewScraperTool()
	registry.Register(scraperTool)

	history, err := store.NewHistoryStore(cfg.Memory.Path)
	if err != nil {
		log.Fatal(err)
	}

	prompts := agent.NewPromptManager("./prompts")

	cronTool := tools.NewCronTool(history)
	registry.Register(cronTool)

	shellTool := tools.NewShellTool()
	registry.Register(shellTool)

	browserTool := tools.NewBrowserTool()
	registry.Register(browserTool)

	systemTool := tools.NewSystemTool()
	registry.Register(systemTool)

	// Initialize LLM (using default enabled provider)
	pName, pCfg := cfg.GetDefaultProvider()
	if pName == "" {
		log.Fatal("No enabled provider found in config")
	}

	var llm llms.Model
	switch pName {
	case "openai", "openrouter":
		opts := []openai.Option{
			openai.WithToken(pCfg.APIKey),
			openai.WithModel(pCfg.Model),
		}
		if pCfg.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(pCfg.BaseURL))
		}
		llm, err = openai.New(opts...)
	default:
		log.Fatalf("Provider %s not yet implemented in main", pName)
	}

	if err != nil {
		log.Fatal(err)
	}

	worker := agent.NewWorkerBrain(llm, registry, history, prompts)
	brain := agent.NewMasterBrain(llm, worker, history, prompts)

	tg, err := gateway.NewTelegramGateway(tgCfg.Token, brain)
	if err != nil {
		log.Fatal(err)
	}

	// Start Background Scheduler
	ctx := context.Background()
	scheduler := agent.NewScheduler(brain, history, tg)
	go scheduler.Start(ctx)

	log.Println("Mishri agent starting...")
	if err := tg.Start(); err != nil {
		log.Fatal(err)
	}
}
