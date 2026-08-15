package piagent

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

//go:embed extension.ts
var assets embed.FS

type Assets struct {
	Extension string
}

func Materialize(stateDir string) (Assets, error) {
	dir := filepath.Join(stateDir, "runtime", "pi")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Assets{}, err
	}
	values := Assets{Extension: filepath.Join(dir, "galpon.ts")}
	data, err := assets.ReadFile("extension.ts")
	if err != nil {
		return Assets{}, err
	}
	if err := replaceIfChanged(values.Extension, data); err != nil {
		return Assets{}, err
	}
	obsoleteTheme := filepath.Join(dir, "galpon-tokyonight-moon.json")
	if err := os.Remove(obsoleteTheme); err != nil && !os.IsNotExist(err) {
		return Assets{}, fmt.Errorf("remove obsolete Pi theme: %w", err)
	}
	return values, nil
}

func Command(cfg config.Config, values Assets, agent model.Agent, contextSessionPath string) []string {
	sessionID := agent.SessionID
	if sessionID == "" {
		sessionID = agent.ID
	}
	args := []string{
		cfg.PiBin,
		"--approve",
		"--provider", cfg.PiProvider,
		"--session-dir", filepath.Join(cfg.StateDir, "agents", agent.ID, "sessions"),
		"--name", agent.Title,
		"--extension", values.Extension,
	}
	if agent.SessionPath == "" && agent.ContextAgentID != "" && contextSessionPath != "" {
		args = append(args, "--fork", contextSessionPath, "--session-id", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	if cfg.PiModel != "" {
		args = append(args, "--model", cfg.PiModel)
	}
	return args
}

func replaceIfChanged(path string, data []byte) error {
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, data) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("install Pi asset: %w", err)
	}
	return nil
}
