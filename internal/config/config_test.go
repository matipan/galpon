package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedDefaultHarnessAndEnvironmentOverride(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("GALPON_STATE_DIR", stateDir)
	t.Setenv("GALPON_DEFAULT_HARNESS", "")
	if err := os.WriteFile(filepath.Join(stateDir, "config.json"), []byte(`{"futureSetting":{"enabled":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveDefaultHarness(stateDir, "codex"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(stateDir, "config.json")); err != nil || !strings.Contains(string(data), "futureSetting") {
		t.Fatalf("saved future config = %s, %v", data, err)
	}
	// An explicitly empty environment value does not mask the saved setting.
	cfg, err := Load()
	if err != nil || cfg.DefaultHarness != "codex" {
		t.Fatalf("empty override config = %#v, %v", cfg, err)
	}
	if err := os.Unsetenv("GALPON_DEFAULT_HARNESS"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil || cfg.DefaultHarness != "codex" {
		t.Fatalf("saved config = %#v, %v", cfg, err)
	}
	t.Setenv("GALPON_DEFAULT_HARNESS", "Cloud")
	cfg, err = Load()
	if err != nil || cfg.DefaultHarness != "claude" {
		t.Fatalf("environment config = %#v, %v", cfg, err)
	}
	if info, err := os.Stat(filepath.Join(stateDir, "config.json")); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %v, %v", info, err)
	}
}
