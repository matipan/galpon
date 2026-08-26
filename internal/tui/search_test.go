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

func TestSwitcherFootersDescribeCurrentMode(t *testing.T) {
	searchFooter := switcherFooter(120, false)
	for _, want := range []string{"SEARCH", "ctrl+space", "actions", "esc", "close"} {
		if !strings.Contains(searchFooter, want) {
			t.Fatalf("search footer omitted %q: %s", want, searchFooter)
		}
	}
	normalFooter := switcherFooter(120, true)
	for _, want := range []string{"NORMAL", "actions", "ctrl+space", "search", "close"} {
		if !strings.Contains(normalFooter, want) {
			t.Fatalf("normal footer omitted %q: %s", want, normalFooter)
		}
	}
	for _, width := range []int{80, 100, 120} {
		for _, normalMode := range []bool{false, true} {
			if got := lipgloss.Width(switcherFooter(width, normalMode)); got > width {
				t.Errorf("mode normal=%v footer width = %d, want at most %d", normalMode, got, width)
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
	if all[0].Kind != resultWorkspace || all[1].Kind != resultAgent || all[2].Kind != resultWorktree || all[3].Kind != resultRepository {
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

func TestBuildResultsPlacesDelegatedAgentsInBottomSection(t *testing.T) {
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Feature"}},
		Agents: []model.Agent{
			{ID: "parent", WorkspaceID: "ws", Title: "Coordinator", Status: "idle"},
			{ID: "child", WorkspaceID: "ws", Title: "Reviewer", Status: "running", CreatedByAgentID: "parent", Presentation: "background"},
		},
		Repositories: []model.Repository{{ID: "repo", Title: "Galpon"}},
	}
	results := buildResults(dashboard, "")
	if len(results) != 4 || results[len(results)-1].ID != "child" || !results[len(results)-1].Delegated || results[len(results)-1].Kind != resultAgent {
		t.Fatalf("delegated result order = %#v", results)
	}
	view := Snapshot(dashboard, 100, 30)
	repositories := strings.Index(view, "REPOSITORIES")
	delegated := strings.Index(view, "DELEGATED AGENTS")
	child := strings.Index(view, "Reviewer")
	if repositories < 0 || delegated <= repositories || child <= delegated {
		t.Fatalf("delegated section is not last:\n%s", view)
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
	for _, want := range []string{"Able agent", "Apple agent", "Beta agent", "Zebra agent", "Alpha workspace  ·  idle", "Zulu workspace  ·  idle"} {
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
