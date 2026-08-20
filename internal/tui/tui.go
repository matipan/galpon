package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/terminal"
)

type screen int

const (
	screenSwitcher screen = iota
	screenForm
	screenTerminal
)

type formKind int

const (
	formNone formKind = iota
	formRepository
	formWorkspace
	formWorktree
	formAgent
	formRemote
)

type Model struct {
	client          *app.Client
	renderer        terminal.Renderer
	screen          screen
	form            formKind
	width, height   int
	dashboard       model.Dashboard
	results         []searchResult
	cursor          int
	normalMode      bool
	query           textinput.Model
	formInput       textinput.Model
	loaded          bool
	status          string
	formContext     string
	busy            bool
	busyTicks       int
	err             error
	quitting        bool
	agentDraft      agentDraft
	agentFocus      int
	worktreeDraft   worktreeDraft
	worktreeFocus   int
	worktreeCommand []string
	terminalTargets []terminalTarget
	terminalCursor  int
	terminalCommand []string
	remoteDraft     remoteDraft
	remoteFocus     int
}

type agentWorktreeDraft struct {
	Repository int
	Remote     int
	Ref        string
	FetchFirst bool
}

type agentDraft struct {
	Name                string
	Role                string
	Context             int
	Placement           int
	PlacementAgent      int
	SuggestedWorktreeID string
	Share               bool
	CWD                 string
	Worktrees           []agentWorktreeDraft
}

type worktreeDraft struct {
	RepositoryID   string
	WorkspaceID    string
	WorkspaceTitle string
	Remote         string
	Ref            string
	FetchFirst     bool
}

type worktreeFieldKind int

const (
	worktreeWorkspace worktreeFieldKind = iota
	worktreeWorkspaceTitle
	worktreeRemote
	worktreeRef
	worktreeFetch
	worktreeCreate
)

type agentFieldKind int

const (
	agentName agentFieldKind = iota
	agentRole
	agentContext
	agentPlacement
	agentRepository
	agentRemote
	agentRef
	agentFetch
	agentAddWorktree
	agentPlacementSource
	agentWorktreeSource
	agentShare
	agentCWD
	agentCreate
)

type agentField struct {
	Kind     agentFieldKind
	Worktree int
}

type terminalTarget struct {
	WorkspaceID string
	AgentID     string
	AgentTitle  string
	WorktreeID  string
	Path        string
	Label       string
	Detail      string
}

type switcherLine struct {
	value       string
	resultIndex int
	group       string
	header      bool
}

type remoteDraft struct {
	Repository  int
	Name        string
	FetchURL    string
	PushURL     string
	PushDefault bool
}

type dashboardMsg struct {
	value model.Dashboard
	err   error
}
type actionMsg struct {
	rendererWorkspaceID, workspaceID string
	err                              error
	quit                             bool
}
type createMsg struct {
	err     error
	quit    bool
	message string
}
type worktreeCreateMsg struct {
	value               app.CreateWorktreeResult
	rendererWorkspaceID string
	err                 error
}
type deleteMsg struct {
	value model.DeletionResult
	title string
	err   error
}
type tickMsg time.Time

func New(client *app.Client, renderer terminal.Renderer) Model {
	query := textinput.New()
	query.Placeholder = "Search titles…"
	query.Prompt = "  "
	query.PromptStyle = lipgloss.NewStyle().Foreground(Tokyo.Cyan).Background(Tokyo.Prompt).Bold(true)
	query.TextStyle = lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Prompt)
	query.PlaceholderStyle = lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Prompt)
	query.CompletionStyle = lipgloss.NewStyle().Foreground(Tokyo.Comment).Background(Tokyo.Prompt)
	query.Cursor.Style = lipgloss.NewStyle().Foreground(Tokyo.Orange).Background(Tokyo.Prompt)
	query.Focus()
	formInput := textinput.New()
	formInput.Prompt = "  › "
	formInput.PromptStyle = lipgloss.NewStyle().Foreground(Tokyo.Cyan).Background(Tokyo.Prompt).Bold(true)
	formInput.TextStyle = lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Prompt)
	formInput.PlaceholderStyle = lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Prompt)
	formInput.Cursor.Style = lipgloss.NewStyle().Foreground(Tokyo.Orange).Background(Tokyo.Prompt)
	return Model{client: client, renderer: renderer, screen: screenSwitcher, query: query, formInput: formInput}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.loadDashboard(), tick()) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch value := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = value.Width
		m.height = value.Height
		m.resize()
		return m, nil
	case dashboardMsg:
		m.loaded = true
		if value.err != nil || m.screen != screenForm {
			m.err = value.err
		}
		if value.err == nil {
			m.dashboard = value.value
			m.refreshResults()
		}
		return m, nil
	case actionMsg:
		m.err = value.err
		if value.err == nil && value.rendererWorkspaceID != "" && m.renderer != nil {
			_ = m.client.SetRenderer(context.Background(), value.workspaceID, m.renderer.Name(), m.renderer.Context(), value.rendererWorkspaceID)
		}
		if value.err == nil && value.quit {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	case createMsg:
		m.busy = false
		m.busyTicks = 0
		m.status = value.message
		m.err = value.err
		if value.err == nil && value.quit {
			m.quitting = true
			return m, tea.Quit
		}
		if value.err == nil {
			m.screen = screenSwitcher
			m.form = formNone
			m.formInput.SetValue("")
			return m, tea.Batch(m.focusSwitcher(), m.loadDashboard())
		}
		m.formInput.Focus()
		return m, nil
	case worktreeCreateMsg:
		m.busy = false
		m.busyTicks = 0
		m.err = value.err
		if value.value.Worktree.ID == "" {
			m.formInput.Focus()
			return m, nil
		}
		m.screen = screenSwitcher
		m.form = formNone
		m.formInput.SetValue("")
		if value.err != nil {
			m.status = "Worktree created, but the terminal did not open: " + value.err.Error()
			m.err = nil
			return m, m.loadDashboard()
		}
		if value.rendererWorkspaceID != "" && m.renderer != nil {
			_ = m.client.SetRenderer(context.Background(), value.value.Workspace.ID, m.renderer.Name(), m.renderer.Context(), value.rendererWorkspaceID)
		}
		m.quitting = true
		return m, tea.Quit
	case deleteMsg:
		m.busy = false
		m.busyTicks = 0
		m.err = value.err
		if value.err != nil {
			m.status = ""
			return m, nil
		}
		dependent := deletionTotal(value.value.Hidden) - 1
		m.status = "Hidden " + value.title
		if dependent == 1 {
			m.status += " and 1 dependent item"
		} else if dependent > 1 {
			m.status += fmt.Sprintf(" and %d dependent items", dependent)
		}
		return m, m.loadDashboard()
	case tickMsg:
		if m.busy {
			m.busyTicks++
		}
		commands := []tea.Cmd{tick()}
		commands = append(commands, m.loadDashboard())
		return m, tea.Batch(commands...)
	}
	if key, ok := msg.(tea.KeyMsg); ok {
		if key.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		switch m.screen {
		case screenSwitcher:
			return m, m.updateSwitcher(key)
		case screenForm:
			return m, m.updateForm(key)
		case screenTerminal:
			return m, m.updateTerminal(key)
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	width := m.width
	if width < 40 {
		width = 80
	}
	height := m.height
	if height < 12 {
		height = 28
	}
	var body string
	switch m.screen {
	case screenForm:
		switch m.form {
		case formAgent:
			body = m.viewAgentForm(width, height)
		case formWorktree:
			body = m.viewWorktreeForm(width, height)
		case formRemote:
			body = m.viewRemoteForm(width, height)
		default:
			body = m.viewForm(width, height)
		}
	case screenTerminal:
		body = m.viewTerminal(width, height)
	default:
		body = m.viewSwitcher(width, height)
	}
	return appBackground.Width(width).Height(height).Render(body)
}

func (m *Model) updateSwitcher(key tea.KeyMsg) tea.Cmd {
	if m.busy {
		return nil
	}
	if key.Type == tea.KeyCtrlAt {
		m.normalMode = !m.normalMode
		return m.focusSwitcher()
	}
	switch key.String() {
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return nil
	case "down", "ctrl+n":
		if m.cursor < len(m.results)-1 {
			m.cursor++
		}
		return nil
	case "enter":
		if len(m.results) == 0 {
			return nil
		}
		selected := m.results[m.cursor]
		if selected.Kind == resultAgent {
			return m.openAgent(selected.ID)
		}
		if selected.Kind == resultWorktree {
			return m.openSelected(selected, nil)
		}
		if selected.Kind == resultRepository {
			m.beginRemoteForm(selected)
			return nil
		}
		return m.beginTerminal(selected, nil)
	case "esc":
		m.quitting = true
		return tea.Quit
	}
	if !m.normalMode {
		var cmd tea.Cmd
		m.query, cmd = m.query.Update(key)
		m.refreshResults()
		return cmd
	}
	switch key.String() {
	case "t":
		if len(m.results) > 0 {
			return m.beginTerminal(m.results[m.cursor], nil)
		}
		return nil
	case "e":
		if len(m.results) > 0 {
			return m.beginTerminal(m.results[m.cursor], EditorCommand())
		}
		return nil
	case "r":
		m.beginForm(formRepository, "Local path or Git URL", "")
		return nil
	case "R":
		if len(m.dashboard.Repositories) == 0 {
			m.status = "Add a repository first"
			return nil
		}
		selected := searchResult{}
		if len(m.results) > 0 {
			selected = m.results[m.cursor]
		}
		m.beginRemoteForm(selected)
		return nil
	case "w":
		m.beginForm(formWorkspace, "Workspace title", "")
		return nil
	case "a":
		wsID := m.selectedWorkspace()
		if wsID == "" {
			m.status = "Select a workspace first"
			return nil
		}
		suggestedWorktreeID := ""
		if m.results[m.cursor].Kind == resultWorktree {
			suggestedWorktreeID = m.results[m.cursor].WorktreeID
		}
		m.beginAgentForm(wsID, suggestedWorktreeID)
		return nil
	case "x":
		if len(m.results) == 0 {
			return nil
		}
		selected := m.results[m.cursor]
		m.busy = true
		m.busyTicks = 0
		m.err = nil
		m.status = "Hiding " + selected.Title + "…"
		return func() tea.Msg {
			value, err := m.client.DeleteResource(context.Background(), string(selected.Kind), selected.ID)
			return deleteMsg{value: value, title: selected.Title, err: err}
		}
	case "q":
		m.quitting = true
		return tea.Quit
	}
	return nil
}

func (m *Model) focusSwitcher() tea.Cmd {
	if m.normalMode {
		m.query.Blur()
		return nil
	}
	return m.query.Focus()
}

func (m *Model) updateForm(key tea.KeyMsg) tea.Cmd {
	if m.form == formAgent {
		return m.updateAgentForm(key)
	}
	if m.form == formWorktree {
		return m.updateWorktreeForm(key)
	}
	if m.form == formRemote {
		return m.updateRemoteForm(key)
	}
	switch key.String() {
	case "esc":
		m.screen = screenSwitcher
		m.form = formNone
		return m.focusSwitcher()
	case "enter":
		if m.busy {
			return nil
		}
		value := strings.TrimSpace(m.formInput.Value())
		if value == "" {
			return nil
		}
		kind := m.form
		m.busy = true
		m.busyTicks = 0
		m.err = nil
		m.formInput.Blur()
		switch kind {
		case formRepository:
			m.status = "Fetching repository branches…"
		case formWorkspace:
			m.status = "Creating workspace…"
		}
		return func() tea.Msg {
			switch kind {
			case formRepository:
				repository, err := m.client.AddRepository(context.Background(), app.AddRepositoryRequest{Path: value})
				return createMsg{err: err, message: "Repository " + repository.Title + " is ready"}
			case formWorkspace:
				workspace, err := m.client.CreateWorkspace(context.Background(), app.CreateWorkspaceRequest{Title: value})
				return createMsg{err: err, message: "Workspace " + workspace.Title + " created"}
			default:
				return createMsg{err: fmt.Errorf("unknown form")}
			}
		}
	}
	var cmd tea.Cmd
	m.formInput, cmd = m.formInput.Update(key)
	return cmd
}

func (m *Model) beginForm(kind formKind, placeholder, contextValue string) {
	m.screen = screenForm
	m.form = kind
	m.formContext = contextValue
	m.status = ""
	m.busy = false
	m.busyTicks = 0
	m.err = nil
	m.formInput.SetValue("")
	m.formInput.Placeholder = placeholder
	m.formInput.Focus()
}

func (m *Model) beginWorktreeForm(repositoryID string, command []string) {
	repository, ok := m.dashboard.Repository(repositoryID)
	if !ok {
		m.err = fmt.Errorf("repository is not available")
		return
	}
	m.screen = screenForm
	m.form = formWorktree
	m.status = ""
	m.busy = false
	m.busyTicks = 0
	m.err = nil
	m.worktreeDraft = worktreeDraft{RepositoryID: repository.ID, Remote: repository.DefaultRemote, Ref: repository.DefaultBranch, FetchFirst: true}
	m.worktreeFocus = 0
	m.worktreeCommand = append([]string(nil), command...)
	m.loadWorktreeInput()
}

func (m *Model) worktreeFields() []worktreeFieldKind {
	fields := []worktreeFieldKind{worktreeWorkspace}
	if m.worktreeDraft.WorkspaceID == "" {
		fields = append(fields, worktreeWorkspaceTitle)
	}
	return append(fields, worktreeRemote, worktreeRef, worktreeFetch, worktreeCreate)
}

func (m *Model) updateWorktreeForm(key tea.KeyMsg) tea.Cmd {
	if m.busy {
		return nil
	}
	if key.String() == "esc" {
		m.screen = screenSwitcher
		m.form = formNone
		return m.focusSwitcher()
	}
	fields := m.worktreeFields()
	field := fields[m.worktreeFocus]
	textField := field == worktreeWorkspaceTitle || field == worktreeRef
	switch key.String() {
	case "tab", "down":
		m.moveWorktreeFocus(1)
		return nil
	case "shift+tab", "up":
		m.moveWorktreeFocus(-1)
		return nil
	case "ctrl+s":
		m.commitWorktreeInput()
		return m.createWorktree()
	case "enter":
		m.commitWorktreeInput()
		if field == worktreeCreate {
			return m.createWorktree()
		}
		m.moveWorktreeFocus(1)
		return nil
	case "left":
		if !textField {
			m.changeWorktreeChoice(field, -1)
			return nil
		}
	case "right":
		if !textField {
			m.changeWorktreeChoice(field, 1)
			return nil
		}
	case " ":
		if field == worktreeFetch {
			m.changeWorktreeChoice(field, 1)
			return nil
		}
	}
	if textField {
		var cmd tea.Cmd
		m.formInput, cmd = m.formInput.Update(key)
		m.commitWorktreeInput()
		return cmd
	}
	return nil
}

func (m *Model) moveWorktreeFocus(delta int) {
	m.commitWorktreeInput()
	fields := m.worktreeFields()
	m.worktreeFocus = (m.worktreeFocus + delta + len(fields)) % len(fields)
	m.loadWorktreeInput()
}

func (m *Model) commitWorktreeInput() {
	fields := m.worktreeFields()
	if m.worktreeFocus < 0 || m.worktreeFocus >= len(fields) {
		return
	}
	switch fields[m.worktreeFocus] {
	case worktreeWorkspaceTitle:
		m.worktreeDraft.WorkspaceTitle = m.formInput.Value()
	case worktreeRef:
		m.worktreeDraft.Ref = m.formInput.Value()
	}
}

func (m *Model) loadWorktreeInput() {
	fields := m.worktreeFields()
	if m.worktreeFocus < 0 || m.worktreeFocus >= len(fields) {
		return
	}
	value, placeholder := "", ""
	switch fields[m.worktreeFocus] {
	case worktreeWorkspaceTitle:
		value, placeholder = m.worktreeDraft.WorkspaceTitle, "Task title"
	case worktreeRef:
		value, placeholder = m.worktreeDraft.Ref, "Git reference"
	default:
		m.formInput.Blur()
		return
	}
	m.formInput.SetValue(value)
	m.formInput.Placeholder = placeholder
	m.formInput.CursorEnd()
	m.formInput.Focus()
}

func (m *Model) changeWorktreeChoice(field worktreeFieldKind, delta int) {
	switch field {
	case worktreeWorkspace:
		current := 0
		for index, workspace := range m.dashboard.Workspaces {
			if workspace.ID == m.worktreeDraft.WorkspaceID {
				current = index + 1
				break
			}
		}
		next := cycle(current, delta, len(m.dashboard.Workspaces)+1)
		m.worktreeDraft.WorkspaceID = ""
		if next > 0 {
			m.worktreeDraft.WorkspaceID = m.dashboard.Workspaces[next-1].ID
		}
		m.worktreeFocus = min(m.worktreeFocus, len(m.worktreeFields())-1)
	case worktreeRemote:
		repository, ok := m.dashboard.Repository(m.worktreeDraft.RepositoryID)
		if !ok || len(repository.Remotes) == 0 {
			break
		}
		current := 0
		for index, remote := range repository.Remotes {
			if remote.Name == m.worktreeDraft.Remote {
				current = index
				break
			}
		}
		m.worktreeDraft.Remote = repository.Remotes[cycle(current, delta, len(repository.Remotes))].Name
	case worktreeFetch:
		m.worktreeDraft.FetchFirst = !m.worktreeDraft.FetchFirst
	}
	m.loadWorktreeInput()
}

func (m *Model) createWorktree() tea.Cmd {
	m.commitWorktreeInput()
	repository, ok := m.dashboard.Repository(m.worktreeDraft.RepositoryID)
	if !ok {
		m.err = fmt.Errorf("repository is not available")
		return nil
	}
	request := app.CreateWorktreeRequest{RepositoryID: repository.ID, Remote: m.worktreeDraft.Remote, Ref: strings.TrimSpace(m.worktreeDraft.Ref), FetchFirst: m.worktreeDraft.FetchFirst}
	if m.worktreeDraft.WorkspaceID == "" {
		request.WorkspaceTitle = strings.TrimSpace(m.worktreeDraft.WorkspaceTitle)
		if request.WorkspaceTitle == "" {
			m.err = fmt.Errorf("task title is required for a new workspace")
			return nil
		}
	} else if _, ok := m.dashboard.Workspace(m.worktreeDraft.WorkspaceID); ok {
		request.WorkspaceID = m.worktreeDraft.WorkspaceID
	} else {
		m.err = fmt.Errorf("workspace is not available")
		return nil
	}
	client := m.client
	renderer := m.renderer
	command := append([]string(nil), m.worktreeCommand...)
	m.busy = true
	m.busyTicks = 0
	m.err = nil
	m.status = "Creating managed worktree…"
	m.formInput.Blur()
	return func() tea.Msg {
		value, err := client.CreateWorktree(context.Background(), request)
		if err != nil {
			return worktreeCreateMsg{err: err}
		}
		if renderer == nil {
			return worktreeCreateMsg{value: value, err: fmt.Errorf("terminal renderer is not configured")}
		}
		label := value.Workspace.Title + " · " + repository.Title
		rendererWorkspaceID, err := renderer.OpenTerminal(context.Background(), value.Workspace, value.Worktree, label, command)
		return worktreeCreateMsg{value: value, rendererWorkspaceID: rendererWorkspaceID, err: err}
	}
}

func (m *Model) beginAgentForm(workspaceID, suggestedWorktreeID string) {
	m.screen = screenForm
	m.form = formAgent
	m.formContext = workspaceID
	m.status = ""
	m.busy = false
	m.busyTicks = 0
	m.err = nil
	repositoryIndex := 0
	remoteIndex := 0
	if suggested, ok := m.dashboard.Worktree(suggestedWorktreeID); ok {
		for index, repository := range m.dashboard.Repositories {
			if repository.ID == suggested.RepositoryID {
				repositoryIndex = index
				for remoteAt, remote := range repository.Remotes {
					if remote.Name == suggested.SourceRemote {
						remoteIndex = remoteAt
					}
				}
				break
			}
		}
	}
	ref := "main"
	if len(m.dashboard.Repositories) > 0 {
		repository := m.dashboard.Repositories[repositoryIndex]
		ref = repository.DefaultBranch
		if suggested, ok := m.dashboard.Worktree(suggestedWorktreeID); ok && suggested.RepositoryID == repository.ID {
			ref = shortRef(suggested.BaseRef)
		}
	}
	placement := 0
	if len(m.dashboard.Repositories) == 0 {
		placement = 2
	}
	if suggested, ok := m.dashboard.Worktree(suggestedWorktreeID); ok && suggested.WorkspaceID == workspaceID {
		placement = 4
	} else {
		suggestedWorktreeID = ""
	}
	m.agentDraft = agentDraft{Placement: placement, SuggestedWorktreeID: suggestedWorktreeID, Worktrees: []agentWorktreeDraft{{Repository: repositoryIndex, Remote: remoteIndex, Ref: ref, FetchFirst: true}}}
	m.agentFocus = 0
	m.loadAgentInput()
}

func (m *Model) updateAgentForm(key tea.KeyMsg) tea.Cmd {
	if key.String() == "esc" {
		m.screen = screenSwitcher
		m.form = formNone
		return m.focusSwitcher()
	}
	if m.busy {
		return nil
	}
	fields := m.agentFields()
	if len(fields) == 0 {
		return nil
	}
	field := fields[m.agentFocus]
	textField := field.Kind == agentName || field.Kind == agentRole || field.Kind == agentRef || field.Kind == agentCWD
	switch key.String() {
	case "tab", "down":
		m.moveAgentFocus(1)
		return nil
	case "shift+tab", "up":
		m.moveAgentFocus(-1)
		return nil
	case "ctrl+s":
		m.commitAgentInput()
		return m.createAgent()
	case "enter":
		m.commitAgentInput()
		switch field.Kind {
		case agentAddWorktree:
			m.addAgentWorktree()
			m.agentFocus = min(m.agentFocus, len(m.agentFields())-1)
			m.loadAgentInput()
			return nil
		case agentCreate:
			return m.createAgent()
		default:
			m.moveAgentFocus(1)
			return nil
		}
	case "+":
		if !textField && m.agentDraft.Placement == 0 {
			m.addAgentWorktree()
			return nil
		}
	case "d":
		if !textField && field.Worktree > 0 && field.Worktree < len(m.agentDraft.Worktrees) {
			m.agentDraft.Worktrees = append(m.agentDraft.Worktrees[:field.Worktree], m.agentDraft.Worktrees[field.Worktree+1:]...)
			m.agentFocus = min(m.agentFocus, len(m.agentFields())-1)
			m.loadAgentInput()
			return nil
		}
	case "left":
		if !textField {
			m.changeAgentChoice(field, -1)
			return nil
		}
	case "right":
		if !textField {
			m.changeAgentChoice(field, 1)
			return nil
		}
	case " ":
		if field.Kind == agentFetch || field.Kind == agentShare {
			m.changeAgentChoice(field, 1)
			return nil
		}
	}
	if textField {
		var cmd tea.Cmd
		m.formInput, cmd = m.formInput.Update(key)
		m.commitAgentInput()
		return cmd
	}
	return nil
}

func (m *Model) agentFields() []agentField {
	fields := []agentField{{Kind: agentName}, {Kind: agentRole}, {Kind: agentContext}, {Kind: agentPlacement}}
	switch m.agentDraft.Placement {
	case 0:
		for index := range m.agentDraft.Worktrees {
			fields = append(fields,
				agentField{Kind: agentRepository, Worktree: index},
				agentField{Kind: agentRemote, Worktree: index},
				agentField{Kind: agentRef, Worktree: index},
				agentField{Kind: agentFetch, Worktree: index},
			)
		}
		fields = append(fields, agentField{Kind: agentAddWorktree})
	case 1:
		fields = append(fields, agentField{Kind: agentPlacementSource}, agentField{Kind: agentShare})
	case 2:
		// Galpon creates the directory. No additional input is needed.
	case 3:
		fields = append(fields, agentField{Kind: agentCWD})
	case 4:
		fields = append(fields, agentField{Kind: agentWorktreeSource}, agentField{Kind: agentShare})
	}
	return append(fields, agentField{Kind: agentCreate})
}

func (m *Model) moveAgentFocus(delta int) {
	m.commitAgentInput()
	fields := m.agentFields()
	m.agentFocus = (m.agentFocus + delta + len(fields)) % len(fields)
	m.loadAgentInput()
}

func (m *Model) commitAgentInput() {
	fields := m.agentFields()
	if m.agentFocus < 0 || m.agentFocus >= len(fields) {
		return
	}
	value := m.formInput.Value()
	field := fields[m.agentFocus]
	switch field.Kind {
	case agentName:
		m.agentDraft.Name = value
	case agentRole:
		m.agentDraft.Role = value
	case agentRef:
		m.agentDraft.Worktrees[field.Worktree].Ref = value
	case agentCWD:
		m.agentDraft.CWD = value
	}
}

func (m *Model) loadAgentInput() {
	fields := m.agentFields()
	if m.agentFocus < 0 || m.agentFocus >= len(fields) {
		return
	}
	field := fields[m.agentFocus]
	value := ""
	placeholder := ""
	switch field.Kind {
	case agentName:
		value, placeholder = m.agentDraft.Name, "Agent name"
	case agentRole:
		value, placeholder = m.agentDraft.Role, "Optional role"
	case agentRef:
		value, placeholder = m.agentDraft.Worktrees[field.Worktree].Ref, "Git reference"
	case agentCWD:
		value, placeholder = m.agentDraft.CWD, "Absolute directory"
	default:
		m.formInput.Blur()
		return
	}
	m.formInput.SetValue(value)
	m.formInput.Placeholder = placeholder
	m.formInput.CursorEnd()
	m.formInput.Focus()
}

func (m *Model) changeAgentChoice(field agentField, delta int) {
	switch field.Kind {
	case agentContext:
		count := len(m.contextAgents()) + 1
		m.agentDraft.Context = cycle(m.agentDraft.Context, delta, count)
	case agentPlacement:
		count := 4
		if m.agentDraft.SuggestedWorktreeID != "" {
			count = 5
		}
		m.agentDraft.Placement = cycle(m.agentDraft.Placement, delta, count)
		m.agentFocus = min(m.agentFocus, len(m.agentFields())-1)
	case agentRepository:
		if len(m.dashboard.Repositories) == 0 {
			return
		}
		draft := &m.agentDraft.Worktrees[field.Worktree]
		draft.Repository = cycle(draft.Repository, delta, len(m.dashboard.Repositories))
		draft.Remote = defaultRemoteIndex(m.dashboard.Repositories[draft.Repository])
		draft.Ref = m.dashboard.Repositories[draft.Repository].DefaultBranch
	case agentRemote:
		draft := &m.agentDraft.Worktrees[field.Worktree]
		if draft.Repository >= len(m.dashboard.Repositories) {
			return
		}
		count := len(m.dashboard.Repositories[draft.Repository].Remotes)
		if count > 0 {
			draft.Remote = cycle(draft.Remote, delta, count)
		}
	case agentFetch:
		m.agentDraft.Worktrees[field.Worktree].FetchFirst = !m.agentDraft.Worktrees[field.Worktree].FetchFirst
	case agentPlacementSource:
		m.agentDraft.PlacementAgent = cycle(m.agentDraft.PlacementAgent, delta, len(m.placementAgents()))
	case agentShare:
		m.agentDraft.Share = !m.agentDraft.Share
	}
	m.loadAgentInput()
}

func (m *Model) addAgentWorktree() {
	if len(m.dashboard.Repositories) == 0 {
		m.err = fmt.Errorf("add a repository before you create a worktree placement")
		return
	}
	next := len(m.agentDraft.Worktrees) % len(m.dashboard.Repositories)
	repository := m.dashboard.Repositories[next]
	m.agentDraft.Worktrees = append(m.agentDraft.Worktrees, agentWorktreeDraft{Repository: next, Remote: defaultRemoteIndex(repository), Ref: repository.DefaultBranch, FetchFirst: true})
}

func (m *Model) contextAgents() []model.Agent {
	out := make([]model.Agent, 0, len(m.dashboard.Agents))
	for _, agent := range m.dashboard.Agents {
		if agent.SessionPath != "" && agent.Status != "running" && agent.Status != "starting" {
			out = append(out, agent)
		}
	}
	return out
}

func (m *Model) placementAgents() []model.Agent {
	return m.dashboard.Agents
}

func (m *Model) createAgent() tea.Cmd {
	m.commitAgentInput()
	name := strings.TrimSpace(m.agentDraft.Name)
	if name == "" {
		m.err = fmt.Errorf("agent name is required")
		return nil
	}
	request := app.CreateAgentRequest{Title: name, Role: strings.TrimSpace(m.agentDraft.Role), WorkspaceID: m.formContext}
	contexts := m.contextAgents()
	if m.agentDraft.Context > 0 && m.agentDraft.Context-1 < len(contexts) {
		request.ContextAgentID = contexts[m.agentDraft.Context-1].ID
	}
	switch m.agentDraft.Placement {
	case 0:
		if len(m.dashboard.Repositories) == 0 || len(m.agentDraft.Worktrees) == 0 {
			m.err = fmt.Errorf("new placement needs at least one repository")
			return nil
		}
		request.Placement.Type = "worktrees"
		for _, draft := range m.agentDraft.Worktrees {
			if draft.Repository < 0 || draft.Repository >= len(m.dashboard.Repositories) {
				m.err = fmt.Errorf("placement repository is not available")
				return nil
			}
			repository := m.dashboard.Repositories[draft.Repository]
			remote := repository.DefaultRemote
			if draft.Remote >= 0 && draft.Remote < len(repository.Remotes) {
				remote = repository.Remotes[draft.Remote].Name
			}
			request.Placement.Worktrees = append(request.Placement.Worktrees, app.AgentPlacementWorktreeRequest{RepositoryID: repository.ID, Remote: remote, Ref: strings.TrimSpace(draft.Ref), FetchFirst: draft.FetchFirst})
		}
	case 1:
		sources := m.placementAgents()
		if len(sources) == 0 || m.agentDraft.PlacementAgent >= len(sources) {
			m.err = fmt.Errorf("choose a placement source agent")
			return nil
		}
		request.Placement = app.AgentPlacementRequest{Type: "agent", SourceAgentID: sources[m.agentDraft.PlacementAgent].ID, Share: m.agentDraft.Share}
	case 2:
		request.Placement = app.AgentPlacementRequest{Type: "directory"}
	case 3:
		request.Placement = app.AgentPlacementRequest{Type: "none", CWD: strings.TrimSpace(m.agentDraft.CWD)}
	case 4:
		if _, ok := m.dashboard.Worktree(m.agentDraft.SuggestedWorktreeID); !ok {
			m.err = fmt.Errorf("selected worktree is not available")
			return nil
		}
		mode := "fork"
		if m.agentDraft.Share {
			mode = "share"
		}
		request.Placement = app.AgentPlacementRequest{Type: "worktrees", Worktrees: []app.AgentPlacementWorktreeRequest{{SourceWorktreeID: m.agentDraft.SuggestedWorktreeID, Mode: mode}}}
	}
	m.busy = true
	m.busyTicks = 0
	m.err = nil
	m.status = "Creating worktrees and durable agent…"
	m.formInput.Blur()
	return func() tea.Msg {
		agent, err := m.client.CreateAgent(context.Background(), request)
		if err == nil {
			_, err = m.client.OpenAgent(context.Background(), agent.ID, true)
		}
		return createMsg{err: err, quit: err == nil}
	}
}

func defaultRemoteIndex(repository model.Repository) int {
	for index, remote := range repository.Remotes {
		if remote.Name == repository.DefaultRemote {
			return index
		}
	}
	return 0
}

func cycle(value, delta, count int) int {
	if count <= 0 {
		return 0
	}
	return (value + delta + count) % count
}

func shortRef(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) == 0 {
		return value
	}
	return parts[len(parts)-1]
}

func (m *Model) beginRemoteForm(selected searchResult) {
	repositoryID := ""
	if selected.Kind == resultRepository {
		repositoryID = selected.ID
	} else if selected.WorktreeID != "" {
		if worktree, ok := m.dashboard.Worktree(selected.WorktreeID); ok {
			repositoryID = worktree.RepositoryID
		}
	}
	repositoryIndex := 0
	for index, repository := range m.dashboard.Repositories {
		if repository.ID == repositoryID {
			repositoryIndex = index
			break
		}
	}
	m.screen = screenForm
	m.form = formRemote
	m.remoteDraft = remoteDraft{Repository: repositoryIndex}
	m.remoteFocus = 0
	m.busy = false
	m.err = nil
	m.status = ""
	m.loadRemoteInput()
}

func (m *Model) updateRemoteForm(key tea.KeyMsg) tea.Cmd {
	if key.String() == "esc" {
		m.screen = screenSwitcher
		m.form = formNone
		return m.focusSwitcher()
	}
	if m.busy {
		return nil
	}
	textField := m.remoteFocus >= 1 && m.remoteFocus <= 3
	switch key.String() {
	case "tab", "down", "enter":
		m.commitRemoteInput()
		if key.String() == "enter" && m.remoteFocus == 5 {
			return m.createRemote()
		}
		m.remoteFocus = (m.remoteFocus + 1) % 6
		m.loadRemoteInput()
		return nil
	case "shift+tab", "up":
		m.commitRemoteInput()
		m.remoteFocus = (m.remoteFocus + 5) % 6
		m.loadRemoteInput()
		return nil
	case "ctrl+s":
		m.commitRemoteInput()
		return m.createRemote()
	case "left":
		if !textField {
			m.changeRemoteChoice(-1)
			return nil
		}
	case "right", " ":
		if !textField {
			m.changeRemoteChoice(1)
			return nil
		}
	}
	if textField {
		var cmd tea.Cmd
		m.formInput, cmd = m.formInput.Update(key)
		m.commitRemoteInput()
		return cmd
	}
	return nil
}

func (m *Model) changeRemoteChoice(delta int) {
	switch m.remoteFocus {
	case 0:
		m.remoteDraft.Repository = cycle(m.remoteDraft.Repository, delta, len(m.dashboard.Repositories))
	case 4:
		m.remoteDraft.PushDefault = !m.remoteDraft.PushDefault
	}
}

func (m *Model) commitRemoteInput() {
	switch m.remoteFocus {
	case 1:
		m.remoteDraft.Name = m.formInput.Value()
	case 2:
		m.remoteDraft.FetchURL = m.formInput.Value()
	case 3:
		m.remoteDraft.PushURL = m.formInput.Value()
	}
}

func (m *Model) loadRemoteInput() {
	value, placeholder := "", ""
	switch m.remoteFocus {
	case 1:
		value, placeholder = m.remoteDraft.Name, "Remote name"
	case 2:
		value, placeholder = m.remoteDraft.FetchURL, "Git SSH or HTTPS URL"
	case 3:
		value, placeholder = m.remoteDraft.PushURL, "Optional separate push URL"
	default:
		m.formInput.Blur()
		return
	}
	m.formInput.SetValue(value)
	m.formInput.Placeholder = placeholder
	m.formInput.CursorEnd()
	m.formInput.Focus()
}

func (m *Model) createRemote() tea.Cmd {
	m.commitRemoteInput()
	if len(m.dashboard.Repositories) == 0 {
		m.err = fmt.Errorf("repository is required")
		return nil
	}
	name := strings.TrimSpace(m.remoteDraft.Name)
	url := strings.TrimSpace(m.remoteDraft.FetchURL)
	if name == "" || url == "" {
		m.err = fmt.Errorf("remote name and fetch URL are required")
		return nil
	}
	repository := m.dashboard.Repositories[m.remoteDraft.Repository]
	pushURL := strings.TrimSpace(m.remoteDraft.PushURL)
	pushDefault := m.remoteDraft.PushDefault
	m.busy = true
	m.busyTicks = 0
	m.err = nil
	m.status = "Adding repository remote…"
	m.formInput.Blur()
	return func() tea.Msg {
		_, err := m.client.AddRepositoryRemote(context.Background(), repository.ID, name, url, pushURL, pushDefault)
		return createMsg{err: err, message: "Remote " + name + " added to " + repository.Title}
	}
}

func (m *Model) refreshResults() {
	m.results = buildResults(m.dashboard, m.query.Value())
	if m.cursor >= len(m.results) {
		m.cursor = max(0, len(m.results)-1)
	}
}
func (m *Model) selectedWorkspace() string {
	if len(m.results) == 0 {
		return ""
	}
	return m.results[m.cursor].WorkspaceID
}

func (m *Model) loadDashboard() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		value, err := m.client.Dashboard(ctx)
		return dashboardMsg{value, err}
	}
}
func tick() tea.Cmd {
	return tea.Tick(650*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func (m *Model) openAgent(id string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.OpenAgent(context.Background(), id, true)
		return actionMsg{err: err, quit: err == nil}
	}
}

func (m *Model) openSelected(selected searchResult, command []string) tea.Cmd {
	worktree, ok := m.dashboard.Worktree(selected.WorktreeID)
	if !ok {
		return func() tea.Msg { return actionMsg{err: fmt.Errorf("worktree not found")} }
	}
	workspace, _ := m.dashboard.Workspace(selected.WorkspaceID)
	repository, _ := m.dashboard.Repository(worktree.RepositoryID)
	return m.openTerminalTarget(terminalTarget{WorkspaceID: workspace.ID, WorktreeID: worktree.ID, Path: worktree.Path, Label: workspace.Title + " · " + repository.Title}, command)
}

func (m *Model) beginTerminal(selected searchResult, command []string) tea.Cmd {
	var targets []terminalTarget
	switch selected.Kind {
	case resultAgent:
		if agent, ok := m.dashboard.Agent(selected.ID); ok {
			targets = m.targetsForAgent(agent)
		}
	case resultWorktree:
		return m.openSelected(selected, command)
	case resultWorkspace:
		seen := map[string]bool{}
		for _, agent := range m.dashboard.Agents {
			if agent.WorkspaceID == selected.WorkspaceID {
				for _, target := range m.targetsForAgent(agent) {
					targets = append(targets, target)
					seen[target.WorktreeID] = target.WorktreeID != ""
				}
			}
		}
		workspace, _ := m.dashboard.Workspace(selected.WorkspaceID)
		for _, worktree := range m.dashboard.Worktrees {
			if worktree.WorkspaceID != selected.WorkspaceID || seen[worktree.ID] {
				continue
			}
			repository, _ := m.dashboard.Repository(worktree.RepositoryID)
			targets = append(targets, terminalTarget{WorkspaceID: workspace.ID, AgentTitle: "Workspace worktrees", WorktreeID: worktree.ID, Path: worktree.Path, Label: workspace.Title + " · " + repository.Title, Detail: "workspace worktree · " + worktree.Branch})
		}
	case resultRepository:
		m.beginWorktreeForm(selected.ID, command)
		return nil
	}
	if len(targets) == 0 {
		m.status = "This selection has no worktree"
		return nil
	}
	if len(targets) == 1 {
		return m.openTerminalTarget(targets[0], command)
	}
	m.screen = screenTerminal
	m.terminalTargets = targets
	m.terminalCursor = 0
	m.terminalCommand = append([]string(nil), command...)
	m.err = nil
	return nil
}

func (m *Model) targetsForAgent(agent model.Agent) []terminalTarget {
	if agent.Placement.Type == "none" {
		return []terminalTarget{{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, AgentTitle: agent.Title, Path: agent.Placement.CWD, Label: agent.Title + " · terminal", Detail: "agent directory"}}
	}
	assignments := make(map[string]model.AgentWorktree, len(agent.Placement.Worktrees))
	for _, assignment := range agent.Placement.Worktrees {
		assignments[assignment.WorktreeID] = assignment
	}
	out := make([]terminalTarget, 0, len(agent.Placement.Worktrees))
	for _, worktree := range m.dashboard.AgentWorktrees(agent) {
		repository, _ := m.dashboard.Repository(worktree.RepositoryID)
		assignment := assignments[worktree.ID]
		kind := "secondary"
		if assignment.Position == 0 {
			kind = "primary"
		}
		out = append(out, terminalTarget{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, AgentTitle: agent.Title, WorktreeID: worktree.ID, Path: worktree.Path, Label: agent.Title + " · " + repository.Title, Detail: kind + " · " + assignment.Mode + " · " + worktree.Branch})
	}
	return out
}

func (m *Model) openTerminalTarget(target terminalTarget, command []string) tea.Cmd {
	dashboard := m.dashboard
	renderer := m.renderer
	return func() tea.Msg {
		if renderer == nil {
			return actionMsg{err: fmt.Errorf("terminal renderer is not configured")}
		}
		workspace, ok := dashboard.Workspace(target.WorkspaceID)
		if !ok {
			return actionMsg{err: fmt.Errorf("workspace not found")}
		}
		worktree := model.Worktree{ID: target.WorktreeID, WorkspaceID: target.WorkspaceID, Path: target.Path}
		if target.WorktreeID != "" {
			if stored, ok := dashboard.Worktree(target.WorktreeID); ok {
				worktree = stored
			}
		}
		id, err := renderer.OpenTerminal(context.Background(), workspace, worktree, target.Label, command)
		return actionMsg{rendererWorkspaceID: id, workspaceID: workspace.ID, err: err, quit: err == nil}
	}
}

func (m *Model) updateTerminal(key tea.KeyMsg) tea.Cmd {
	switch key.String() {
	case "esc", "q":
		m.screen = screenSwitcher
		return m.focusSwitcher()
	case "up", "ctrl+p":
		if m.terminalCursor > 0 {
			m.terminalCursor--
		}
	case "down", "ctrl+n":
		if m.terminalCursor < len(m.terminalTargets)-1 {
			m.terminalCursor++
		}
	case "enter":
		if len(m.terminalTargets) > 0 {
			return m.openTerminalTarget(m.terminalTargets[m.terminalCursor], m.terminalCommand)
		}
	}
	return nil
}
func (m *Model) resize() {
	m.query.Width = max(20, m.width-8)
	m.formInput.Width = max(20, min(70, m.width-10))
}

func (m Model) viewSwitcher(width, height int) string {
	foregroundAgents, delegatedAgents := 0, 0
	for _, agent := range m.dashboard.Agents {
		if agent.IsBackground() {
			delegatedAgents++
		} else {
			foregroundAgents++
		}
	}
	counts := fmt.Sprintf("%d workspaces  ·  %d worktrees  ·  %d agents", len(m.dashboard.Workspaces), len(m.dashboard.Worktrees), foregroundAgents)
	if delegatedAgents != 0 {
		counts += fmt.Sprintf("  ·  %d delegated", delegatedAgents)
	}
	header := titleLine("Command center", counts, width)
	search := searchStyle.Width(max(20, width-4)).Render(m.query.View())
	footerLine := switcherFooter(width, m.normalMode)
	resultsHeight := max(4, height-lipgloss.Height(header)-lipgloss.Height(search)-lipgloss.Height(footerLine)-3)
	if m.err != nil {
		errorLine := lipgloss.NewStyle().BorderStyle(lipgloss.Border{Left: "┃"}).BorderLeft(true).BorderForeground(Tokyo.Red).Foreground(Tokyo.Red).Background(Tokyo.Surface).PaddingLeft(1).Render(m.err.Error())
		results := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(resultsHeight).Padding(1, 2).Render(errorLine)
		return strings.Join([]string{header, search, results, footerLine}, "\n")
	}
	if m.loaded && len(m.dashboard.Repositories) == 0 {
		results := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(resultsHeight).Render(emptyState(width))
		return strings.Join([]string{header, search, results, footerLine}, "\n")
	}
	rowWidth := max(20, width-4)
	var lines []switcherLine
	if m.status != "" {
		color := Tokyo.Green
		if m.busy {
			color = Tokyo.Orange
		}
		notice := lipgloss.NewStyle().BorderStyle(lipgloss.Border{Left: "┃"}).BorderLeft(true).BorderForeground(color).Foreground(color).Background(Tokyo.Surface).PaddingLeft(1).Render(m.status)
		lines = append(lines,
			switcherLine{value: notice, resultIndex: -1},
			switcherLine{value: rowStyle.Width(rowWidth).Render(""), resultIndex: -1},
		)
	}
	lastGroup := ""
	for index, item := range m.results {
		group, title := switcherGroup(item)
		if group != lastGroup {
			if len(lines) > 0 {
				lines = append(lines, switcherLine{value: lipgloss.NewStyle().Background(Tokyo.Surface).Width(rowWidth).Render(""), resultIndex: -1, group: group})
			}
			lines = append(lines, switcherLine{
				value:       groupStyle.Width(rowWidth).Render(truncateText(title, max(1, rowWidth-2))),
				resultIndex: -1,
				group:       group,
				header:      true,
			})
			lastGroup = group
		}
		lines = append(lines, switcherLine{value: switcherRow(item, m.query.Value(), index == m.cursor, rowWidth), resultIndex: index, group: group})
	}
	if len(m.results) == 0 {
		lines = append(lines, switcherLine{value: lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Surface).Padding(1, 2).Render("No title matches " + fmt.Sprintf("%q", m.query.Value())), resultIndex: -1})
	}
	visible := visibleSwitcherLines(lines, m.cursor, max(3, resultsHeight))
	results := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(resultsHeight).Render(strings.Join(visible, "\n"))
	return strings.Join([]string{header, search, results, footerLine}, "\n")
}

func switcherGroup(item searchResult) (string, string) {
	if item.Kind == resultAgent && item.Delegated {
		return "delegated-agents", "DELEGATED AGENTS"
	}
	if item.Kind != resultAgent {
		return string(item.Kind), groupTitle(item.Kind)
	}
	workspaceTitle := strings.TrimSpace(item.WorkspaceTitle)
	if workspaceTitle == "" {
		workspaceTitle = "Unknown workspace"
	}
	return string(item.Kind) + ":" + item.WorkspaceID, groupTitle(item.Kind) + "  ·  " + workspaceTitle
}

func visibleSwitcherLines(lines []switcherLine, cursor, limit int) []string {
	if len(lines) == 0 || limit <= 0 {
		return nil
	}
	selectedLine := -1
	for index, line := range lines {
		if line.resultIndex == cursor {
			selectedLine = index
			break
		}
	}
	start := 0
	if selectedLine >= limit {
		start = selectedLine - limit + 1
	}
	if selectedLine >= 0 {
		selectedGroup := lines[selectedLine].group
		for index := selectedLine - 1; index >= 0 && lines[index].group == selectedGroup; index-- {
			if lines[index].header {
				if index < start && selectedLine-index < limit {
					start = index
				}
				break
			}
		}
	}
	end := min(len(lines), start+limit)
	out := make([]string, 0, end-start)
	for _, line := range lines[start:end] {
		out = append(out, line.value)
	}
	return out
}

func switcherRow(item searchResult, query string, selected bool, width int) string {
	background := Tokyo.Surface
	style := rowStyle
	prefix := "  "
	if selected {
		background = Tokyo.Selection
		style = selectedStyle
		prefix = "❯ "
	}
	prefixStyle := lipgloss.NewStyle().Foreground(Tokyo.Orange).Background(background).Bold(true)
	titleValue := truncateText(item.Title, max(10, width*45/100))
	detailWidth := max(8, width-lipgloss.Width(prefix)-lipgloss.Width(titleValue)-5)
	detail := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(background).Render(truncateText(item.Detail, detailWidth))
	title := matchedTitle(titleValue, query, background)
	gap := max(1, width-lipgloss.Width(prefix)-lipgloss.Width(title)-lipgloss.Width(detail)-2)
	row := prefixStyle.Render(prefix) + title + lipgloss.NewStyle().Background(background).Render(strings.Repeat(" ", gap)) + detail
	return style.Width(width).Padding(0, 1).Render(row)
}

func switcherFooter(width int, normalMode bool) string {
	if !normalMode {
		if width < 100 {
			return footerBar(width, keyHint("SEARCH", "type"), keyHint("↑ ↓", "select"), keyHint("↵", "open"), keyHint("ctrl+space", "actions"), keyHint("esc", "close"))
		}
		return footerBar(width, keyHint("SEARCH", "type to filter"), keyHint("↑ ↓", "select"), keyHint("enter", "open"), keyHint("ctrl+space", "actions"), keyHint("esc", "close"))
	}
	if width < 120 {
		return footerBar(width, keyHint("NORMAL", "actions"), keyHint("enter", "open"), keyHint("r/R", "git"), keyHint("w/a", "new"), keyHint("ctrl+space", "search"))
	}
	return footerBar(width, keyHint("NORMAL", "actions"), keyHint("enter", "open"), keyHint("t/e", "term/edit"), keyHint("x", "hide"), keyHint("r/R", "git"), keyHint("w/a", "new"), keyHint("q", "close"), keyHint("ctrl+space", "search"))
}

func deletionTotal(counts model.ResourceCounts) int {
	return counts.Repositories + counts.Workspaces + counts.Worktrees + counts.Agents
}

func truncateText(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func matchedTitle(title, query string, background lipgloss.Color) string {
	base := lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(background)
	match := lipgloss.NewStyle().Foreground(Tokyo.Cyan).Background(background)
	needle := []rune(strings.ToLower(strings.TrimSpace(query)))
	if len(needle) == 0 {
		return base.Render(title)
	}
	at := 0
	var out strings.Builder
	for _, r := range title {
		if at < len(needle) && unicode.ToLower(r) == needle[at] {
			out.WriteString(match.Render(string(r)))
			at++
		} else {
			out.WriteString(base.Render(string(r)))
		}
	}
	return out.String()
}

func (m Model) viewWorktreeForm(width, height int) string {
	header := titleLine("New worktree", "managed work without an agent", width)
	footerLine := footerBar(width, keyHint("tab", "next"), keyHint("← →", "change"), keyHint("ctrl+s", "open"), keyHint("esc", "cancel"))
	if m.busy {
		footerLine = footerBar(width, keyHint("wait", "creating worktree"))
	}
	fields := m.worktreeFields()
	var lines []string
	lastSection := ""
	for index, field := range fields {
		section := "SOURCE"
		switch field {
		case worktreeWorkspace, worktreeWorkspaceTitle:
			section = "WORKSPACE"
		case worktreeCreate:
			section = "ACTION"
		}
		if section != lastSection {
			if len(lines) > 0 {
				lines = append(lines, rowStyle.Width(max(20, width-4)).Render(""))
			}
			lines = append(lines, groupStyle.Width(max(20, width-4)).Render(section))
			lastSection = section
			if section == "SOURCE" {
				repository, _ := m.dashboard.Repository(m.worktreeDraft.RepositoryID)
				lines = append(lines, formChoiceRow("Repository", repository.Title, false, max(20, width-4)))
			}
		}
		label, value := m.worktreeFieldDisplay(field, index == m.worktreeFocus)
		lines = append(lines, formChoiceRow(label, value, index == m.worktreeFocus, max(20, width-4)))
	}
	feedback := ""
	if m.busy {
		frames := []string{"◐", "◓", "◑", "◒"}
		feedback = frames[m.busyTicks%len(frames)] + " " + m.status
	} else if m.err != nil {
		feedback = "! " + m.err.Error()
	}
	contentHeight := max(8, height-lipgloss.Height(header)-lipgloss.Height(footerLine)-2)
	if feedback != "" {
		contentHeight--
	}
	content := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(contentHeight).Padding(1, 2).Render(strings.Join(lines, "\n"))
	if feedback != "" {
		color := Tokyo.Orange
		if m.err != nil {
			color = Tokyo.Red
		}
		content += "\n" + lipgloss.NewStyle().Foreground(color).Background(Tokyo.SurfaceRaised).Width(width).PaddingLeft(2).Render(feedback)
	}
	return strings.Join([]string{header, content, footerLine}, "\n")
}

func (m Model) worktreeFieldDisplay(field worktreeFieldKind, selected bool) (string, string) {
	textValue := func(value, placeholder string) string {
		if selected {
			return m.formInput.View()
		}
		if strings.TrimSpace(value) == "" {
			return mutedStyle.Background(Tokyo.Surface).Render(placeholder)
		}
		return value
	}
	switch field {
	case worktreeWorkspace:
		if m.worktreeDraft.WorkspaceID == "" {
			return "Workspace", "Create a new workspace"
		}
		if workspace, ok := m.dashboard.Workspace(m.worktreeDraft.WorkspaceID); ok {
			return "Workspace", workspace.Title
		}
		return "Workspace", "Not available"
	case worktreeWorkspaceTitle:
		return "Task title", textValue(m.worktreeDraft.WorkspaceTitle, "required")
	case worktreeRemote:
		repository, ok := m.dashboard.Repository(m.worktreeDraft.RepositoryID)
		if !ok || len(repository.Remotes) == 0 {
			return "Source remote", "No remotes"
		}
		return "Source remote", m.worktreeDraft.Remote
	case worktreeRef:
		return "Source ref", textValue(m.worktreeDraft.Ref, "default branch")
	case worktreeFetch:
		if m.worktreeDraft.FetchFirst {
			return "Fetch first", "Yes"
		}
		return "Fetch first", "No"
	default:
		if len(m.worktreeCommand) > 0 {
			return "Open", "Create worktree and open editor"
		}
		return "Open", "Create worktree and open terminal"
	}
}

func (m Model) viewAgentForm(width, height int) string {
	header := titleLine("New agent", "workspace placement", width)
	footerLine := footerBar(width, keyHint("tab", "next"), keyHint("← →", "change"), keyHint("+", "secondary"), keyHint("ctrl+s", "start"), keyHint("esc", "cancel"))
	fields := m.agentFields()
	var lines []string
	selectedLine := 0
	lastSection := ""
	for index, field := range fields {
		section := agentFieldSection(field)
		if section != lastSection {
			if len(lines) > 0 {
				lines = append(lines, rowStyle.Width(max(20, width-4)).Render(""))
			}
			lines = append(lines, groupStyle.Width(max(20, width-4)).Render(section))
			lastSection = section
		}
		if index == m.agentFocus {
			selectedLine = len(lines)
		}
		label, value := m.agentFieldDisplay(field, index == m.agentFocus)
		lines = append(lines, formChoiceRow(label, value, index == m.agentFocus, max(20, width-4)))
	}
	feedback := ""
	if m.busy {
		frames := []string{"◐", "◓", "◑", "◒"}
		feedback = frames[m.busyTicks%len(frames)] + " " + m.status
	} else if m.err != nil {
		feedback = "! " + m.err.Error()
	}
	contentHeight := max(6, height-lipgloss.Height(header)-lipgloss.Height(footerLine)-2)
	if feedback != "" {
		contentHeight--
	}
	start := 0
	if selectedLine >= contentHeight {
		start = selectedLine - contentHeight + 1
	}
	end := min(len(lines), start+contentHeight)
	visible := lines[start:end]
	content := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(contentHeight).Padding(1, 2).Render(strings.Join(visible, "\n"))
	if feedback != "" {
		color := Tokyo.Orange
		if m.err != nil {
			color = Tokyo.Red
		}
		content += "\n" + lipgloss.NewStyle().Foreground(color).Background(Tokyo.SurfaceRaised).Width(width).PaddingLeft(2).Render(feedback)
	}
	return strings.Join([]string{header, content, footerLine}, "\n")
}

func agentFieldSection(field agentField) string {
	switch field.Kind {
	case agentName, agentRole:
		return "IDENTITY"
	case agentContext:
		return "CONTEXT"
	case agentPlacement:
		return "PLACEMENT"
	case agentRepository, agentRemote, agentRef, agentFetch, agentAddWorktree:
		return "WORKTREES"
	case agentPlacementSource, agentWorktreeSource, agentShare:
		return "PLACEMENT SOURCE"
	case agentCWD:
		return "DIRECTORY"
	default:
		return "ACTION"
	}
}

func (m Model) agentFieldDisplay(field agentField, selected bool) (string, string) {
	textValue := func(value, placeholder string) string {
		if selected {
			return m.formInput.View()
		}
		if strings.TrimSpace(value) == "" {
			return mutedStyle.Background(Tokyo.Surface).Render(placeholder)
		}
		return value
	}
	switch field.Kind {
	case agentName:
		return "Name", textValue(m.agentDraft.Name, "required")
	case agentRole:
		return "Role", textValue(m.agentDraft.Role, "optional")
	case agentContext:
		if m.agentDraft.Context == 0 {
			return "Context", "Fresh"
		}
		agents := m.contextAgents()
		if m.agentDraft.Context-1 < len(agents) {
			return "Context", "Fork from " + agents[m.agentDraft.Context-1].Title
		}
		return "Context", "Fresh"
	case agentPlacement:
		options := []string{"New private worktrees", "Copy an agent placement", "New managed directory", "Use external directory"}
		if m.agentDraft.SuggestedWorktreeID != "" {
			options = append(options, "Use selected worktree")
		}
		return "Type", options[m.agentDraft.Placement]
	case agentRepository:
		kind := "Secondary"
		if field.Worktree == 0 {
			kind = "Primary"
		}
		if len(m.dashboard.Repositories) == 0 {
			return kind + " repository", "No repositories"
		}
		draft := m.agentDraft.Worktrees[field.Worktree]
		return kind + " repository", m.dashboard.Repositories[draft.Repository].Title
	case agentRemote:
		draft := m.agentDraft.Worktrees[field.Worktree]
		if draft.Repository >= len(m.dashboard.Repositories) || len(m.dashboard.Repositories[draft.Repository].Remotes) == 0 {
			return "Source remote", "No remotes"
		}
		return "Source remote", m.dashboard.Repositories[draft.Repository].Remotes[draft.Remote].Name
	case agentRef:
		return "Source ref", textValue(m.agentDraft.Worktrees[field.Worktree].Ref, "default branch")
	case agentFetch:
		if m.agentDraft.Worktrees[field.Worktree].FetchFirst {
			return "Fetch first", "Yes"
		}
		return "Fetch first", "No"
	case agentAddWorktree:
		return "+", "Add secondary repository"
	case agentPlacementSource:
		agents := m.placementAgents()
		if len(agents) == 0 {
			return "Agent", "No agents"
		}
		return "Agent", agents[min(m.agentDraft.PlacementAgent, len(agents)-1)].Title
	case agentWorktreeSource:
		worktree, ok := m.dashboard.Worktree(m.agentDraft.SuggestedWorktreeID)
		if !ok {
			return "Worktree", "Not available"
		}
		repository, _ := m.dashboard.Repository(worktree.RepositoryID)
		return "Worktree", repository.Title + " · " + worktree.Branch
	case agentShare:
		if m.agentDraft.Share {
			return "Assignment", "Exact share"
		}
		return "Assignment", "Private forks"
	case agentCWD:
		return "Directory", textValue(m.agentDraft.CWD, "absolute path")
	default:
		return "Start", "Create agent and open Pi"
	}
}

func formChoiceRow(label, value string, selected bool, width int) string {
	background := Tokyo.Surface
	prefix := "  "
	if selected {
		background = Tokyo.Selection
		prefix = "❯ "
	}
	labelText := lipgloss.NewStyle().Foreground(Tokyo.Cyan).Background(background).Bold(selected).Width(24).Render(prefix + label)
	valueText := lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(background).Render(value)
	return lipgloss.NewStyle().Background(background).Width(width).Padding(0, 1).Render(labelText + valueText)
}

func (m Model) viewTerminal(width, height int) string {
	header := titleLine("Open terminal", "choose a worktree", width)
	footerLine := footerBar(width, keyHint("enter", "open"), keyHint("↑ ↓", "select"), keyHint("esc", "back"))
	var lines []string
	lastAgent := ""
	selectedLine := 0
	for index, target := range m.terminalTargets {
		if target.AgentTitle != lastAgent {
			if len(lines) > 0 {
				lines = append(lines, rowStyle.Width(max(20, width-4)).Render(""))
			}
			lines = append(lines, groupStyle.Width(max(20, width-4)).Render(strings.ToUpper(target.AgentTitle)))
			lastAgent = target.AgentTitle
		}
		if index == m.terminalCursor {
			selectedLine = len(lines)
		}
		item := searchResult{Title: target.Label, Detail: target.Detail}
		lines = append(lines, switcherRow(item, "", index == m.terminalCursor, max(20, width-4)))
	}
	contentHeight := max(4, height-lipgloss.Height(header)-lipgloss.Height(footerLine)-2)
	start := 0
	if selectedLine >= contentHeight {
		start = selectedLine - contentHeight + 1
	}
	end := min(len(lines), start+contentHeight)
	content := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(contentHeight).Padding(1, 2).Render(strings.Join(lines[start:end], "\n"))
	return strings.Join([]string{header, content, footerLine}, "\n")
}

func (m Model) viewRemoteForm(width, height int) string {
	header := titleLine("Add remote", "repository settings", width)
	footerLine := footerBar(width, keyHint("tab", "next"), keyHint("← →", "change"), keyHint("ctrl+s", "save"), keyHint("esc", "cancel"))
	repository := "No repositories"
	if len(m.dashboard.Repositories) > 0 {
		repository = m.dashboard.Repositories[min(m.remoteDraft.Repository, len(m.dashboard.Repositories)-1)].Title
	}
	textValue := func(index int, value, placeholder string) string {
		if m.remoteFocus == index {
			return m.formInput.View()
		}
		if strings.TrimSpace(value) == "" {
			return placeholder
		}
		return value
	}
	values := [][2]string{
		{"Repository", repository},
		{"Name", textValue(1, m.remoteDraft.Name, "required")},
		{"Fetch URL", textValue(2, m.remoteDraft.FetchURL, "required")},
		{"Push URL", textValue(3, m.remoteDraft.PushURL, "same as fetch URL")},
		{"Default push", map[bool]string{true: "Yes", false: "No"}[m.remoteDraft.PushDefault]},
		{"Save", "Add remote"},
	}
	lines := []string{groupStyle.Width(max(20, width-4)).Render("REMOTE")}
	for index, value := range values {
		lines = append(lines, formChoiceRow(value[0], value[1], index == m.remoteFocus, max(20, width-4)))
	}
	if m.busy {
		lines = append(lines, lipgloss.NewStyle().Foreground(Tokyo.Orange).Background(Tokyo.Surface).Render("◐ "+m.status))
	} else if m.err != nil {
		lines = append(lines, lipgloss.NewStyle().Foreground(Tokyo.Red).Background(Tokyo.Surface).Render("! "+m.err.Error()))
	}
	contentHeight := max(8, height-lipgloss.Height(header)-lipgloss.Height(footerLine)-2)
	content := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(contentHeight).Padding(1, 2).Render(strings.Join(lines, "\n"))
	return strings.Join([]string{header, content, footerLine}, "\n")
}

func (m Model) viewForm(width, height int) string {
	title := "New item"
	extra := ""
	switch m.form {
	case formRepository:
		title = "Add repository"
		extra = "Use a local path or a Git SSH/HTTPS URL. Galpon fetches branches into a private bare repository."
	case formWorkspace:
		title = "New workspace"
		extra = "A workspace groups related human and agent work. Worktrees hold the managed files."
	}
	feedback := ""
	if m.busy {
		frames := []string{"◐", "◓", "◑", "◒"}
		elapsed := time.Duration(m.busyTicks) * 650 * time.Millisecond
		progress := fmt.Sprintf("%s %s  %s", frames[m.busyTicks%len(frames)], m.status, elapsed.Round(time.Second))
		feedback = "\n\n" + lipgloss.NewStyle().Foreground(Tokyo.Orange).Background(Tokyo.Surface).Bold(true).Render(progress) + "\n" + mutedStyle.Background(Tokyo.Surface).Render("The first fetch can take time for a large repository.")
	} else if m.err != nil {
		feedback = "\n\n" + lipgloss.NewStyle().Foreground(Tokyo.Red).Background(Tokyo.Surface).Render("! "+m.err.Error())
	}
	actions := keyHint("enter", "create") + "  " + keyHint("esc", "cancel")
	if m.busy {
		actions = keyHint("esc", "close")
	}
	formWidth := max(30, min(76, width-8))
	heading := lipgloss.NewStyle().Foreground(Tokyo.Blue).Background(Tokyo.Surface).Bold(true).Render(title)
	description := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Surface).Width(max(20, formWidth-4)).Render(extra)
	input := searchStyle.Width(max(20, formWidth-8)).Render(m.formInput.View())
	box := panelStyle.Width(formWidth).Padding(1, 2).Render(heading + "\n" + description + "\n\n" + input + feedback + "\n\n" + actions)
	return titleLine("Command center", debugSize(width, height), width) + "\n" + lipgloss.Place(width, height-3, lipgloss.Center, lipgloss.Center, box, lipgloss.WithWhitespaceBackground(Tokyo.Background))
}

func Run(client *app.Client, renderer terminal.Renderer) error {
	applyPalette(configuredPalette())
	program := tea.NewProgram(New(client, renderer), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

func Snapshot(dashboard model.Dashboard, width, height int) string {
	m := New(nil, nil)
	m.width = width
	m.height = height
	m.resize()
	m.dashboard = dashboard
	m.loaded = true
	m.refreshResults()
	return m.View()
}

func EditorCommand() []string { return []string{"sh", "-lc", "exec \"${EDITOR:-nvim}\" ."} }
func DefaultEditor() string {
	if value := strings.TrimSpace(os.Getenv("EDITOR")); value != "" {
		return value
	}
	return "nvim"
}
