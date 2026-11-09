package tui

import (
	"fmt"
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
	fields := [][2]string{{"Repository", repository + "  " + consoleMark(iconCollapsed)}, {"Name", value(1, draft.remoteDraft.Name)}, {"Fetch URL", value(2, draft.remoteDraft.FetchURL)}, {"Push URL", value(3, draft.remoteDraft.PushURL)}, {"Default push", pushDefault}}
	var lines []string
	for i, field := range fields {
		lines = append(lines, formChoiceRow(field[0], field[1], i == m.remoteFocus, m.consoleFormWidth()))
	}
	lines = append(lines, "", formSubmitRow("Add remote", m.remoteFocus == len(fields), m.consoleFormWidth()))
	selected := m.remoteFocus
	if m.remoteFocus == len(fields) {
		selected++
	}
	return m.consoleFormLayout("Add remote", mutedStyle.Render(consoleText(repository)), strings.Join(lines, "\n"), selected, width, height, keyHint("tab", "choose / next"), keyHint("ctrl+s", "add"), keyHint("esc", "back"))
}

// viewConsoleSimpleForm is a form with one field, which shows at the top:
// New workspace, Add repository and Rename.
func (m Model) viewConsoleSimpleForm(width, height int) string {
	title, description, submit := "New workspace", "Group related agent and human work.", "Create workspace"
	switch m.form {
	case formRepository:
		title, description, submit = "Add repository", "Use a local path or a Git SSH / HTTPS URL.", "Add repository"
	case formRename:
		title, description, submit = "Rename agent", "Give this agent a new name. Other agents can have the same name.", "Rename the agent"
	}
	body := consoleParagraph(description, m.consoleFormWidth()) + "\n\n" + formSubmitRow(submit, true, m.consoleFormWidth())
	return m.consoleFormLayout(title, m.formInput.View(), body, 2, width, height, keyHint("enter", strings.ToLower(strings.Fields(submit)[0])), keyHint("esc", "back"))
}

// viewConsoleChoice is the choice of a form field: its filter at the top and
// its options below, without a side pane.
func (m Model) viewConsoleChoice(width, height int) string {
	inner := max(1, width-4)
	bodyHeight := max(1, height-5)
	indexes := m.filteredChoiceIndexes()
	start := max(0, m.choice.Cursor-bodyHeight+2)
	end := min(len(indexes), start+bodyHeight)
	var lines []string
	for cursor := start; cursor < end; cursor++ {
		option := m.choice.Options[indexes[cursor]]
		prefix := "  "
		style := rowStyle
		if cursor == m.choice.Cursor {
			prefix = consoleMark(iconFocus) + " "
			style = selectedStyle.Foreground(Tokyo.Status).Bold(true)
		}
		labelWidth := min(40, max(12, inner/2))
		text := style.Render(prefix+consoleCell(consoleText(option.Label), labelWidth)+"  ") + style.Foreground(Tokyo.Muted).Render(ansi.Truncate(consoleText(option.Detail), max(0, inner-labelWidth-4), "…"))
		lines = append(lines, consoleFillRow(text, inner, style))
	}
	if len(lines) == 0 {
		lines = append(lines, mutedStyle.Render("No option matches."))
	}
	position := ""
	if len(indexes) > 0 {
		position = fmt.Sprintf("%d of %d", m.choice.Cursor+1, len(indexes))
	}
	return lipgloss.NewStyle().Padding(0, 2).Render(strings.Join([]string{
		screenHeader(choiceLabel(m.choice.Title), m.choiceInput.View(), inner),
		consoleRows(strings.Join(lines, "\n"), inner, bodyHeight),
		mutedStyle.Render(position),
		footerBar(inner, keyHint("enter", "choose"), keyHint("↑↓", "select"), keyHint("esc", "back")),
	}, "\n"))
}

// choiceLabel names a choice on its chip: "Select source remote" is "Source
// remote".
func choiceLabel(title string) string {
	label := strings.TrimPrefix(title, "Select ")
	if label == "" {
		return title
	}
	return strings.ToUpper(label[:1]) + label[1:]
}
