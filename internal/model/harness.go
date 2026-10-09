package model

import (
	"fmt"
	"strings"
)

const (
	HarnessPi     = "pi"
	HarnessClaude = "claude"
	HarnessCodex  = "codex"
)

// ParseHarness keeps an omitted creation value compatible with Pi agents.
func ParseHarness(value string) (string, error) {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "", HarnessPi:
		return HarnessPi, nil
	case HarnessClaude, HarnessCodex:
		return value, nil
	default:
		return "", fmt.Errorf("unknown harness %q: use pi, claude, or codex", value)
	}
}

func (a Agent) Harness() string {
	if a.Kind == "" {
		return HarnessPi
	}
	return a.Kind
}

func HarnessLabel(kind string) string {
	switch kind {
	case "", HarnessPi:
		return "Pi"
	case HarnessClaude:
		return "Claude Code"
	case HarnessCodex:
		return "Codex"
	default:
		return kind
	}
}
