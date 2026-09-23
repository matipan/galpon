package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m Model) viewConsoleRemoteForm(width, height int) string {
	draft := m
	draft.commitRemoteInput()
	repository := "Choose a repository"
	if draft.remoteDraft.Repository >= 0 && draft.remoteDraft.Repository < len(m.dashboard.Repositories) {
		repository = m.dashboard.Repositories[draft.remoteDraft.Repository].Title
	}
	value := func(index int, text string) string {
		if index == m.remoteFocus {
			return m.formInput.View()
		}
		return consoleText(text)
	}
	pushDefault := "[ ] Keep current"
	if m.remoteDraft.PushDefault {
		pushDefault = "[x] Use this remote"
	}
	fields := [][2]string{{"Repository", repository + "  " + consoleMark(iconExpanded)}, {"Name", value(1, draft.remoteDraft.Name)}, {"Fetch URL", value(2, draft.remoteDraft.FetchURL)}, {"Push URL", value(3, draft.remoteDraft.PushURL)}, {"Default push", pushDefault}, {"Save", "Add remote"}}
	lines := []string{consoleSection("REMOTE")}
	for i, field := range fields {
		lines = append(lines, formChoiceRow(field[0], field[1], i == m.remoteFocus, m.consoleFormWidth()))
	}
	summaryWidth := max(1, width-m.consoleFormWidth()-7)
	effect := "Add a named source to the selected repository. No agent or worktree is created."
	if m.remoteDraft.PushDefault {
		effect += "\n\nNew pushes will use this remote by default."
	}
	summary := consoleSection("ON SAVE") + "\n\n" + consoleParagraph(effect, summaryWidth)
	return m.consoleFormLayout("Add remote", strings.Join(lines, "\n"), summary, m.remoteFocus+1, width, height, keyHint("tab", "choose / next"), keyHint("ctrl+s", "save"), keyHint("esc", "cancel"))
}

func (m Model) viewConsoleSimpleForm(width, height int) string {
	title, label, description, summary := "New workspace", "Title", "Group related agent and human work.", "Create a durable workspace. Repositories and agents are added separately."
	if m.form == formRepository {
		title, label, description, summary = "Add repository", "Path or URL", "Use a local path or a Git SSH / HTTPS URL.", "Fetch branches into a private bare repository. Existing source files stay in place. No agent is created."
	}
	left := consoleSection("DETAILS") + "\n\n" + consoleParagraph(description, m.consoleFormWidth()) + "\n\n" + formChoiceRow(label, m.formInput.View(), true, m.consoleFormWidth())
	if m.err != nil {
		left += "\n\n" + consoleError("! "+m.err.Error(), m.consoleFormWidth())
	}
	return m.consoleFormLayout(title, left, consoleSection("ON CREATE")+"\n\n"+consoleParagraph(summary, max(1, width-m.consoleFormWidth()-7)), 4, width, height, keyHint("enter", "create"), keyHint("esc", "cancel"))
}

func (m Model) viewConsoleChoice(width, height int) string {
	inner := max(1, width-4)
	bodyHeight := max(1, height-7)
	indexes := m.filteredChoiceIndexes()
	start := max(0, m.choice.Cursor-bodyHeight+2)
	end := min(len(indexes), start+bodyHeight)
	listWidth := inner
	if inner >= 104 {
		listWidth, _ = consoleSplit(inner)
	}
	var lines []string
	for cursor := start; cursor < end; cursor++ {
		option := m.choice.Options[indexes[cursor]]
		prefix := "  "
		style := rowStyle
		if cursor == m.choice.Cursor {
			prefix = consoleMark(iconFocus) + " "
			style = selectedStyle.Foreground(Tokyo.Status).Bold(true)
		}
		label := consoleText(option.Label)
		if option.Detail != "" {
			label += " · " + consoleText(option.Detail)
		}
		lines = append(lines, style.Render(consoleCell(prefix+label, listWidth)))
	}
	if len(lines) == 0 {
		lines = append(lines, mutedStyle.Render("No matching options. Change the filter."))
	}
	body := consoleRows(strings.Join(lines, "\n"), inner, bodyHeight)
	if inner >= 104 {
		detail := consoleSection("DETAIL") + "\n\n"
		if option, ok := m.selectedChoiceOption(); ok {
			detail += consoleParagraph(option.Label+"\n\n"+option.Detail, inner-listWidth-3)
		}
		body = consoleColumns(strings.Join(lines, "\n"), detail, listWidth, inner-listWidth-3, bodyHeight)
	}
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join([]string{titleLine(m.choice.Title, "CHOOSE", inner), ansi.Truncate(m.choiceInput.View(), inner, ""), "", body, consoleRule(inner), footerBar(inner, keyHint("type", "filter"), keyHint("up/down", "select"), keyHint("enter", "use"), keyHint("esc", "cancel"))}, "\n"))
}
