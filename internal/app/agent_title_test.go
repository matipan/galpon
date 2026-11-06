package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

// renameTestAgent opens an app with renderer and creates one agent with the
// title "Original".
func renameTestAgent(t *testing.T, renderer *cleanupRenderer) (*App, model.Agent) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestApp(t, application) })
	workspace, err := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Rename"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Original", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	return application, agent
}

func TestRenameAgentAlsoRenamesItsOpenView(t *testing.T) {
	ctx := context.Background()
	renderer := &cleanupRenderer{name: "test-renderer", context: "test-context"}
	application, agent := renameTestAgent(t, renderer)

	// The agent has no view yet, so no terminal app gets the title.
	renamed, err := application.RenameAgent(ctx, agent.ID, "  Billing lead  ")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := application.Store.Agent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "Billing lead" || stored.Title != "Billing lead" || len(renderer.renamed) != 0 {
		t.Fatalf("renamed = %q, stored = %q, renderer renames = %v", renamed.Title, stored.Title, renderer.renamed)
	}

	if err := application.Store.SetAgentRenderer(ctx, agent.ID, renderer.Name(), renderer.Context(), "pane"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.RenameAgent(ctx, agent.ID, "Reviewer"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(renderer.renamed, []string{"Reviewer"}) {
		t.Fatalf("renderer renames = %v, want the open view renamed to Reviewer", renderer.renamed)
	}

	// The tab keeps its old name until the agent opens again, but the agent
	// has the new title.
	renderer.renameErr = errors.New("the window is busy")
	renamed, err = application.RenameAgent(ctx, agent.ID, "Release")
	if err != nil {
		t.Fatalf("a view that cannot be renamed stopped the rename: %v", err)
	}
	if stored, err := application.Store.Agent(ctx, agent.ID); err != nil || renamed.Title != "Release" || stored.Title != "Release" {
		t.Fatalf("renamed = %q, stored = %#v, %v", renamed.Title, stored, err)
	}
}

func TestRenameAgentUsesTheTitleRulesOfANewAgent(t *testing.T) {
	ctx := context.Background()
	application, agent := renameTestAgent(t, &cleanupRenderer{name: "test-renderer", context: "test-context"})
	for _, title := range []string{"", "   ", "Tab\tname", "Zero\u200bwidth", strings.Repeat("a", agentTitleLimit+1)} {
		if _, err := application.RenameAgent(ctx, agent.ID, title); !IsInvalidRequest(err) {
			t.Errorf("rename to %q: error = %v, want an invalid request", title, err)
		}
	}
	if stored, err := application.Store.Agent(ctx, agent.ID); err != nil || stored.Title != "Original" {
		t.Fatalf("an invalid title changed the agent: %#v, %v", stored, err)
	}
	if _, err := application.RenameAgent(ctx, agent.ID, strings.Repeat("a", agentTitleLimit)); err != nil {
		t.Fatalf("rename to the longest title: %v", err)
	}

	if _, err := application.DeleteResource(ctx, "agent", agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.RenameAgent(ctx, agent.ID, "Back"); !IsNotFound(err) {
		t.Fatalf("rename of a hidden agent: error = %v, want not found", err)
	}
	if _, err := application.RestoreResource(ctx, "agent", agent.ID); err != nil {
		t.Fatal(err)
	}
	if stored, err := application.Store.Agent(ctx, agent.ID); err != nil || stored.Title != strings.Repeat("a", agentTitleLimit) {
		t.Fatalf("the rename of the hidden agent changed it: %#v, %v", stored, err)
	}
}

// Running agents read their title through GET /v1/agents/{id}/title after a
// rename through POST /v1/agents/{id}/rename.
func TestRenameAndTitleEndpoints(t *testing.T) {
	application, agent := renameTestAgent(t, &cleanupRenderer{name: "test-renderer", context: "test-context"})
	server := NewServer(application)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	if response := call(http.MethodPost, "/v1/agents/"+agent.ID+"/rename", `{"title":"Night shift"}`); response.Code != http.StatusOK {
		t.Fatalf("rename status = %d: %s", response.Code, response.Body.String())
	}
	if response := call(http.MethodPost, "/v1/agents/"+agent.ID+"/rename", `{"title":" "}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rename to an empty title: status = %d: %s", response.Code, response.Body.String())
	}
	response := call(http.MethodGet, "/v1/agents/"+agent.ID+"/title", "")
	var title struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &title); err != nil || response.Code != http.StatusOK || title.Title != "Night shift" {
		t.Fatalf("title status = %d, body = %s, %v", response.Code, response.Body.String(), err)
	}
	if response := call(http.MethodGet, "/v1/agents/no-such-agent/title", ""); response.Code != http.StatusNotFound {
		t.Fatalf("title of an unknown agent: status = %d", response.Code)
	}
}
