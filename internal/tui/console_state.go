package tui

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

func consoleASCII() bool { return os.Getenv("GALPON_ASCII") == "1" }

func consoleGlyph(symbol, fallback string) string {
	if consoleASCII() {
		return fallback
	}
	return symbol
}

func consolePulse(frame int) string {
	if os.Getenv("GALPON_UI_MOTION") == "0" {
		return consoleGlyph("◐", "*")
	}
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if consoleASCII() {
		frames = []string{"|", "/", "-", "\\"}
	}
	return frames[frame%len(frames)]
}

const consoleAnimationInterval = 80 * time.Millisecond

type consoleAnimationTick time.Time

func consoleActivityMark() string {
	return consolePulse(int(time.Now().UnixMilli() / consoleAnimationInterval.Milliseconds()))
}

// Animation redraws local state only. Dashboard polling keeps its own cadence.
func (m *Model) scheduleConsoleAnimation() tea.Cmd {
	if m.animationPending || m.quitting || os.Getenv("GALPON_UI_MOTION") == "0" {
		return nil
	}
	active := m.busy
	if m.screen == screenSwitcher {
		for _, item := range m.results {
			if item.Kind == resultAgent && item.AgentState == agentStateWorking {
				active = true
				break
			}
		}
	}
	if m.screen == screenOperations {
		active = active || !m.operationsLoaded || m.operations.Agent.Status == "running" || m.operations.Agent.Status == "starting"
		for _, row := range flattenAgentOperationsWork(m.operations) {
			active = active || consoleWorkIsLive(row.item.Observation)
		}
	}
	if !active {
		return nil
	}
	m.animationPending = true
	return tea.Tick(consoleAnimationInterval, func(t time.Time) tea.Msg { return consoleAnimationTick(t) })
}

func consoleWorkIsLive(observation model.WorkObservation) bool {
	return observation.State == "started" && observation.Lease == "fresh" && observation.FreshnessAt > time.Now().UnixMilli()
}

func (m Model) consoleFormWidth() int {
	terminalWidth := m.width
	if terminalWidth <= 0 {
		terminalWidth = 80
	}
	width := max(1, terminalWidth-4)
	if terminalWidth >= 108 {
		left, _ := consoleSplit(width)
		return left
	}
	return width
}

func (m *Model) loadControlInspector() tea.Cmd {
	if m.client == nil || m.screen != screenSwitcher || m.cursor < 0 || m.cursor >= len(m.results) {
		return nil
	}
	item := m.results[m.cursor]
	if item.Kind != resultAgent {
		return nil
	}
	if m.operationsAgent == item.ID && m.operationsInFlight {
		return nil
	}
	if m.operationsAgent != item.ID {
		m.operationsLoaded = false
		m.operationsErr = nil
		m.operationsRefreshErr = nil
	}
	m.operationsAgent = item.ID
	m.operationsGeneration++
	m.operationsInFlight = true
	return m.loadOperations(item.ID, m.operationsGeneration)
}
