package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type ShellTool struct{}

func NewShellTool() *ShellTool {
	return &ShellTool{}
}

func (s *ShellTool) Name() string {
	return "shell"
}

func (s *ShellTool) Description() string {
	return "Execute system shell commands. Use with caution. Access to full shell environment."
}

func (s *ShellTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute",
			},
		},
		"required": []string{"command"},
	}
}

func (s *ShellTool) Execute(ctx context.Context, input string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	if args.Command == "" {
		return "Error: empty command", nil
	}

	// Filter out potentially interactive or blocking commands if possible?
	// For now, let's keep it simple.

	cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)

	// Create a combined output capture
	output, err := cmd.CombinedOutput()

	result := strings.TrimSpace(string(output))
	if result == "" {
		result = "(no output)"
	}

	if err != nil {
		return fmt.Sprintf("Command failed with error: %v\nOutput: %s", err, result), nil
	}

	return result, nil
}
