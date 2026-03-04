package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DynamicSkillTool represents a tool loaded dynamically from a Markdown file
type DynamicSkillTool struct {
	name        string
	description string
	parameters  map[string]any
	script      string
}

func (s *DynamicSkillTool) Name() string {
	return s.name
}

func (s *DynamicSkillTool) Description() string {
	return s.description
}

func (s *DynamicSkillTool) Parameters() map[string]any {
	return s.parameters
}

// Execute runs the skill's script. It passes parameters as a JSON string to a known env var.
func (s *DynamicSkillTool) Execute(ctx context.Context, input string) (string, error) {
	// Create a temporary script file
	tmpFile, err := os.CreateTemp("", "mishri-skill-*.sh")
	if err != nil {
		return "", fmt.Errorf("failed to create temp script file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	// Ensure script has a shebang
	scriptBody := s.script
	if !strings.HasPrefix(strings.TrimSpace(scriptBody), "#!") {
		scriptBody = "#!/bin/bash\n" + scriptBody
	}

	_, err = tmpFile.WriteString(scriptBody)
	if err != nil {
		return "", fmt.Errorf("failed to write to temp script file: %w", err)
	}
	tmpFile.Close()

	if err := os.Chmod(tmpFile.Name(), 0700); err != nil {
		return "", fmt.Errorf("failed to make temp script executable: %w", err)
	}

	// Make sure the input is valid JSON (sometimes it can be just a raw string if no parameters)
	if input == "" {
		input = "{}"
	}

	// Prepare the command
	cmd := exec.CommandContext(ctx, "bash", tmpFile.Name())

	// Pass the input parameters as an environment variable so the script can parse it with jq or python
	cmd.Env = append(os.Environ(), fmt.Sprintf("SKILL_ARGS=%s", input))

	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return "", fmt.Errorf("skill execution failed: %v\nStderr: %s", err, stderr.String())
	}

	return out.String(), nil
}

// ---------------------------------------------------------------------

// SkillFrontmatter matches the YAML metadata at the top of a skill .md file
type SkillFrontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Parameters  map[string]any `yaml:"parameters"`
}

// LoadSkills parses a directory of markdown files and returns a list of dynamic tools.
func LoadSkills(dir string) ([]Tool, error) {
	var tools []Tool

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Skills dir might not exist yet, that's fine
			return tools, nil
		}
		return nil, fmt.Errorf("failed to read skills directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		tool, err := parseSkillFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load skill %s: %v\n", path, err)
			continue
		}
		tools = append(tools, tool)
	}

	return tools, nil
}

func parseSkillFile(path string) (Tool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	strContent := string(content)

	// Split by "---" assuming standard YAML frontmatter
	parts := strings.SplitN(strContent, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid file format: expected yaml frontmatter surrounded by '---'")
	}

	yamlContent := parts[1]
	scriptContent := strings.TrimSpace(parts[2])

	var fm SkillFrontmatter
	err = yaml.Unmarshal([]byte(yamlContent), &fm)
	if err != nil {
		return nil, fmt.Errorf("failed to parse yaml frontmatter: %w", err)
	}

	if fm.Name == "" {
		return nil, fmt.Errorf("skill is missing a 'name' in frontmatter")
	}

	// If no parameters explicitly provided, default to an empty object
	if fm.Parameters == nil {
		fm.Parameters = map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	return &DynamicSkillTool{
		name:        fm.Name,
		description: fm.Description,
		parameters:  fm.Parameters,
		script:      scriptContent,
	}, nil
}
