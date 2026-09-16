package piagent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"testing"
)

func TestPlanModeDefaultsAllowGalponTools(t *testing.T) {
	var settings struct {
		Tools []string `json:"defaultPlanTools"`
	}
	if err := json.Unmarshal(planModeDefaults, &settings); err != nil {
		t.Fatal(err)
	}
	extension, err := assets.ReadFile("extension.ts")
	if err != nil {
		t.Fatal(err)
	}
	tools := regexp.MustCompile(`name: "(galpon_[a-z_]+)"`).FindAllSubmatch(extension, -1)
	if len(tools) == 0 {
		t.Fatal("no Galpon tools found")
	}
	allowed := []string{"read", "bash", "powershell", "grep", "find", "ls"}
	for _, tool := range tools {
		allowed = append(allowed, string(tool[1]))
	}
	for _, tool := range allowed {
		if !slices.Contains(settings.Tools, tool) {
			t.Errorf("Plan mode excludes %s", tool)
		}
	}
	seen := make(map[string]bool)
	for _, tool := range settings.Tools {
		if !slices.Contains(allowed, tool) || seen[tool] {
			t.Errorf("unexpected or duplicate default Plan tool %s", tool)
		}
		seen[tool] = true
	}
}

func TestPlanModeDefaultsPublishOnce(t *testing.T) {
	dir := t.TempDir()
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if err := ensurePlanModeDefaults(dir); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	path := filepath.Join(dir, "pi-plan-mode.json")
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, planModeDefaults) {
		t.Fatalf("incomplete Plan defaults: %s; %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Plan settings permissions: %v; %v", info, err)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("Plan setup left temporary files: %v; %v", files, err)
	}
}

func TestPlanModeDefaultsPreserveUserSettings(t *testing.T) {
	for _, test := range []struct {
		name, file, content string
	}{
		{"custom", "pi-plan-mode.json", `{ "defaultPlanTools": ["read", "custom_tool"], "thinkingLevel": "high" }`},
		{"none", "pi-plan-mode.json", `{"defaultPlanTools":[]}`},
		{"automatic", "pi-plan-mode.json", `{}`},
		{"invalid", "pi-plan-mode.json", `{`},
		{"legacy", "plan-mode.json", `{ "defaultPlanTools": ["read"] }`},
		{"invalid-legacy", "plan-mode.json", `{`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, test.file)
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := ensurePlanModeDefaults(dir); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != test.content {
				t.Fatalf("user Plan settings changed: %s; %v", data, err)
			}
			if test.file == "plan-mode.json" {
				if _, err := os.Lstat(filepath.Join(dir, "pi-plan-mode.json")); !os.IsNotExist(err) {
					t.Fatalf("defaults replaced the legacy policy: %v", err)
				}
			}
		})
	}
}

func TestPlanModeDefaultsPreserveSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "missing-user-settings")
	path := filepath.Join(dir, "pi-plan-mode.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := ensurePlanModeDefaults(dir); err != nil {
		t.Fatal(err)
	}
	if actual, err := os.Readlink(path); err != nil || actual != target {
		t.Fatalf("Plan settings link changed: %s; %v", actual, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("Plan setup followed the user link: %v", err)
	}
}
