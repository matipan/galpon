package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHarnessLaunchesFollowUserPATH(t *testing.T) {
	root := t.TempDir()
	first, upgraded, override := filepath.Join(root, "first"), filepath.Join(root, "upgraded"), filepath.Join(root, "override")
	for _, directory := range []string{first, upgraded, override} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"pi", "claude", "codex"} {
			body := "#!/bin/sh\nprintf '%s' '" + filepath.Base(directory) + "'\n"
			if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GALPON_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("PATH", first)
	t.Setenv("GALPON_PI_BIN", filepath.Join(override, "pi"))
	t.Setenv("GALPON_CLAUDE_BIN", filepath.Join(override, "claude"))
	t.Setenv("GALPON_CODEX_BIN", filepath.Join(override, "codex"))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{first, upgraded} {
		t.Setenv("PATH", directory)
		for _, command := range []string{cfg.PiBin, cfg.ClaudeBin, cfg.CodexBin} {
			output, err := exec.Command(command).Output()
			if err != nil || string(output) != filepath.Base(directory) {
				t.Fatalf("launch %s: output %q, error %v", command, output, err)
			}
		}
	}
}
