package herdr

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestActiveContextUsesPopupVariablesAndIgnoresPaneVariable(t *testing.T) {
	values := map[string]string{
		"HERDR_ACTIVE_WORKSPACE_ID": "herdr-workspace",
		"HERDR_ACTIVE_TAB_ID":       "herdr-tab",
		"HERDR_ACTIVE_PANE_ID":      "herdr-pane",
		"HERDR_ACTIVE_PANE_CWD":     "/managed/worktree",
		"HERDR_SOCKET_PATH":         "/run/herdr.sock",
		"HERDR_BIN_PATH":            "/usr/bin/herdr",
		"HERDR_SESSION":             "work",
		"HERDR_PANE_ID":             "popup-pane-must-not-be-used",
	}
	active, err := activeContextFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if active.PaneID != "herdr-pane" || active.WorkspaceID != "herdr-workspace" || active.TabID != "herdr-tab" || active.RendererContext != "work" {
		t.Fatalf("active context = %#v", active)
	}
}

func TestParseSessionSnapshotReadsActivePaneAndPiSession(t *testing.T) {
	snapshot := validSnapshot("/sessions/current.jsonl", true)
	data := encodeSnapshot(t, snapshot)
	parsed, err := parseSessionSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	active := testActiveContext()
	pane, agent, err := activePaneFacts(parsed, active)
	if err != nil {
		t.Fatal(err)
	}
	if pane.PaneID != active.PaneID || agent == nil || agent.AgentSession == nil || agent.AgentSession.Agent != "Builder" || agent.AgentSession.Source != "galpon" || agent.AgentSession.Value != "/sessions/current.jsonl" {
		t.Fatalf("pane = %#v, agent = %#v", pane, agent)
	}
}

func TestResolveNewAgentContextPrefersExactPiSession(t *testing.T) {
	setActiveContextEnv(t, validSnapshot("/sessions/current.jsonl", true))
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "workspace", Title: "Current"}},
		Agents:     []model.Agent{{ID: "agent", WorkspaceID: "workspace", Kind: "pi", SessionPath: "/sessions/current.jsonl", Renderer: "herdr", RendererContext: "default", RendererID: "old-pane"}},
	}
	workspace, agent, err := ResolveNewAgentContext(t.Context(), dashboard)
	if err != nil {
		t.Fatal(err)
	}
	if workspace != "workspace" || agent.ID != "agent" {
		t.Fatalf("context = workspace %q agent %#v", workspace, agent)
	}
}

func TestResolveNewAgentWorkspaceFromManagedNonAgentPane(t *testing.T) {
	setActiveContextEnv(t, validSnapshot("", false))
	dashboard := model.Dashboard{Workspaces: []model.Workspace{{ID: "workspace", Renderer: "herdr", RendererContext: "default", RendererID: "herdr-workspace"}}}
	workspace, err := ResolveNewAgentWorkspace(t.Context(), dashboard)
	if err != nil {
		t.Fatal(err)
	}
	if workspace != "workspace" {
		t.Fatalf("workspace = %q", workspace)
	}
}

func TestResolveNewAgentWorkspaceUsesAgentPaneMappingAsBoundedFallback(t *testing.T) {
	setActiveContextEnv(t, validSnapshot("", true))
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "workspace"}},
		Agents:     []model.Agent{{ID: "agent", WorkspaceID: "workspace", Kind: "pi", Renderer: "herdr", RendererContext: "default", RendererID: "herdr-pane"}},
	}
	workspace, err := ResolveNewAgentWorkspace(t.Context(), dashboard)
	if err != nil {
		t.Fatal(err)
	}
	if workspace != "workspace" {
		t.Fatalf("workspace = %q", workspace)
	}
}

func TestResolveOperationsUsesExactSessionAndRendererFallback(t *testing.T) {
	tests := []struct {
		name      string
		session   string
		dashboard model.Dashboard
		wantAgent string
	}{
		{
			name:    "session path",
			session: "/sessions/current.jsonl",
			dashboard: model.Dashboard{
				Workspaces: []model.Workspace{{ID: "workspace"}},
				Agents:     []model.Agent{{ID: "session-agent", WorkspaceID: "workspace", Kind: "pi", SessionPath: "/sessions/current.jsonl", Renderer: "herdr", RendererContext: "default", RendererID: "old-pane"}},
			},
			wantAgent: "session-agent",
		},
		{
			name: "renderer pane fallback",
			dashboard: model.Dashboard{
				Workspaces: []model.Workspace{{ID: "workspace", Renderer: "herdr", RendererContext: "default", RendererID: "herdr-workspace"}},
				Agents:     []model.Agent{{ID: "renderer-agent", WorkspaceID: "workspace", Kind: "pi", Renderer: "herdr", RendererContext: "default", RendererID: "herdr-pane"}},
			},
			wantAgent: "renderer-agent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setActiveContextEnv(t, validSnapshot(test.session, true))
			agent, workspace, err := ResolveOperationsAgent(t.Context(), test.dashboard)
			if err != nil {
				t.Fatal(err)
			}
			if agent.ID != test.wantAgent || workspace != "workspace" {
				t.Fatalf("agent = %q, workspace = %q", agent.ID, workspace)
			}
		})
	}
}

func TestResolveOperationsRejectsNonAgentPane(t *testing.T) {
	setActiveContextEnv(t, validSnapshot("", false))
	dashboard := model.Dashboard{Workspaces: []model.Workspace{{ID: "workspace", Renderer: "herdr", RendererContext: "default", RendererID: "herdr-workspace"}}}
	_, _, err := ResolveOperationsAgent(t.Context(), dashboard)
	if !errors.Is(err, errActiveContextNonAgent) {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveOperationsRejectsNonGalponAgentSession(t *testing.T) {
	setActiveContextEnv(t, validSnapshot("/sessions/external.jsonl", true))
	dashboard := model.Dashboard{
		Workspaces: []model.Workspace{{ID: "workspace"}},
		Agents:     []model.Agent{{ID: "external", WorkspaceID: "workspace", Kind: "other", SessionPath: "/sessions/external.jsonl"}},
	}
	_, _, err := ResolveOperationsAgent(t.Context(), dashboard)
	if !errors.Is(err, errActiveContextNonAgent) {
		t.Fatalf("error = %v", err)
	}
}

func TestActiveContextRejectsMissingStaleAmbiguousAndMalformedFacts(t *testing.T) {
	t.Run("missing popup variable", func(t *testing.T) {
		setActiveContextEnv(t, validSnapshot("", false))
		t.Setenv("HERDR_ACTIVE_PANE_ID", "")
		_, err := ResolveNewAgentWorkspace(t.Context(), model.Dashboard{})
		if !errors.Is(err, errActiveContextMissing) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("stale pane", func(t *testing.T) {
		snapshot := validSnapshot("", false)
		snapshot.Panes[0].PaneID = "closed-pane"
		setActiveContextEnv(t, snapshot)
		_, err := ResolveNewAgentWorkspace(t.Context(), model.Dashboard{})
		if !errors.Is(err, errActiveContextStale) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("ambiguous pane", func(t *testing.T) {
		snapshot := validSnapshot("", false)
		snapshot.Panes = append(snapshot.Panes, snapshot.Panes[0])
		setActiveContextEnv(t, snapshot)
		_, err := ResolveNewAgentWorkspace(t.Context(), model.Dashboard{})
		if !errors.Is(err, errActiveContextAmbiguous) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("malformed snapshot", func(t *testing.T) {
		setActiveContextEnv(t, validSnapshot("", false))
		t.Setenv("HERDR_TEST_SNAPSHOT", "not json")
		_, err := ResolveNewAgentWorkspace(t.Context(), model.Dashboard{})
		if !errors.Is(err, errActiveContextMalformed) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("ambiguous session", func(t *testing.T) {
		setActiveContextEnv(t, validSnapshot("/sessions/current.jsonl", true))
		dashboard := model.Dashboard{
			Workspaces: []model.Workspace{{ID: "one"}, {ID: "two"}},
			Agents: []model.Agent{
				{ID: "one", WorkspaceID: "one", Kind: "pi", SessionPath: "/sessions/current.jsonl"},
				{ID: "two", WorkspaceID: "two", Kind: "pi", SessionPath: "/sessions/current.jsonl"},
			},
		}
		_, err := ResolveNewAgentWorkspace(t.Context(), dashboard)
		if !errors.Is(err, errActiveContextAmbiguous) {
			t.Fatalf("error = %v", err)
		}
	})
}

func validSnapshot(sessionPath string, agent bool) sessionSnapshot {
	snapshot := sessionSnapshot{
		Workspaces: []workspaceFact{{WorkspaceID: "herdr-workspace"}},
		Tabs:       []tabFact{{TabID: "herdr-tab", WorkspaceID: "herdr-workspace"}},
		Panes:      []paneFact{{PaneID: "herdr-pane", TabID: "herdr-tab", WorkspaceID: "herdr-workspace"}},
	}
	if agent {
		fact := paneFact{PaneID: "herdr-pane", TabID: "herdr-tab", WorkspaceID: "herdr-workspace"}
		if sessionPath != "" {
			fact.AgentSession = &sessionFact{Source: "galpon", Agent: "Builder", Kind: "path", Value: sessionPath}
		}
		snapshot.Agents = []paneFact{fact}
	}
	return snapshot
}

func encodeSnapshot(t *testing.T, snapshot sessionSnapshot) []byte {
	t.Helper()
	response := snapshotResponse{}
	response.Result.Type = "session_snapshot"
	response.Result.Snapshot = snapshot
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testActiveContext() ActiveContext {
	return ActiveContext{WorkspaceID: "herdr-workspace", TabID: "herdr-tab", PaneID: "herdr-pane", PaneCWD: "/managed/worktree", SocketPath: "/run/herdr.sock", BinPath: "/usr/bin/herdr", RendererContext: "default"}
}

func setActiveContextEnv(t *testing.T, snapshot sessionSnapshot) {
	t.Helper()
	root := t.TempDir()
	binary := filepath.Join(root, "herdr")
	script := "#!/bin/sh\n[ \"$1 $2\" = \"api snapshot\" ] || exit 2\nprintf '%s' \"$HERDR_TEST_SNAPSHOT\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	active := testActiveContext()
	t.Setenv("HERDR_ACTIVE_WORKSPACE_ID", active.WorkspaceID)
	t.Setenv("HERDR_ACTIVE_TAB_ID", active.TabID)
	t.Setenv("HERDR_ACTIVE_PANE_ID", active.PaneID)
	t.Setenv("HERDR_ACTIVE_PANE_CWD", active.PaneCWD)
	t.Setenv("HERDR_SOCKET_PATH", active.SocketPath)
	t.Setenv("HERDR_BIN_PATH", binary)
	t.Setenv("HERDR_SESSION", "")
	t.Setenv("HERDR_PANE_ID", "popup-pane")
	t.Setenv("HERDR_TEST_SNAPSHOT", string(encodeSnapshot(t, snapshot)))
}

func TestContextErrorsDoNotShowPathsOrOpaqueIDs(t *testing.T) {
	setActiveContextEnv(t, validSnapshot("", false))
	_, err := ResolveNewAgentWorkspace(t.Context(), model.Dashboard{})
	if err == nil {
		t.Fatal("expected an unmanaged context error")
	}
	for _, private := range []string{"/managed/worktree", "/run/herdr.sock", "herdr-pane", "herdr-workspace"} {
		if strings.Contains(err.Error(), private) {
			t.Fatalf("error shows private context %q: %v", private, err)
		}
	}
}
