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

// The views of an agent. ViewTerminal runs the harness in a terminal. ViewApp
// shows a Pi or Claude Code agent in a tab of the Herdr app: a host runs the
// harness without its terminal interface, and the agent mod of the app draws
// the conversation.
const (
	ViewTerminal = ""
	ViewApp      = "app"
)

// HarnessPiApp and HarnessClaudeApp are the harness choices of the forms for
// an agent in the app view. They are not harnesses: the agent gets HarnessPi
// or HarnessClaude and ViewApp.
const (
	HarnessPiApp     = "pi-app"
	HarnessClaudeApp = "claude-app"
)

// AppViewHarness reports whether an agent of the harness can open in the app
// view. Codex has no app view.
func AppViewHarness(harness string) bool {
	return harness == "" || harness == HarnessPi || harness == HarnessClaude
}

// ParseHarnessChoice reads a harness choice of a form or of the
// configuration: a harness, HarnessPiApp or HarnessClaudeApp. It returns the
// harness and the view.
func ParseHarnessChoice(value string) (harness, view string, err error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case HarnessPiApp:
		return HarnessPi, ViewApp, nil
	case HarnessClaudeApp:
		return HarnessClaude, ViewApp, nil
	}
	harness, err = ParseHarness(value)
	if err != nil {
		return "", "", fmt.Errorf("unknown harness %q: use pi-app, claude-app, pi, claude, or codex", strings.TrimSpace(value))
	}
	return harness, ViewTerminal, nil
}

// HarnessChoice gives the harness choice of a harness and a view.
func HarnessChoice(harness, view string) string {
	if view == ViewApp {
		switch harness {
		case "", HarnessPi:
			return HarnessPiApp
		case HarnessClaude:
			return HarnessClaudeApp
		}
	}
	if harness == "" {
		return HarnessPi
	}
	return harness
}

// HarnessChoiceLabel names a harness choice for people.
func HarnessChoiceLabel(choice string) string {
	switch choice {
	case HarnessPiApp:
		return "Pi app"
	case HarnessClaudeApp:
		return "Claude app"
	}
	return HarnessLabel(choice)
}

// IsApp reports whether the agent opens in a tab of the Herdr app.
func (a Agent) IsApp() bool { return a.View == ViewApp && AppViewHarness(a.Harness()) }

// ParseHarness normalizes a harness name. An empty value means Pi.
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
