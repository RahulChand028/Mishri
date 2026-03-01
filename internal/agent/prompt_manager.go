package agent

import (
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"
	"sort"
	"strings"
)

type PromptManager struct {
	Directory      string
	overridePrompt string // When set, GetLeanWorkerPrompt returns this instead of reading files.
}

func NewPromptManager(dir string) *PromptManager {
	return &PromptManager{Directory: dir}
}

func (pm *PromptManager) GetWorkerPrompt() (string, error) {
	files, err := ioutil.ReadDir(pm.Directory)
	if err != nil {
		return "", fmt.Errorf("failed to read prompts directory: %v", err)
	}

	var contents []string

	// Sort files to ensure deterministic prompt order
	// We might want a specific order: identity, soul, capabilities, user
	order := map[string]int{
		"identity.md":         1,
		"soul.md":             2,
		"capabilities.md":     3,
		"worker_directive.md": 4,
		"user.md":             5,
	}

	sort.Slice(files, func(i, j int) bool {
		oi, okI := order[files[i].Name()]
		oj, okJ := order[files[j].Name()]
		if okI && okJ {
			return oi < oj
		}
		if okI {
			return true
		}
		if okJ {
			return false
		}
		return files[i].Name() < files[j].Name()
	})

	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".md") && f.Name() != "planner.md" {
			path := filepath.Join(pm.Directory, f.Name())
			data, err := ioutil.ReadFile(path)
			if err != nil {
				log.Printf("Warning: Failed to read prompt file %s: %v", path, err)
				continue
			}
			contents = append(contents, string(data))
		}
	}

	if len(contents) == 0 {
		return "", fmt.Errorf("no prompt files found in %s", pm.Directory)
	}

	return strings.Join(contents, "\n\n---\n\n"), nil
}

func (pm *PromptManager) GetPlannerPrompt() (string, error) {
	path := filepath.Join(pm.Directory, "planner.md")
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read planner prompt: %v", err)
	}
	return string(data), nil
}

// GetLeanWorkerPrompt returns the lean worker prompt.
// If overridePrompt is set (e.g. by ThinkWithSystemPrompt), that is returned directly
// so the agent type can inject its own fully-crafted system prompt.
func (pm *PromptManager) GetLeanWorkerPrompt() (string, error) {
	if pm.overridePrompt != "" {
		return pm.overridePrompt, nil
	}
	path := filepath.Join(pm.Directory, "worker_lean.md")
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read lean worker prompt: %v", err)
	}
	return string(data), nil
}

// GetAgentBasePrompt returns the base prompt template for a given agent type.
// Templates live in prompts/agents/<type>_base.md.
func (pm *PromptManager) GetAgentBasePrompt(agentType string) (string, error) {
	path := filepath.Join(pm.Directory, "agents", agentType+"_base.md")
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no base prompt for agent type %q: %v", agentType, err)
	}
	return string(data), nil
}

// BuildAgentSystemPrompt assembles the full system prompt for an autonomous agent
// by combining the base template with the manager-provided goal, prior reports, and tools.
func (pm *PromptManager) BuildAgentSystemPrompt(agentType, goal, priorReports, toolsList string) (string, error) {
	base, err := pm.GetAgentBasePrompt(agentType)
	if err != nil {
		// Graceful fallback: construct a minimal prompt without a template
		base = "You are an autonomous agent. Complete your task using the available tools."
	}

	prompt := strings.ReplaceAll(base, "{{GOAL}}", goal)
	prompt = strings.ReplaceAll(prompt, "{{PRIOR_REPORTS}}", priorReports)
	prompt = strings.ReplaceAll(prompt, "{{TOOLS}}", toolsList)

	if priorReports == "" {
		prompt = strings.ReplaceAll(prompt, "Prior context: \n", "")
	}

	return prompt, nil
}
