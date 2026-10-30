package app

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

type worktreeTestEnvironment struct {
	app        *App
	config     config.Config
	source     string
	repository model.Repository
	workspace  model.Workspace
}

func newWorktreeTestEnvironment(t *testing.T) worktreeTestEnvironment {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestApp(t, application) })
	source := createAppRepository(t, root, "source")
	repository, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: source})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Domain"})
	if err != nil {
		t.Fatal(err)
	}
	return worktreeTestEnvironment{app: application, config: cfg, source: source, repository: repository, workspace: workspace}
}

func (e worktreeTestEnvironment) createAgent(t *testing.T, title string, placement AgentPlacementRequest) model.Agent {
	t.Helper()
	agent, err := e.app.CreateAgent(context.Background(), CreateAgentRequest{Title: title, WorkspaceID: e.workspace.ID, Placement: placement})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func (e worktreeTestEnvironment) repositoryPlacement() AgentPlacementRequest {
	return AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: e.repository.ID}}}
}

func (e worktreeTestEnvironment) agentWorktrees(t *testing.T, agentID string) []model.Worktree {
	t.Helper()
	view, err := e.app.Store.AgentView(context.Background(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	return view.Worktrees
}

func commitInWorktree(t *testing.T, path, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAppGit(t, path, "add", name)
	runAppGit(t, path, "-c", "user.name=Galpon Test", "-c", "user.email=galpon@example.invalid", "commit", "-m", name)
	return runAppGitOutput(t, path, "rev-parse", "HEAD")
}

func TestAddAgentWorktreeCreatesItInTheAgentFolder(t *testing.T) {
	ctx := context.Background()
	env := newWorktreeTestEnvironment(t)
	agent := env.createAgent(t, "Billing", env.repositoryPlacement())
	primary := env.agentWorktrees(t, agent.ID)[0]
	folder := filepath.Dir(primary.Path)

	feature, err := env.app.AddAgentWorktree(ctx, agent.ID, AddAgentWorktreeRequest{Branch: "feature/invoices"})
	if err != nil {
		t.Fatal(err)
	}
	review, err := env.app.AddAgentWorktree(ctx, agent.ID, AddAgentWorktreeRequest{Base: "main", Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []AddAgentWorktreeResult{feature, review} {
		if result.Folder != folder || filepath.Dir(result.Worktree.Path) != folder {
			t.Fatalf("worktree %s is outside the agent folder %s", result.Worktree.Path, folder)
		}
	}
	if branch := runAppGitOutput(t, feature.Worktree.Path, "branch", "--show-current"); branch != "feature/invoices" {
		t.Fatalf("feature branch = %q", branch)
	}
	mainHead := runAppGitOutput(t, env.source, "rev-parse", "HEAD")
	if head := runAppGitOutput(t, review.Worktree.Path, "rev-parse", "HEAD"); head != mainHead {
		t.Fatalf("review head = %s, want %s", head, mainHead)
	}
	if branch := runAppGitOutput(t, review.Worktree.Path, "branch", "--show-current"); branch != "" {
		t.Fatalf("review worktree is on branch %q, want a detached checkout", branch)
	}

	worktrees := env.agentWorktrees(t, agent.ID)
	if len(worktrees) != 3 || worktrees[0].ID != primary.ID || worktrees[1].ID != feature.Worktree.ID || worktrees[2].ID != review.Worktree.ID {
		t.Fatalf("placement worktrees = %#v", worktrees)
	}
	stored, err := env.app.Store.Agent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Placement.PrimaryWorktreeID != primary.ID {
		t.Fatalf("primary worktree changed to %s", stored.Placement.PrimaryWorktreeID)
	}
	for _, assignment := range stored.Placement.Worktrees[1:] {
		if assignment.Mode != "private" {
			t.Fatalf("added worktree assignment = %#v", assignment)
		}
	}
	if _, err := env.app.AddAgentWorktree(ctx, agent.ID, AddAgentWorktreeRequest{Branch: "feature/invoices"}); err == nil {
		t.Fatal("a branch that another worktree has checked out was added again")
	}
}

func TestRemoveAgentWorktreeKeepsThePrimaryWorktreeAndUnpublishedWork(t *testing.T) {
	ctx := context.Background()
	env := newWorktreeTestEnvironment(t)
	agent := env.createAgent(t, "Billing", env.repositoryPlacement())
	primary := env.agentWorktrees(t, agent.ID)[0]
	added, err := env.app.AddAgentWorktree(ctx, agent.ID, AddAgentWorktreeRequest{Branch: "feature/local"})
	if err != nil {
		t.Fatal(err)
	}
	head := commitInWorktree(t, added.Worktree.Path, "local.txt")
	if err := os.WriteFile(filepath.Join(added.Worktree.Path, "notes.txt"), []byte("draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := env.app.RemoveAgentWorktree(ctx, agent.ID, RemoveAgentWorktreeRequest{Worktree: primary.Path}); err == nil || !IsInvalidRequest(err) {
		t.Fatalf("primary worktree removal error = %v", err)
	}
	if _, err := env.app.RemoveAgentWorktree(ctx, agent.ID, RemoveAgentWorktreeRequest{Worktree: added.Worktree.Path}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("removal with uncommitted changes error = %v", err)
	}
	if len(env.agentWorktrees(t, agent.ID)) != 2 {
		t.Fatal("a refused removal changed the placement")
	}
	removed, err := env.app.RemoveAgentWorktree(ctx, agent.ID, RemoveAgentWorktreeRequest{Worktree: added.Worktree.ID, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed.KeptBranches) != 1 || removed.KeptBranches[0] != "feature/local" {
		t.Fatalf("removal result = %#v", removed)
	}
	if kept := runAppGitOutput(t, "", "--git-dir", env.repository.MirrorPath, "rev-parse", "refs/heads/feature/local"); kept != head {
		t.Fatalf("kept branch points to %s, want %s", kept, head)
	}
	if _, err := os.Stat(added.Worktree.Path); !os.IsNotExist(err) {
		t.Fatalf("removed worktree still exists: %v", err)
	}
	if worktrees := env.agentWorktrees(t, agent.ID); len(worktrees) != 1 || worktrees[0].ID != primary.ID {
		t.Fatalf("placement after removal = %#v", worktrees)
	}

	shared, err := env.app.AddAgentWorktree(ctx, agent.ID, AddAgentWorktreeRequest{Branch: "feature/shared"})
	if err != nil {
		t.Fatal(err)
	}
	env.createAgent(t, "Reviewer", AgentPlacementRequest{Type: "agent", SourceAgentID: agent.ID, Share: true})
	if _, err := env.app.RemoveAgentWorktree(ctx, agent.ID, RemoveAgentWorktreeRequest{Worktree: shared.Worktree.Path}); err == nil || !strings.Contains(err.Error(), "Reviewer") {
		t.Fatalf("shared worktree removal error = %v", err)
	}
}

func TestPlacementCopyStartsFromTheCurrentCommitOfEveryWorktree(t *testing.T) {
	ctx := context.Background()
	env := newWorktreeTestEnvironment(t)
	source := env.createAgent(t, "Billing", env.repositoryPlacement())
	primary := env.agentWorktrees(t, source.ID)[0]
	runAppGit(t, primary.Path, "switch", "-c", "feature/next")
	primaryHead := commitInWorktree(t, primary.Path, "next.txt")
	extra, err := env.app.AddAgentWorktree(ctx, source.ID, AddAgentWorktreeRequest{Branch: "feature/parallel"})
	if err != nil {
		t.Fatal(err)
	}
	extraHead := commitInWorktree(t, extra.Worktree.Path, "parallel.txt")

	dashboard, err := env.app.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dashboard = env.app.withCheckouts(ctx, dashboard)
	if worktree, _ := dashboard.Worktree(primary.ID); worktree.CheckoutLabel() != "feature/next" {
		t.Fatalf("dashboard checkout = %q, want feature/next", worktree.CheckoutLabel())
	}
	stored, _ := dashboard.Agent(source.ID)
	if description := PlacementDescription(dashboard, stored); !strings.Contains(description, primary.Path+" on branch feature/next") {
		t.Fatalf("placement description = %q", description)
	}

	copied := env.createAgent(t, "Billing follow-up", AgentPlacementRequest{Type: "agent", SourceAgentID: source.ID})
	worktrees := env.agentWorktrees(t, copied.ID)
	if len(worktrees) != 2 {
		t.Fatalf("copy has %d worktrees, want 2", len(worktrees))
	}
	for index, want := range []string{primaryHead, extraHead} {
		if head := runAppGitOutput(t, worktrees[index].Path, "rev-parse", "HEAD"); head != want {
			t.Fatalf("copied worktree %d starts at %s, want %s", index, head, want)
		}
	}
}

func TestDirectoryAgentWorktreesAreCopiedAndCleanedWithTheAgent(t *testing.T) {
	ctx := context.Background()
	env := newWorktreeTestEnvironment(t)
	agent := env.createAgent(t, "Reviewer", AgentPlacementRequest{Type: "directory"})
	if _, err := env.app.AddAgentWorktree(ctx, agent.ID, AddAgentWorktreeRequest{}); err == nil || !IsInvalidRequest(err) {
		t.Fatalf("worktree without a repository error = %v", err)
	}
	review, err := env.app.AddAgentWorktree(ctx, agent.ID, AddAgentWorktreeRequest{Repository: env.repository.Title, Base: "main", Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := env.app.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := dashboard.Agent(agent.ID)
	folder := AgentFolder(env.config.StateDir, dashboard, stored)
	if review.Folder != folder || filepath.Dir(review.Worktree.Path) != folder {
		t.Fatalf("review worktree %s is outside the agent folder %s", review.Worktree.Path, folder)
	}
	if stored.Placement.Type != "none" || stored.Placement.PrimaryWorktreeID != "" {
		t.Fatalf("directory placement changed: %#v", stored.Placement)
	}
	if err := os.WriteFile(filepath.Join(folder, "scratch", "notes.txt"), []byte("notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	copied := env.createAgent(t, "Second reviewer", AgentPlacementRequest{Type: "agent", SourceAgentID: agent.ID})
	if copied.Placement.Type != "none" || copied.Placement.CWD == agent.Placement.CWD || len(copied.Placement.Worktrees) != 1 {
		t.Fatalf("copied placement = %#v", copied.Placement)
	}
	copiedReview := env.agentWorktrees(t, copied.ID)[0]
	if head := runAppGitOutput(t, copiedReview.Path, "rev-parse", "HEAD"); head != runAppGitOutput(t, review.Worktree.Path, "rev-parse", "HEAD") {
		t.Fatalf("copied review worktree starts at %s", head)
	}

	if _, err := env.app.DeleteResource(ctx, "agent", agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.app.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{review.Worktree.Path, folder} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup left %s: %v", path, err)
		}
	}
	if _, err := os.Stat(copiedReview.Path); err != nil {
		t.Fatalf("cleanup removed the copy's worktree: %v", err)
	}
}
