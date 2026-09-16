package piagent

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed plan-mode-defaults.json
var planModeDefaults []byte

// Seed new installations only. A current or legacy settings file can contain
// an explicit policy, including the upstream automatic policy (no tool list).
// Never replace that choice, even when the existing file is invalid.
func ensurePlanModeDefaults(configDir string) error {
	for _, name := range []string{"pi-plan-mode.json", "plan-mode.json"} {
		if _, err := os.Lstat(filepath.Join(configDir, name)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect Pi plan-mode settings: %w", err)
		}
	}
	file, err := os.CreateTemp(configDir, ".galpon-plan-mode-*.json")
	if err != nil {
		return fmt.Errorf("stage Pi plan-mode defaults: %w", err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if _, err := file.Write(planModeDefaults); err != nil {
		return fmt.Errorf("write Pi plan-mode defaults: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Pi plan-mode defaults: %w", err)
	}
	// Publish complete settings without overwriting a concurrent user save.
	if err := os.Link(file.Name(), filepath.Join(configDir, "pi-plan-mode.json")); err != nil && !os.IsExist(err) {
		return fmt.Errorf("publish Pi plan-mode defaults: %w", err)
	}
	return nil
}
