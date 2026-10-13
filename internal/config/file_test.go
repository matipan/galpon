package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalConfigDefaultsAndValidation(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		missing, bad     bool
	}{
		{name: "no file", want: "pi", missing: true},
		{name: "no setting", body: "# Global defaults\n", want: "pi"},
		{name: "pi", body: `default_harness = "pi"`, want: "pi"},
		{name: "claude", body: `default_harness = 'claude' # native agent`, want: "claude"},
		{name: "codex", body: `default_harness = "codex"`, want: "codex"},
		{name: "normalized", body: `default_harness = " Claude "`, want: "claude"},
		{name: "unknown harness", body: `default_harness = "other"`, bad: true},
		{name: "wrong type", body: `default_harness = 42`, bad: true},
		{name: "syntax", body: `default_harness = "pi`, bad: true},
		{name: "unknown key", body: `default_harnes = "claude"`, bad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "settings"))
			t.Setenv("GALPON_STATE_DIR", filepath.Join(root, "state"))
			path := filepath.Join(root, "settings", "galpon", "config.toml")
			if !test.missing {
				writeConfig(t, path, test.body)
			}
			cfg, err := Load()
			if test.bad {
				if err == nil || !strings.Contains(err.Error(), path) {
					t.Fatalf("invalid config must name its file: %v", err)
				}
				return
			}
			if err != nil || cfg.DefaultAgentHarness() != test.want {
				t.Fatalf("default = %q, error %v", cfg.DefaultAgentHarness(), err)
			}
		})
	}
}

func TestGlobalConfigLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GALPON_STATE_DIR", filepath.Join(home, "state"))
	writeConfig(t, filepath.Join(home, ".config", "galpon", "config.toml"), `default_harness = "claude"`)
	for _, xdg := range []string{"", "relative/config"} {
		t.Setenv("XDG_CONFIG_HOME", xdg)
		cfg, err := Load()
		if err != nil || cfg.DefaultAgentHarness() != "claude" {
			t.Fatalf("home configuration: %#v, %v", cfg, err)
		}
	}
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg, err := Load()
	if err != nil || cfg.DefaultAgentHarness() != "pi" {
		t.Fatalf("missing XDG file must not use home configuration: %#v, %v", cfg, err)
	}
	path := filepath.Join(xdg, "galpon", "config.toml")
	writeConfig(t, path, `default_harness = "codex"`)
	cfg, err = Load()
	if err != nil || cfg.DefaultAgentHarness() != "codex" {
		t.Fatalf("XDG configuration: %#v, %v", cfg, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("unreadable configuration must not silently use Pi: %v", err)
	}
}

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
