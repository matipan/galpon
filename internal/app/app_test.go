package app

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestAgentPlacementSupportsPrivateCopiesSharingSecondariesAndNoWorktree(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	repoA, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "primary")})
	if err != nil {
		t.Fatal(err)
	}
	repoB, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "secondary")})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Feature"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := application.CreateAgent(ctx, CreateAgentRequest{
		Title: "Implementation A", Role: "implementer", WorkspaceID: workspace.ID,
		Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{
			{RepositoryID: repoA.ID, Remote: repoA.DefaultRemote, Ref: repoA.DefaultBranch},
			{RepositoryID: repoB.ID, Remote: repoB.DefaultRemote, Ref: repoB.DefaultBranch},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Store.RegisterAgentRuntime(ctx, first.ID, "runtime", first.SessionID, filepath.Join(root, "first.jsonl")); err != nil {
		t.Fatal(err)
	}
	first, err = application.Store.Agent(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	copyAgent, err := application.CreateAgent(ctx, CreateAgentRequest{
		Title: "Implementation B", WorkspaceID: workspace.ID, ContextAgentID: first.ID,
		Placement: AgentPlacementRequest{Type: "agent", SourceAgentID: first.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	sharedAgent, err := application.CreateAgent(ctx, CreateAgentRequest{
		Title: "Reviewer", WorkspaceID: workspace.ID,
		Placement: AgentPlacementRequest{Type: "agent", SourceAgentID: first.ID, Share: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktreeless, err := application.CreateAgent(ctx, CreateAgentRequest{
		Title: "Coordinator", WorkspaceID: workspace.ID,
		Placement: AgentPlacementRequest{Type: "none", CWD: root},
	})
	if err != nil {
		t.Fatal(err)
	}

	dashboard, err := application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storedFirst, _ := dashboard.Agent(first.ID)
	storedCopy, _ := dashboard.Agent(copyAgent.ID)
	storedShared, _ := dashboard.Agent(sharedAgent.ID)
	if len(storedFirst.Placement.Worktrees) != 2 || len(storedCopy.Placement.Worktrees) != 2 || len(storedShared.Placement.Worktrees) != 2 {
		t.Fatalf("placements = first %#v copy %#v shared %#v", storedFirst.Placement, storedCopy.Placement, storedShared.Placement)
	}
	if storedCopy.ContextAgentID != first.ID || storedCopy.Placement.PrimaryWorktreeID == storedFirst.Placement.PrimaryWorktreeID {
		t.Fatalf("private copy = %#v", storedCopy)
	}
	if storedShared.Placement.PrimaryWorktreeID != storedFirst.Placement.PrimaryWorktreeID || storedFirst.Placement.Worktrees[0].Mode != "shared" || storedShared.Placement.Worktrees[0].Mode != "shared" {
		t.Fatalf("exact share = first %#v shared %#v", storedFirst.Placement, storedShared.Placement)
	}
	if worktreeless.Placement.Type != "none" || worktreeless.Placement.CWD != root {
		t.Fatalf("worktreeless placement = %#v", worktreeless.Placement)
	}
	for _, worktree := range dashboard.AgentWorktrees(storedCopy) {
		if _, err := os.Stat(filepath.Join(worktree.Path, "README.md")); err != nil {
			t.Fatalf("copied private worktree: %v", err)
		}
	}
}

func TestAgentWaitRejectsDirectAndIndirectCycles(t *testing.T) {
	application := &App{}
	finishAB, err := application.beginAgentWait("agent-a", model.AgentMessage{ID: "message-ab", TargetAgentID: "agent-b"})
	if err != nil {
		t.Fatal(err)
	}
	defer finishAB()
	finishBC, err := application.beginAgentWait("agent-b", model.AgentMessage{ID: "message-bc", TargetAgentID: "agent-c"})
	if err != nil {
		t.Fatal(err)
	}
	defer finishBC()
	if _, err := application.beginAgentWait("agent-c", model.AgentMessage{ID: "message-ca", TargetAgentID: "agent-a"}); err == nil || !strings.Contains(err.Error(), "agent-c -> agent-a -> agent-b -> agent-c") {
		t.Fatalf("cycle error = %v", err)
	}
	finishAB()
	finishCA, err := application.beginAgentWait("agent-c", model.AgentMessage{ID: "message-ca", TargetAgentID: "agent-a"})
	if err != nil {
		t.Fatalf("wait after cycle was removed: %v", err)
	}
	finishCA()
}

func TestAgentWaitTimeoutIsBounded(t *testing.T) {
	for _, test := range []struct {
		args map[string]any
		want time.Duration
	}{
		{args: map[string]any{}, want: 60 * time.Second},
		{args: map[string]any{"timeout_seconds": float64(12)}, want: 12 * time.Second},
		{args: map[string]any{"timeout_seconds": float64(0)}, want: time.Second},
		{args: map[string]any{"timeout_seconds": float64(900)}, want: 300 * time.Second},
	} {
		if got := agentWaitTimeout(test.args); got != test.want {
			t.Errorf("agentWaitTimeout(%v) = %v, want %v", test.args, got, test.want)
		}
	}
}

func TestCleanupRemovesDeletedManagedStateAndAllowsRepositoryReadd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	source := createAppRepository(t, root, "source")
	repository, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Disposable", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worktree, ok := dashboard.PrimaryWorktree(agent)
	if !ok {
		t.Fatal("agent has no worktree")
	}
	agentDir := filepath.Join(cfg.StateDir, "agents", agent.ID, "sessions")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "session.jsonl"), []byte("session\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.Store.RegisterAgentRuntime(ctx, agent.ID, "runtime", agent.SessionID, filepath.Join(agentDir, "session.jsonl")); err != nil {
		t.Fatal(err)
	}
	deleted, err := application.DeleteResource(ctx, "repository", repository.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Hidden.Repositories != 1 || deleted.Hidden.Agents != 1 || deleted.Hidden.Worktrees != 1 || deleted.Hidden.Workspaces != 0 {
		t.Fatalf("repository cascade = %#v", deleted)
	}
	if _, err := application.Cleanup(ctx); err == nil || !strings.Contains(err.Error(), "while Pi is active") {
		t.Fatalf("active cleanup error = %v", err)
	}
	if _, err := os.Stat(worktree.Path); err != nil {
		t.Fatalf("blocked cleanup removed worktree: %v", err)
	}
	if err := application.StopRuntime(ctx, agent.ID, "runtime", ""); err != nil {
		t.Fatal(err)
	}
	cleaned, err := application.Cleanup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.Removed.Repositories != 1 || cleaned.Removed.Agents != 1 || cleaned.Removed.Worktrees != 1 || cleaned.Removed.Workspaces != 0 {
		t.Fatalf("cleanup result = %#v", cleaned)
	}
	for _, path := range []string{worktree.Path, filepath.Join(cfg.StateDir, "agents", agent.ID), repository.MirrorPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("managed path still exists: %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(source, "README.md")); err != nil {
		t.Fatalf("source checkout was removed: %v", err)
	}
	dashboard, err = application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Repositories) != 0 || len(dashboard.Agents) != 0 || len(dashboard.Worktrees) != 0 || len(dashboard.Workspaces) != 1 {
		t.Fatalf("dashboard after cleanup = %#v", dashboard)
	}
	readded, reused, err := application.AddRepository(ctx, AddRepositoryRequest{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	if reused || readded.ID == repository.ID {
		t.Fatalf("repository was not re-created: %#v reused=%v", readded, reused)
	}
}

func createAppRepository(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	runAppGit(t, "", "init", "-b", "main", path)
	runAppGit(t, path, "config", "user.name", "Galpon Test")
	runAppGit(t, path, "config", "user.email", "galpon@example.invalid")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAppGit(t, path, "add", "README.md")
	runAppGit(t, path, "commit", "-m", "fixture")
	return path
}

func runAppGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = cwd
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
