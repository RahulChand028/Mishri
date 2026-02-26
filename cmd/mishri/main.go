package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rahul/mishri/internal/agent"
	"github.com/rahul/mishri/internal/gateway"
	"github.com/rahul/mishri/internal/governance"
	"github.com/rahul/mishri/internal/observability"
	"github.com/rahul/mishri/internal/store"
	"github.com/rahul/mishri/internal/tools"
	"github.com/rahul/mishri/pkg/config"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

func main() {
	observability.PrintBanner()
	observability.InitializeTerminal()

	// Route all log output through the terminal mutex so it never
	// interrupts the dashboard's cursor save/restore sequence.
	log.SetOutput(observability.NewTermWriter())

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
	gov := governance.NewDefaultPolicyEngine()
	// Default safety rules: Block dangerous destructive commands
	_ = gov.DenyArguments(`rm\s+-rf`)
	_ = gov.DenyArguments(`mkfs`)
	_ = gov.DenyArguments(`shutdown`)
	_ = gov.DenyArguments(`reboot`)

	logger := observability.NewLogger()

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

	worker := agent.NewWorkerBrain(llm, registry, history, prompts, gov, logger)
	brain := agent.NewMasterBrain(llm, worker, history, prompts, logger)

	tg, err := gateway.NewTelegramGateway(tgCfg.Token, brain)
	if err != nil {
		log.Fatal(err)
	}

	// Start Background Scheduler with a cancelable context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	scheduler := agent.NewScheduler(brain, history, tg)
	go scheduler.Start(ctx)

	// Start Live Resource Dashboard (1-second updates)
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				observability.PrintLiveStatus()
			}
		}
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				observability.Heartbeat()
			}
		}
	}()

	// Start Gateway in a goroutine so we can wait for context in the main loop
	go func() {
		if err := tg.Start(); err != nil {
			log.Printf("\033[91m[ FAIL ] GATEWAY CRITICAL ERROR: %v\033[0m", err)
			stop() // stop caller if gateway dies
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()

	// Reset terminal aesthetics
	observability.CleanupTerminal()

	// Give a short time for final logs/syncs
	time.Sleep(500 * time.Millisecond)
	log.Println("\033[95m[ EXIT ] CORE DE-INITIALIZED. GOODBYE.\033[0m")
}
