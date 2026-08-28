package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
	"github.com/muesli/termenv"
)

func TestRepositoryFormShowsProgressAndErrors(t *testing.T) {
	m := New(nil, nil)
	m.width = 100
	m.height = 30
	m.screen = screenForm
	m.form = formRepository
	m.busy = true
	m.status = "Fetching repository branches…"
	if view := m.View(); !strings.Contains(view, "Fetching repository branches") || !strings.Contains(view, "first fetch") {
		t.Fatalf("busy form omitted progress: %s", view)
	}
	m.busy = false
	m.err = errors.New("permission denied")
	if view := m.View(); !strings.Contains(view, "permission denied") {
		t.Fatalf("form omitted error: %s", view)
	}
}

func TestRepositoryFormEnterSendsRequestAndShowsResult(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "galpon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan app.AddRepositoryRequest, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/repositories" {
			http.NotFound(w, r)
			return
		}
		var request app.AddRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- request
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"repository": model.Repository{ID: "repo", Title: "dagger"}})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	m := New(app.NewClient(socket), nil)
	m.beginForm(formRepository, "Local path or Git URL", "")
	m.formInput.SetValue("git@github.com:dagger/dagger")
	command := m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || !m.busy {
		t.Fatal("Enter did not start repository creation")
	}
	message := command()
	request := <-requests
	if request.Path != "git@github.com:dagger/dagger" {
		t.Fatalf("repository path = %q", request.Path)
	}
	updated, _ := m.Update(message)
	got := updated.(Model)
	if got.screen != screenSwitcher || got.form != formNone {
		t.Fatalf("successful add did not return to switcher: screen=%d form=%d", got.screen, got.form)
	}
	if got.status != "Repository dagger is ready" {
		t.Fatalf("success status = %q", got.status)
	}
}

func TestRepositoryTerminalCreatesStandaloneWorktreeAndOpensIt(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "galpon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan app.CreateWorktreeRequest, 1)
	workspace := model.Workspace{ID: "ws", Title: "Human fix"}
	worktree := model.Worktree{ID: "wt", WorkspaceID: workspace.ID, RepositoryID: "repo", Path: "/managed/human-fix", Branch: "galpon/human-fix", Lifecycle: "workspace"}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/worktrees":
			var request app.CreateWorktreeRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			requests <- request
			_ = json.NewEncoder(w).Encode(app.CreateWorktreeResult{Workspace: workspace, Worktree: worktree})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/ws/renderer":
			_ = json.NewEncoder(w).Encode(map[string]any{"saved": true})
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	renderer := &recordingRenderer{}
	m := New(app.NewClient(socket), renderer)
	m.dashboard = model.Dashboard{Repositories: []model.Repository{{ID: "repo", Title: "Galpon", DefaultRemote: "origin", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}}}}}
	if command := m.beginTerminal(searchResult{Kind: resultRepository, ID: "repo", Title: "Galpon"}, nil); command != nil || m.form != formWorktree {
		t.Fatalf("repository terminal did not open the worktree form: form=%d command=%v", m.form, command)
	}
	m.worktreeDraft.WorkspaceTitle = workspace.Title
	command := m.createWorktree()
	if command == nil || !m.busy {
		t.Fatal("worktree creation did not start")
	}
	message := command()
	request := <-requests
	if request.WorkspaceTitle != workspace.Title || request.RepositoryID != "repo" || request.Remote != "origin" || request.Ref != "main" || !request.FetchFirst {
		t.Fatalf("create request = %#v", request)
	}
	updated, quit := m.Update(message)
	got := updated.(Model)
	if !got.quitting || quit == nil || got.err != nil {
		t.Fatalf("successful terminal open = quitting=%v quit=%v err=%v", got.quitting, quit != nil, got.err)
	}
	if renderer.worktree.ID != worktree.ID || renderer.label != "Human fix · Galpon" {
		t.Fatalf("renderer target = %#v label=%q", renderer.worktree, renderer.label)
	}
}

func TestWorktreeFormKeepsStableWorkspaceAndDoesNotCancelWhileBusy(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 100, 30
	m.dashboard = model.Dashboard{
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon", DefaultRemote: "origin", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}}}},
		Workspaces:   []model.Workspace{{ID: "first", Title: "First"}, {ID: "second", Title: "Second"}},
	}
	m.beginWorktreeForm("repo", nil)
	m.changeWorktreeChoice(worktreeWorkspace, 1)
	if m.worktreeDraft.WorkspaceID != "first" {
		t.Fatalf("selected workspace = %q", m.worktreeDraft.WorkspaceID)
	}
	m.dashboard.Workspaces[0], m.dashboard.Workspaces[1] = m.dashboard.Workspaces[1], m.dashboard.Workspaces[0]
	if _, value := m.worktreeFieldDisplay(worktreeWorkspace, false); value != "First" {
		t.Fatalf("workspace changed after dashboard reorder: %q", value)
	}
	m.busy = true
	m.updateWorktreeForm(tea.KeyMsg{Type: tea.KeyEsc})
	if m.screen != screenForm || m.form != formWorktree {
		t.Fatalf("Esc canceled active creation: screen=%d form=%d", m.screen, m.form)
	}
	if view := m.View(); !strings.Contains(view, "creating worktree") {
		t.Fatalf("busy form footer did not show wait state:\n%s", view)
	}
}

func TestAgentFormCanForkOrShareSelectedStandaloneWorktree(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 100, 30
	m.dashboard = model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Human fix"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon", DefaultRemote: "origin", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}}}},
		Worktrees:    []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/human-fix", BaseRef: "refs/remotes/origin/main", SourceRemote: "origin", Lifecycle: "workspace"}},
	}
	m.beginAgentForm("ws", "wt")
	if m.agentDraft.Placement != 4 || m.agentDraft.SuggestedWorktreeID != "wt" || m.agentDraft.Share {
		t.Fatalf("selected placement draft = %#v", m.agentDraft)
	}
	view := m.View()
	for _, want := range []string{"Use selected worktree", "Galpon · galpon/human-fix", "Private forks"} {
		if !strings.Contains(view, want) {
			t.Fatalf("selected worktree form omitted %q:\n%s", want, view)
		}
	}
}

type recordingRenderer struct {
	worktree model.Worktree
	label    string
}

func (r *recordingRenderer) Name() string    { return "test" }
func (r *recordingRenderer) Context() string { return "test" }
func (r *recordingRenderer) OpenTerminal(_ context.Context, _ model.Workspace, worktree model.Worktree, label string, _ []string) (string, error) {
	r.worktree = worktree
	r.label = label
	return "renderer-workspace", nil
}
func (r *recordingRenderer) OpenAgent(context.Context, model.Workspace, model.Worktree, model.Agent, []string, bool) (string, string, bool, error) {
	return "", "", false, nil
}
func (r *recordingRenderer) CloseAgent(context.Context, model.Agent) error { return nil }
func (r *recordingRenderer) ReportAgent(context.Context, model.Agent, string, string) error {
	return nil
}

func TestSwitcherStartsInSearchModeAndActionKeysFilter(t *testing.T) {
	for _, input := range []string{"t", "e", "r", "R", "w", "a", "x", "q", "qraw", " "} {
		t.Run(input, func(t *testing.T) {
			m := New(nil, nil)
			if !m.query.Focused() || m.normalMode {
				t.Fatal("switcher did not start in search mode")
			}
			key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(input)}
			if input == " " {
				key.Type = tea.KeySpace
			}
			m.updateSwitcher(key)
			if got := m.query.Value(); got != input {
				t.Fatalf("query = %q, want %q", got, input)
			}
			if m.screen != screenSwitcher || m.busy || m.quitting {
				t.Fatalf("search key started an action: screen=%d busy=%v quitting=%v", m.screen, m.busy, m.quitting)
			}
		})
	}
}

func TestCtrlSpaceTogglesSwitcherNormalMode(t *testing.T) {
	m := New(nil, nil)
	m.query.SetValue("agent")
	m.refreshResults()

	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
	if !m.normalMode || m.query.Focused() || m.query.Value() != "agent" {
		t.Fatalf("normal mode = %v focused=%v query=%q", m.normalMode, m.query.Focused(), m.query.Value())
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
	if m.normalMode || !m.query.Focused() || m.query.Value() != "agent" {
		t.Fatalf("search mode = %v focused=%v query=%q", m.normalMode, m.query.Focused(), m.query.Value())
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.query.Value() != "agentq" || m.quitting {
		t.Fatalf("query after second toggle = %q, quitting=%v", m.query.Value(), m.quitting)
	}
}

func TestSwitcherNormalModeRunsActionsAndKeepsSelectionKeys(t *testing.T) {
	for _, normalMode := range []bool{false, true} {
		m := New(nil, nil)
		m.dashboard = model.Dashboard{Workspaces: []model.Workspace{{ID: "one", Title: "One"}, {ID: "two", Title: "Two"}}}
		m.refreshResults()
		if normalMode {
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
		}
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
		if m.cursor != 1 {
			t.Fatalf("normal mode %v: down cursor = %d", normalMode, m.cursor)
		}
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyUp})
		if m.cursor != 0 {
			t.Fatalf("normal mode %v: up cursor = %d", normalMode, m.cursor)
		}

		m = New(nil, nil)
		m.dashboard = model.Dashboard{Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}}}
		m.refreshResults()
		if normalMode {
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
		}
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
		if m.screen != screenForm || m.form != formRemote {
			t.Fatalf("normal mode %v: enter did not open selection: screen=%d form=%d", normalMode, m.screen, m.form)
		}
	}

	m := New(nil, nil)
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if m.screen != screenForm || m.form != formRepository {
		t.Fatalf("normal-mode action did not open repository form: screen=%d form=%d", m.screen, m.form)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.normalMode || m.query.Focused() {
		t.Fatalf("form return did not preserve normal mode: normal=%v focused=%v", m.normalMode, m.query.Focused())
	}
}

func TestCtrlNStartsAgentForSelectedWorkspaceInBothSwitcherModes(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Shortcut work"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon", DefaultBranch: "main"}},
		Agents: []model.Agent{{
			ID: "agent", WorkspaceID: "ws", Title: "Existing agent",
			Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "wt", Worktrees: []model.AgentWorktree{{WorktreeID: "wt", Position: 0, Mode: "share"}}},
		}},
		Worktrees: []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "shortcut"}},
	}
	for _, normalMode := range []bool{false, true} {
		for _, kind := range []resultKind{resultWorkspace, resultAgent, resultWorktree} {
			t.Run(fmt.Sprintf("normal=%v/%s", normalMode, kind), func(t *testing.T) {
				m := New(nil, nil)
				m.dashboard = dashboard
				m.refreshResults()
				for index, result := range m.results {
					if result.Kind == kind {
						m.cursor = index
						break
					}
				}
				if normalMode {
					m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
				}
				m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlN})
				if m.screen != screenForm || m.form != formAgent || m.formContext != "ws" {
					t.Fatalf("agent form = screen %d form %d workspace %q", m.screen, m.form, m.formContext)
				}
				if m.agentDraft.Name != "" || m.agentDraft.SuggestedWorktreeID != "" || m.agentDraft.Share {
					t.Fatalf("selection populated title or placement: %#v", m.agentDraft)
				}
				if m.agentFocus != 0 || m.formInput.Placeholder != "Agent name" {
					t.Fatalf("initial agent field = focus %d placeholder %q", m.agentFocus, m.formInput.Placeholder)
				}
			})
		}
	}
}

func TestCtrlNReplacesNextResultAndDownStillNavigates(t *testing.T) {
	newModel := func(normalMode bool) Model {
		m := New(nil, nil)
		m.dashboard = model.Dashboard{Workspaces: []model.Workspace{{ID: "one", Title: "One"}, {ID: "two", Title: "Two"}}}
		m.refreshResults()
		if normalMode {
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
		}
		return m
	}
	for _, normalMode := range []bool{false, true} {
		m := newModel(normalMode)
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlN})
		if m.screen != screenForm || m.form != formAgent || m.formContext != "one" || m.cursor != 0 {
			t.Fatalf("normal mode %v: Ctrl-N result = screen %d form %d workspace %q cursor %d", normalMode, m.screen, m.form, m.formContext, m.cursor)
		}
		m = newModel(normalMode)
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
		if m.screen != screenSwitcher || m.cursor != 1 {
			t.Fatalf("normal mode %v: Down result = screen %d cursor %d", normalMode, m.screen, m.cursor)
		}
	}
}

func TestCtrlOOpensSelectedAgentWorkspaceInBothSwitcherModes(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "first", Title: "First"}, {ID: "agent-workspace", Title: "Agent workspace"}},
		Agents:     []model.Agent{{ID: "agent", WorkspaceID: "agent-workspace", Title: "Selected agent"}},
	}
	for _, normalMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("normal=%v", normalMode), func(t *testing.T) {
			m := New(nil, nil)
			m.dashboard = dashboard
			m.refreshResults()
			for index, result := range m.results {
				if result.Kind == resultAgent && result.ID == "agent" {
					m.cursor = index
					m.results[index].WorkspaceID = "first"
					break
				}
			}
			if normalMode {
				m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
			}
			queryBefore := m.query.Value()
			command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlO})
			if command == nil || m.screen != screenOperations {
				t.Fatalf("Ctrl-O did not open Operations: command nil=%v screen=%d", command == nil, m.screen)
			}
			if m.operationsWorkspace != "agent-workspace" {
				t.Fatalf("Operations workspace = %q, want selected agent workspace", m.operationsWorkspace)
			}
			if m.query.Value() != queryBefore {
				t.Fatalf("Ctrl-O changed the search query to %q", m.query.Value())
			}
		})
	}
}

func TestCtrlOHandlesUnsafeSwitcherSelections(t *testing.T) {
	for _, normalMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("no-selection/normal=%v", normalMode), func(t *testing.T) {
			m := New(nil, nil)
			if normalMode {
				m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
			}
			if command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlO}); command != nil {
				t.Fatal("Ctrl-O returned a command without a selection")
			}
			if m.screen != screenSwitcher || m.status != "Select an agent to open Operations" {
				t.Fatalf("no-selection result = screen %d status %q", m.screen, m.status)
			}
		})

		for _, test := range []struct {
			name       string
			dashboard  model.Dashboard
			wantStatus string
		}{
			{
				name:       "non-agent",
				dashboard:  model.Dashboard{Workspaces: []model.Workspace{{ID: "workspace", Title: "Workspace"}}},
				wantStatus: "The selected item is not an agent. Select an agent to open Operations",
			},
			{
				name:       "missing-workspace",
				dashboard:  model.Dashboard{Agents: []model.Agent{{ID: "agent", Title: "Agent"}}},
				wantStatus: "The selected agent does not have an available workspace",
			},
			{
				name:       "invalid-workspace",
				dashboard:  model.Dashboard{Agents: []model.Agent{{ID: "agent", WorkspaceID: "missing", Title: "Agent"}}},
				wantStatus: "The selected agent does not have an available workspace",
			},
		} {
			t.Run(fmt.Sprintf("%s/normal=%v", test.name, normalMode), func(t *testing.T) {
				m := New(nil, nil)
				m.dashboard = test.dashboard
				m.refreshResults()
				if normalMode {
					m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
				}
				if command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlO}); command != nil {
					t.Fatal("Ctrl-O returned a command for an unsafe selection")
				}
				if m.screen != screenSwitcher || m.status != test.wantStatus {
					t.Fatalf("unsafe selection result = screen %d status %q", m.screen, m.status)
				}
			})
		}
	}
}

func TestCtrlODoesNotConflictWithNavigationOrNormalOperationsKey(t *testing.T) {
	newModel := func() Model {
		m := New(nil, nil)
		m.dashboard = model.Dashboard{
			Workspaces: []model.Workspace{{ID: "one", Title: "One"}, {ID: "two", Title: "Two"}},
			Agents:     []model.Agent{{ID: "agent", WorkspaceID: "two", Title: "Agent"}},
		}
		m.refreshResults()
		return m
	}

	m := newModel()
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	if m.screen != screenSwitcher || m.cursor != 1 {
		t.Fatalf("Down result = screen %d cursor %d", m.screen, m.cursor)
	}

	m = newModel()
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
	command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if command == nil || m.screen != screenOperations || m.operationsWorkspace != "two" {
		t.Fatalf("normal o result = command nil=%v screen %d workspace %q", command == nil, m.screen, m.operationsWorkspace)
	}
}

func TestCtrlNHandlesMissingOrInvalidWorkspace(t *testing.T) {
	for _, normalMode := range []bool{false, true} {
		t.Run(fmt.Sprintf("no-selection/normal=%v", normalMode), func(t *testing.T) {
			m := New(nil, nil)
			if normalMode {
				m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
			}
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlN})
			if m.screen != screenSwitcher || m.status != "Select an item that has an available workspace" {
				t.Fatalf("no-selection result = screen %d status %q", m.screen, m.status)
			}
		})
		for _, test := range []struct {
			name      string
			dashboard model.Dashboard
		}{
			{name: "invalid-workspace", dashboard: model.Dashboard{Agents: []model.Agent{{ID: "orphan", WorkspaceID: "missing", Title: "Orphan"}}}},
			{name: "repository", dashboard: model.Dashboard{Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}}}},
		} {
			t.Run(fmt.Sprintf("%s/normal=%v", test.name, normalMode), func(t *testing.T) {
				m := New(nil, nil)
				m.dashboard = test.dashboard
				m.refreshResults()
				if normalMode {
					m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
				}
				m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlN})
				if m.screen != screenSwitcher || m.status != "This item does not have an available workspace" {
					t.Fatalf("invalid-workspace result = screen %d status %q", m.screen, m.status)
				}
			})
		}
	}
}

func TestSwitcherFootersDescribeCurrentMode(t *testing.T) {
	searchFooter := switcherFooter(120, false)
	for _, want := range []string{"SEARCH", "ctrl+o", "operations", "ctrl+n", "new agent", "ctrl+space", "actions", "esc", "close"} {
		if !strings.Contains(searchFooter, want) {
			t.Fatalf("search footer omitted %q: %s", want, searchFooter)
		}
	}
	normalFooter := switcherFooter(120, true)
	for _, want := range []string{"NORMAL", "actions", "ctrl+o", "operations", "ctrl+n", "new agent", "ctrl+space", "search", "close"} {
		if !strings.Contains(normalFooter, want) {
			t.Fatalf("normal footer omitted %q: %s", want, normalFooter)
		}
	}
	for _, width := range []int{12, 24, 35, 36, 48, 60, 72, 80, 100, 120} {
		for _, normalMode := range []bool{false, true} {
			footer := switcherFooter(width, normalMode)
			if got := lipgloss.Width(footer); got != width {
				t.Errorf("mode normal=%v footer width = %d, want %d", normalMode, got, width)
			}
			if lines := lipgloss.Height(footer); lines != 1 {
				t.Errorf("mode normal=%v width=%d footer lines = %d, want 1: %q", normalMode, width, lines, footer)
			}
			if width < 24 {
				for _, want := range []string{"^N", "new", "^O", "ops"} {
					if !strings.Contains(footer, want) {
						t.Errorf("mode normal=%v width=%d compact footer omitted %q: %s", normalMode, width, want, footer)
					}
				}
				continue
			}
			if width < 35 {
				for _, want := range []string{"^N", "new", "^O", "operations"} {
					if !strings.Contains(footer, want) {
						t.Errorf("mode normal=%v width=%d compact footer omitted %q: %s", normalMode, width, want, footer)
					}
				}
				continue
			}
			wants := []string{"ctrl+n", "new agent", "ctrl+o", "operations"}
			if width >= 48 && width < 80 {
				wants = []string{"^N", "new", "^S", "repo", "^O", "operations"}
			}
			if width >= 80 {
				wants = []string{"tab", "expand", "ctrl+n", "new agent", "ctrl+s", "repository", "ctrl+o", "operations"}
			}
			for _, want := range wants {
				if !strings.Contains(footer, want) {
					t.Errorf("mode normal=%v width=%d footer omitted %q: %s", normalMode, width, want, footer)
				}
			}
		}
	}
}

func TestXSoftDeletesSelectedResultAndShowsCascade(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "galpon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/workspaces/ws" {
			http.NotFound(w, r)
			return
		}
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.DeletionResult{Kind: "workspace", ID: "ws", Hidden: model.ResourceCounts{Workspaces: 1, Worktrees: 2, Agents: 1}})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	m := New(app.NewClient(socket), nil)
	m.dashboard = model.Dashboard{Workspaces: []model.Workspace{{ID: "ws", Title: "Old feature"}}}
	m.refreshResults()
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlAt})
	command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if command == nil || !m.busy || m.status != "Hiding Old feature…" {
		t.Fatalf("delete did not start: busy=%v status=%q", m.busy, m.status)
	}
	message := command()
	if path := <-requests; path != "/v1/workspaces/ws" {
		t.Fatalf("delete path = %q", path)
	}
	updated, _ := m.Update(message)
	got := updated.(Model)
	if got.busy || got.err != nil || got.status != "Hidden Old feature and 3 dependent items" {
		t.Fatalf("delete result = busy=%v err=%v status=%q", got.busy, got.err, got.status)
	}
}

func TestBuildResultsGroupsAndSearchesTitlesOnly(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Search polish"}},
		Agents:       []model.Agent{{ID: "agent", WorkspaceID: "ws", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "wt", Worktrees: []model.AgentWorktree{{WorktreeID: "wt", Position: 0, Mode: "private"}}}, Title: "Implementer", Status: "running", LastError: "secret needle"}},
		Worktrees:    []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/search", Path: "/contains/needle"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Tailcall", SourcePath: "/private/needle", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}}}},
	}
	all := buildResults(dashboard, "")
	if len(all) != 4 {
		t.Fatalf("got %d results, want 4", len(all))
	}
	if all[0].Kind != resultAgent || all[1].Kind != resultWorkspace || all[2].Kind != resultWorktree || all[3].Kind != resultRepository {
		t.Fatalf("groups are not stable: %#v", all)
	}
	if got := buildResults(dashboard, "needle"); len(got) != 0 {
		t.Fatalf("private detail matched search: %#v", got)
	}
	if got := buildResults(dashboard, "impl"); len(got) != 2 || got[0].Kind != resultAgent || got[1].Kind != resultWorktree {
		t.Fatalf("owner-title fuzzy matches = %#v", got)
	}
}

func TestBuildResultsSearchesWorktreesByOwnerTitle(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Command center"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
		Worktrees:    []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/command-center/terminal-implementer-deadbeef/galpon-cafebabe"}},
		Agents: []model.Agent{{
			ID: "agent-deadbeef", WorkspaceID: "ws", Title: "Terminal implementer",
			Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "wt", Worktrees: []model.AgentWorktree{{WorktreeID: "wt", Position: 0, Mode: "private"}}},
		}},
	}
	var worktree searchResult
	for _, result := range buildResults(dashboard, "terminal implementer") {
		if result.Kind == resultWorktree {
			worktree = result
		}
	}
	if worktree.ID != "wt" || !strings.Contains(worktree.Title, "Terminal implementer") {
		t.Fatalf("owner-title worktree result = %#v", worktree)
	}
}

func TestBuildResultsSearchesReadableWorktreeBranch(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Command center"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
		Worktrees:    []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/command-center/feature-readable-branch/galpon-cafebabe"}},
	}
	for _, query := range []string{"readable branch", "readable-branch"} {
		results := buildResults(dashboard, query)
		if len(results) != 1 || results[0].Kind != resultWorktree || results[0].ID != "wt" {
			t.Fatalf("branch query %q results = %#v", query, results)
		}
	}
}

func TestBuildResultsDoesNotSearchWorktreePathsOrOpaqueIDs(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Safe workspace"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Safe repository"}},
		Worktrees:    []model.Worktree{{ID: "worktree-needle", WorkspaceID: "ws", RepositoryID: "repo", Path: "/private/path-needle", Branch: "galpon/safe-workspace/builder-a1b2c3d4/safe-repository-e5f60718"}},
		Agents: []model.Agent{{
			ID: "agent-opaque", WorkspaceID: "ws", Title: "Builder",
			Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "worktree-needle"},
		}},
	}
	for _, query := range []string{"path needle", "worktree needle", "agent opaque", "a1b2c3d4", "e5f60718"} {
		for _, result := range buildResults(dashboard, query) {
			if result.Kind == resultWorktree {
				t.Fatalf("private query %q matched worktree: %#v", query, result)
			}
		}
	}
	for _, result := range buildResults(dashboard, "") {
		if result.Kind == resultWorktree && (strings.Contains(result.Title, "a1b2c3d4") || strings.Contains(result.Detail, "e5f60718")) {
			t.Fatalf("worktree label exposes an opaque suffix: %#v", result)
		}
	}
}

func TestBuildResultsGivesSameRepositoryWorktreesDistinctLabels(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Command center"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
		Worktrees: []model.Worktree{
			{ID: "alpha-wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/command-center/alpha-deadbeef/galpon-11111111", CreatedAt: 1},
			{ID: "beta-wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/command-center/beta-cafebabe/galpon-22222222", CreatedAt: 2},
			{ID: "standalone-a", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/command-center/worktree-33333333/galpon-33333333", CreatedAt: 3},
			{ID: "standalone-b", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/command-center/worktree-44444444/galpon-44444444", CreatedAt: 4},
		},
		Agents: []model.Agent{
			{ID: "alpha", WorkspaceID: "ws", Title: "Alpha owner", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "alpha-wt"}},
			{ID: "beta", WorkspaceID: "ws", Title: "Beta owner", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "beta-wt"}},
		},
	}
	titles := make(map[string]string)
	for _, result := range buildResults(dashboard, "") {
		if result.Kind != resultWorktree {
			continue
		}
		if previous := titles[result.Title]; previous != "" {
			t.Fatalf("worktrees %q and %q have the same label %q", previous, result.ID, result.Title)
		}
		titles[result.Title] = result.ID
	}
	if len(titles) != 4 {
		t.Fatalf("distinct worktree labels = %#v", titles)
	}
}

func TestBuildResultsKeepsTwelveDuplicateWorktreesInCreationOrder(t *testing.T) {
	worktrees := make([]model.Worktree, 0, 12)
	for number := 12; number >= 1; number-- {
		worktrees = append(worktrees, model.Worktree{
			ID:           fmt.Sprintf("opaque-worktree-%02d", number),
			WorkspaceID:  "ws",
			RepositoryID: "repo",
			Branch:       fmt.Sprintf("galpon/command-center/worktree-%08x/galpon-%08x", number, number),
			CreatedAt:    int64(number),
		})
	}
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Command center"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
		Worktrees:    worktrees,
	}
	var got []searchResult
	for _, result := range buildResults(dashboard, "") {
		if result.Kind == resultWorktree {
			got = append(got, result)
		}
	}
	if len(got) != 12 {
		t.Fatalf("worktree count = %d, want 12", len(got))
	}
	for index, result := range got {
		number := index + 1
		wantID := fmt.Sprintf("opaque-worktree-%02d", number)
		if result.ID != wantID {
			t.Fatalf("worktree %d = %q, want %q", number, result.ID, wantID)
		}
		if wantSuffix := fmt.Sprintf(" · %d", number); !strings.HasSuffix(result.Title, wantSuffix) {
			t.Fatalf("worktree %d title = %q, want suffix %q", number, result.Title, wantSuffix)
		}
		if strings.Contains(result.Title, result.ID) || strings.Contains(result.Detail, result.ID) {
			t.Fatalf("worktree %d exposes opaque ID in its label: %#v", number, result)
		}
	}
}

func TestWorktreeSearchEnterOpensMatchedRealTerminal(t *testing.T) {
	renderer := &recordingRenderer{}
	m := New(nil, renderer)
	m.dashboard = model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Command center"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
		Worktrees: []model.Worktree{
			{ID: "first", WorkspaceID: "ws", RepositoryID: "repo", Path: "/managed/first", Branch: "galpon/command-center/first-branch/galpon-11111111"},
			{ID: "second", WorkspaceID: "ws", RepositoryID: "repo", Path: "/managed/second", Branch: "galpon/command-center/open-second/galpon-22222222"},
		},
	}
	m.query.SetValue("open second")
	m.refreshResults()
	if len(m.results) != 1 || m.results[0].Kind != resultWorktree || m.results[0].ID != "second" {
		t.Fatalf("matched results = %#v", m.results)
	}
	command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("Enter did not start terminal open")
	}
	message := command()
	if renderer.worktree.ID != "second" || renderer.worktree.Path != "/managed/second" {
		t.Fatalf("renderer worktree = %#v", renderer.worktree)
	}
	if result := message.(actionMsg); result.err != nil || !result.quit {
		t.Fatalf("terminal result = %#v", result)
	}
}

func TestSwitcherHidesAndExpandsDelegatedAgentsInline(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Feature"}},
		Agents: []model.Agent{
			{ID: "parent", WorkspaceID: "ws", Title: "Coordinator", Status: "idle"},
			{ID: "child", WorkspaceID: "ws", Title: "Reviewer", Status: "running", CreatedByAgentID: "parent", Presentation: "background"},
		},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
	}
	m := New(nil, nil)
	m.width, m.height, m.dashboard, m.loaded = 100, 30, dashboard, true
	m.refreshResults()
	view := m.View()
	if strings.Contains(view, "DELEGATED AGENTS") || strings.Contains(view, "Reviewer") || !strings.Contains(view, "🤖 1") {
		t.Fatalf("collapsed delegated agents are not represented by the parent badge:\n%s", view)
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	view = m.View()
	if !strings.Contains(view, "Reviewer") || strings.Index(view, "Reviewer") < strings.Index(view, "Coordinator") {
		t.Fatalf("delegated agent did not expand below its parent:\n%s", view)
	}
	m.query.SetValue("review")
	m.refreshResults()
	if len(m.results) != 1 || m.results[0].ID != "child" {
		t.Fatalf("search did not return delegated result: %#v", m.results)
	}
}

func TestSwitcherSortsAgentsByActivityAndCollapsesOlderItems(t *testing.T) {
	now := time.Now()
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Feature"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
		Agents: []model.Agent{
			{ID: "older", WorkspaceID: "ws", Title: "Older active", UpdatedAt: now.Add(-2 * time.Hour).UnixMilli()},
			{ID: "newer", WorkspaceID: "ws", Title: "Newer active", UpdatedAt: now.Add(-time.Minute).UnixMilli()},
			{ID: "stale-agent", WorkspaceID: "ws", Title: "Dormant agent", UpdatedAt: now.Add(-30 * 24 * time.Hour).UnixMilli()},
		},
		Worktrees: []model.Worktree{
			{ID: "recent-wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "recent", CreatedAt: now.Add(-time.Hour).UnixMilli()},
			{ID: "stale-wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "old", CreatedAt: now.Add(-30 * 24 * time.Hour).UnixMilli()},
		},
	}
	m := New(nil, nil)
	m.width, m.height, m.dashboard, m.loaded = 110, 36, dashboard, true
	m.refreshResults()
	if len(m.results) < 2 || m.results[0].ID != "newer" || m.results[1].ID != "older" {
		t.Fatalf("recent agent order = %#v", m.results)
	}
	view := m.View()
	for _, hidden := range []string{"Dormant agent", "Feature · Galpon · old"} {
		if strings.Contains(view, hidden) {
			t.Fatalf("stale item %q is visible before expansion:\n%s", hidden, view)
		}
	}
	for _, want := range []string{"Older agents", "1 inactive", "Older worktrees"} {
		if !strings.Contains(view, want) {
			t.Fatalf("collapsed stale groups omitted %q:\n%s", want, view)
		}
	}
	for index, result := range m.results {
		if result.Disclosure == "older-agents" {
			m.cursor = index
			break
		}
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	if view := m.View(); !strings.Contains(view, "Dormant agent") || !strings.Contains(view, "tab to collapse") {
		t.Fatalf("older agents did not expand:\n%s", view)
	}
	m.query.SetValue("dormant")
	m.refreshResults()
	if len(m.results) != 1 || m.results[0].ID != "stale-agent" {
		t.Fatalf("search incorrectly applied stale collapse: %#v", m.results)
	}
}

func TestSwitcherPreservesSelectedAgentAcrossActivityReorder(t *testing.T) {
	m := New(nil, nil)
	now := time.Now().UnixMilli()
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Feature"}},
		Agents: []model.Agent{
			{ID: "first", WorkspaceID: "ws", Title: "First", UpdatedAt: now},
			{ID: "second", WorkspaceID: "ws", Title: "Second", UpdatedAt: now - 2},
		},
	}
	m.refreshResults()
	m.cursor = 1
	m.dashboard.Agents[1].UpdatedAt = now + 1
	m.refreshResults()
	if m.cursor != 0 || m.results[m.cursor].ID != "second" {
		t.Fatalf("selection moved after activity reorder: cursor=%d results=%#v", m.cursor, m.results)
	}
}

func TestRecentDelegatedActivityKeepsParentOutOfOlderGroup(t *testing.T) {
	now := time.Now()
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Feature"}},
		Agents: []model.Agent{
			{ID: "parent", WorkspaceID: "ws", Title: "Coordinator", UpdatedAt: now.Add(-30 * 24 * time.Hour).UnixMilli()},
			{ID: "child", WorkspaceID: "ws", Title: "Active worker", Presentation: "background", CreatedByAgentID: "parent", UpdatedAt: now.UnixMilli()},
		},
	}
	m := New(nil, nil)
	m.dashboard = dashboard
	m.refreshResults()
	if len(m.results) == 0 || m.results[0].ID != "parent" || m.results[0].Disclosure != "" {
		t.Fatalf("active delegated work left parent in older group: %#v", m.results)
	}
}

func TestSearchUsesRecentActivityToBreakEqualAgentRank(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Feature"}},
		Agents: []model.Agent{
			{ID: "old", WorkspaceID: "ws", Title: "Builder", UpdatedAt: 10},
			{ID: "new", WorkspaceID: "ws", Title: "Builder", UpdatedAt: 20},
		},
	}
	results := buildResults(dashboard, "builder")
	if len(results) != 2 || results[0].ID != "new" || results[1].ID != "old" {
		t.Fatalf("activity tie-break order = %#v", results)
	}
}

func TestBuildResultsSortsAgentsByTitleAcrossWorkspaces(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{
			{ID: "zulu", Title: "Zulu workspace"},
			{ID: "alpha", Title: "Alpha workspace"},
		},
		Agents: []model.Agent{
			{ID: "zulu-b", WorkspaceID: "zulu", Title: "Beta"},
			{ID: "alpha-z", WorkspaceID: "alpha", Title: "Zebra"},
			{ID: "zulu-a", WorkspaceID: "zulu", Title: "Able"},
			{ID: "alpha-a", WorkspaceID: "alpha", Title: "Apple"},
		},
	}
	var got []string
	for _, result := range buildResults(dashboard, "") {
		if result.Kind == resultAgent {
			got = append(got, result.ID)
		}
	}
	want := []string{"zulu-a", "alpha-a", "zulu-b", "alpha-z"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("agent order = %v, want %v", got, want)
	}
}

func TestFuzzyScoreUsesTheBestContiguousOccurrence(t *testing.T) {
	word, ok := fuzzyScore("reimage image", "image")
	if !ok {
		t.Fatal("word occurrence did not match")
	}
	middle, ok := fuzzyScore("reimagetool", "image")
	if !ok {
		t.Fatal("middle occurrence did not match")
	}
	if word <= middle {
		t.Fatalf("word score %d did not beat middle score %d", word, middle)
	}
}

func TestBuildResultsRanksAgentTitleMatchesAcrossWorkspaces(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "alpha", Title: "Alpha"}, {ID: "zulu", Title: "Zulu"}},
		Agents: []model.Agent{
			{ID: "fuzzy", WorkspaceID: "alpha", Title: "Important migration agent helper executor"},
			{ID: "substring", WorkspaceID: "alpha", Title: "Reimagetool"},
			{ID: "word", WorkspaceID: "alpha", Title: "Review image worker"},
			{ID: "prefix", WorkspaceID: "zulu", Title: "Image worker"},
			{ID: "exact", WorkspaceID: "zulu", Title: "Image"},
		},
	}
	var got []string
	for _, result := range buildResults(dashboard, "image") {
		if result.Kind == resultAgent {
			got = append(got, result.ID)
		}
	}
	want := []string{"exact", "prefix", "word", "substring", "fuzzy"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ranked agent matches = %v, want %v", got, want)
	}
}

func TestSwitcherShowsOneAgentGroupWithInlineWorkspaceContext(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{
			{ID: "zulu", Title: "Zulu workspace"},
			{ID: "alpha", Title: "Alpha workspace"},
		},
		Agents: []model.Agent{
			{ID: "zulu-b", WorkspaceID: "zulu", Title: "Beta agent", Status: "idle"},
			{ID: "alpha-z", WorkspaceID: "alpha", Title: "Zebra agent", Status: "idle"},
			{ID: "zulu-a", WorkspaceID: "zulu", Title: "Able agent", Status: "idle"},
			{ID: "alpha-a", WorkspaceID: "alpha", Title: "Apple agent", Status: "idle"},
		},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
	}
	view := Snapshot(dashboard, 100, 30)
	if strings.Contains(view, "AGENTS  ·") || strings.Count(view, "AGENTS") != 1 {
		t.Fatalf("switcher did not use one agent group:\n%s", view)
	}
	for _, want := range []string{"Able agent", "Apple agent", "Beta agent", "Zebra agent", "[Alpha workspace]", "[Zulu workspace]", "idle"} {
		if !strings.Contains(view, want) {
			t.Fatalf("agent list omitted %q:\n%s", want, view)
		}
	}

	m := New(nil, nil)
	m.width, m.height = 100, 12
	m.dashboard = dashboard
	m.loaded = true
	m.refreshResults()
	for index, result := range m.results {
		if result.ID == "zulu-b" {
			m.cursor = index
			break
		}
	}
	smallView := m.View()
	for _, want := range []string{"AGENTS", "Beta agent", "Zulu workspace"} {
		if !strings.Contains(smallView, want) {
			t.Fatalf("selected agent context omitted %q:\n%s", want, smallView)
		}
	}
}

func TestSwitcherAgentStatesAreDistinctAndUseTheStatePalette(t *testing.T) {
	now := time.Now().UnixMilli()
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Feature"}},
		Agents: []model.Agent{
			{ID: "working", WorkspaceID: "ws", Title: "Working", Status: "running", CreatedAt: now, UpdatedAt: now},
			{ID: "active", WorkspaceID: "ws", Title: "Active", Status: "idle", RendererID: "pane", CreatedAt: now, UpdatedAt: now + 1},
			{ID: "changed", WorkspaceID: "ws", Title: "Changed", Status: "idle", CreatedAt: now, UpdatedAt: now + 1},
			{ID: "idle", WorkspaceID: "ws", Title: "Idle", Status: "idle", CreatedAt: now, UpdatedAt: now},
			{ID: "failed", WorkspaceID: "ws", Title: "Failed", Status: "failed", CreatedAt: now, UpdatedAt: now + 1},
		},
	}
	states := make(map[string]agentSwitcherState)
	for _, result := range buildResults(dashboard, "") {
		if result.Kind == resultAgent {
			states[result.ID] = result.AgentState
		}
	}
	want := map[string]agentSwitcherState{"working": agentStateWorking, "active": agentStateActive, "changed": agentStateChanged, "idle": agentStateIdle, "failed": agentStateFailed}
	for id, state := range want {
		if states[id] != state {
			t.Errorf("agent %s state = %q, want %q", id, states[id], state)
		}
	}

	lipgloss.SetColorProfile(termenv.TrueColor)
	background := Tokyo.Surface
	markers := map[agentSwitcherState]string{
		agentStateWorking: lipgloss.NewStyle().Foreground(Tokyo.Yellow).Background(background).Bold(true).Render("◐ "),
		agentStateChanged: lipgloss.NewStyle().Foreground(Tokyo.Blue).Background(background).Bold(true).Render("● "),
		agentStateActive:  lipgloss.NewStyle().Foreground(Tokyo.Green).Background(background).Bold(true).Render("● "),
		agentStateIdle:    lipgloss.NewStyle().Foreground(Tokyo.Comment).Background(background).Bold(true).Render("○ "),
		agentStateFailed:  lipgloss.NewStyle().Foreground(Tokyo.Red).Background(background).Bold(true).Render("× "),
	}
	for state, marker := range markers {
		if got := switcherAgentMarker(state, background); got != marker {
			t.Errorf("agent %s marker = %q, want %q", state, got, marker)
		}
	}
}

func TestNeovimMoonPaletteMatchesActiveConfiguration(t *testing.T) {
	colors := map[string]struct {
		got  lipgloss.Color
		want string
	}{
		"normal": {Tokyo.Background, "#222436"}, "telescope": {Tokyo.Surface, "#1e2030"},
		"prompt": {Tokyo.Prompt, "#2d3149"}, "selection": {Tokyo.Selection, "#2d3f76"},
		"foreground": {Tokyo.Foreground, "#c8d3f5"}, "muted": {Tokyo.Muted, "#9aa5ce"},
		"status": {Tokyo.Status, "#7aa2f7"}, "blue": {Tokyo.Blue, "#82aaff"},
		"cyan": {Tokyo.Cyan, "#65bcff"}, "purple": {Tokyo.Purple, "#c099ff"},
		"green": {Tokyo.Green, "#c3e88d"}, "orange": {Tokyo.Orange, "#ff966c"},
		"red": {Tokyo.Red, "#ff757f"}, "yellow": {Tokyo.Yellow, "#ffc777"}, "teal": {Tokyo.Teal, "#4fd6be"},
	}
	for name, color := range colors {
		if string(color.got) != color.want {
			t.Errorf("%s = %s, want %s", name, color.got, color.want)
		}
	}
}

func TestNeovimMoonSnapshotHasFlatClearGroups(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	dashboard := model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Visual polish"}},
		Agents:       []model.Agent{{ID: "agent", WorkspaceID: "ws", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "wt", Worktrees: []model.AgentWorktree{{WorktreeID: "wt", Position: 0, Mode: "private"}}}, Title: "Designer", Status: "running"}},
		Worktrees:    []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Branch: "galpon/visual"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
	}
	view := Snapshot(dashboard, 100, 30)
	for _, text := range []string{"GALPÓN", "WORKSPACES", "AGENTS", "WORKTREES", "REPOSITORIES", "Visual polish", "Designer"} {
		if !strings.Contains(view, text) {
			t.Errorf("snapshot omitted %q", text)
		}
	}
	if !strings.Contains(view, "\x1b[") {
		t.Fatal("snapshot has no ANSI color")
	}
	for _, roundedBorder := range []string{"╭", "╮", "╰", "╯"} {
		if strings.Contains(view, roundedBorder) {
			t.Fatalf("switcher still contains rounded card border %q", roundedBorder)
		}
	}
}

func TestFuzzyScoreRewardsConsecutiveMatches(t *testing.T) {
	consecutive, ok := fuzzyScore("Agent runner", "agent")
	if !ok {
		t.Fatal("expected match")
	}
	scattered, ok := fuzzyScore("A green engine now turns", "agent")
	if !ok {
		t.Fatal("expected scattered match")
	}
	if consecutive <= scattered {
		t.Fatalf("consecutive score %d <= scattered %d", consecutive, scattered)
	}
}

func TestCtrlNShowsChangeableWorkspaceAndCopiesSourceRepositoryConfig(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 110, 36
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "current", Title: "Current work"}, {ID: "other", Title: "Other work"}},
		Repositories: []model.Repository{
			{ID: "first", Title: "First", DefaultRemote: "origin", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}}},
			{ID: "source", Title: "Source", DefaultRemote: "upstream", DefaultBranch: "trunk", Remotes: []model.RepositoryRemote{{Name: "origin"}, {Name: "upstream"}}},
		},
		Worktrees: []model.Worktree{{ID: "source-wt", WorkspaceID: "current", RepositoryID: "source", SourceRemote: "upstream", BaseRef: "refs/remotes/upstream/feature"}},
		Agents:    []model.Agent{{ID: "current-agent", WorkspaceID: "current", Title: "Current agent", Placement: model.AgentPlacement{PrimaryWorktreeID: "source-wt"}}},
	}
	m.refreshResults()
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlN})
	if m.form != formAgent || m.agentDraft.WorkspaceID != "current" {
		t.Fatalf("Ctrl-N workspace draft = %#v", m.agentDraft)
	}
	draft := m.agentDraft.Worktrees[0]
	if draft.Repository != 1 || draft.Remote != 1 || draft.Ref != "feature" || m.agentDraft.Placement != 0 {
		t.Fatalf("Ctrl-N source defaults = %#v", m.agentDraft)
	}
	if view := m.View(); !strings.Contains(view, "Current work") || !strings.Contains(view, "Source") {
		t.Fatalf("agent form does not show selected workspace and repository:\n%s", view)
	}
	for index, field := range m.agentFields() {
		if field.Kind == agentWorkspace {
			m.agentFocus = index
			break
		}
	}
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyTab})
	if !m.choice.Open || len(m.choice.Options) != 2 {
		t.Fatalf("workspace list did not open: %#v", m.choice)
	}
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyDown})
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyEnter})
	if m.agentDraft.WorkspaceID != "other" || m.formContext != "other" {
		t.Fatalf("workspace list selection = %q / %q", m.agentDraft.WorkspaceID, m.formContext)
	}
}

func TestCtrlNUsesSelectedWorktreeSourceWithoutCopyingPlacement(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Work"}},
		Repositories: []model.Repository{
			{ID: "first", Title: "First", DefaultRemote: "origin", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}}},
			{ID: "selected", Title: "Selected", DefaultRemote: "upstream", DefaultBranch: "trunk", Remotes: []model.RepositoryRemote{{Name: "origin"}, {Name: "upstream"}}},
		},
		Worktrees: []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "selected", SourceRemote: "upstream", BaseRef: "refs/remotes/upstream/topic", Branch: "topic"}},
	}
	m.refreshResults()
	for index, result := range m.results {
		if result.Kind == resultWorktree {
			m.cursor = index
			break
		}
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlN})
	draft := m.agentDraft.Worktrees[0]
	if m.form != formAgent || draft.Repository != 1 || draft.Remote != 1 || draft.Ref != "topic" || m.agentDraft.Placement != 0 || m.agentDraft.SuggestedWorktreeID != "" {
		t.Fatalf("worktree Ctrl-N defaults = %#v", m.agentDraft)
	}
}

func TestSourceWorktreeUsesDefaultRemoteWhenSourceRemoteIsMissing(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Work"}},
		Repositories: []model.Repository{{ID: "repo", Title: "Repo", DefaultRemote: "upstream", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}, {Name: "upstream"}}}},
		Worktrees:    []model.Worktree{{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo"}},
	}
	m.beginAgentFormWithSource("ws", "", "", "wt")
	if m.agentDraft.Worktrees[0].Remote != 1 {
		t.Fatalf("default remote index = %d", m.agentDraft.Worktrees[0].Remote)
	}
}

func TestTabOpensRepositoryAndRemoteChoiceLists(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Work"}},
		Repositories: []model.Repository{
			{ID: "one", Title: "One", DefaultRemote: "origin", Remotes: []model.RepositoryRemote{{Name: "origin"}}},
			{ID: "two", Title: "Two", DefaultRemote: "upstream", Remotes: []model.RepositoryRemote{{Name: "origin"}, {Name: "upstream"}}},
		},
	}
	m.beginAgentForm("ws", "")
	for index, field := range m.agentFields() {
		if field.Kind == agentRepository {
			m.agentFocus = index
			break
		}
	}
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyTab})
	if !m.choice.Open || m.choice.Title != "Select repository" || len(m.choice.Options) != 2 {
		t.Fatalf("repository list = %#v", m.choice)
	}
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyDown})
	m.dashboard.Repositories[0], m.dashboard.Repositories[1] = m.dashboard.Repositories[1], m.dashboard.Repositories[0]
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyEnter})
	if m.agentDraft.Worktrees[0].Repository != 0 || m.agentDraft.Worktrees[0].Remote != 1 || m.dashboard.Repositories[m.agentDraft.Worktrees[0].Repository].ID != "two" {
		t.Fatalf("repository selection did not survive dashboard reorder: %#v", m.agentDraft.Worktrees[0])
	}
	for index, field := range m.agentFields() {
		if field.Kind == agentRemote {
			m.agentFocus = index
			break
		}
	}
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyTab})
	if !m.choice.Open || m.choice.Title != "Select source remote" || len(m.choice.Options) != 2 {
		t.Fatalf("remote list = %#v", m.choice)
	}
}

func TestDashboardRemovalInvalidatesFormRepositorySelections(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Work"}},
		Repositories: []model.Repository{{ID: "keep", Title: "Keep"}, {ID: "remove", Title: "Remove"}},
	}
	m.beginAgentForm("ws", "")
	m.agentDraft.Worktrees[0].Repository = 1
	m.replaceDashboard(model.Dashboard{Workspaces: m.dashboard.Workspaces, Repositories: []model.Repository{{ID: "keep", Title: "Keep"}}})
	if m.agentDraft.Worktrees[0].Repository != -1 {
		t.Fatalf("removed agent repository index = %d", m.agentDraft.Worktrees[0].Repository)
	}
	m.formInput.SetValue("Builder")
	if command := m.createAgent(); command != nil || m.err == nil || !strings.Contains(m.err.Error(), "not available") {
		t.Fatalf("removed agent repository create result: command=%v error=%v", command != nil, m.err)
	}
	if view := m.View(); !strings.Contains(view, "No longer available") {
		t.Fatalf("agent form did not show removed repository:\n%s", view)
	}

	m.dashboard = model.Dashboard{Repositories: []model.Repository{{ID: "keep", Title: "Keep"}, {ID: "remove", Title: "Remove"}}}
	m.beginRemoteForm(searchResult{Kind: resultRepository, ID: "remove"})
	m.replaceDashboard(model.Dashboard{Repositories: []model.Repository{{ID: "keep", Title: "Keep"}}})
	if m.remoteDraft.Repository != -1 {
		t.Fatalf("removed remote repository index = %d", m.remoteDraft.Repository)
	}
	m.remoteDraft.Name, m.remoteDraft.FetchURL = "fork", "git@example.com:fork/repo"
	if command := m.createRemote(); command != nil || m.err == nil || !strings.Contains(m.err.Error(), "required") {
		t.Fatalf("removed remote repository create result: command=%v error=%v", command != nil, m.err)
	}
	if view := m.View(); !strings.Contains(view, "No longer available") {
		t.Fatalf("remote form did not show removed repository:\n%s", view)
	}
}

func TestDisclosureRowsRejectActionModeCommands(t *testing.T) {
	m := New(nil, nil)
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Work"}},
		Agents:     []model.Agent{{ID: "old", WorkspaceID: "ws", Title: "Old", UpdatedAt: time.Now().Add(-30 * 24 * time.Hour).UnixMilli()}},
	}
	m.refreshResults()
	m.normalMode = true
	for _, key := range []rune{'t', 'e', 'x'} {
		m.status, m.busy = "", false
		command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
		if command != nil || m.busy || m.screen != screenSwitcher || !strings.Contains(m.status, "Expand this group") {
			t.Fatalf("disclosure command %q: command=%v busy=%v screen=%d status=%q", key, command != nil, m.busy, m.screen, m.status)
		}
	}
}

func TestCtrlSOpensRepositoryPrompt(t *testing.T) {
	m := New(nil, nil)
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.screen != screenForm || m.form != formRepository {
		t.Fatalf("Ctrl-S route = screen %d form %d", m.screen, m.form)
	}
}

func TestAgentFormShowsIndependentContextAndPlacementWithSecondaries(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 110, 36
	m.dashboard = model.Dashboard{
		Repositories: []model.Repository{
			{ID: "repo", Title: "Dagger", DefaultRemote: "origin", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}, {Name: "matipan"}}},
			{ID: "docs", Title: "Docs", DefaultRemote: "origin", DefaultBranch: "main", Remotes: []model.RepositoryRemote{{Name: "origin"}}},
		},
		Workspaces: []model.Workspace{{ID: "ws", Title: "Three implementations"}},
		Agents:     []model.Agent{{ID: "source", WorkspaceID: "ws", Title: "Coordinator", SessionPath: "/session.jsonl", Status: "idle", Placement: model.AgentPlacement{Type: "none", CWD: "/tmp"}}},
	}
	m.beginAgentForm("ws", "")
	view := m.View()
	for _, want := range []string{"IDENTITY", "CONTEXT", "PLACEMENT", "WORKTREES", "Fresh", "New private worktrees", "Add secondary repository"} {
		if !strings.Contains(view, want) {
			t.Fatalf("agent form omitted %q:\n%s", want, view)
		}
	}
	fields := m.agentFields()
	for index, field := range fields {
		if field.Kind == agentAddWorktree {
			m.agentFocus = index
			break
		}
	}
	m.updateAgentForm(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.agentDraft.Worktrees) != 2 {
		t.Fatalf("secondary count = %d", len(m.agentDraft.Worktrees))
	}
}

func TestTerminalSelectionUsesAgentPlacement(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 100, 30
	agent := model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Implementation A", Placement: model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "primary", Worktrees: []model.AgentWorktree{{WorktreeID: "primary", Position: 0, Mode: "private"}, {WorktreeID: "secondary", Position: 1, Mode: "private"}}}}
	m.dashboard = model.Dashboard{
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Feature"}},
		Agents:       []model.Agent{agent},
		Repositories: []model.Repository{{ID: "repo", Title: "Dagger"}, {ID: "docs", Title: "Docs"}},
		Worktrees: []model.Worktree{
			{ID: "primary", WorkspaceID: "ws", RepositoryID: "repo", Branch: "implementation-a", Path: "/tmp/a"},
			{ID: "secondary", WorkspaceID: "ws", RepositoryID: "docs", Branch: "implementation-a-docs", Path: "/tmp/docs"},
		},
	}
	m.beginTerminal(searchResult{Kind: resultAgent, ID: agent.ID, WorkspaceID: "ws"}, nil)
	if m.screen != screenTerminal || len(m.terminalTargets) != 2 {
		t.Fatalf("terminal selection = screen %d targets %#v", m.screen, m.terminalTargets)
	}
	view := m.View()
	for _, want := range []string{"OPEN TERMINAL", "Implementation A · Dagger", "Implementation A · Docs", "primary", "secondary"} {
		if !strings.Contains(strings.ToUpper(view), strings.ToUpper(want)) {
			t.Fatalf("terminal form omitted %q:\n%s", want, view)
		}
	}
}

func TestRemoteFormCanTargetSelectedPlacementRepository(t *testing.T) {
	m := New(nil, nil)
	m.width, m.height = 100, 30
	m.dashboard = model.Dashboard{
		Repositories: []model.Repository{{ID: "repo", Title: "Dagger"}, {ID: "docs", Title: "Docs"}},
		Worktrees:    []model.Worktree{{ID: "wt", RepositoryID: "docs"}},
	}
	m.beginRemoteForm(searchResult{WorktreeID: "wt"})
	if m.remoteDraft.Repository != 1 {
		t.Fatalf("repository index = %d", m.remoteDraft.Repository)
	}
	for _, want := range []string{"ADD REMOTE", "Docs", "Fetch URL", "Default push"} {
		if view := m.View(); !strings.Contains(strings.ToUpper(view), strings.ToUpper(want)) {
			t.Fatalf("remote form omitted %q:\n%s", want, view)
		}
	}
}
