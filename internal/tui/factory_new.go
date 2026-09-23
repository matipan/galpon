package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/matipan/galpon/internal/factory"
	"github.com/matipan/galpon/internal/model"
)

const (
	factoryConfigRepository = iota
	factoryConfigWorkspace
	factoryConfigBaseRef
	factoryConfigIssue
	factoryConfigTitle
	factoryConfigStart
	factoryConfigFields
)

type factoryPickerOption struct {
	label  string
	detail string
	value  string
}

func (m *FactoryModel) beginFactoryFeature() {
	m.mode = "new"
	m.err = nil
	m.form = newFactoryForm()
	current, hasCurrent := m.current()
	if hasCurrent {
		for index, repository := range m.dashboard.Repositories {
			if repository.ID == current.RepositoryID {
				m.form.repo = index
				m.form.inherited = true
				break
			}
		}
		for index, workspace := range m.dashboard.Workspaces {
			if workspace.ID == current.WorkspaceID {
				m.form.workspace = index
				m.form.inherited = true
				break
			}
		}
	}
	if repository, ok := m.factoryFormRepository(); ok {
		m.form.base.SetValue(repository.DefaultBranch)
	}
	if hasCurrent && strings.TrimSpace(current.BaseRef) != "" {
		m.form.base.SetValue(current.BaseRef)
	}
	styleFactoryMissionEditor(&m.form)
}

func styleFactoryMissionEditor(form *factoryForm) {
	for _, style := range []*textarea.Style{&form.request.FocusedStyle, &form.request.BlurredStyle} {
		style.Base = rowStyle
		style.CursorLine = rowStyle
		style.CursorLineNumber = mutedStyle
		style.EndOfBuffer = lipgloss.NewStyle().Foreground(Tokyo.Background).Background(Tokyo.Background)
		style.LineNumber = mutedStyle
		style.Placeholder = mutedStyle
		style.Prompt = lipgloss.NewStyle().Foreground(Tokyo.Status).Background(Tokyo.Background)
		style.Text = rowStyle
	}
	form.request.Cursor.Style = lipgloss.NewStyle().Foreground(Tokyo.StatusInk).Background(Tokyo.Status)
	for _, input := range []*textinput.Model{&form.title, &form.issue, &form.base} {
		input.PromptStyle = lipgloss.NewStyle().Foreground(Tokyo.Status).Background(Tokyo.Background).Bold(true)
		input.TextStyle = rowStyle
		input.PlaceholderStyle = mutedStyle
		input.CompletionStyle = lipgloss.NewStyle().Foreground(Tokyo.Comment).Background(Tokyo.Background)
		input.Cursor.Style = lipgloss.NewStyle().Foreground(Tokyo.StatusInk).Background(Tokyo.Status)
	}
}

func (m *FactoryModel) updateFactoryFeature(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if factoryLaunchKey(key) {
		return m.launchFactoryFeature()
	}
	switch m.form.focus {
	case "brief":
		switch key.String() {
		case "esc":
			m.mode = ""
			return m, nil
		case "tab", "shift+tab":
			m.form.focus = "config"
			m.form.request.Blur()
			return m, nil
		}
		var command tea.Cmd
		m.form.request, command = m.form.request.Update(key)
		if strings.TrimSpace(m.form.request.Value()) != "" {
			m.form.validation = ""
		}
		return m, command
	case "config":
		switch key.String() {
		case "esc", "shift+tab", "tab":
			m.focusFactoryBrief()
		case "up", "k":
			m.form.field = (m.form.field - 1 + factoryConfigFields) % factoryConfigFields
		case "down", "j":
			m.form.field = (m.form.field + 1) % factoryConfigFields
		case "enter":
			if m.form.field == factoryConfigStart {
				return m.launchFactoryFeature()
			}
			m.openFactoryConfigField()
		}
		return m, nil
	case "picker":
		options := m.factoryPickerOptions()
		switch key.String() {
		case "esc":
			m.form.focus = "config"
		case "up", "k":
			m.form.picker = max(0, m.form.picker-1)
		case "down", "j":
			m.form.picker = min(max(0, len(options)-1), m.form.picker+1)
		case "enter":
			m.chooseFactoryPicker(options)
		}
		return m, nil
	case "text":
		switch key.String() {
		case "esc", "tab", "shift+tab", "enter":
			m.blurFactoryConfigInputs()
			m.form.focus = "config"
			return m, nil
		}
		var command tea.Cmd
		switch m.form.field {
		case factoryConfigBaseRef:
			m.form.base, command = m.form.base.Update(key)
		case factoryConfigIssue:
			m.form.issue, command = m.form.issue.Update(key)
		case factoryConfigTitle:
			m.form.title, command = m.form.title.Update(key)
		}
		return m, command
	}
	return m, nil
}

func factoryLaunchKey(key tea.KeyMsg) bool {
	// Common terminals encode Ctrl+Enter as the same LF event as Enter.
	// Alt+Enter is the only global launch key so Enter can always add a line.
	return key.Type == tea.KeyEnter && key.Alt
}

func (m *FactoryModel) launchFactoryFeature() (tea.Model, tea.Cmd) {
	request := strings.TrimSpace(m.form.request.Value())
	if request == "" {
		m.submitting = false
		m.form.validation = "Tell the Factory what to build before launching."
		m.focusFactoryBrief()
		return m, nil
	}
	if len(m.dashboard.Repositories) == 0 || len(m.dashboard.Workspaces) == 0 {
		m.form.validation = "Add a repository and workspace before launching."
		return m, nil
	}
	repository := m.dashboard.Repositories[m.form.repo]
	workspace := m.dashboard.Workspaces[m.form.workspace]
	title := strings.TrimSpace(m.form.title.Value())
	if title == "" {
		title = factoryTitleFromBrief(request)
	}
	baseRef := strings.TrimSpace(m.form.base.Value())
	if baseRef == "" {
		baseRef = repository.DefaultBranch
	}
	input := factory.CreateRequest{
		Title:          title,
		IssueURL:       strings.TrimSpace(m.form.issue.Value()),
		Request:        request,
		BaseRef:        baseRef,
		RepositoryID:   repository.ID,
		RepositoryPath: repository.SourcePath,
		WorkspaceID:    workspace.ID,
	}
	now := time.Now().UnixMilli()
	m.launchingID = fmt.Sprintf("pending:%d", now)
	optimistic := factory.WorkOrder{
		ID:             m.launchingID,
		Title:          title,
		Request:        request,
		IssueURL:       input.IssueURL,
		RepositoryID:   repository.ID,
		RepositoryPath: repository.SourcePath,
		WorkspaceID:    workspace.ID,
		BaseRef:        baseRef,
		Stage:          factory.StageIntake,
		Status:         "active",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	m.orders = sortFactoryOrders(append(m.orders, optimistic))
	m.resetFactoryQueueClasses()
	for index := range m.orders {
		if m.orders[index].ID == optimistic.ID {
			m.selected = index
			break
		}
	}
	m.events = nil
	m.detailRuns = nil
	m.operations = nil
	m.agent = nil
	m.clearFactoryFileChanges()
	m.err = nil
	m.submitting = true
	m.mode = ""
	m.surface = "detail"
	m.toast = ""
	m.toastUntil = 0
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		order, err := m.client.Create(ctx, input)
		return factoryCreated{order: order, err: err}
	}
}

func (m *FactoryModel) removeFactoryOrder(id string) {
	for index := range m.orders {
		if m.orders[index].ID == id {
			m.orders = append(m.orders[:index], m.orders[index+1:]...)
			break
		}
	}
	m.selected = min(m.selected, max(0, len(m.orders)-1))
	if m.queueClasses != nil {
		delete(m.queueClasses, id)
	}
}

func (m *FactoryModel) focusFactoryBrief() {
	m.blurFactoryConfigInputs()
	m.form.focus = "brief"
	m.form.request.Focus()
}

func (m *FactoryModel) blurFactoryConfigInputs() {
	m.form.title.Blur()
	m.form.issue.Blur()
	m.form.base.Blur()
}

func (m *FactoryModel) openFactoryConfigField() {
	switch m.form.field {
	case factoryConfigRepository, factoryConfigWorkspace, factoryConfigBaseRef:
		m.form.focus = "picker"
		m.form.picker = m.factoryCurrentPickerIndex()
	case factoryConfigIssue:
		m.form.focus = "text"
		m.form.issue.Focus()
	case factoryConfigTitle:
		m.form.focus = "text"
		m.form.title.Focus()
	}
}

func (m *FactoryModel) factoryCurrentPickerIndex() int {
	options := m.factoryPickerOptions()
	current := ""
	switch m.form.field {
	case factoryConfigRepository:
		if repository, ok := m.factoryFormRepository(); ok {
			current = repository.ID
		}
	case factoryConfigWorkspace:
		if m.form.workspace >= 0 && m.form.workspace < len(m.dashboard.Workspaces) {
			current = m.dashboard.Workspaces[m.form.workspace].ID
		}
	case factoryConfigBaseRef:
		current = strings.TrimSpace(m.form.base.Value())
	}
	for index, option := range options {
		if option.value == current {
			return index
		}
	}
	return 0
}

func (m *FactoryModel) chooseFactoryPicker(options []factoryPickerOption) {
	if len(options) == 0 || m.form.picker < 0 || m.form.picker >= len(options) {
		m.form.focus = "config"
		return
	}
	option := options[m.form.picker]
	switch m.form.field {
	case factoryConfigRepository:
		for index, repository := range m.dashboard.Repositories {
			if repository.ID == option.value {
				m.form.repo = index
				m.form.base.SetValue(repository.DefaultBranch)
				m.form.inherited = false
				break
			}
		}
	case factoryConfigWorkspace:
		for index, workspace := range m.dashboard.Workspaces {
			if workspace.ID == option.value {
				m.form.workspace = index
				m.form.inherited = false
				break
			}
		}
	case factoryConfigBaseRef:
		if option.value == "__custom__" {
			m.form.focus = "text"
			m.form.base.Focus()
			return
		}
		m.form.base.SetValue(option.value)
	}
	m.form.focus = "config"
}

func (m *FactoryModel) factoryPickerOptions() []factoryPickerOption {
	switch m.form.field {
	case factoryConfigRepository:
		options := make([]factoryPickerOption, 0, len(m.dashboard.Repositories))
		for _, repository := range m.dashboard.Repositories {
			detail := repository.DefaultBranch
			if detail == "" {
				detail = "default branch"
			}
			options = append(options, factoryPickerOption{label: repository.Title, detail: detail, value: repository.ID})
		}
		return options
	case factoryConfigWorkspace:
		options := make([]factoryPickerOption, 0, len(m.dashboard.Workspaces))
		for _, workspace := range m.dashboard.Workspaces {
			detail := workspace.Status
			if detail == "" {
				detail = "workspace"
			}
			options = append(options, factoryPickerOption{label: workspace.Title, detail: detail, value: workspace.ID})
		}
		return options
	case factoryConfigBaseRef:
		return m.factoryBaseRefOptions()
	default:
		return nil
	}
}

func (m *FactoryModel) factoryBaseRefOptions() []factoryPickerOption {
	repository, ok := m.factoryFormRepository()
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	options := make([]factoryPickerOption, 0, 6)
	add := func(value, detail string) {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		options = append(options, factoryPickerOption{label: value, detail: detail, value: value})
	}
	add(repository.DefaultBranch, "default branch")
	for _, worktree := range m.dashboard.Worktrees {
		if worktree.RepositoryID != repository.ID {
			continue
		}
		add(worktree.Branch, "known branch")
		add(worktree.BaseRef, "known base ref")
	}
	options = append(options, factoryPickerOption{label: "Other ref…", detail: "type a branch or ref", value: "__custom__"})
	return options
}

func (m *FactoryModel) factoryFormRepository() (model.Repository, bool) {
	if m.form.repo < 0 || m.form.repo >= len(m.dashboard.Repositories) {
		return model.Repository{}, false
	}
	return m.dashboard.Repositories[m.form.repo], true
}

func (m *FactoryModel) formView() string {
	inner := max(1, m.width-4)
	header := m.factoryNewHeader(inner)
	footer := m.factoryNewFooter(inner)
	notice := m.factoryNewNotice(inner)
	bodyHeight := max(1, m.height-lipgloss.Height(header)-lipgloss.Height(notice)-1-lipgloss.Height(footer))
	var body string
	if m.width >= factoryMediumWidth {
		leftWidth, rightWidth := consoleSplit(inner)
		body = consoleColumns(m.factoryMissionPane(leftWidth, bodyHeight), m.factoryLaunchPane(rightWidth, bodyHeight), leftWidth, rightWidth, bodyHeight)
	} else if m.form.focus == "brief" {
		body = m.factoryMissionPane(inner, bodyHeight)
	} else {
		body = m.factoryLaunchPane(inner, bodyHeight)
	}
	content := strings.Join([]string{header, body, notice, consoleRule(inner), footer}, "\n")
	content = lipgloss.NewStyle().Padding(0, 2).Render(content)
	return factoryPersistentBlock(appBackground.Render(content), Tokyo.Background)
}

func (m *FactoryModel) factoryNewHeader(width int) string {
	context := "Brief first · configuration is optional"
	if preview := factoryTitleFromBrief(m.form.request.Value()); preview != "" && width >= 90 {
		context = preview
	}
	return titleLine("New Feature", context, width)
}

func (m *FactoryModel) factoryMissionPane(width, height int) string {
	label := consoleSection("BRIEF")
	if m.form.focus == "brief" {
		label = lipgloss.NewStyle().Foreground(Tokyo.Status).Background(Tokyo.Background).Render(consoleMark(iconSection)+" ") + lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Background).Bold(true).Render("BRIEF")
	}
	instruction := mutedStyle.Render(ansi.Truncate("Describe the outcome, important constraints, and acceptance criteria.", width, "…"))
	validationLines := 0
	if m.form.validation != "" {
		validationLines = 2
	}
	editorHeight := max(4, height-5-validationLines)
	m.form.request.SetWidth(max(1, width))
	m.form.request.SetHeight(editorHeight)
	lines := []string{label, instruction, "", factoryPersistentBlock(m.form.request.View(), Tokyo.Background), "", mutedStyle.Render("Enter adds a line · Markdown is supported")}
	if m.form.validation != "" {
		lines = append(lines, consoleError("! "+m.form.validation, width))
	}
	return factoryPaneRows(strings.Join(lines, "\n"), width, height)
}

func (m *FactoryModel) factoryLaunchPane(width, height int) string {
	var lines []string
	if m.form.focus == "picker" {
		lines = m.factoryPickerLines(width, height)
	} else {
		lines = m.factoryConfigLines(width, height)
	}
	return factoryPaneRows(strings.Join(lines, "\n"), width, height)
}

func (m *FactoryModel) factoryConfigLines(width, height int) []string {
	lines := []string{consoleSection("LAUNCH"), mutedStyle.Render("Defaults follow the selected feature when available."), ""}
	for field := 0; field < factoryConfigFields; field++ {
		lines = append(lines, m.factoryConfigRow(field, width, true)...)
	}
	lines = append(lines,
		"",
		consoleSection("ON START"),
		consoleParagraph("Create the feature, start or queue planning, then wait for plan review.", width),
		"",
		mutedStyle.Render(factoryNewLifecycle(width)),
	)
	if len(lines) > height {
		return lines[:height]
	}
	return lines
}

func (m *FactoryModel) factoryConfigRow(field, width int, _ bool) []string {
	label, value, helper := m.factoryConfigValue(field)
	selected := m.form.focus != "brief" && m.form.field == field
	if selected && m.form.focus == "text" {
		input := m.factoryFocusedConfigInput()
		labelWidth := min(22, max(12, width*2/5))
		input.Width = max(6, width-labelWidth-2)
		value = input.View()
	}
	if field <= factoryConfigBaseRef {
		value += "  " + consoleMark(iconExpanded)
	}
	lines := []string{formChoiceRow(label, value, selected, width)}
	if selected && helper != "" {
		lines = append(lines, mutedStyle.Render("  "+ansi.Truncate(helper, max(1, width-2), "…")))
	}
	return lines
}

func (m *FactoryModel) factoryFocusedConfigInput() *textinput.Model {
	switch m.form.field {
	case factoryConfigBaseRef:
		return &m.form.base
	case factoryConfigIssue:
		return &m.form.issue
	default:
		return &m.form.title
	}
}

func (m *FactoryModel) factoryConfigValue(field int) (string, string, string) {
	switch field {
	case factoryConfigRepository:
		if repository, ok := m.factoryFormRepository(); ok {
			helper := "Factory default"
			if m.form.inherited {
				helper = "from selected feature"
			}
			return "Repository", repository.Title, helper
		}
		return "Repository", "Not configured", "required"
	case factoryConfigWorkspace:
		if m.form.workspace >= 0 && m.form.workspace < len(m.dashboard.Workspaces) {
			helper := "current Factory workspace"
			if !m.form.inherited {
				helper = "Factory default"
			}
			return "Workspace", m.dashboard.Workspaces[m.form.workspace].Title, helper
		}
		return "Workspace", "Not configured", "required"
	case factoryConfigBaseRef:
		value := strings.TrimSpace(m.form.base.Value())
		if value == "" {
			value = "—"
		}
		helper := "custom ref"
		if repository, ok := m.factoryFormRepository(); ok && value == repository.DefaultBranch {
			helper = "default branch"
		}
		return "Base ref", value, helper
	case factoryConfigIssue:
		value := factoryIssueSummary(m.form.issue.Value())
		return "GitHub issue", value, "optional"
	case factoryConfigTitle:
		if title := strings.TrimSpace(m.form.title.Value()); title != "" {
			return "Title", title, "Optional override"
		}
		title := factoryTitleFromBrief(m.form.request.Value())
		if title == "" {
			title = "Generated from brief"
		}
		return "Title", title, "Generated from the first line of the brief"
	case factoryConfigStart:
		return "Start", "Start planning", "Create the feature and return to the work queue"
	default:
		return "", "", ""
	}
}

func factoryIssueSummary(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "—"
	}
	trimmed := strings.TrimSuffix(value, "/")
	parts := strings.Split(trimmed, "/")
	last := parts[len(parts)-1]
	if last != "" {
		digits := true
		for _, character := range last {
			if character < '0' || character > '9' {
				digits = false
				break
			}
		}
		if digits {
			return "#" + last
		}
	}
	return clip(value, 40)
}

func (m *FactoryModel) factoryPickerLines(width, height int) []string {
	label, _, _ := m.factoryConfigValue(m.form.field)
	lines := []string{consoleSection("SELECT " + strings.ToUpper(label)), mutedStyle.Render("Choose a value, then return to launch configuration."), ""}
	options := m.factoryPickerOptions()
	available := max(1, height-len(lines))
	start := max(0, m.form.picker-available+1)
	end := min(len(options), start+available)
	for index := start; index < end; index++ {
		option := options[index]
		value := option.label
		if option.detail != "" {
			value += " · " + option.detail
		}
		lines = append(lines, formChoiceRow("", value, index == m.form.picker, width))
	}
	if len(options) == 0 {
		lines = append(lines, consoleError("No choices are available.", width))
	}
	return lines
}

func factoryNewLifecycle(width int) string {
	arrow := consoleGlyph(" → ", " > ")
	full := strings.Join([]string{"BRIEF", "PLAN", "BUILD", "TEST", "REVIEW", "SHIP"}, arrow)
	if lipgloss.Width(full) > width {
		full = strings.Join([]string{"BRIEF", "PLAN", "BUILD", consoleMark(iconMore)}, arrow)
	}
	return ansi.Truncate(full, width, "…")
}

func (m *FactoryModel) factoryNewFooter(width int) string {
	var hints []string
	switch m.form.focus {
	case "picker":
		hints = []string{keyHint("esc", "back"), keyHint("↑ ↓", "select"), keyHint("enter", "choose")}
	case "config":
		hints = []string{keyHint("esc / shift+tab", "brief"), keyHint("↑ ↓", "field"), keyHint("enter", "change / start"), keyHint("alt+enter", "start")}
	case "text":
		hints = []string{keyHint("esc", "back"), keyHint("enter", "apply")}
	default:
		hints = []string{keyHint("esc", "cancel"), keyHint("tab", "configuration"), keyHint("alt+enter", "start")}
	}
	return footerBar(width, hints...)
}

func (m *FactoryModel) factoryNewNotice(width int) string {
	text := "Draft · kept while you configure or resize; Esc cancels it"
	color := Tokyo.Muted
	if m.submitting {
		text = consoleActivityMark() + " Starting feature"
		color = Tokyo.Yellow
	} else if m.form.validation != "" {
		text = "! " + m.form.validation
		color = Tokyo.Red
	}
	return lipgloss.NewStyle().Foreground(color).Background(Tokyo.Background).Render(ansi.Truncate(text, width, "…"))
}
