package agent

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptManager_GetWorkerPrompt(t *testing.T) {
	tempDir, err := ioutil.TempDir("", "prompts_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	files := map[string]string{
		"identity.md":     "Identity Content",
		"soul.md":         "Soul Content",
		"capabilities.md": "Capabilities Content",
		"user.md":         "User Content",
		"extra.md":        "Extra Content",
	}

	for name, content := range files {
		err := ioutil.WriteFile(filepath.Join(tempDir, name), []byte(content), 0644)
		if err != nil {
			t.Fatal(err)
		}
	}

	pm := NewPromptManager(tempDir)
	prompt, err := pm.GetWorkerPrompt()
	if err != nil {
		t.Fatal(err)
	}

	expectedParts := []string{
		"Identity Content",
		"Soul Content",
		"Capabilities Content",
		"User Content",
		"Extra Content",
	}

	for _, part := range expectedParts {
		if !strings.Contains(prompt, part) {
			t.Errorf("Prompt missing expected part: %s", part)
		}
	}

	// Verify order
	if strings.Index(prompt, "Identity Content") >= strings.Index(prompt, "Soul Content") {
		t.Error("Identity should be before Soul")
	}
	if strings.Index(prompt, "Soul Content") >= strings.Index(prompt, "Capabilities Content") {
		t.Error("Soul should be before Capabilities")
	}
	if strings.Index(prompt, "Capabilities Content") >= strings.Index(prompt, "User Content") {
		t.Error("Capabilities should be before User")
	}
}
