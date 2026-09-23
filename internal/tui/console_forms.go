package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) consoleFormLayout(title, left, summary string, selectedLine, width, height int, hints ...string) string {
	inner := max(1, width-4)
	leftWidth := m.consoleFormWidth()
	bodyHeight := max(1, height-7)
	lines := strings.Split(left, "\n")
	start := max(0, selectedLine-bodyHeight+2)
	start = min(start, max(0, len(lines)-bodyHeight))
	left = strings.Join(lines[start:min(len(lines), start+bodyHeight)], "\n")
	body := consoleRows(left, inner, bodyHeight)
	if width >= 108 {
		body = consoleColumns(left, summary, leftWidth, inner-leftWidth-3, bodyHeight)
	}
	notice := "Draft · changes apply only on submit"
	if m.busy {
		notice = consoleActivityMark() + " " + m.status
	}
	if m.err != nil {
		notice = "! " + m.err.Error()
		if m.form == formAgent || m.form == formWorkspace || m.form == formRepository {
			notice = "Correct the marked field before you submit."
		}
	}
	color := Tokyo.Muted
	if m.busy {
		color = Tokyo.Yellow
	}
	if m.err != nil {
		color = Tokyo.Red
	}
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join([]string{
		titleLine(title, "", inner), "", body,
		lipgloss.NewStyle().Foreground(color).Render(ansi.Truncate(notice, inner, "…")), consoleRule(inner), footerBar(inner, hints...),
	}, "\n"))
}

func (m Model) viewConsoleAgentForm(width, height int) string {
	fields := m.agentFields()
	fieldWidth := m.consoleFormWidth()
	var lines []string
	selectedLine, lastSection := 0, ""
	for index, field := range fields {
		section := "PLACEMENT"
		switch field.Kind {
		case agentName, agentRole:
			section = "IDENTITY"
		case agentWorkspace, agentContext:
			section = "WORKSPACE / CONTEXT"
		case agentCreate:
			section = ""
		}
		if section != lastSection {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			if section != "" {
				lines = append(lines, consoleSection(section))
			}
			lastSection = section
		}
		if index == m.agentFocus {
			selectedLine = len(lines)
		}
		label, value := m.agentFieldDisplay(field, index == m.agentFocus)
		switch field.Kind {
		case agentWorkspace, agentContext, agentPlacement, agentRepository, agentRemote, agentPlacementSource:
			value += "  " + consoleMark(iconExpanded)
		case agentFetch:
			if m.agentDraft.Worktrees[field.Worktree].FetchFirst {
				value = "[x] Yes"
			} else {
				value = "[ ] No"
			}
		case agentShare:
			if m.agentDraft.Share {
				value = "[x] Shared files"
			} else {
				value = "[ ] Private forks"
			}
		case agentAddWorktree:
			label, value = "", consoleMark(iconAdd)+" Add secondary repository"
		case agentCreate:
			value = "Create agent and open Pi"
			if m.busy {
				value = lipgloss.NewStyle().Foreground(Tokyo.Yellow).Render(consoleActivityMark() + " Creating")
			}
		}
		lines = append(lines, formChoiceRow(label, value, index == m.agentFocus, fieldWidth))
		if index == m.agentFocus && m.err != nil {
			lines = append(lines, consoleError("  ! "+m.err.Error(), fieldWidth))
		}
		if field.Kind == agentShare && m.agentDraft.Share && width < 108 && index == m.agentFocus {
			lines = append(lines, consoleParagraph("  Both agents will edit the same files.", fieldWidth))
		}
		if field.Kind == agentFetch && field.Worktree > 0 && index == m.agentFocus {
			lines = append(lines, mutedStyle.Render("  d remove this secondary repository"))
		}
	}
	startKey := "ctrl+s"
	if m.startupRoute.Plan != nil {
		startKey = "enter on Start"
	}
	return m.consoleFormLayout("New agent", strings.Join(lines, "\n"), m.creationSummary(max(1, width-fieldWidth-7)), selectedLine, width, height,
		keyHint("tab", "choose / next"), keyHint("up/down", "fields"), keyHint(startKey, "create"), keyHint("esc", "close"))
}

func (m Model) creationSummary(width int) string {
	lines := []string{consoleSection("ON START"), ""}
	effect := func(label, text string) {
		heading := lipgloss.NewStyle().Foreground(Tokyo.Foreground).Bold(true).Render(label)
		lines = append(lines, heading, consoleParagraph(text, min(84, width)), "")
	}
	switch m.agentDraft.Placement {
	case 0:
		n := len(m.agentDraft.Worktrees)
		text := fmt.Sprintf("Create %d separate worktrees on new agent branches.", n)
		if n == 1 {
			text = "Create a separate worktree on a new agent branch."
		}
		for _, draft := range m.agentDraft.Worktrees {
			if draft.FetchFirst {
				text += " Fetch the marked sources first."
				break
			}
		}
		effect("Isolated files", text)
	case 1, 4:
		if m.agentDraft.Share {
			effect("Shared files", "Both agents edit the same files. Changes are visible to both; no checkout is created.")
		} else {
			effect("Isolated files", "Create private forks of the selected worktrees. Other agents' files stay separate.")
		}
	case 2:
		effect("Managed directory", "Create a private directory without a Git checkout.")
	case 3:
		effect("Existing files", "Work directly in the selected directory. No separate checkout is created.")
	}
	if m.startupRoute.Plan != nil {
		effect("Approved Plan", "Deliver the approved Plan and start work. The source session stays separate.")
	} else if m.agentDraft.Context > 0 {
		effect("Conversation copy", "Copy the selected agent's conversation. Its later messages stay separate.")
	} else {
		effect("New conversation", "No previous messages are copied.")
	}
	effect("Durable agent", "Closing Pi does not delete the agent or its placement.")
	return strings.Join(lines, "\n")
}

func (m *Model) focusAgentError() {
	if m.err == nil {
		return
	}
	text := strings.ToLower(m.err.Error())
	kind := agentCreate
	switch text {
	case "agent name is required":
		kind = agentName
	case "workspace is not available":
		kind = agentWorkspace
	case "new placement needs at least one repository", "placement repository is not available":
		kind = agentRepository
	case "choose a placement source agent":
		kind = agentPlacementSource
	case "selected worktree is not available":
		kind = agentPlacement
	}
	for i, field := range m.agentFields() {
		if field.Kind == kind {
			m.agentFocus = i
			m.loadAgentInput()
			return
		}
	}
}

func (m Model) viewConsoleWorktreeForm(width, height int) string {
	lines := []string{consoleSection("WORKSPACE / SOURCE")}
	selected := 0
	for i, field := range m.worktreeFields() {
		if i == m.worktreeFocus {
			selected = len(lines)
		}
		label, value := m.worktreeFieldDisplay(field, i == m.worktreeFocus)
		if field == worktreeWorkspace || field == worktreeRemote {
			value += "  " + consoleMark(iconExpanded)
		}
		if field == worktreeCreate {
			lines = append(lines, "")
			if i == m.worktreeFocus {
				selected++
			}
		}
		lines = append(lines, formChoiceRow(label, value, i == m.worktreeFocus, m.consoleFormWidth()))
	}
	summary := consoleSection("ON START") + "\n\n" + consoleParagraph("Create a checkout from the selected source.\n\nOpen a real terminal.\n\nNo agent will be created.", max(1, width-m.consoleFormWidth()-7))
	return m.consoleFormLayout("New worktree", strings.Join(lines, "\n"), summary, selected, width, height, keyHint("tab", "choose / next"), keyHint("ctrl+s", "create / open"), keyHint("esc", "cancel"))
}
