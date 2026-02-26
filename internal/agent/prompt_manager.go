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
	Directory string
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
