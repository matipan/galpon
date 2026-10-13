package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/matipan/galpon/internal/model"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	DefaultHarness string
	StateDir       string
	Socket         string
	PiBin          string
	PiProvider     string
	PiModel        string
	ClaudeBin      string
	CodexBin       string
	HerdrBin       string
}

func Load() (Config, error) {
	defaultHarness, err := loadDefaultHarness()
	if err != nil {
		return Config{}, err
	}
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
	piProvider := strings.TrimSpace(os.Getenv("GALPON_PI_PROVIDER"))
	if piProvider == "" {
		piProvider = "openai-codex"
	}
	herdrBin := strings.TrimSpace(os.Getenv("GALPON_HERDR_BIN"))
	if herdrBin == "" {
		herdrBin = "herdr"
	}
	return Config{
		DefaultHarness: defaultHarness,
		StateDir:       abs,
		Socket:         filepath.Join(abs, "galpon.sock"),
		PiBin:          "pi",
		PiProvider:     piProvider,
		PiModel:        strings.TrimSpace(os.Getenv("GALPON_PI_MODEL")),
		ClaudeBin:      "claude",
		CodexBin:       "codex",
		HerdrBin:       herdrBin,
	}, nil
}

func (c Config) DefaultAgentHarness() string {
	if c.DefaultHarness == "" {
		return model.HarnessPi
	}
	return c.DefaultHarness
}

func loadDefaultHarness() (string, error) {
	root := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(root) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate Galpon configuration: %w", err)
		}
		root = filepath.Join(home, ".config")
	}
	path := filepath.Join(root, "galpon", "config.toml")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return model.HarnessPi, nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var settings struct {
		DefaultHarness string `toml:"default_harness"`
	}
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&settings); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	harness, err := model.ParseHarness(settings.DefaultHarness)
	if err != nil {
		return "", fmt.Errorf("%s: default_harness: %w", path, err)
	}
	return harness, nil
}
