package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	StateDir       string
	Socket         string
	DefaultHarness string
	PiBin          string
	PiProvider     string
	PiModel        string
	CodexBin       string
	CodexModel     string
	ClaudeBin      string
	ClaudeModel    string
	HerdrBin       string
}

func Load() (Config, error) {
	stateDir := strings.TrimSpace(os.Getenv("GALPON_STATE_DIR"))
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, err
		}
		stateDir = filepath.Join(home, ".local", "state", "galpon")
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return Config{}, err
	}
	if abs == string(filepath.Separator) {
		return Config{}, errors.New("galpon state directory cannot be the filesystem root")
	}
	piBin := strings.TrimSpace(os.Getenv("GALPON_PI_BIN"))
	if piBin == "" {
		piBin = "pi"
	}
	piProvider := strings.TrimSpace(os.Getenv("GALPON_PI_PROVIDER"))
	if piProvider == "" {
		piProvider = "openai-codex"
	}
	codexBin := strings.TrimSpace(os.Getenv("GALPON_CODEX_BIN"))
	if codexBin == "" {
		codexBin = "codex"
	}
	claudeBin := strings.TrimSpace(os.Getenv("GALPON_CLAUDE_BIN"))
	if claudeBin == "" {
		claudeBin = "claude"
	}
	herdrBin := strings.TrimSpace(os.Getenv("GALPON_HERDR_BIN"))
	if herdrBin == "" {
		herdrBin = "herdr"
	}
	defaultHarness := ""
	if data, readErr := os.ReadFile(filepath.Join(abs, "config.json")); readErr == nil {
		var saved struct {
			DefaultHarness string `json:"defaultHarness"`
		}
		if json.Unmarshal(data, &saved) == nil {
			defaultHarness = strings.ToLower(strings.TrimSpace(saved.DefaultHarness))
		}
	}
	if override, ok := os.LookupEnv("GALPON_DEFAULT_HARNESS"); ok && strings.TrimSpace(override) != "" {
		defaultHarness = strings.ToLower(strings.TrimSpace(override))
	}
	if defaultHarness == "" {
		defaultHarness = "pi"
	}
	if defaultHarness == "cloud" {
		defaultHarness = "claude"
	}
	if defaultHarness != "pi" && defaultHarness != "codex" && defaultHarness != "claude" {
		return Config{}, errors.New("GALPON_DEFAULT_HARNESS must be pi, codex, or claude")
	}
	return Config{
		StateDir: abs, Socket: filepath.Join(abs, "galpon.sock"), DefaultHarness: defaultHarness,
		PiBin: piBin, PiProvider: piProvider, PiModel: strings.TrimSpace(os.Getenv("GALPON_PI_MODEL")),
		CodexBin: codexBin, CodexModel: strings.TrimSpace(os.Getenv("GALPON_CODEX_MODEL")),
		ClaudeBin: claudeBin, ClaudeModel: strings.TrimSpace(os.Getenv("GALPON_CLAUDE_MODEL")),
		HerdrBin: herdrBin,
	}, nil
}

func SaveDefaultHarness(stateDir, value string) error {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "cloud" {
		value = "claude"
	}
	if value != "pi" && value != "codex" && value != "claude" {
		return fmt.Errorf("default harness must be pi, codex, or claude")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(stateDir, "config.json")
	settings := map[string]any{}
	if current, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(current, &settings); err != nil {
			return fmt.Errorf("parse Galpon configuration: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	settings["defaultHarness"] = value
	data, _ := json.MarshalIndent(settings, "", "  ")
	data = append(data, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("save Galpon configuration: %w", err)
	}
	return nil
}
