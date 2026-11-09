package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// consoleFormLayout is the frame of a form: a chip with the title and the
// main field at the top, the rows, a notice, and the keys. The form has no
// side pane.
func (m Model) consoleFormLayout(title, head, body string, selectedLine, width, height int, hints ...string) string {
	inner := max(1, width-4)
	bodyHeight := max(1, height-5)
	lines := strings.Split(body, "\n")
	start := max(0, selectedLine-bodyHeight+2)
	start = min(start, max(0, len(lines)-bodyHeight))
	body = strings.Join(lines[start:min(len(lines), start+bodyHeight)], "\n")
	notice, color := "", Tokyo.Muted
	if m.busy {
		notice, color = consoleActivityMark()+" "+m.status, Tokyo.Yellow
	} else if m.err != nil {
		notice, color = "! "+m.err.Error(), Tokyo.Red
	}
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join([]string{
		screenHeader(title, head, inner), consoleRows(body, inner, bodyHeight),
		lipgloss.NewStyle().Foreground(color).Render(ansi.Truncate(notice, inner, "…")), footerBar(inner, hints...),
	}, "\n"))
}

// formSubmitRow is the last row of a form: the action that Enter or ctrl+s
// runs.
func formSubmitRow(text string, selected bool, width int) string {
	style := rowStyle.Foreground(Tokyo.Foreground)
	prefix := "  "
	key := "ctrl+s"
	if selected {
		style = selectedStyle.Foreground(Tokyo.Status).Bold(true)
		prefix = consoleMark(iconFocus) + " "
		key = "enter"
	}
	return consoleFillRow(style.Render(prefix+consoleGlyph("✓", "+")+" "+consoleCell(text, max(8, width-14)))+style.Foreground(Tokyo.Muted).Render(key), width, style)
}

func (m Model) viewConsoleAgentForm(width, height int) string {
	fields := m.agentFields()
	fieldWidth := m.consoleFormWidth()
	var lines []string
	selectedLine := 0
	head := ""
	for index, field := range fields {
		selected := index == m.agentFocus
		label, value := m.agentFieldDisplay(field, selected)
		if field.Kind == agentName {
			head = value
			if !selected && strings.TrimSpace(m.agentDraft.Name) == "" {
				head = mutedStyle.Render("Agent name")
			}
			continue
		}
		if selected {
			selectedLine = len(lines)
		}
		switch field.Kind {
		case agentHarness, agentWorkspace, agentContext, agentPlacement, agentRepository, agentRemote, agentPlacementSource:
			value += "  " + consoleMark(iconCollapsed)
		case agentFetch:
			value = "[ ] No"
			if m.agentDraft.Worktrees[field.Worktree].FetchFirst {
				value = "[x] Yes"
			}
		case agentShare:
			value = "[ ] Private forks"
			if m.agentDraft.Share {
				value = "[x] Shared files"
			}
		case agentAddWorktree:
			label, value = "", consoleMark(iconAdd)+" Add another repository"
		case agentMore:
			marker := iconCollapsed
			if m.agentMore {
				marker = iconExpanded
			}
			label, value = consoleMark(marker)+" "+label, mutedStyle.Render(value)
		case agentCreate:
			lines = append(lines, "", formSubmitRow(value, selected, fieldWidth))
			if selected {
				selectedLine = len(lines) - 1
			}
			continue
		}
		lines = append(lines, formChoiceRow(label, value, selected, fieldWidth))
		if selected && m.err != nil {
			lines = append(lines, consoleError("  ! "+m.err.Error(), fieldWidth))
		}
		if field.Kind == agentContext && m.crossHarnessContext() && selected {
			lines = append(lines, consoleParagraph("  Copy saved history, images, and active context. Past tools stay as text.", fieldWidth))
		}
		if field.Kind == agentShare && m.agentDraft.Share && selected {
			lines = append(lines, consoleParagraph("  Both agents will edit the same files.", fieldWidth))
		}
		if field.Kind == agentFetch && field.Worktree > 0 && selected {
			lines = append(lines, mutedStyle.Render("  d removes this repository"))
		}
	}
	title := "New agent"
	if m.agentDraft.Context > 0 {
		title = "Fork agent"
	}
	enter := "create"
	switch fields[m.agentFocus].Kind {
	case agentHarness, agentWorkspace, agentContext, agentPlacement, agentRepository, agentRemote, agentPlacementSource:
		enter = "choose"
	case agentMore:
		enter = "more options"
		if m.agentMore {
			enter = "fewer options"
		}
	}
	return m.consoleFormLayout(title, head, strings.Join(lines, "\n"), selectedLine, width, height,
		keyHint("enter", enter), keyHint("tab", "next"), keyHint("ctrl+s", "create"), keyHint("esc", "back"))
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
	var lines []string
	selected := 0
	for i, field := range m.worktreeFields() {
		label, value := m.worktreeFieldDisplay(field, i == m.worktreeFocus)
		if field == worktreeCreate {
			lines = append(lines, "", formSubmitRow(value, i == m.worktreeFocus, m.consoleFormWidth()))
			if i == m.worktreeFocus {
				selected = len(lines) - 1
			}
			continue
		}
		if i == m.worktreeFocus {
			selected = len(lines)
		}
		if field == worktreeWorkspace || field == worktreeRemote {
			value += "  " + consoleMark(iconCollapsed)
		}
		lines = append(lines, formChoiceRow(label, value, i == m.worktreeFocus, m.consoleFormWidth()))
	}
	repository, _ := m.dashboard.Repository(m.worktreeDraft.RepositoryID)
	return m.consoleFormLayout("New worktree", mutedStyle.Render(consoleText(repository.Title)), strings.Join(lines, "\n"), selected, width, height,
		keyHint("tab", "next"), keyHint("ctrl+s", "create and open"), keyHint("esc", "back"))
}
