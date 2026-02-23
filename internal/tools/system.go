package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type SystemTool struct{}

func NewSystemTool() *SystemTool {
	return &SystemTool{}
}

func (s *SystemTool) Name() string {
	return "system"
}

func (s *SystemTool) Description() string {
	return "Control the system GUI (mouse and keyboard) and capture desktop state. Actions: 'mouse_move', 'mouse_click', 'key_press', 'type_text', 'desktop_screenshot'."
}

func (s *SystemTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"mouse_move", "mouse_click", "key_press", "type_text", "desktop_screenshot"},
				"description": "The GUI action to perform.",
			},
			"x": map[string]any{
				"type":        "integer",
				"description": "X coordinate for mouse_move.",
			},
			"y": map[string]any{
				"type":        "integer",
				"description": "Y coordinate for mouse_move.",
			},
			"button": map[string]any{
				"type":        "string",
				"description": "Mouse button for mouse_click (1=left, 2=middle, 3=right). Default is 1.",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "The key or key combination for key_press (e.g., 'Return', 'alt+Tab').",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "The string of text to type for type_text.",
			},
		},
		"required": []string{"action"},
	}
}

func (s *SystemTool) Execute(ctx context.Context, input string) (string, error) {
	var args struct {
		Action string `json:"action"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Button string `json:"button"`
		Key    string `json:"key"`
		Text   string `json:"text"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	switch args.Action {
	case "desktop_screenshot":
		return s.captureDesktop(ctx)
	default:
		return s.executeXdotool(ctx, args.Action, args.X, args.Y, args.Button, args.Key, args.Text)
	}
}

func (s *SystemTool) captureDesktop(ctx context.Context) (string, error) {
	os.MkdirAll("screenshots", 0755)
	filename := fmt.Sprintf("desktop_%d.png", time.Now().Unix())
	path := filepath.Join("screenshots", filename)

	// Try ffmpeg first (since we already verified it's available)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-f", "x11grab", "-i", ":0.0", "-frames:v", "1", path, "-y")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Fallback to scrot just in case the user installs it later
		cmd = exec.CommandContext(ctx, "scrot", path)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Sprintf("Error capturing desktop: %v\nOutput: %s", err, string(output)), nil
		}
	}

	absPath, _ := filepath.Abs(path)
	return fmt.Sprintf("Desktop screenshot saved to %s", absPath), nil
}

func (s *SystemTool) executeXdotool(ctx context.Context, action string, x, y int, button, key, text string) (string, error) {
	var cmdArgs []string
	switch action {
	case "mouse_move":
		cmdArgs = []string{"mousemove", strconv.Itoa(x), strconv.Itoa(y)}
	case "mouse_click":
		if button == "" {
			button = "1"
		}
		cmdArgs = []string{"click", button}
	case "key_press":
		if key == "" {
			return "Error: key is required for key_press", nil
		}
		cmdArgs = []string{"key", key}
	case "type_text":
		if text == "" {
			return "Error: text is required for type_text", nil
		}
		cmdArgs = []string{"type", text}
	default:
		return "Invalid action.", nil
	}

	cmd := exec.CommandContext(ctx, "xdotool", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(err.Error(), "executable file not found") {
			return "Error: xdotool is not installed. Please install it using 'sudo apt-get install xdotool'.", nil
		}
		return fmt.Sprintf("Error executing xdotool: %v\nOutput: %s", err, string(output)), nil
	}

	return fmt.Sprintf("Successfully executed action: %s", action), nil
}
