package tui

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

// openWorkspaceScope selects a workspace in the Workspaces scope and shows
// its agents with Tab.
func openWorkspaceScope(t *testing.T, m *Model, id string) {
	t.Helper()
	m.enterScope(listScope{Kind: scopeWorkspaces, Title: "Workspaces"})
	for index, result := range m.results {
		if result.Kind == resultWorkspace && result.ID == id {
			m.cursor = index
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
			if m.scope == nil || m.scope.Kind != scopeWorkspace || m.scope.ID != id {
				t.Fatalf("Tab on workspace %q opened %#v", id, m.scope)
			}
			return
		}
	}
	t.Fatalf("workspace %q is not visible", id)
}

func TestTabOnAWorkspaceShowsItsAgentsByActivity(t *testing.T) {
	now := time.Now().UnixMilli()
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Project"}, {ID: "other", Title: "Other project"}},
		Agents: []model.Agent{
			{ID: "old", WorkspaceID: "ws", Title: "Dormant", UpdatedAt: now - (30 * 24 * time.Hour).Milliseconds()},
			{ID: "tie-b", WorkspaceID: "ws", Title: "Beta", UpdatedAt: now - 1},
			{ID: "other-agent", WorkspaceID: "other", Title: "Elsewhere", UpdatedAt: now + 10},
			{ID: "tie-a", WorkspaceID: "ws", Title: "Alpha", UpdatedAt: now - 1},
			{ID: "new", WorkspaceID: "ws", Title: "Newest", UpdatedAt: now},
		},
	}
	// The workspace shows from the Workspaces scope and from a search.
	for _, query := range []string{"", "project"} {
		m := New(nil, nil)
		m.dashboard = dashboard
		m.refreshResults()
		if query == "" {
			openWorkspaceScope(t, &m, "ws")
		} else {
			m.query.SetValue(query)
			m.refreshResults()
			for index, result := range m.results {
				if result.Kind == resultWorkspace && result.ID == "ws" {
					m.cursor = index
				}
			}
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
		}
		if got, want := searchCategoryIDs(m.results, resultAgent), []string{"new", "tie-a", "tie-b", "old"}; !slices.Equal(got, want) {
			t.Fatalf("query %q: workspace agents = %v, want %v", query, got, want)
		}
		if last := m.results[len(m.results)-1]; last.Kind != resultLaunch || last.WorkspaceID != "ws" {
			t.Fatalf("query %q: the last row = %#v, want a new agent in the workspace", query, last)
		}
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEsc})
		if m.scope != nil || m.quitting || m.query.Value() != "" {
			t.Fatalf("query %q: Esc left scope %#v, quitting %v, query %q", query, m.scope, m.quitting, m.query.Value())
		}
	}
}

func TestWorkspaceScopeSearchesItsAgentsAndBackspaceLeaves(t *testing.T) {
	m := searchResultsModel(3)
	m.dashboard.Agents[2].WorkspaceID = "ws-01"
	m.refreshResults()
	openWorkspaceScope(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("01"), Paste: true})
	if got := searchCategoryIDs(m.results, resultAgent); !slices.Equal(got, []string{"agent-01"}) {
		t.Fatalf("scope search = %v, want only the matching agent of the workspace", got)
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlU})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.scope != nil {
		t.Fatalf("Backspace in an empty search kept the scope %#v", m.scope)
	}
}

func TestWorkspaceScopeRefreshKeepsTheSelectedAgent(t *testing.T) {
	m := searchResultsModel(5)
	openWorkspaceScope(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	selected := m.results[m.cursor].ID
	m.dashboard.Agents[4].UpdatedAt += 1000
	updated, _ := m.Update(dashboardMsg{value: m.dashboard})
	m = updated.(Model)
	if m.results[m.cursor].ID != selected {
		t.Fatalf("a refresh moved the selection from %s to %s", selected, m.results[m.cursor].ID)
	}
	m.dashboard.Agents = slices.DeleteFunc(m.dashboard.Agents, func(agent model.Agent) bool { return agent.ID == selected })
	updated, _ = m.Update(dashboardMsg{value: m.dashboard})
	m = updated.(Model)
	if m.cursor < 0 || m.cursor >= len(m.results) {
		t.Fatalf("removing the selected agent left the cursor at %d of %d", m.cursor, len(m.results))
	}
}

func TestWorkspaceScopeIncludesDelegatedAgentsOnce(t *testing.T) {
	m := searchResultsModel(2)
	m.dashboard.Agents[1].CreatedByAgentID = "agent-00"
	m.dashboard.Agents[1].Presentation = "background"
	m.refreshResults()
	openWorkspaceScope(t, &m, "ws-00")
	var ids []string
	for _, result := range m.results {
		if result.Kind == resultAgent {
			ids = append(ids, result.ID)
		}
	}
	if !slices.Equal(ids, []string{"agent-00", "agent-01"}) {
		t.Fatalf("workspace omitted or duplicated a delegated agent: %v", ids)
	}
}

func TestWorkspaceScopeHiddenAgentKeepsActionGuard(t *testing.T) {
	m := searchResultsModel(1)
	m.dashboard.Agents[0].Hidden = true
	m.refreshResults()
	openWorkspaceScope(t, &m, "ws-00")
	if !m.results[0].Hidden {
		t.Fatal("workspace did not keep the hidden state of its agent")
	}
	if cmd := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || !strings.Contains(m.status, "is hidden") {
		t.Fatal("the agent of a workspace bypassed the hidden-resource guard")
	}
}

func TestWorkspaceScopeEnterOpensSelectedAgent(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "galpon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Focus bool `json:"focus"`
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil || !request.Focus {
			http.Error(w, "expected a focused agent open", http.StatusBadRequest)
			return
		}
		requests <- r.URL.Path
		_ = json.NewEncoder(w).Encode(model.Agent{ID: "agent-01"})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	m := searchResultsModel(2)
	m.client = app.NewClient(socket)
	openWorkspaceScope(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	if m.results[m.cursor].ID != "agent-01" {
		t.Fatal("could not select the second workspace agent")
	}
	command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("Enter did not open the selected workspace agent")
	}
	result := command().(actionMsg)
	if result.err != nil || !result.quit {
		t.Fatalf("open failed: %#v", result)
	}
	if path := <-requests; path != "/v1/agents/agent-01/open" {
		t.Fatalf("opened the wrong agent: %s", path)
	}
}

func TestEmptyWorkspaceScopeOffersANewAgentThere(t *testing.T) {
	m := searchResultsModel(2)
	m.dashboard.Agents = nil
	m.refreshResults()
	openWorkspaceScope(t, &m, "ws-01")
	if len(m.results) != 1 || m.results[0].Kind != resultLaunch {
		t.Fatalf("empty workspace rows = %#v", m.results)
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
	if m.screen != screenForm || m.form != formAgent || m.formContext != "ws-01" {
		t.Fatalf("new agent row = screen %d form %d workspace %q", m.screen, m.form, m.formContext)
	}
}
