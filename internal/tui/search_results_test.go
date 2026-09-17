package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/model"
)

func searchResultsModel(count int) Model {
	m := New(nil, nil)
	m.width, m.height, m.loaded = 120, 18, true
	now := time.Now().UnixMilli()
	for index := range count {
		suffix := fmt.Sprintf("%02d", index)
		m.dashboard.Workspaces = append(m.dashboard.Workspaces, model.Workspace{ID: "ws-" + suffix, Title: "Match workspace " + suffix})
		m.dashboard.Repositories = append(m.dashboard.Repositories, model.Repository{ID: "repo-" + suffix, Title: "Match repository " + suffix})
		m.dashboard.Agents = append(m.dashboard.Agents, model.Agent{ID: "agent-" + suffix, WorkspaceID: "ws-00", Title: "Match agent " + suffix, UpdatedAt: now - int64(index)})
		m.dashboard.Worktrees = append(m.dashboard.Worktrees, model.Worktree{ID: "wt-" + suffix, WorkspaceID: "ws-00", RepositoryID: "repo-00", Branch: "match/" + suffix, CreatedAt: now})
	}
	m.refreshResults()
	return m
}

func searchCategoryIDs(results []searchResult, kind resultKind) []string {
	var ids []string
	for _, result := range results {
		if result.Kind == kind {
			ids = append(ids, result.ID)
		}
	}
	return ids
}

func selectSearchDisclosure(t *testing.T, m *Model, kind resultKind) string {
	t.Helper()
	for index, result := range m.results {
		group, _ := switcherGroup(result)
		if result.Kind == resultDisclosure && group == string(kind) {
			m.cursor = index
			return result.Disclosure
		}
	}
	t.Fatalf("no search disclosure for %s", kind)
	return ""
}

func TestSwitcherSearchEditsSelectFirstResult(t *testing.T) {
	for _, test := range []struct {
		name, initial, next string
		key                 tea.KeyMsg
	}{
		{"type", "mat", "matc", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")}},
		{"backspace", "match", "matc", tea.KeyMsg{Type: tea.KeyBackspace}},
		{"clear", "match", "", tea.KeyMsg{Type: tea.KeyCtrlU}},
		{"paste", "", "match", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("match"), Paste: true}},
		{"remove-selected-match", "", "match agent 00", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("match agent 00"), Paste: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := searchResultsModel(15)
			m.query.SetValue(test.initial)
			m.query.CursorEnd()
			m.refreshResults()
			m.cursor = 8
			m.updateSwitcher(test.key)
			if m.query.Value() != test.next || m.cursor != 0 {
				t.Fatalf("query=%q cursor=%d, want %q and first result", m.query.Value(), m.cursor, test.next)
			}
			if m.results[0].ID != "agent-00" {
				t.Fatalf("top agent was not selected: %#v", m.results[0])
			}
		})
	}
}

func TestSwitcherSearchLimitsEachCategoryWithoutChangingRank(t *testing.T) {
	for _, count := range []int{0, 1, 10, 11, 25} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := searchResultsModel(count)
			m.query.SetValue("match")
			m.refreshResults()
			all := buildResults(m.dashboard, "match")
			for _, kind := range []resultKind{resultAgent, resultWorkspace, resultWorktree, resultRepository} {
				want := searchCategoryIDs(all, kind)
				want = want[:min(10, len(want))]
				if got := searchCategoryIDs(m.results, kind); !slices.Equal(got, want) {
					t.Fatalf("%s results=%v, want=%v", kind, got, want)
				}
			}
			disclosures, previous := 0, -1
			for _, result := range m.results {
				group, _ := switcherGroup(result)
				order := resultOrder(searchResult{Kind: resultKind(group)})
				if order < previous {
					t.Fatalf("search changed category order: %#v", m.results)
				}
				previous = order
				if result.Kind == resultDisclosure {
					disclosures++
					if result.DisclosureCount != count-10 || !strings.Contains(result.Detail, "tab to expand") {
						t.Fatalf("wrong disclosure: %#v", result)
					}
				}
			}
			wantDisclosures := 0
			if count > 10 {
				wantDisclosures = 4
			}
			if disclosures != wantDisclosures {
				t.Fatalf("disclosures=%d, want=%d", disclosures, wantDisclosures)
			}
		})
	}
}

func TestSwitcherSearchExpandsInPlaceAndKeepsGroupsIndependent(t *testing.T) {
	for _, kind := range []resultKind{resultAgent, resultWorkspace, resultWorktree, resultRepository} {
		t.Run(string(kind), func(t *testing.T) {
			m := searchResultsModel(15)
			m.query.SetValue("match")
			m.refreshResults()
			disclosure := selectSearchDisclosure(t, &m, kind)
			position := m.cursor
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
			if m.cursor != position || m.results[m.cursor].Disclosure != disclosure || !strings.Contains(m.results[m.cursor].Detail, "tab to collapse") {
				t.Fatalf("expansion moved the control or selection: cursor=%d results=%#v", m.cursor, m.results)
			}
			all := buildResults(m.dashboard, "match")
			if got, want := searchCategoryIDs(m.results, kind), searchCategoryIDs(all, kind); !slices.Equal(got, want) {
				t.Fatalf("expansion changed ranked matches: %v, want %v", got, want)
			}
			if m.results[m.cursor+1].ID != searchCategoryIDs(all, kind)[10] {
				t.Fatal("new matches did not appear directly below the expansion row")
			}
			for _, other := range []resultKind{resultAgent, resultWorkspace, resultWorktree, resultRepository} {
				if other != kind && len(searchCategoryIDs(m.results, other)) != 10 {
					t.Fatalf("expanding %s also expanded %s", kind, other)
				}
			}
			updated, _ := m.Update(dashboardMsg{value: m.dashboard})
			m = updated.(Model)
			if len(searchCategoryIDs(m.results, kind)) != 15 || m.results[m.cursor].Disclosure != disclosure {
				t.Fatal("background refresh lost expansion or selection")
			}
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
			if len(searchCategoryIDs(m.results, kind)) != 10 || m.results[m.cursor].Disclosure != disclosure {
				t.Fatal("Enter did not collapse the category in place")
			}
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
			m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
			if len(searchCategoryIDs(m.results, kind)) != 10 || m.cursor != 0 {
				t.Fatal("query edit did not reset expansion and selection")
			}
		})
	}
}

func TestSwitcherSearchPreservesNavigationAndBackgroundSelection(t *testing.T) {
	m := searchResultsModel(15)
	m.query.SetValue("match")
	m.refreshResults()
	for range 9 {
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	}
	selected := m.results[m.cursor].ID
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyLeft})
	if m.cursor != 9 || m.results[m.cursor].ID != selected {
		t.Fatal("moving the text cursor reset result navigation")
	}
	// A new high-ranked match must not hide the selected tenth result. Keep
	// that category open instead of silently selecting another resource.
	m.dashboard.Agents[14].UpdatedAt = time.Now().Add(time.Second).UnixMilli()
	updated, _ := m.Update(dashboardMsg{value: m.dashboard})
	m = updated.(Model)
	if m.results[m.cursor].ID != selected || len(searchCategoryIDs(m.results, resultAgent)) != 15 {
		t.Fatal("background ranking hid or changed the selected result")
	}
	if len(searchCategoryIDs(m.results, resultWorkspace)) != 10 {
		t.Fatal("preserving selection expanded another category")
	}
}

func TestSwitcherSearchEditResetsAnExpandedDeepSelection(t *testing.T) {
	m := searchResultsModel(25)
	m.query.SetValue("match")
	m.refreshResults()
	selectSearchDisclosure(t, &m, resultAgent)
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyTab})
	for range 12 {
		m.updateSwitcher(tea.KeyMsg{Type: tea.KeyDown})
	}
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if m.cursor != 0 || m.results[0].ID != "agent-00" || len(searchCategoryIDs(m.results, resultAgent)) != 10 {
		t.Fatal("new query retained a deep selection or an expanded category")
	}
}

func TestSwitcherSearchSelectsFirstAvailableCategory(t *testing.T) {
	m := searchResultsModel(15)
	m.cursor = 12
	m.query.SetValue("workspace")
	m.refreshResults()
	if m.cursor != 0 || m.results[0].Kind != resultWorkspace {
		t.Fatal("a query without agent matches did not select the first workspace")
	}
	if len(searchCategoryIDs(m.results, resultAgent)) != 0 || len(searchCategoryIDs(m.results, resultWorkspace)) != 10 {
		t.Fatal("search changed matching or omitted the category limit")
	}
}

func TestSwitcherSearchRefreshRemovesUnneededDisclosure(t *testing.T) {
	m := searchResultsModel(15)
	m.query.SetValue("match")
	m.refreshResults()
	selectSearchDisclosure(t, &m, resultAgent)
	m.dashboard.Agents = m.dashboard.Agents[:10]
	updated, _ := m.Update(dashboardMsg{value: m.dashboard})
	m = updated.(Model)
	if m.results[m.cursor].Kind != resultAgent {
		t.Fatal("removing an unneeded disclosure selected another category")
	}
	for _, result := range m.results {
		group, _ := switcherGroup(result)
		if result.Kind == resultDisclosure && group == string(resultAgent) {
			t.Fatal("a ten-result category kept an unnecessary disclosure")
		}
	}
}

func TestSwitcherSearchLimitsDoNotChangeEmptyQueryBrowsing(t *testing.T) {
	m := searchResultsModel(15)
	if len(searchCategoryIDs(m.results, resultAgent)) != 15 {
		t.Fatal("search limit changed empty-query browsing")
	}
	m.query.SetValue("no such match")
	m.refreshResults()
	if len(m.results) != 0 || m.cursor != 0 {
		t.Fatalf("empty results left an invalid selection: %#v", m.results)
	}
	m.query.SetValue("")
	m.refreshResults()
	if len(searchCategoryIDs(m.results, resultAgent)) != 15 || m.cursor != 0 {
		t.Fatal("clearing search did not restore normal browsing at the top")
	}
}
