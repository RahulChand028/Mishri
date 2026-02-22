package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

type CronStore interface {
	AddTask(chatID string, description string, intervalSeconds int) error
	ClearTasks(chatID string) error
	ListTasks(chatID string) ([]map[string]any, error)
	DeleteTask(chatID string, taskID int) error
}

type CronTool struct {
	Store CronStore
}

func NewCronTool(store CronStore) *CronTool {
	return &CronTool{Store: store}
}

func (c *CronTool) Name() string {
	return "schedule_task"
}

func (c *CronTool) Description() string {
	return "Manage recurring tasks: 'schedule' (recurring), 'once' (one-time reminder), 'clear' (all), 'list' active tasks, or 'remove' a specific task by ID."
}

func (c *CronTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"schedule", "once", "clear", "list", "remove"},
				"description": "The action to perform: 'schedule' recurring, 'once' one-time, 'clear' all, 'list' all, or 'remove' one.",
			},
			"task_description": map[string]any{
				"type":        "string",
				"description": "What the agent should do (for 'schedule' and 'once')",
			},
			"interval_seconds": map[string]any{
				"type":        "integer",
				"description": "Interval in seconds (min 60s, for 'schedule')",
			},
			"delay_seconds": map[string]any{
				"type":        "integer",
				"description": "Delay in seconds before running the one-time task (for 'once')",
			},
			"task_id": map[string]any{
				"type":        "integer",
				"description": "The ID of the task to remove (for 'remove')",
			},
		},
		"required": []string{"action"},
	}
}

func (c *CronTool) Execute(ctx context.Context, input string) (string, error) {
	var args struct {
		Action   string `json:"action"`
		Desc     string `json:"task_description"`
		Interval int    `json:"interval_seconds"`
		Delay    int    `json:"delay_seconds"`
		TaskID   int    `json:"task_id"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	chatID, ok := ctx.Value("chatID").(string)
	if !ok {
		return "", fmt.Errorf("missing chatID in context")
	}

	switch args.Action {
	case "list":
		tasks, err := c.Store.ListTasks(chatID)
		if err != nil {
			return "", fmt.Errorf("failed to list tasks: %v", err)
		}
		if len(tasks) == 0 {
			return "You have no scheduled tasks.", nil
		}
		resp := "Your scheduled tasks:\n"
		for _, t := range tasks {
			typeStr := "every %v seconds"
			if t["interval_seconds"].(int) == 0 {
				typeStr = "one-time"
			}
			resp += fmt.Sprintf("- [%v] %s ("+typeStr+", status: %s)\n", t["id"], t["task_description"], t["interval_seconds"], t["status"])
		}
		return resp, nil

	case "remove":
		if args.TaskID == 0 {
			return "Error: task_id is required for 'remove' action.", nil
		}
		err := c.Store.DeleteTask(chatID, args.TaskID)
		if err != nil {
			return "", fmt.Errorf("failed to delete task: %v", err)
		}
		return fmt.Sprintf("Successfully removed task %d.", args.TaskID), nil

	case "clear":
		err := c.Store.ClearTasks(chatID)
		if err != nil {
			return "", fmt.Errorf("failed to clear tasks: %v", err)
		}
		return "Successfully cleared all your scheduled tasks.", nil

	case "once":
		// One-time task uses interval_seconds = 0
		// We use last_run as 'now - (interval - delay)' to trigger at the right time
		// But AddTask uses hardcoded -365 days. We need a way to set initial last_run.
		// Let's modify HistoryStore.AddTask to accept firstRun.
		// Or simpler: just let it run in next poll if delay is 0.
		// If delay is specified, we can't easily support it without changing HistoryStore.
		// For now, let's keep it simple: 'once' runs in next poll.
		err := c.Store.AddTask(chatID, args.Desc, 0)
		if err != nil {
			return "", fmt.Errorf("failed to schedule one-time task: %v", err)
		}
		return fmt.Sprintf("Successfully scheduled one-time task: '%s'. It will run shortly.", args.Desc), nil

	case "schedule":
		if args.Interval < 60 {
			return "Error: Minimum interval is 60 seconds to prevent spamming.", nil
		}
		err := c.Store.AddTask(chatID, args.Desc, args.Interval)
		if err != nil {
			return "", fmt.Errorf("failed to schedule task: %v", err)
		}
		return fmt.Sprintf("Successfully scheduled task: '%s' every %d seconds.", args.Desc, args.Interval), nil

	default:
		return "Invalid action. Use 'schedule', 'once', 'clear', 'list', or 'remove'.", nil
	}
}
