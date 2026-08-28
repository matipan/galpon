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
	screenOperations
)

type StartupTarget int

const (
	StartupDefault StartupTarget = iota
	StartupNewAgent
	StartupNewRepository
	StartupOperations
)

type StartupRoute struct {
	Target      StartupTarget
	WorkspaceID string
	AgentID     string
}

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
	client                 *app.Client
	renderer               terminal.Renderer
	screen                 screen
	form                   formKind
	width, height          int
	dashboard              model.Dashboard
	results                []searchResult
	cursor                 int
	normalMode             bool
	query                  textinput.Model
	formInput              textinput.Model
	loaded                 bool
	status                 string
	formContext            string
	busy                   bool
	busyTicks              int
	err                    error
	quitting               bool
	agentDraft             agentDraft
	agentFocus             int
	worktreeDraft          worktreeDraft
	worktreeFocus          int
	worktreeCommand        []string
	terminalTargets        []terminalTarget
	terminalCursor         int
	terminalCommand        []string
	remoteDraft            remoteDraft
	remoteFocus            int
	operationsWorkspace    string
	operations             model.WorkspaceOperations
	operationsLoaded       bool
	operationsCursor       int
	operationsErr          error
	operationsRefreshErr   error
	operationsGeneration   uint64
	operationsInFlight     bool
	operationsSelectedID   string
	startupRoute           StartupRoute
	startupPending         bool
	expandedAgents         map[string]bool
	expandedOlderAgents    bool
	expandedOlderWorktrees bool
	choice                 choiceOverlay
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
	WorkspaceID         string
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
	agentWorkspace
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

type choiceKind int

const (
	choiceNone choiceKind = iota
	choiceAgentWorkspace
	choiceAgentContext
	choiceAgentPlacement
	choiceAgentRepository
	choiceAgentRemote
	choiceAgentPlacementSource
	choiceWorktreeWorkspace
	choiceWorktreeRemote
	choiceRemoteRepository
)

type choiceOption struct {
	Label  string
	Detail string
	Value  string
}

type choiceOverlay struct {
	Open     bool
	Kind     choiceKind
	Title    string
	Options  []choiceOption
	Cursor   int
	Worktree int
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
type operationsMsg struct {
	workspaceID string
	generation  uint64
	value       model.WorkspaceOperations
	err         error
}

func New(client *app.Client, renderer terminal.Renderer) Model {
	return NewWithStartup(client, renderer, StartupRoute{})
}

func NewWithStartup(client *app.Client, renderer terminal.Renderer, route StartupRoute) Model {
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
	return Model{client: client, renderer: renderer, screen: screenSwitcher, query: query, formInput: formInput, startupRoute: route, startupPending: route.Target != StartupDefault, expandedAgents: make(map[string]bool)}
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
			m.replaceDashboard(value.value)
			m.refreshResults()
			if m.startupPending {
				m.startupPending = false
				return m, m.applyStartupRoute()
			}
		}
		return m, nil
	case operationsMsg:
		if value.workspaceID != m.operationsWorkspace || value.generation != m.operationsGeneration || m.screen != screenOperations {
			return m, nil
		}
		m.operationsInFlight = false
		if value.err != nil {
			if m.operationsLoaded {
				m.operationsRefreshErr = value.err
			} else {
				m.operationsLoaded = true
				m.operationsErr = value.err
			}
			return m, nil
		}
		m.operationsLoaded = true
		m.operationsErr = nil
		m.operationsRefreshErr = nil
		m.operations = value.value
		rows := flattenOperationsWork(m.operations.Work)
		m.operationsCursor = operationsCursorForID(rows, m.operationsSelectedID, m.operationsCursor)
		if len(rows) > 0 {
			m.operationsSelectedID = rows[m.operationsCursor].item.ID
		} else {
			m.operationsSelectedID = ""
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
		return m, tea.Batch(tick(), m.loadDashboard())
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
		case screenOperations:
			return m, m.updateOperations(key)
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 28
	}
	if m.screen != screenOperations && width < 40 {
		width = 80
	}
	if m.screen != screenOperations && height < 12 {
		height = 28
	}
	var body string
	switch m.screen {
	case screenForm:
		if m.choice.Open {
			body = m.viewChoiceOverlay(width, height)
			break
		}
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
	case screenOperations:
		body = m.viewOperations(width, height)
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
	case "down":
		if m.cursor < len(m.results)-1 {
			m.cursor++
		}
		return nil
	case "ctrl+n":
		workspaceID := m.selectedWorkspace()
		if workspaceID == "" {
			if m.cursor < 0 || m.cursor >= len(m.results) {
				m.status = "Select an item that has an available workspace"
			} else {
				m.status = "This item does not have an available workspace"
			}
			return nil
		}
		sourceAgentID := ""
		if m.cursor >= 0 && m.cursor < len(m.results) && m.results[m.cursor].Kind == resultAgent {
			sourceAgentID = m.results[m.cursor].ID
		}
		m.beginAgentFormFromSource(workspaceID, "", sourceAgentID)
		return nil
	case "ctrl+s":
		m.beginForm(formRepository, "Local path or Git URL", "")
		return nil
	case "ctrl+o":
		return m.beginSelectedAgentOperations()
	case "tab":
		m.toggleSwitcherExpansion()
		return nil
	case "enter":
		if len(m.results) == 0 {
			return nil
		}
		selected := m.results[m.cursor]
		if selected.Kind == resultDisclosure {
			m.toggleSwitcherExpansion()
			return nil
		}
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
	case "o":
		workspaceID := m.selectedWorkspace()
		if workspaceID == "" {
			m.status = "Select a workspace first"
			return nil
		}
		return m.beginOperations(workspaceID)
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
	m.choice = choiceOverlay{}
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
	m.choice = choiceOverlay{}
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
	if m.choice.Open {
		return m.updateChoiceOverlay(key)
	}
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
	case "tab":
		if m.openWorktreeChoice(field) {
			return nil
		}
		m.moveWorktreeFocus(1)
		return nil
	case "down":
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

func (m *Model) openWorktreeChoice(field worktreeFieldKind) bool {
	options := make([]choiceOption, 0)
	cursor := 0
	kind := choiceNone
	title := ""
	switch field {
	case worktreeWorkspace:
		kind, title = choiceWorktreeWorkspace, "Select workspace"
		options = append(options, choiceOption{Label: "Create a new workspace", Detail: "New"})
		for index, workspace := range m.dashboard.Workspaces {
			options = append(options, choiceOption{Label: workspace.Title, Detail: workspace.Status, Value: workspace.ID})
			if workspace.ID == m.worktreeDraft.WorkspaceID {
				cursor = index + 1
			}
		}
	case worktreeRemote:
		repository, ok := m.dashboard.Repository(m.worktreeDraft.RepositoryID)
		if !ok || len(repository.Remotes) == 0 {
			return false
		}
		kind, title = choiceWorktreeRemote, "Select source remote"
		for index, remote := range repository.Remotes {
			options = append(options, choiceOption{Label: remote.Name, Detail: remote.FetchURL, Value: remote.Name})
			if remote.Name == m.worktreeDraft.Remote {
				cursor = index
			}
		}
	default:
		return false
	}
	m.choice = choiceOverlay{Open: true, Kind: kind, Title: title, Options: options, Cursor: cursor}
	m.formInput.Blur()
	return true
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
	m.beginAgentFormFromSource(workspaceID, suggestedWorktreeID, "")
}

func (m *Model) beginAgentFormFromSource(workspaceID, suggestedWorktreeID, sourceAgentID string) {
	m.choice = choiceOverlay{}
	m.screen = screenForm
	m.form = formAgent
	m.formContext = workspaceID
	m.status = ""
	m.busy = false
	m.busyTicks = 0
	m.err = nil
	repositoryIndex := 0
	remoteIndex := 0
	sourceWorktreeID := suggestedWorktreeID
	if sourceWorktreeID == "" && sourceAgentID != "" {
		if source, ok := m.dashboard.Agent(sourceAgentID); ok {
			sourceWorktreeID = source.Placement.PrimaryWorktreeID
		}
	}
	if source, ok := m.dashboard.Worktree(sourceWorktreeID); ok {
		for index, repository := range m.dashboard.Repositories {
			if repository.ID == source.RepositoryID {
				repositoryIndex = index
				for remoteAt, remote := range repository.Remotes {
					if remote.Name == source.SourceRemote {
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
		if source, ok := m.dashboard.Worktree(sourceWorktreeID); ok && source.RepositoryID == repository.ID {
			if sourceRef := shortRef(source.BaseRef); sourceRef != "" {
				ref = sourceRef
			}
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
	m.agentDraft = agentDraft{WorkspaceID: workspaceID, Placement: placement, SuggestedWorktreeID: suggestedWorktreeID, Worktrees: []agentWorktreeDraft{{Repository: repositoryIndex, Remote: remoteIndex, Ref: ref, FetchFirst: true}}}
	m.agentFocus = 0
	m.loadAgentInput()
}

func (m *Model) updateAgentForm(key tea.KeyMsg) tea.Cmd {
	if m.choice.Open {
		return m.updateChoiceOverlay(key)
	}
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
	case "tab":
		if m.openAgentChoice(field) {
			return nil
		}
		m.moveAgentFocus(1)
		return nil
	case "down":
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

func (m *Model) openAgentChoice(field agentField) bool {
	options := make([]choiceOption, 0)
	cursor := 0
	kind := choiceNone
	title := ""
	switch field.Kind {
	case agentWorkspace:
		kind, title = choiceAgentWorkspace, "Select workspace"
		for index, workspace := range m.dashboard.Workspaces {
			options = append(options, choiceOption{Label: workspace.Title, Detail: workspace.Status, Value: workspace.ID})
			if workspace.ID == m.agentDraft.WorkspaceID {
				cursor = index
			}
		}
	case agentContext:
		kind, title, cursor = choiceAgentContext, "Select conversation context", m.agentDraft.Context
		options = append(options, choiceOption{Label: "Fresh", Detail: "Start without prior conversation context"})
		for _, agent := range m.contextAgents() {
			options = append(options, choiceOption{Label: agent.Title, Detail: "Fork conversation context", Value: agent.ID})
		}
	case agentPlacement:
		kind, title, cursor = choiceAgentPlacement, "Select placement type", m.agentDraft.Placement
		for index, label := range []string{"New private worktrees", "Copy an agent placement", "New managed directory", "Use external directory"} {
			options = append(options, choiceOption{Label: label, Value: fmt.Sprint(index)})
		}
		if m.agentDraft.SuggestedWorktreeID != "" {
			options = append(options, choiceOption{Label: "Use selected worktree", Value: "4"})
		}
	case agentRepository:
		kind, title, cursor = choiceAgentRepository, "Select repository", m.agentDraft.Worktrees[field.Worktree].Repository
		for _, repository := range m.dashboard.Repositories {
			options = append(options, choiceOption{Label: repository.Title, Detail: repository.DefaultBranch, Value: repository.ID})
		}
	case agentRemote:
		draft := m.agentDraft.Worktrees[field.Worktree]
		if draft.Repository < 0 || draft.Repository >= len(m.dashboard.Repositories) {
			return false
		}
		kind, title, cursor = choiceAgentRemote, "Select source remote", draft.Remote
		for _, remote := range m.dashboard.Repositories[draft.Repository].Remotes {
			options = append(options, choiceOption{Label: remote.Name, Detail: remote.FetchURL, Value: remote.Name})
		}
	case agentPlacementSource:
		kind, title, cursor = choiceAgentPlacementSource, "Select placement source", m.agentDraft.PlacementAgent
		for _, agent := range m.placementAgents() {
			workspace, _ := m.dashboard.Workspace(agent.WorkspaceID)
			options = append(options, choiceOption{Label: agent.Title, Detail: workspace.Title, Value: agent.ID})
		}
	default:
		return false
	}
	if len(options) == 0 {
		return false
	}
	m.choice = choiceOverlay{Open: true, Kind: kind, Title: title, Options: options, Cursor: min(cursor, len(options)-1), Worktree: field.Worktree}
	m.formInput.Blur()
	return true
}

func (m *Model) agentFields() []agentField {
	fields := []agentField{{Kind: agentName}, {Kind: agentRole}, {Kind: agentWorkspace}, {Kind: agentContext}, {Kind: agentPlacement}}
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
	case agentWorkspace:
		if len(m.dashboard.Workspaces) == 0 {
			return
		}
		current := 0
		for index, workspace := range m.dashboard.Workspaces {
			if workspace.ID == m.agentDraft.WorkspaceID {
				current = index
				break
			}
		}
		m.agentDraft.WorkspaceID = m.dashboard.Workspaces[cycle(current, delta, len(m.dashboard.Workspaces))].ID
		m.formContext = m.agentDraft.WorkspaceID
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
	if _, ok := m.dashboard.Workspace(m.agentDraft.WorkspaceID); !ok {
		m.err = fmt.Errorf("workspace is not available")
		return nil
	}
	request := app.CreateAgentRequest{Title: name, Role: strings.TrimSpace(m.agentDraft.Role), WorkspaceID: m.agentDraft.WorkspaceID}
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

func (m *Model) openRemoteChoice() bool {
	if len(m.dashboard.Repositories) == 0 {
		return false
	}
	options := make([]choiceOption, 0, len(m.dashboard.Repositories))
	for _, repository := range m.dashboard.Repositories {
		options = append(options, choiceOption{Label: repository.Title, Detail: repository.DefaultBranch, Value: repository.ID})
	}
	m.choice = choiceOverlay{Open: true, Kind: choiceRemoteRepository, Title: "Select repository", Options: options, Cursor: min(m.remoteDraft.Repository, len(options)-1)}
	m.formInput.Blur()
	return true
}

func (m *Model) beginRemoteForm(selected searchResult) {
	m.choice = choiceOverlay{}
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
	if m.choice.Open {
		return m.updateChoiceOverlay(key)
	}
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
	case "tab":
		if m.remoteFocus == 0 && m.openRemoteChoice() {
			return nil
		}
		m.commitRemoteInput()
		m.remoteFocus = (m.remoteFocus + 1) % 6
		m.loadRemoteInput()
		return nil
	case "down", "enter":
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

const switcherOlderAfter = 7 * 24 * time.Hour

func (m *Model) replaceDashboard(next model.Dashboard) {
	var repositoryIDs, remoteNames []string
	contextAgentID, placementAgentID, remoteRepositoryID := "", "", ""
	if m.form == formAgent {
		for _, draft := range m.agentDraft.Worktrees {
			repositoryID, remoteName := "", ""
			if draft.Repository >= 0 && draft.Repository < len(m.dashboard.Repositories) {
				repository := m.dashboard.Repositories[draft.Repository]
				repositoryID = repository.ID
				if draft.Remote >= 0 && draft.Remote < len(repository.Remotes) {
					remoteName = repository.Remotes[draft.Remote].Name
				}
			}
			repositoryIDs = append(repositoryIDs, repositoryID)
			remoteNames = append(remoteNames, remoteName)
		}
		contexts := m.contextAgents()
		if m.agentDraft.Context > 0 && m.agentDraft.Context-1 < len(contexts) {
			contextAgentID = contexts[m.agentDraft.Context-1].ID
		}
		placements := m.placementAgents()
		if m.agentDraft.PlacementAgent >= 0 && m.agentDraft.PlacementAgent < len(placements) {
			placementAgentID = placements[m.agentDraft.PlacementAgent].ID
		}
	}
	if m.form == formRemote && m.remoteDraft.Repository >= 0 && m.remoteDraft.Repository < len(m.dashboard.Repositories) {
		remoteRepositoryID = m.dashboard.Repositories[m.remoteDraft.Repository].ID
	}
	m.dashboard = next
	if m.form == formAgent {
		for index := range m.agentDraft.Worktrees {
			if repositoryIndex, ok := repositoryIndexByID(next.Repositories, repositoryIDs[index]); ok {
				draft := &m.agentDraft.Worktrees[index]
				draft.Repository = repositoryIndex
				draft.Remote = 0
				for remoteIndex, remote := range next.Repositories[repositoryIndex].Remotes {
					if remote.Name == remoteNames[index] {
						draft.Remote = remoteIndex
						break
					}
				}
			}
		}
		m.agentDraft.Context = 0
		for index, agent := range m.contextAgents() {
			if agent.ID == contextAgentID {
				m.agentDraft.Context = index + 1
				break
			}
		}
		m.agentDraft.PlacementAgent = 0
		for index, agent := range m.placementAgents() {
			if agent.ID == placementAgentID {
				m.agentDraft.PlacementAgent = index
				break
			}
		}
	}
	if m.form == formRemote {
		if repositoryIndex, ok := repositoryIndexByID(next.Repositories, remoteRepositoryID); ok {
			m.remoteDraft.Repository = repositoryIndex
		}
	}
}

func (m *Model) refreshResults() {
	selectedID, selectedDisclosure := "", ""
	if m.cursor >= 0 && m.cursor < len(m.results) {
		selectedID = m.results[m.cursor].ID
		selectedDisclosure = m.results[m.cursor].Disclosure
	}
	all := buildResults(m.dashboard, m.query.Value())
	if normalizedSearchText(m.query.Value()) != "" {
		m.results = all
	} else {
		m.results = m.defaultSwitcherResults(all, time.Now())
	}
	m.cursor = min(m.cursor, max(0, len(m.results)-1))
	for index, result := range m.results {
		if selectedID != "" && result.ID == selectedID || selectedDisclosure != "" && result.Disclosure == selectedDisclosure {
			m.cursor = index
			break
		}
	}
}

func (m *Model) defaultSwitcherResults(all []searchResult, now time.Time) []searchResult {
	children := make(map[string][]searchResult)
	var agents, workspaces, worktrees, repositories []searchResult
	for _, result := range all {
		switch result.Kind {
		case resultAgent:
			if result.Delegated {
				children[result.ParentAgentID] = append(children[result.ParentAgentID], result)
			} else {
				agents = append(agents, result)
			}
		case resultWorkspace:
			workspaces = append(workspaces, result)
		case resultWorktree:
			worktrees = append(worktrees, result)
		case resultRepository:
			repositories = append(repositories, result)
		}
	}
	cutoff := now.Add(-switcherOlderAfter).UnixMilli()
	isOlder := func(result searchResult) bool { return result.ActivityAt > 0 && result.ActivityAt < cutoff }
	appendAgent := func(out []searchResult, agent searchResult, depth int) []searchResult {
		agent.Depth = depth
		out = append(out, agent)
		if !m.expandedAgents[agent.ID] {
			return out
		}
		for _, child := range children[agent.ID] {
			out = appendAgentResult(out, child, depth+1, children, m.expandedAgents)
		}
		return out
	}
	out := make([]searchResult, 0, len(all))
	var olderAgents []searchResult
	for _, agent := range agents {
		if isOlder(agent) {
			olderAgents = append(olderAgents, agent)
			continue
		}
		out = appendAgent(out, agent, 0)
	}
	if len(olderAgents) > 0 {
		action := "tab to expand"
		if m.expandedOlderAgents {
			action = "tab to collapse"
		}
		out = append(out, searchResult{Kind: resultDisclosure, Title: "Older agents", Detail: fmt.Sprintf("%d inactive  ·  %s", len(olderAgents), action), Disclosure: "older-agents", DisclosureCount: len(olderAgents)})
		if m.expandedOlderAgents {
			for _, agent := range olderAgents {
				out = appendAgent(out, agent, 1)
			}
		}
	}
	out = append(out, workspaces...)
	var olderWorktrees []searchResult
	for _, worktree := range worktrees {
		if isOlder(worktree) {
			olderWorktrees = append(olderWorktrees, worktree)
			continue
		}
		out = append(out, worktree)
	}
	if len(olderWorktrees) > 0 {
		action := "tab to expand"
		if m.expandedOlderWorktrees {
			action = "tab to collapse"
		}
		out = append(out, searchResult{Kind: resultDisclosure, Title: "Older worktrees", Detail: fmt.Sprintf("%d inactive  ·  %s", len(olderWorktrees), action), Disclosure: "older-worktrees", DisclosureCount: len(olderWorktrees)})
		if m.expandedOlderWorktrees {
			for _, worktree := range olderWorktrees {
				worktree.Depth = 1
				out = append(out, worktree)
			}
		}
	}
	return append(out, repositories...)
}

func appendAgentResult(out []searchResult, agent searchResult, depth int, children map[string][]searchResult, expanded map[string]bool) []searchResult {
	agent.Depth = depth
	out = append(out, agent)
	if !expanded[agent.ID] {
		return out
	}
	for _, child := range children[agent.ID] {
		out = appendAgentResult(out, child, depth+1, children, expanded)
	}
	return out
}

func (m *Model) toggleSwitcherExpansion() {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return
	}
	selected := m.results[m.cursor]
	switch selected.Disclosure {
	case "older-agents":
		m.expandedOlderAgents = !m.expandedOlderAgents
	case "older-worktrees":
		m.expandedOlderWorktrees = !m.expandedOlderWorktrees
	default:
		if selected.Kind != resultAgent || selected.DelegatedCount == 0 || normalizedSearchText(m.query.Value()) != "" {
			return
		}
		m.expandedAgents[selected.ID] = !m.expandedAgents[selected.ID]
	}
	m.refreshResults()
}
func (m *Model) selectedWorkspace() string {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		return ""
	}
	workspaceID := m.results[m.cursor].WorkspaceID
	if _, ok := m.dashboard.Workspace(workspaceID); !ok {
		return ""
	}
	return workspaceID
}

func (m *Model) beginSelectedAgentOperations() tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.results) {
		m.status = "Select an agent to open Operations"
		return nil
	}
	selected := m.results[m.cursor]
	if selected.Kind != resultAgent {
		m.status = "The selected item is not an agent. Select an agent to open Operations"
		return nil
	}
	agent, ok := m.dashboard.Agent(selected.ID)
	if !ok {
		m.status = "The selected agent is not available"
		return nil
	}
	if _, ok := m.dashboard.Workspace(agent.WorkspaceID); !ok {
		m.status = "The selected agent does not have an available workspace"
		return nil
	}
	return m.beginOperations(agent.WorkspaceID)
}

func (m *Model) loadOperations(workspaceID string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		value, err := m.client.WorkspaceOperations(ctx, workspaceID)
		return operationsMsg{workspaceID: workspaceID, generation: generation, value: value, err: err}
	}
}

func (m *Model) beginOperations(workspaceID string) tea.Cmd {
	m.screen = screenOperations
	m.operationsWorkspace = workspaceID
	m.operations = model.WorkspaceOperations{}
	m.operationsLoaded = false
	m.operationsCursor = 0
	m.operationsErr = nil
	m.operationsRefreshErr = nil
	m.operationsSelectedID = ""
	m.operationsGeneration++
	m.operationsInFlight = true
	return m.loadOperations(workspaceID, m.operationsGeneration)
}

func operationsCursorForID(rows []operationsWorkRow, id string, fallback int) int {
	if id != "" {
		for index, row := range rows {
			if row.item.ID == id {
				return index
			}
		}
	}
	return min(max(0, fallback), max(0, len(rows)-1))
}

func (m *Model) applyStartupRoute() tea.Cmd {
	switch m.startupRoute.Target {
	case StartupNewAgent:
		if _, ok := m.dashboard.Workspace(m.startupRoute.WorkspaceID); !ok {
			m.err = fmt.Errorf("the current Galpon workspace is no longer available")
			return nil
		}
		m.beginAgentFormFromSource(m.startupRoute.WorkspaceID, "", m.startupRoute.AgentID)
		return nil
	case StartupNewRepository:
		m.beginForm(formRepository, "Local path or Git URL", "")
		return nil
	case StartupOperations:
		agent, ok := m.dashboard.Agent(m.startupRoute.AgentID)
		if !ok || agent.WorkspaceID != m.startupRoute.WorkspaceID {
			m.err = fmt.Errorf("the current Galpon agent is no longer available")
			return nil
		}
		if _, ok := m.dashboard.Workspace(m.startupRoute.WorkspaceID); !ok {
			m.err = fmt.Errorf("the current Galpon workspace is no longer available")
			return nil
		}
		return m.beginOperations(m.startupRoute.WorkspaceID)
	default:
		return nil
	}
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

func (m *Model) updateOperations(key tea.KeyMsg) tea.Cmd {
	rows := flattenOperationsWork(m.operations.Work)
	switch key.String() {
	case "esc", "q":
		m.operationsGeneration++
		m.operationsInFlight = false
		m.screen = screenSwitcher
		m.operationsWorkspace = ""
		m.operationsErr = nil
		m.operationsRefreshErr = nil
		return m.focusSwitcher()
	case "r":
		if m.operationsInFlight || m.operationsWorkspace == "" {
			return nil
		}
		m.operationsGeneration++
		m.operationsInFlight = true
		m.operationsRefreshErr = nil
		return m.loadOperations(m.operationsWorkspace, m.operationsGeneration)
	case "up", "ctrl+p":
		if m.operationsCursor > 0 {
			m.operationsCursor--
		}
	case "down", "ctrl+n":
		if m.operationsCursor < len(rows)-1 {
			m.operationsCursor++
		}
	}
	if len(rows) > 0 {
		m.operationsSelectedID = rows[m.operationsCursor].item.ID
	}
	return nil
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
func (m *Model) updateChoiceOverlay(key tea.KeyMsg) tea.Cmd {
	if !m.choice.Open {
		return nil
	}
	switch key.String() {
	case "esc":
		m.choice = choiceOverlay{}
		m.restoreFormInput()
	case "up", "shift+tab":
		m.choice.Cursor = cycle(m.choice.Cursor, -1, len(m.choice.Options))
	case "down", "tab":
		m.choice.Cursor = cycle(m.choice.Cursor, 1, len(m.choice.Options))
	case "enter":
		m.applyChoice()
		m.choice = choiceOverlay{}
		m.restoreFormInput()
	}
	return nil
}

func (m *Model) restoreFormInput() {
	switch m.form {
	case formAgent:
		m.loadAgentInput()
	case formWorktree:
		m.loadWorktreeInput()
	case formRemote:
		m.loadRemoteInput()
	}
}

func (m *Model) applyChoice() {
	if m.choice.Cursor < 0 || m.choice.Cursor >= len(m.choice.Options) {
		return
	}
	index := m.choice.Cursor
	value := m.choice.Options[index].Value
	switch m.choice.Kind {
	case choiceAgentWorkspace:
		if _, ok := m.dashboard.Workspace(value); ok {
			m.agentDraft.WorkspaceID = value
			m.formContext = value
		}
	case choiceAgentContext:
		m.agentDraft.Context = 0
		for current, agent := range m.contextAgents() {
			if agent.ID == value {
				m.agentDraft.Context = current + 1
				break
			}
		}
	case choiceAgentPlacement:
		m.agentDraft.Placement = index
		m.agentFocus = min(m.agentFocus, len(m.agentFields())-1)
	case choiceAgentRepository:
		if repositoryIndex, ok := repositoryIndexByID(m.dashboard.Repositories, value); ok && m.choice.Worktree < len(m.agentDraft.Worktrees) {
			repository := m.dashboard.Repositories[repositoryIndex]
			draft := &m.agentDraft.Worktrees[m.choice.Worktree]
			draft.Repository = repositoryIndex
			draft.Remote = defaultRemoteIndex(repository)
			draft.Ref = repository.DefaultBranch
		}
	case choiceAgentRemote:
		if m.choice.Worktree < len(m.agentDraft.Worktrees) {
			draft := &m.agentDraft.Worktrees[m.choice.Worktree]
			if draft.Repository >= 0 && draft.Repository < len(m.dashboard.Repositories) {
				for current, remote := range m.dashboard.Repositories[draft.Repository].Remotes {
					if remote.Name == value {
						draft.Remote = current
						break
					}
				}
			}
		}
	case choiceAgentPlacementSource:
		for current, agent := range m.placementAgents() {
			if agent.ID == value {
				m.agentDraft.PlacementAgent = current
				break
			}
		}
	case choiceWorktreeWorkspace:
		m.worktreeDraft.WorkspaceID = ""
		if _, ok := m.dashboard.Workspace(value); ok {
			m.worktreeDraft.WorkspaceID = value
		}
		m.worktreeFocus = min(m.worktreeFocus, len(m.worktreeFields())-1)
	case choiceWorktreeRemote:
		if repository, ok := m.dashboard.Repository(m.worktreeDraft.RepositoryID); ok {
			for _, remote := range repository.Remotes {
				if remote.Name == value {
					m.worktreeDraft.Remote = value
					break
				}
			}
		}
	case choiceRemoteRepository:
		if repositoryIndex, ok := repositoryIndexByID(m.dashboard.Repositories, value); ok {
			m.remoteDraft.Repository = repositoryIndex
		}
	}
}

func repositoryIndexByID(repositories []model.Repository, id string) (int, bool) {
	for index, repository := range repositories {
		if repository.ID == id {
			return index, true
		}
	}
	return 0, false
}

func (m *Model) resize() {
	m.query.Width = max(20, m.width-8)
	m.formInput.Width = max(20, min(70, m.width-10))
}

func (m Model) viewChoiceOverlay(width, height int) string {
	header := titleLine(m.choice.Title, fmt.Sprintf("%d options", len(m.choice.Options)), width)
	footerLine := footerBar(width, keyHint("↑ ↓", "select"), keyHint("enter", "use"), keyHint("esc", "cancel"))
	contentHeight := max(4, height-lipgloss.Height(header)-lipgloss.Height(footerLine)-2)
	start := 0
	if m.choice.Cursor >= contentHeight {
		start = m.choice.Cursor - contentHeight + 1
	}
	end := min(len(m.choice.Options), start+contentHeight)
	lines := make([]string, 0, end-start)
	for index := start; index < end; index++ {
		option := m.choice.Options[index]
		item := searchResult{Title: option.Label, Detail: option.Detail}
		lines = append(lines, switcherRow(item, "", index == m.choice.Cursor, max(20, width-4)))
	}
	content := lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Height(contentHeight).Padding(1, 2).Render(strings.Join(lines, "\n"))
	return strings.Join([]string{header, content, footerLine}, "\n")
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
				value:       switcherGroupHeader(group, title, rowWidth),
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

type operationsWorkRow struct {
	item  model.WorkItem
	depth int
}

func flattenOperationsWork(items []model.WorkItem) []operationsWorkRow {
	rows := make([]operationsWorkRow, 0)
	var visit func([]model.WorkItem, int)
	visit = func(values []model.WorkItem, depth int) {
		for _, item := range values {
			rows = append(rows, operationsWorkRow{item: item, depth: depth})
			visit(item.Children, depth+1)
		}
	}
	visit(items, 0)
	return rows
}

func (m Model) viewOperations(width, height int) string {
	width = max(1, width)
	height = max(1, height)
	if width < 20 || height < 6 {
		return m.compactOperationsView(width, height)
	}
	workspaceTitle := m.operations.Workspace.Title
	if workspaceTitle == "" {
		if workspace, ok := m.dashboard.Workspace(m.operationsWorkspace); ok {
			workspaceTitle = workspace.Title
		}
	}
	header := operationsTitleLine(safeOperationsTitle(workspaceTitle), width)
	footerLine := footerBar(width, keyHint("↑ ↓", "select"), keyHint("r", "refresh"), keyHint("q", "back"))
	if width < 40 {
		footerLine = footerBar(width, keyHint("q", "back"))
	}
	bodyHeight := max(1, height-lipgloss.Height(header)-lipgloss.Height(footerLine)-1)
	var body string
	switch {
	case !m.operationsLoaded:
		body = operationsStatePanel("Loading operations…", "Reading current workspace facts.", width, bodyHeight, Tokyo.Blue)
	case m.operationsErr != nil:
		body = operationsStatePanel("Operations unavailable", m.operationsErr.Error(), width, bodyHeight, Tokyo.Red)
	default:
		body = m.operationsBody(width, bodyHeight)
	}
	return strings.Join([]string{header, body, footerLine}, "\n")
}

func (m Model) compactOperationsView(width, height int) string {
	lines := []string{truncateText("GALPÓN Ops", width)}
	switch {
	case !m.operationsLoaded:
		lines = append(lines, truncateText("Loading…", width))
	case m.operationsErr != nil:
		lines = append(lines, truncateText("Unavailable", width))
	default:
		rows := flattenOperationsWork(m.operations.Work)
		if len(rows) == 0 {
			lines = append(lines, truncateText("No work", width))
		} else {
			item := rows[min(m.operationsCursor, len(rows)-1)].item
			lines = append(lines, truncateText(item.Title, width), truncateText(item.Observation.State+" · "+item.Observation.Lease, width))
		}
	}
	if m.operationsRefreshErr != nil && len(lines) < height-1 {
		lines = append(lines, truncateText("Refresh failed", width))
	}
	if len(lines) < height {
		lines = append(lines, truncateText("q back", width))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func safeOperationsTitle(value string) string {
	out := make([]rune, 0, 96)
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			continue
		}
		out = append(out, r)
		if len(out) == 96 {
			break
		}
	}
	if title := strings.TrimSpace(string(out)); title != "" {
		return title
	}
	return "Workspace"
}

func operationsTitleLine(workspace string, width int) string {
	if width >= 52 {
		return titleLine("Operations", truncateText(workspace, max(8, width/3)), width)
	}
	brand := brandStyle.Render("GALPÓN")
	remaining := max(1, width-lipgloss.Width(brand))
	label := lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.SurfaceRaised).Bold(true).Width(remaining).PaddingLeft(1).Render(truncateText("Operations · "+workspace, max(1, remaining-1)))
	return brand + label
}

func operationsStatePanel(title, detail string, width, height int, color lipgloss.Color) string {
	lines := []string{
		lipgloss.NewStyle().Foreground(color).Background(Tokyo.Surface).Bold(true).Render(truncateText(title, max(1, width-4))),
		lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Surface).Render(truncateText(detail, max(1, width-4))),
	}
	return lipgloss.NewStyle().Background(Tokyo.Surface).Width(max(1, width-4)).Height(max(1, height-2)).Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func (m Model) operationsBody(width, height int) string {
	summary := m.operations.Summary
	truncated := ""
	if m.operations.Truncation.Truncated {
		truncated = " · more facts omitted"
	}
	summaryText := fmt.Sprintf("%d agents · %d active · %d queued work · %d durable inbound queued · %d durable claims · %d reported blockers · %d stale observations%s",
		summary.Agents, summary.ActiveWork, summary.QueuedWork, m.operations.Queue.InboundQueued, m.operations.Queue.InboundClaimed, summary.ReportedBlockers, summary.StaleObservations, truncated)
	if m.operationsRefreshErr != nil {
		summaryText += " · refresh failed; showing prior facts"
	}
	summaryBand := lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.Prompt).Width(max(1, width-2)).Padding(0, 1).Render(truncateText(summaryText, max(1, width-2)))
	contentHeight := max(1, height-lipgloss.Height(summaryBand)-1)
	rows := flattenOperationsWork(m.operations.Work)
	if width >= 96 && contentHeight >= 8 {
		leftWidth := max(32, width*46/100)
		rightWidth := width - leftWidth
		topHeight := max(4, contentHeight*2/3)
		outline := m.operationsOutline(rows, leftWidth, topHeight)
		detail := m.operationsDetail(rows, rightWidth, topHeight)
		runtime := m.operationsRuntime(width, max(2, contentHeight-topHeight))
		return strings.Join([]string{summaryBand, lipgloss.JoinHorizontal(lipgloss.Top, outline, detail), runtime}, "\n")
	}
	outlineHeight := max(2, contentHeight/2)
	detailHeight := max(1, (contentHeight-outlineHeight)/2)
	runtimeHeight := max(1, contentHeight-outlineHeight-detailHeight)
	return strings.Join([]string{
		summaryBand,
		m.operationsOutline(rows, width, outlineHeight),
		m.operationsDetail(rows, width, detailHeight),
		m.operationsRuntime(width, runtimeHeight),
	}, "\n")
}

func operationsPanelTitle(value string, width int) string {
	return lipgloss.NewStyle().Foreground(Tokyo.Comment).Background(Tokyo.Surface).Bold(true).Width(max(1, width-1)).PaddingLeft(1).Render(truncateText(value, max(1, width-1)))
}

func (m Model) operationsOutline(rows []operationsWorkRow, width, height int) string {
	width, height = max(1, width), max(1, height)
	lines := []string{operationsPanelTitle("WORK OUTLINE", width)}
	available := max(0, height-1)
	if len(rows) == 0 && available > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.Surface).Width(max(1, width-1)).PaddingLeft(1).Render(truncateText("No active or recent delegated work", max(1, width-1))))
	}
	start := 0
	if m.operationsCursor >= available && available > 0 {
		start = m.operationsCursor - available + 1
	}
	for index := start; index < len(rows) && len(lines) < height; index++ {
		row := rows[index]
		selected := index == m.operationsCursor
		background := Tokyo.Surface
		prefix := "  "
		if selected {
			background = Tokyo.Selection
			prefix = "❯ "
		}
		indent := strings.Repeat("  ", min(row.depth, 6))
		state := row.item.Observation.State
		text := prefix + indent + operationsStateMark(state) + " " + row.item.Title + " · " + strings.ReplaceAll(row.item.Priority, "_", " ")
		style := lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(background).Width(width)
		if selected {
			style = style.Bold(true)
		}
		lines = append(lines, style.Render(truncateText(text, width)))
	}
	for len(lines) < height {
		lines = append(lines, lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Render(""))
	}
	return strings.Join(lines[:height], "\n")
}

func operationsObservedAge(timestamp int64) string {
	elapsed := max(int64(0), time.Now().UnixMilli()-timestamp)
	switch {
	case timestamp <= 0:
		return "at an unknown time"
	case elapsed < 1_000:
		return "now"
	case elapsed < 60_000:
		return fmt.Sprintf("%ds ago", elapsed/1_000)
	case elapsed < 3_600_000:
		return fmt.Sprintf("%dm ago", elapsed/60_000)
	case elapsed < 86_400_000:
		return fmt.Sprintf("%dh ago", elapsed/3_600_000)
	default:
		return fmt.Sprintf("%dd ago", elapsed/86_400_000)
	}
}

func operationsSourceLabel(source string) string {
	switch source {
	case "observed":
		return "Observed"
	case "reported":
		return "Reported"
	default:
		return "Other"
	}
}

func (m Model) operationsDetail(rows []operationsWorkRow, width, height int) string {
	width, height = max(1, width), max(1, height)
	lines := []string{operationsPanelTitle("SELECTED DETAIL", width)}
	if len(rows) == 0 {
		lines = append(lines, "No work item is selected.")
	} else {
		item := rows[min(m.operationsCursor, len(rows)-1)].item
		observed := fmt.Sprintf("Observed · %s · attempt %d · lease %s", item.Observation.State, item.Observation.Attempt, item.Observation.Lease)
		if item.Observation.State == "started" && item.Observation.LeaseObservedAt > 0 {
			observed += " · lease observed " + operationsObservedAge(item.Observation.LeaseObservedAt)
		}
		lines = append(lines, item.Title, observed)
		if item.Result != nil {
			lines = append(lines, "Observed result · "+item.Result.Label)
		}
		if item.Checkpoint != nil {
			lines = append(lines, "Reported · "+item.Checkpoint.Phase+" · "+item.Checkpoint.Summary)
			if item.Checkpoint.Blocker != "" {
				lines = append(lines, "Reported blocker · "+item.Checkpoint.Blocker)
			}
		} else {
			lines = append(lines, "Reported · No current checkpoint")
		}
		if item.Observation.Lease == "stale" {
			lines = append(lines, "A stale observation does not mean that work is stuck.")
		}
		for index := len(item.Timeline) - 1; index >= 0 && len(lines) < height; index-- {
			event := item.Timeline[index]
			lines = append(lines, fmt.Sprintf("%s · %s · %s", operationsSourceLabel(event.Source), event.Kind, event.Label))
		}
	}
	styled := make([]string, 0, height)
	for index, line := range lines {
		if index == 0 {
			styled = append(styled, line)
			continue
		}
		color := Tokyo.Foreground
		if strings.HasPrefix(line, "Observed") || strings.HasPrefix(line, "Reported") {
			color = Tokyo.Cyan
		}
		styled = append(styled, lipgloss.NewStyle().Foreground(color).Background(Tokyo.Surface).Width(max(1, width-1)).PaddingLeft(1).Render(truncateText(line, max(1, width-1))))
		if len(styled) == height {
			break
		}
	}
	for len(styled) < height {
		styled = append(styled, lipgloss.NewStyle().Background(Tokyo.Surface).Width(width).Render(""))
	}
	return strings.Join(styled[:height], "\n")
}

func (m Model) operationsRuntime(width, height int) string {
	width, height = max(1, width), max(1, height)
	lines := []string{operationsPanelTitle("AGENT RUNTIME", width)}
	for _, agent := range m.operations.Agents {
		line := operationsStateMark(agent.Status) + " " + agent.Title + " · " + agent.Status
		delivery := agent.CurrentDelivery
		prefix := "current"
		if delivery == nil {
			delivery = agent.ObservedDelivery
			prefix = "latest observed"
		}
		if delivery != nil {
			observation := delivery.Observation
			line += " · " + prefix + " " + observation.State + " delivery · " + observation.Lease + " lease"
			if observation.LeaseObservedAt > 0 {
				line += " observed " + operationsObservedAge(observation.LeaseObservedAt)
			}
			if delivery.Checkpoint != nil {
				line += " · reported: " + delivery.Checkpoint.Summary
			}
		} else {
			line += " · no observed delivery · no lease"
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(Tokyo.Foreground).Background(Tokyo.SurfaceRaised).Width(max(1, width-1)).PaddingLeft(1).Render(truncateText(line, max(1, width-1))))
		if len(lines) == height {
			break
		}
	}
	if m.operations.Activity != nil && len(m.operations.Activity.Facts) > 0 && len(lines) < height {
		lines = append(lines, lipgloss.NewStyle().Foreground(Tokyo.Comment).Background(Tokyo.SurfaceRaised).Width(max(1, width-1)).PaddingLeft(1).Render(truncateText("OBSERVED ACTIVITY", max(1, width-1))))
		for _, activity := range m.operations.Activity.Facts {
			prefix := "observed"
			if time.Now().UnixMilli()-activity.ObservedAt > 30_000 {
				prefix = "last"
			}
			line := activity.Category + " · " + activity.Status + " · " + prefix + " " + operationsObservedAge(activity.ObservedAt)
			lines = append(lines, lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.SurfaceRaised).Width(max(1, width-1)).PaddingLeft(1).Render(truncateText(line, max(1, width-1))))
			if len(lines) == height {
				break
			}
		}
	}
	for len(lines) < height {
		lines = append(lines, lipgloss.NewStyle().Background(Tokyo.SurfaceRaised).Width(width).Render(""))
	}
	return strings.Join(lines[:height], "\n")
}

func operationsStateMark(state string) string {
	switch state {
	case "running", "started":
		return "◐"
	case "starting", "queued":
		return "○"
	case "idle", "completed":
		return "✓"
	case "failed", "canceled", "expired":
		return "×"
	default:
		return "·"
	}
}

func switcherGroup(item searchResult) (string, string) {
	if item.Kind == resultDisclosure {
		if item.Disclosure == "older-agents" {
			return string(resultAgent), groupTitle(resultAgent)
		}
		return string(resultWorktree), groupTitle(resultWorktree)
	}
	return string(item.Kind), groupTitle(item.Kind)
}

func switcherGroupHeader(group, title string, width int) string {
	foreground := Tokyo.StatusInk
	background := Tokyo.Blue
	symbol := "◆"
	switch group {
	case string(resultWorkspace):
		background, symbol = Tokyo.Purple, "▦"
	case string(resultWorktree):
		background, symbol = Tokyo.Teal, "⑂"
	case string(resultRepository):
		background, symbol = Tokyo.Orange, "⌂"
	case string(resultAgent):
		background, symbol = Tokyo.Cyan, "●"
	}
	label := " " + symbol + "  " + title + " "
	labelStyle := lipgloss.NewStyle().Foreground(foreground).Background(background).Bold(true)
	fillStyle := lipgloss.NewStyle().Background(Tokyo.SurfaceRaised)
	fill := strings.Repeat(" ", max(0, width-lipgloss.Width(label)))
	return labelStyle.Render(label) + fillStyle.Render(fill)
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
	indent := strings.Repeat("  ", min(item.Depth, 5))
	prefixStyle := lipgloss.NewStyle().Foreground(Tokyo.Orange).Background(background).Bold(true)
	if item.Kind == resultDisclosure {
		marker := "▸"
		if strings.Contains(item.Detail, "collapse") {
			marker = "▾"
		}
		text := marker + " " + item.Title
		detail := lipgloss.NewStyle().Foreground(Tokyo.Comment).Background(background).Render(item.Detail)
		gap := max(1, width-lipgloss.Width(prefix)-lipgloss.Width(indent)-lipgloss.Width(text)-lipgloss.Width(detail)-2)
		row := prefixStyle.Render(prefix+indent) + lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(background).Bold(true).Render(text) + lipgloss.NewStyle().Background(background).Render(strings.Repeat(" ", gap)) + detail
		return style.Width(width).Padding(0, 1).Render(row)
	}
	titleLimit := max(10, width*34/100)
	titleValue := truncateText(item.Title, titleLimit)
	title := matchedTitle(titleValue, query, background)
	workspace := ""
	if item.Kind == resultAgent && item.WorkspaceTitle != "" {
		workspace = lipgloss.NewStyle().Foreground(Tokyo.Purple).Background(background).Bold(true).Render("  [" + truncateText(item.WorkspaceTitle, max(8, width/5)) + "]")
	}
	badge := ""
	if item.Kind == resultAgent && item.DelegatedCount > 0 {
		badge = lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(background).Bold(true).Render(fmt.Sprintf("  🤖 %d", item.DelegatedCount))
	}
	used := lipgloss.Width(prefix) + lipgloss.Width(indent) + lipgloss.Width(title) + lipgloss.Width(workspace) + lipgloss.Width(badge) + 3
	detailWidth := max(0, width-used)
	detail := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(background).Render(truncateText(item.Detail, detailWidth))
	gap := max(1, width-lipgloss.Width(prefix)-lipgloss.Width(indent)-lipgloss.Width(title)-lipgloss.Width(workspace)-lipgloss.Width(badge)-lipgloss.Width(detail)-2)
	row := prefixStyle.Render(prefix+indent) + title + workspace + badge + lipgloss.NewStyle().Background(background).Render(strings.Repeat(" ", gap)) + detail
	return style.Width(width).Padding(0, 1).Render(row)
}

func switcherHint(key, label string) string {
	keyPart := lipgloss.NewStyle().Foreground(Tokyo.StatusInk).Background(Tokyo.Status).Bold(true).Render(key)
	if label == "" {
		return keyPart
	}
	labelPart := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.SurfaceRaised).Render(" " + label)
	return keyPart + labelPart
}

func tightSwitcherHint(key, label string) string {
	keyPart := lipgloss.NewStyle().Foreground(Tokyo.StatusInk).Background(Tokyo.Status).Bold(true).Render(key)
	labelPart := lipgloss.NewStyle().Foreground(Tokyo.Muted).Background(Tokyo.SurfaceRaised).Render(":" + label)
	return keyPart + labelPart
}

func switcherFooter(width int, normalMode bool) string {
	if width < 24 {
		return footerBar(width, tightSwitcherHint("^N", "new")+tightSwitcherHint("^O", "ops"))
	}
	if width < 35 {
		return footerBar(width, switcherHint("^N", "new"), switcherHint("^O", "operations"))
	}

	mode := switcherHint("SEARCH", "")
	modeWithAction := switcherHint("SEARCH", "type")
	modeSwitch := switcherHint("^Sp", "actions")
	closeHint := switcherHint("esc", "close")
	if normalMode {
		mode = switcherHint("NORMAL", "")
		modeWithAction = switcherHint("NORMAL", "actions")
		modeSwitch = switcherHint("^Sp", "search")
		closeHint = switcherHint("q", "close")
	}
	newAgent := switcherHint("ctrl+n", "new agent")
	newRepository := switcherHint("ctrl+s", "new repository")
	operations := switcherHint("ctrl+o", "operations")
	shortcuts := []string{newAgent, operations}
	if width < 48 {
		return footerBar(width, shortcuts...)
	}
	if width < 60 {
		return footerBar(width, append([]string{mode}, shortcuts...)...)
	}
	selectHint := switcherHint("↑ ↓", "select")
	if width < 72 {
		return footerBar(width, append([]string{mode, selectHint}, shortcuts...)...)
	}
	openHint := switcherHint("enter", "open")
	if width < 80 {
		return footerBar(width, append([]string{mode, selectHint, openHint}, shortcuts...)...)
	}
	if width < 100 {
		parts := []string{mode, selectHint, openHint, newAgent, operations, modeSwitch}
		return footerBar(width, parts...)
	}
	if width < 120 {
		parts := []string{modeWithAction, newAgent, newRepository, operations, modeSwitch}
		return footerBar(width, parts...)
	}
	expand := switcherHint("tab", "expand")
	if !normalMode {
		return footerBar(width, modeWithAction, expand, newAgent, newRepository, operations, switcherHint("ctrl+space", "actions"), closeHint)
	}
	return footerBar(width, modeWithAction, expand, newAgent, newRepository, operations, switcherHint("ctrl+space", "search"), closeHint)
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
	footerLine := footerBar(width, keyHint("tab", "list / next"), keyHint("← →", "change"), keyHint("ctrl+s", "open"), keyHint("esc", "cancel"))
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
	footerLine := footerBar(width, keyHint("tab", "list / next"), keyHint("← →", "change"), keyHint("+", "secondary"), keyHint("ctrl+s", "start"), keyHint("esc", "cancel"))
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
	case agentWorkspace:
		return "WORKSPACE"
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
	case agentWorkspace:
		if workspace, ok := m.dashboard.Workspace(m.agentDraft.WorkspaceID); ok {
			return "Workspace", workspace.Title
		}
		return "Workspace", "Not available"
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
	footerLine := footerBar(width, keyHint("tab", "list / next"), keyHint("← →", "change"), keyHint("ctrl+s", "save"), keyHint("esc", "cancel"))
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
	return RunWithStartup(client, renderer, StartupRoute{})
}

func RunWithStartup(client *app.Client, renderer terminal.Renderer, route StartupRoute) error {
	applyPalette(configuredPalette())
	program := tea.NewProgram(NewWithStartup(client, renderer, route), tea.WithAltScreen())
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
