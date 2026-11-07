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
// shows a Pi agent in a tab of the Herdr app: a host runs Pi in RPC mode, and
// the agent mod of the app draws the conversation.
const (
	ViewTerminal = ""
	ViewApp      = "app"
)

// HarnessPiApp is the harness choice of the forms for a Pi agent in the app
// view. It is not a harness: the agent gets HarnessPi and ViewApp.
const HarnessPiApp = "pi-app"

// ParseHarnessChoice reads a harness choice of a form or of the
// configuration: a harness, or HarnessPiApp. It returns the harness and the
// view.
func ParseHarnessChoice(value string) (harness, view string, err error) {
	if strings.EqualFold(strings.TrimSpace(value), HarnessPiApp) {
		return HarnessPi, ViewApp, nil
	}
	harness, err = ParseHarness(value)
	if err != nil {
		return "", "", fmt.Errorf("unknown harness %q: use pi-app, pi, claude, or codex", strings.TrimSpace(value))
	}
	return harness, ViewTerminal, nil
}

// HarnessChoice gives the harness choice of a harness and a view.
func HarnessChoice(harness, view string) string {
	if view == ViewApp && (harness == "" || harness == HarnessPi) {
		return HarnessPiApp
	}
	if harness == "" {
		return HarnessPi
	}
	return harness
}

// HarnessChoiceLabel names a harness choice for people.
func HarnessChoiceLabel(choice string) string {
	if choice == HarnessPiApp {
		return "Pi app"
	}
	return HarnessLabel(choice)
}

// IsApp reports whether the agent opens in a tab of the Herdr app.
func (a Agent) IsApp() bool { return a.View == ViewApp && a.Harness() == HarnessPi }

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
