package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/matipan/galpon/internal/model"
)

type consoleMessageResult struct {
	target  string
	message model.AgentMessage
	err     error
}

func (m *Model) beginMessage() tea.Cmd {
	if !m.selectedSwitcherActionable() || m.selectedHiddenBlocked() || m.results[m.cursor].Kind != resultAgent {
		m.status = "Select an available agent to message"
		return nil
	}
	m.messageTarget = m.results[m.cursor].ID
	if m.messageDrafts == nil {
		m.messageDrafts = map[string]string{}
	}
	m.messageInput = textarea.New()
	m.messageInput.Prompt = "  "
	m.messageInput.Placeholder = "Write a message. Nothing is sent until you select Send."
	m.messageInput.ShowLineNumbers = false
	m.messageInput.CharLimit = 0
	m.messageInput.SetValue(m.messageDrafts[m.messageTarget])
	m.messageInput.FocusedStyle.Text = rowStyle
	m.messageInput.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(Tokyo.Status)
	m.messageInput.FocusedStyle.Placeholder = mutedStyle
	m.screen = screenMessage
	m.messageSendFocus = false
	m.err = nil
	m.resizeMessage()
	return m.messageInput.Focus()
}

func (m *Model) resizeMessage() {
	m.messageInput.SetWidth(max(12, min(100, m.width-6)))
	agent, _ := m.dashboard.Agent(m.messageTarget)
	workspace, _ := m.dashboard.Workspace(agent.WorkspaceID)
	addressLines := lipgloss.Height(consoleParagraph("TO    "+agent.Title+" / "+workspace.Title, max(1, m.width-4)))
	errorLines := 0
	if m.err != nil {
		errorLines = lipgloss.Height(consoleParagraph("! "+m.err.Error(), max(1, m.width-4)))
	}
	m.messageInput.SetHeight(max(3, m.height-10-addressLines-errorLines))
}

func (m *Model) updateMessage(key tea.KeyMsg) tea.Cmd {
	if m.busy {
		return nil
	}
	switch key.String() {
	case "esc":
		m.messageDrafts[m.messageTarget] = m.messageInput.Value()
		m.screen = screenSwitcher
		m.status = "Draft kept in this popup. Open Message on this agent to continue."
		return m.focusSwitcher()
	case "tab", "shift+tab":
		m.messageSendFocus = !m.messageSendFocus
		if m.messageSendFocus {
			m.messageInput.Blur()
			return nil
		}
		return m.messageInput.Focus()
	case "enter":
		if m.messageSendFocus {
			text := m.messageInput.Value()
			if strings.TrimSpace(text) == "" {
				m.err = fmt.Errorf("write a message before sending")
				m.resizeMessage()
				return nil
			}
			target := m.messageTarget
			m.messageDrafts[target] = text
			m.busy = true
			m.err = nil
			return func() tea.Msg {
				message, err := m.client.Send(context.Background(), target, text)
				return consoleMessageResult{target, message, err}
			}
		}
	}
	if m.messageSendFocus {
		return nil
	}
	var command tea.Cmd
	m.messageInput, command = m.messageInput.Update(key)
	m.messageDrafts[m.messageTarget] = m.messageInput.Value()
	return command
}

func (m Model) viewMessage(width, height int) string {
	inner := max(1, width-4)
	agent, _ := m.dashboard.Agent(m.messageTarget)
	workspace, _ := m.dashboard.Workspace(agent.WorkspaceID)
	state := "DRAFT / not submitted"
	if m.busy {
		state = "SUBMITTING / awaiting confirmation"
	}
	if m.err != nil {
		state = "NOT CONFIRMED"
	}
	lines := []string{titleLine(consoleMark(iconMessage)+" Message", state, inner), "", "FROM  You", consoleParagraph("TO    "+agent.Title+" / "+workspace.Title, inner), "", m.messageInput.View()}
	style := rowStyle
	action := "  Send message"
	if m.messageSendFocus {
		style = selectedStyle.Foreground(Tokyo.Status).Bold(true)
		action = consoleMark(iconFocus) + " Send message · enter"
	}
	if m.busy {
		style = style.Foreground(Tokyo.Yellow)
		action = consoleActivityMark() + " Submitting message"
	}
	action = style.Render(consoleCell(action, inner))
	lines = append(lines, "", action)
	if m.err != nil {
		lines = append(lines, consoleError("! "+m.err.Error(), inner))
	}
	lines = append(lines, "", mutedStyle.Render("tab draft / send   enter newline   esc back (draft kept in this popup)"))
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join(lines, "\n"))
}
