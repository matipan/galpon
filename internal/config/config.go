package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	StateDir   string
	Socket     string
	PiBin      string
	PiProvider string
	PiModel    string
	ClaudeBin  string
	CodexBin   string
	HerdrBin   string
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
	piProvider := strings.TrimSpace(os.Getenv("GALPON_PI_PROVIDER"))
	if piProvider == "" {
		piProvider = "openai-codex"
	}
	herdrBin := strings.TrimSpace(os.Getenv("GALPON_HERDR_BIN"))
	if herdrBin == "" {
		herdrBin = "herdr"
	}
	return Config{
		StateDir:   abs,
		Socket:     filepath.Join(abs, "galpon.sock"),
		PiBin:      "pi",
		PiProvider: piProvider,
		PiModel:    strings.TrimSpace(os.Getenv("GALPON_PI_MODEL")),
		ClaudeBin:  "claude",
		CodexBin:   "codex",
		HerdrBin:   herdrBin,
	}, nil
}
