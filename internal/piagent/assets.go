package piagent

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

//go:embed extension.ts galpon-console.ts galpon-native-tools.ts galpon-background.ts galpon-review.ts galpon-neovim-review.ts neovim-review.lua builtin/rpiv-todo
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
	// Install imported modules before the entry point. A live Pi watcher can then
	// reload the entry point only after all of its dependencies are available.
	for _, name := range []string{"builtin/rpiv-todo/view/tool-frame.ts", "galpon-background.ts", "galpon-native-tools.ts", "galpon-console.ts", "galpon-review.ts", "galpon-neovim-review.ts", "neovim-review.lua"} {
		data, err := assets.ReadFile(name)
		if err != nil {
			return Assets{}, err
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			return Assets{}, err
		}
		if err := replaceIfChanged(filepath.Join(dir, name), data); err != nil {
			return Assets{}, err
		}
	}
	data, err := assets.ReadFile("extension.ts")
	if err != nil {
		return Assets{}, err
	}
	if err := replaceIfChanged(values.Extension, data); err != nil {
		return Assets{}, err
	}
	// Remove runtime files that the installed extension does not use. Do this
	// after the entry point, so that a live Pi reloads only the current files.
	for _, name := range []string{"galpon-tokyonight-moon.json", "galpon-plan.ts", "galpon-plan-launch.ts", "plan-launches"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			return Assets{}, fmt.Errorf("remove obsolete Pi runtime file %s: %w", name, err)
		}
	}
	return values, nil
}

func Command(cfg config.Config, values Assets, agent model.Agent, contextSessionPath string) []string {
	return command(cfg, values, agent, contextSessionPath, false)
}

func BackgroundCommand(cfg config.Config, values Assets, agent model.Agent, contextSessionPath string) []string {
	return command(cfg, values, agent, contextSessionPath, true)
}

func command(cfg config.Config, values Assets, agent model.Agent, contextSessionPath string, background bool) []string {
	sessionID := agent.SessionID
	if sessionID == "" {
		sessionID = agent.ID
	}
	args := []string{
		cfg.PiBin,
		"--approve",
		"--session-dir", filepath.Join(cfg.StateDir, "agents", agent.ID, "sessions"),
		"--name", agent.Title,
		"--extension", values.Extension,
	}
	if agent.SessionPath == "" && contextSessionPath != "" {
		args = append(args, "--fork", contextSessionPath, "--session-id", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	// Pi uses --provider only to resolve --model and rejects it alone. Without
	// a configured model, Pi starts with its own default model.
	if cfg.PiModel != "" {
		if cfg.PiProvider != "" {
			args = append(args, "--provider", cfg.PiProvider)
		}
		args = append(args, "--model", cfg.PiModel)
	}
	if background {
		args = append(args, "--mode", "rpc")
	}
	return args
}

// ImportedSessionTarget is where an imported conversation waits for the
// agent's first Pi launch.
func ImportedSessionTarget(stateDir, agentID string) string {
	return filepath.Join(stateDir, "agents", agentID, "import", "session.jsonl")
}

// ImportedSessionPath returns the imported conversation if it still waits.
func ImportedSessionPath(stateDir, agentID string) string {
	path := ImportedSessionTarget(stateDir, agentID)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}

// ForkSource prefers a staged import or converted snapshot over the live source.
// A native session from another harness must never reach Pi's session loader.
func ForkSource(stateDir string, agent model.Agent, contextAgent func(string) model.Agent) (string, error) {
	if agent.SessionPath != "" {
		return "", nil
	}
	if path := ImportedSessionPath(stateDir, agent.ID); path != "" {
		return path, nil
	}
	if agent.ContextAgentID != "" {
		source := contextAgent(agent.ContextAgentID)
		if source.ID == "" || source.SessionPath == "" {
			return "", fmt.Errorf("context source is unavailable and no saved fork snapshot exists")
		}
		if source.Harness() != agent.Harness() {
			return "", fmt.Errorf("converted conversation snapshot is missing; create the cross-harness fork again")
		}
		return source.SessionPath, nil
	}
	return "", nil
}

func materializeDirectory(source, target string) error {
	return fs.WalkDir(assets, source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		data, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		return replaceIfChanged(destination, data)
	})
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
