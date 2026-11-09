package tui

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

func TestRenameFormStartsWithTheNameAndRenamesTheAgent(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "galpon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	titles := make(chan string, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/agents/agent/rename" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		titles <- request.Title
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Billing lead"})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	m := New(app.NewClient(socket), nil)
	m.dashboard = model.Dashboard{
		Workspaces: []model.Workspace{{ID: "ws", Title: "Billing"}},
		Agents:     []model.Agent{{ID: "agent", WorkspaceID: "ws", Title: "Invoices"}},
	}
	m.refreshResults()
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyCtrlK})
	m.updateSwitcher(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.screen != screenForm || m.form != formRename || m.formInput.Value() != "Invoices" {
		t.Fatalf("n on an agent: screen=%d form=%d value=%q", m.screen, m.form, m.formInput.Value())
	}
	m.formInput.SetValue("Billing lead")
	command := m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || !m.busy {
		t.Fatal("Enter did not start the rename")
	}
	message := command()
	if title := <-titles; title != "Billing lead" {
		t.Fatalf("rename title = %q", title)
	}
	updated, _ := m.Update(message)
	got := updated.(Model)
	if got.screen != screenSwitcher || got.form != formNone || got.status != "Renamed the agent to Billing lead" {
		t.Fatalf("after the rename: screen=%d form=%d status=%q", got.screen, got.form, got.status)
	}
}
