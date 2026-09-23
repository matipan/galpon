package tui

import (
	"encoding/json"
	"fmt"
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

func selectWorkspaceResult(t *testing.T, m *Model, id string) int {
	t.Helper()
	if m.query.Value() == "" && m.controlKind != resultWorkspace {
		m.controlKind = resultWorkspace
		m.refreshResults()
	}
	for index, result := range m.results {
		if result.Kind == resultWorkspace && result.ID == id {
			m.cursor = index
			return index
		}
	}
	t.Fatalf("workspace %q is not visible", id)
	return -1
}

func workspaceAgentRows(results []searchResult, id string) []searchResult {
	for index, result := range results {
		if result.Kind != resultWorkspace || result.ID != id {
			continue
		}
		var rows []searchResult
		for _, child := range results[index+1:] {
			if child.Kind != resultAgent || child.Depth != 1 {
				break
			}
			rows = append(rows, child)
		}
		return rows
	}
	return nil
}

func TestSwitcherWorkspaceTabShowsAgentsByActivity(t *testing.T) {
	for _, query := range []string{"", "project"} {
		for _, normalMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("query=%q/actions=%t", query, normalMode), func(t *testing.T) {
				m := New(nil, nil)
				m.normalMode = normalMode
				now := time.Now().UnixMilli()
				m.dashboard = model.Dashboard{
					Workspaces: []model.Workspace{{ID: "ws", Title: "Project"}, {ID: "other", Title: "Other project"}},
					Agents: []model.Agent{
						{ID: "old", WorkspaceID: "ws", Title: "Dormant", UpdatedAt: now - (30 * 24 * time.Hour).Milliseconds()},
						{ID: "tie-b", WorkspaceID: "ws", Title: "Beta", UpdatedAt: now - 1},
						{ID: "other-agent", WorkspaceID: "other", Title: "Elsewhere", UpdatedAt: now + 10},
						{ID: "tie-a", WorkspaceID: "ws", Title: "Alpha", UpdatedAt: now - 1},
						{ID: "new", WorkspaceID: "ws", Title: "Newest", UpdatedAt: now},
					},
				}
				m.query.SetValue(query)
				m.refreshResults()
				position := selectWorkspaceResult(t, &m, "ws")
				if len(workspaceAgentRows(m.results, "ws")) != 0 {
					t.Fatal("workspace agents are initially expanded")
				}
				m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
				if m.cursor != position || m.results[m.cursor].Kind != resultWorkspace {
					t.Fatal("Tab moved selection off the workspace")
				}
				rows := workspaceAgentRows(m.results, "ws")
				if got, want := searchCategoryIDs(rows, resultAgent), []string{"new", "tie-a", "tie-b", "old"}; !slices.Equal(got, want) {
					t.Fatalf("workspace agents = %v, want %v", got, want)
				}
				for _, row := range rows {
					if group, _ := switcherGroup(row); group != string(resultWorkspace) {
						t.Fatalf("workspace child opened a separate category: %#v", row)
					}
				}
				if !strings.Contains(m.results[m.cursor].Detail, "4 agents") || !strings.Contains(m.results[m.cursor].Detail, "tab to collapse") {
					t.Fatalf("workspace omitted the count or collapse hint: %#v", m.results[m.cursor])
				}
				m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
				if len(workspaceAgentRows(m.results, "ws")) != 0 || m.cursor != position {
					t.Fatal("second Tab did not collapse in place")
				}
				if !strings.Contains(m.results[m.cursor].Detail, "tab to expand") {
					t.Fatal("collapsed workspace omitted the expand hint")
				}
			})
		}
	}
}

func TestSwitcherWorkspaceExpansionIsIndependent(t *testing.T) {
	m := searchResultsModel(2)
	m.dashboard.Agents[1].WorkspaceID = "ws-01"
	m.refreshResults()
	for _, id := range []string{"ws-00", "ws-01"} {
		selectWorkspaceResult(t, &m, id)
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	}
	for _, id := range []string{"ws-00", "ws-01"} {
		if len(workspaceAgentRows(m.results, id)) != 1 {
			t.Fatalf("workspace %s did not stay expanded", id)
		}
	}
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	if len(workspaceAgentRows(m.results, "ws-00")) != 0 || len(workspaceAgentRows(m.results, "ws-01")) != 1 {
		t.Fatal("collapsing one workspace changed another")
	}
}

func TestSwitcherWorkspaceRefreshKeepsNestedSelection(t *testing.T) {
	for _, query := range []string{"", "match"} {
		t.Run(query, func(t *testing.T) {
			m := searchResultsModel(15)
			m.query.SetValue(query)
			m.refreshResults()
			selectWorkspaceResult(t, &m, "ws-00")
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
			if got := len(workspaceAgentRows(m.results, "ws-00")); got != 15 {
				t.Fatalf("workspace list was capped: %d agents", got)
			}
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
			selected := m.results[m.cursor].ID
			if m.results[m.cursor].Depth != 1 {
				t.Fatal("Down did not select the first workspace agent")
			}
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyLeft})
			if m.results[m.cursor].Depth != 1 || m.results[m.cursor].ID != selected {
				t.Fatal("text-cursor movement selected the global copy of the agent")
			}
			// Keep the selected child inside its workspace, even when activity
			// pushes its global copy below the search limit.
			m.dashboard.Agents[0].UpdatedAt -= 100
			updated, _ := m.Update(dashboardMsg{value: m.dashboard})
			m = updated.(Model)
			if m.results[m.cursor].Depth != 1 || m.results[m.cursor].ID != selected {
				t.Fatal("background refresh moved the selected child out of its workspace")
			}
			if query != "" && m.expandedSearchGroups[resultAgent] {
				t.Fatal("nested selection expanded the global agent category")
			}
			if first := workspaceAgentRows(m.results, "ws-00")[0].ID; first != "agent-01" {
				t.Fatalf("workspace agents did not update activity order: %s", first)
			}
		})
	}
}

func TestSwitcherWorkspaceSearchKeepsParentVisible(t *testing.T) {
	m := searchResultsModel(15)
	m.query.SetValue("match")
	m.refreshResults()
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	if len(workspaceAgentRows(m.results, "ws-00")) == 0 {
		t.Fatal("workspace did not expand")
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	selected := m.results[m.cursor].ID
	m.dashboard.Workspaces[0].Title = "Match workspace ZZZ"
	updated, _ := m.Update(dashboardMsg{value: m.dashboard})
	m = updated.(Model)
	if m.results[m.cursor].ID != selected || m.results[m.cursor].Depth != 1 || len(workspaceAgentRows(m.results, "ws-00")) == 0 {
		t.Fatal("workspace category hid the selected nested agent after a rank change")
	}
}

func TestSwitcherWorkspaceQueryEditCollapsesAgents(t *testing.T) {
	m := searchResultsModel(2)
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("match"), Paste: true})
	if m.cursor != 0 || len(workspaceAgentRows(m.results, "ws-00")) != 0 {
		t.Fatal("search edit did not reset selection and workspace expansion")
	}
}

func TestSwitcherWorkspaceMissingChildSelectsParent(t *testing.T) {
	m := searchResultsModel(2)
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	if len(workspaceAgentRows(m.results, "ws-00")) == 0 {
		t.Fatal("workspace did not expand")
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	m.dashboard.Agents = nil
	updated, _ := m.Update(dashboardMsg{value: m.dashboard})
	m = updated.(Model)
	if m.results[m.cursor].Kind != resultWorkspace || m.results[m.cursor].ID != "ws-00" {
		t.Fatal("removing the selected agent moved selection to another resource")
	}
	if !strings.Contains(m.results[m.cursor].Detail, "0 agents") {
		t.Fatal("empty workspace did not report an empty list")
	}
}

func TestSwitcherWorkspaceIncludesDelegatedAgentsOnce(t *testing.T) {
	m := searchResultsModel(2)
	m.dashboard.Agents[1].CreatedByAgentID = "agent-00"
	m.dashboard.Agents[1].Presentation = "background"
	m.expandedAgents["agent-00"] = true
	m.refreshResults()
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	rows := workspaceAgentRows(m.results, "ws-00")
	if got := searchCategoryIDs(rows, resultAgent); !slices.Equal(got, []string{"agent-00", "agent-01"}) || !rows[1].Delegated {
		t.Fatalf("workspace omitted or duplicated a delegated agent: %#v", rows)
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	if !m.expandedAgents["agent-00"] || len(workspaceAgentRows(m.results, "ws-00")) != 2 {
		t.Fatal("Tab on a workspace child changed the global delegated group")
	}
}

func TestSwitcherWorkspaceHiddenAgentKeepsActionGuard(t *testing.T) {
	m := searchResultsModel(1)
	m.dashboard.Agents[0].Hidden = true
	m.refreshResults()
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	rows := workspaceAgentRows(m.results, "ws-00")
	if len(rows) != 1 || !rows[0].Hidden {
		t.Fatal("workspace did not preserve hidden agent state")
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	if cmd := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil || !strings.Contains(m.status, "is hidden") {
		t.Fatal("nested agent bypassed the hidden-resource action guard")
	}
}

func TestSwitcherWorkspaceEnterOpensSelectedAgent(t *testing.T) {
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
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	if m.results[m.cursor].ID != "agent-01" || m.results[m.cursor].Depth != 1 {
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

func TestSwitcherWorkspaceRenderingKeepsCategories(t *testing.T) {
	m := searchResultsModel(2)
	m.width, m.height = 150, 60
	m.query.SetValue("match")
	m.refreshResults()
	selectWorkspaceResult(t, &m, "ws-00")
	collapsed := switcherRow(m.results[m.cursor], "", true, 140)
	if !strings.Contains(collapsed, "▸ ") {
		t.Fatal("workspace has no collapsed marker")
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	expanded := switcherRow(m.results[m.cursor], "", true, 140)
	if !strings.Contains(expanded, "▾ ") {
		t.Fatal("workspace has no expanded marker")
	}
	view := m.controlList(140, 50)
	previous := -1
	for _, title := range []string{"AGENTS", "WORKSPACES", "WORKTREES", "REPOSITORIES"} {
		position := strings.Index(view, title)
		if strings.Count(view, title) != 1 || position <= previous {
			t.Fatalf("workspace expansion changed category headers:\n%s", view)
		}
		previous = position
	}
}

func TestSwitcherWorkspaceDoesNotCatchMissingAgentDisclosure(t *testing.T) {
	m := searchResultsModel(15)
	m.query.SetValue("match")
	m.refreshResults()
	selectWorkspaceResult(t, &m, "ws-00")
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	selectSearchDisclosure(t, &m, resultAgent)
	for index := range m.dashboard.Agents {
		m.dashboard.Agents[index].Title = "Builder"
	}
	m.refreshResults()
	if m.cursor != 0 || m.results[m.cursor].Kind != resultWorkspace {
		t.Fatal("missing agent category selected a nonmatching workspace child")
	}
}

func TestSwitcherWorkspaceEmptyExpansionIsSafe(t *testing.T) {
	m := searchResultsModel(1)
	m.dashboard.Agents = nil
	m.refreshResults()
	position := selectWorkspaceResult(t, &m, "ws-00")
	for range 2 {
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
		if m.cursor != position || len(workspaceAgentRows(m.results, "ws-00")) != 0 || !strings.Contains(m.results[m.cursor].Detail, "0 agents") {
			t.Fatal("empty workspace expansion changed selection or omitted its empty state")
		}
	}
}
