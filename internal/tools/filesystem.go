package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type FilesystemTool struct {
	Root string
}

func NewFilesystemTool(root string) *FilesystemTool {
	absRoot, _ := filepath.Abs(root)
	return &FilesystemTool{Root: absRoot}
}

func (f *FilesystemTool) Name() string {
	return "filesystem"
}

func (f *FilesystemTool) Description() string {
	return "Manage files in the local workspace: read, write, list, delete, and mkdir."
}

func (f *FilesystemTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"enum":        []string{"read", "write", "list", "delete", "mkdir"},
				"description": "The operation to perform",
			},
			"filename": map[string]any{
				"type":        "string",
				"description": "The name of the file or directory",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The content to write (only for 'write' command)",
			},
		},
		"required": []string{"command", "filename"},
	}
}

func (f *FilesystemTool) Execute(ctx context.Context, input string) (string, error) {
	var args struct {
		Command  string `json:"command"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %v", err)
	}

	targetPath := filepath.Join(f.Root, args.Filename)

	// Safety check: ensure targetPath is within f.Root
	rel, err := filepath.Rel(f.Root, targetPath)
	if err != nil || (len(rel) >= 2 && rel[:2] == "..") {
		return "", fmt.Errorf("unsafe path attempt: %s", args.Filename)
	}

	switch args.Command {
	case "read":
		data, err := os.ReadFile(targetPath)
		if err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}
		return string(data), nil
	case "write":
		err := os.WriteFile(targetPath, []byte(args.Content), 0644)
		if err != nil {
			return "", fmt.Errorf("failed to write file: %w", err)
		}
		return fmt.Sprintf("Successfully wrote to %s", args.Filename), nil
	case "list":
		entries, err := os.ReadDir(targetPath)
		if err != nil {
			return "", fmt.Errorf("failed to list directory: %w", err)
		}
		var output string
		for _, entry := range entries {
			typeStr := "file"
			if entry.IsDir() {
				typeStr = "dir"
			}
			output += fmt.Sprintf("[%s] %s\n", typeStr, entry.Name())
		}
		if output == "" {
			return "Directory is empty", nil
		}
		return output, nil
	case "delete":
		err := os.Remove(targetPath)
		if err != nil {
			return "", fmt.Errorf("failed to delete: %w", err)
		}
		return fmt.Sprintf("Successfully deleted %s", args.Filename), nil
	case "mkdir":
		err := os.MkdirAll(targetPath, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}
		return fmt.Sprintf("Successfully created directory %s", args.Filename), nil
	default:
		return "Invalid command. Use 'read', 'write', 'list', 'delete', or 'mkdir'", nil
	}
}
