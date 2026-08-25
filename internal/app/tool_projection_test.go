package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/model"
)

func TestAgentToolViewsExcludeRuntimeSessionRendererAndPaths(t *testing.T) {
	dashboard := model.Dashboard{
		Agents: []model.Agent{{
			ID: "agent", WorkspaceID: "workspace", Title: "Worker", Kind: "codex", Status: "running",
			SessionID: "secret-session", SessionPath: "/secret/session", RuntimeID: "secret-runtime",
			Renderer: "herdr", RendererContext: "secret-context", RendererID: "secret-pane", LastError: "secret-error",
			Placement: model.AgentPlacement{Type: "worktrees", CWD: "/secret/cwd", PrimaryWorktreeID: "worktree", Worktrees: []model.AgentWorktree{{WorktreeID: "worktree", Position: 0, Mode: "private"}}},
		}},
		Worktrees: []model.Worktree{{ID: "worktree", WorkspaceID: "workspace", RepositoryID: "repository", Path: "/secret/worktree", Branch: "branch"}},
	}
	data, err := json.Marshal(agentToolViews(dashboard))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"secret-session", "/secret/session", "secret-runtime", "secret-context", "secret-pane", "secret-error", "/secret/cwd", "/secret/worktree"} {
		if strings.Contains(text, secret) {
			t.Fatalf("tool projection leaked %q: %s", secret, text)
		}
	}
	created := safeToolAgent(dashboard.Agents[0])
	if created.RuntimeID != "" || created.SessionID != "" || created.SessionPath != "" || created.RendererID != "" {
		t.Fatalf("created-agent projection leaked private state: %#v", created)
	}
	message := safeToolMessage(model.AgentMessage{ID: "message", RuntimeID: "secret-runtime", LastError: "secret-error"})
	if message.RuntimeID != "" || message.LastError != "" {
		t.Fatalf("message projection leaked private state: %#v", message)
	}
	repositories, _ := json.Marshal(repositoryToolViews([]model.Repository{{ID: "repository", Title: "Repo", SourcePath: "/secret/source", MirrorPath: "/secret/mirror", FetchURL: "https://token@example.invalid/repo", Remotes: []model.RepositoryRemote{{Name: "origin", FetchURL: "https://token@example.invalid/repo"}}}}))
	workspaces, _ := json.Marshal(workspaceToolViews([]model.Workspace{{ID: "workspace", Title: "Work", RendererID: "secret-pane", RendererContext: "secret-context"}}))
	if strings.Contains(string(repositories), "token@") || strings.Contains(string(repositories), "/secret/") || strings.Contains(string(workspaces), "secret-") {
		t.Fatalf("list projection leaked private data: %s %s", repositories, workspaces)
	}
	for _, want := range []string{"Worker", "codex", "worktree", "branch"} {
		if !strings.Contains(text, want) {
			t.Fatalf("tool projection omitted %q: %s", want, text)
		}
	}
}
