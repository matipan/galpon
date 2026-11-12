package tui

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

func TestPullRequestLookupOpensTheRecordedAgent(t *testing.T) {
	creator := model.PullRequestCreator{
		Repository: "dagger/dagger", Number: 14964, WorkspaceTitle: "Engine",
		Agent: model.Agent{ID: "creator", Title: "Engine startup", WorkspaceID: "engine"},
	}
	socket := filepath.Join(t.TempDir(), "galpon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{}, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/pull-request-creators" && r.URL.Query().Get("query") == "dagger/dagger#14964":
			_ = json.NewEncoder(w).Encode([]model.PullRequestCreator{creator})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/creator/open":
			var request struct{ Focus bool }
			if json.NewDecoder(r.Body).Decode(&request) != nil || !request.Focus {
				http.Error(w, "expected a focused open", http.StatusBadRequest)
				return
			}
			opened <- struct{}{}
			_ = json.NewEncoder(w).Encode(creator.Agent)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	m := searchResultsModel(2)
	m.client = app.NewClient(socket)
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://github.com/dagger/dagger/pull/14964"), Paste: true})
	if len(m.results) != 0 || !strings.Contains(m.controlList(100, 10), "Looking up PR creators") {
		t.Fatal("a pending lookup must not offer an unrelated agent or a launch row")
	}
	command := m.readPullRequestCreators(m.prLookup.generation)
	m.applyPullRequestLookup(command().(pullRequestLookupMsg))
	if len(m.results) != 1 || m.results[0].Kind != resultAgent || m.results[0].ID != "creator" {
		t.Fatalf("lookup rows = %+v", m.results)
	}
	view := m.controlList(100, 10)
	for _, text := range []string{"Engine startup", "Created dagger/dagger#14964"} {
		if !strings.Contains(view, text) {
			t.Fatalf("lookup row does not show %q: %s", text, view)
		}
	}
	command = m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil {
		t.Fatal("Enter did not open the creator")
	}
	result := command().(actionMsg)
	if result.err != nil || !result.quit {
		t.Fatalf("open creator: %+v", result)
	}
	select {
	case <-opened:
	default:
		t.Fatal("Enter did not request an agent open")
	}
}

func TestPullRequestLookupRejectsSupersededResponses(t *testing.T) {
	m := searchResultsModel(2)
	m.client = app.NewClient("unused")
	search := func(query string) uint64 {
		m.query.SetValue(query)
		m.lookupPullRequestCreators()
		m.refreshResults()
		return m.prLookup.generation
	}
	first := search("#1")
	search("#2")
	current := search("#1")
	if command := m.readPullRequestCreators(first); command != nil {
		t.Fatal("a superseded debounce timer started a request")
	}
	creator := model.PullRequestCreator{Repository: "acme/tools", Number: 1, Agent: model.Agent{ID: "creator", Title: "Creator"}}
	m.applyPullRequestLookup(pullRequestLookupMsg{generation: first, creators: []model.PullRequestCreator{creator}})
	if len(m.results) != 0 {
		t.Fatal("an earlier request for the same query replaced the pending result")
	}
	m.applyPullRequestLookup(pullRequestLookupMsg{generation: current, creators: []model.PullRequestCreator{creator}})
	if len(m.results) != 1 || m.results[0].ID != "creator" {
		t.Fatalf("current lookup rows = %+v", m.results)
	}
	other := model.PullRequestCreator{Repository: "aa/first", Number: 1, Agent: model.Agent{ID: "other", Title: "Another creator"}}
	m.applyPullRequestLookup(pullRequestLookupMsg{generation: current, creators: []model.PullRequestCreator{other, creator}})
	if len(m.results) != 2 || m.results[0].ID != "creator" || m.results[1].ID != "other" {
		t.Fatalf("a refresh moved the existing creator row: %+v", m.results)
	}
	m.query.SetValue("match agent 00")
	m.lookupPullRequestCreators()
	m.refreshResults()
	m.applyPullRequestLookup(pullRequestLookupMsg{generation: current, creators: []model.PullRequestCreator{creator}})
	if got := withoutLaunch(m.results); len(got) != 1 || got[0].ID != "agent-00" {
		t.Fatalf("normal title search rows = %+v", m.results)
	}
	pending := search("#1")
	m.enterScope(listScope{Kind: scopePullRequests, Title: "Pull requests"})
	if command := m.readPullRequestCreators(pending); command != nil {
		t.Fatal("a lookup continued after entering a scope")
	}
	m.leaveScope()
	m.query.SetValue("#1")
	m.refreshResults()
	if command := m.lookupPullRequestCreators(); command == nil {
		t.Fatal("returning from a scope left the same lookup stuck pending")
	}
}

func TestPullRequestLookupReportsMissingRecordsAndErrors(t *testing.T) {
	m := searchResultsModel(1)
	m.query.SetValue("#42")
	m.prLookup = &pullRequestLookup{key: "#42", generation: 1}
	m.applyPullRequestLookup(pullRequestLookupMsg{generation: 1, err: errors.New("daemon unavailable")})
	if view := m.controlList(100, 10); !strings.Contains(view, "Cannot look up PR creators: daemon unavailable") {
		t.Fatalf("lookup failure: %s", view)
	}
	m.applyPullRequestLookup(pullRequestLookupMsg{generation: 1})
	if view := m.controlList(100, 10); !strings.Contains(view, "No recorded agent.") {
		t.Fatalf("empty lookup: %s", view)
	}
	if command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter}); command != nil || m.screen != screenSwitcher {
		t.Fatal("Enter on an empty lookup must not create or open an agent")
	}
}

func TestPullRequestLookupKeepsHiddenCreatorsAndAllReferences(t *testing.T) {
	agent := model.Agent{ID: "creator", Title: "Creator", WorkspaceID: "ws", Hidden: true, Kind: "claude", View: model.ViewApp}
	creators := []model.PullRequestCreator{
		{Repository: "acme/one", Number: 42, Agent: agent, WorkspaceTitle: "Work"},
		{Repository: "acme/two", Number: 42, Agent: agent, WorkspaceTitle: "Work"},
	}
	m := searchResultsModel(1)
	m.query.SetValue("#42")
	m.prLookup = &pullRequestLookup{key: "#42", loaded: true, creators: creators}
	m.refreshResults()
	if len(m.results) != 1 || m.results[0].CreatorRef != "acme/one#42 · acme/two#42" || !m.results[0].Hidden {
		t.Fatalf("creator references = %+v", m.results)
	}
	if command := m.updateSwitcher(tea.KeyMsg{Type: tea.KeyEnter}); command != nil || !strings.Contains(m.status, "is hidden") {
		t.Fatal("lookup bypassed the hidden-agent guard")
	}
	native := BuildNativePullRequestCreators(creators)
	if len(native) != 1 || native[0].Kind != "agent" || native[0].ID != agent.ID || !native[0].Hidden || native[0].Harness != "claude" || native[0].View != model.ViewApp || native[0].Ref != "acme/one#42 · acme/two#42" {
		t.Fatalf("native creator rows = %+v", native)
	}
}
