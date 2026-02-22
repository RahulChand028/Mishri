package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

type CronStore interface {
	AddTask(chatID string, description string, intervalSeconds int) error
	ClearTasks(chatID string) error
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
	return "Manage recurring tasks: 'schedule' or 'clear' all current alarms."
}

func (c *CronTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"schedule", "clear"},
				"description": "The action to perform: 'schedule' a new task or 'clear' all ones.",
			},
			"task_description": map[string]any{
				"type":        "string",
				"description": "What the agent should do (only for 'schedule' action)",
			},
			"interval_seconds": map[string]any{
				"type":        "integer",
				"description": "The interval in seconds (minimum 60s, only for 'schedule' action)",
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
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	chatID, ok := ctx.Value("chatID").(string)
	if !ok {
		return "", fmt.Errorf("missing chatID in context")
	}

	switch args.Action {
	case "clear":
		err := c.Store.ClearTasks(chatID)
		if err != nil {
			return "", fmt.Errorf("failed to clear tasks: %v", err)
		}
		return "Successfully cleared all your scheduled tasks.", nil

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
		return "Invalid action. Use 'schedule' or 'clear'.", nil
	}
}
