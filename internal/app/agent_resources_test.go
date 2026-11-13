package app

import (
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/matipan/galpon/internal/config"
)

func TestAgentFormCreatesAndReusesResources(t *testing.T) {
	root := t.TempDir()
	ctx := t.Context()
	application, err := Open(ctx, config.Config{StateDir: filepath.Join(root, "state")}, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestApp(t, application) })
	source := createAppRepository(t, root, "project")
	sourceURL := (&url.URL{Scheme: "file", Path: source}).String()
	request := CreateAgentRequest{Title: "Builder", WorkspaceTitle: " New work ", Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryURL: sourceURL}}}}
	agent, err := application.CreateAgent(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workspace, ok := dashboard.Workspace(agent.WorkspaceID)
	if !ok || workspace.Title != "New work" || len(dashboard.Repositories) != 1 {
		t.Fatalf("created resources = %+v", dashboard)
	}
	worktree, ok := dashboard.PrimaryWorktree(agent)
	if !ok {
		t.Fatal("created agent has no primary worktree")
	}
	readme, err := os.ReadFile(filepath.Join(worktree.Path, "README.md"))
	if err != nil || string(readme) != "project\n" {
		t.Fatalf("checkout = %q, %v", readme, err)
	}
	if dashboard.Repositories[0].DefaultBranch != "main" || worktree.SourceRemote != "origin" {
		t.Fatalf("repository defaults = %+v, %+v", dashboard.Repositories[0], worktree)
	}

	// Independent submissions reuse setup, but each agent owns its own files.
	var wg sync.WaitGroup
	errors := make(chan error, 3)
	for i := range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := request
			next.Title, next.WorkspaceTitle = fmt.Sprintf("Worker %d", i), "new WORK"
			_, err := application.CreateAgent(ctx, next)
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	dashboard, err = application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Workspaces) != 1 || len(dashboard.Repositories) != 1 || len(dashboard.Agents) != 4 || len(dashboard.Worktrees) != 4 {
		t.Fatalf("reused resources = %+v", dashboard)
	}
	for _, item := range dashboard.Agents {
		if item.WorkspaceID != workspace.ID {
			t.Fatalf("agent uses another workspace: %+v", item)
		}
	}
}

func TestAgentFormFailureRemovesOnlyItsResources(t *testing.T) {
	for _, failure := range []string{"clone", "checkout"} {
		t.Run(failure, func(t *testing.T) {
			e := newWorktreeTestEnvironment(t)
			ctx := t.Context()
			root := filepath.Dir(e.source)
			source := createAppRepository(t, root, "new-project")
			newURL := (&url.URL{Scheme: "file", Path: source}).String()
			worktrees := []AgentPlacementWorktreeRequest{{RepositoryID: e.repository.ID}, {RepositoryURL: newURL}}
			if failure == "checkout" {
				worktrees[1].Ref = "no-such-reference"
			} else {
				missing := (&url.URL{Scheme: "file", Path: filepath.Join(root, "missing")}).String()
				worktrees = append(worktrees, AgentPlacementWorktreeRequest{RepositoryURL: missing})
			}
			before := runAppGitOutput(t, e.repository.MirrorPath, "worktree", "list", "--porcelain")
			request := CreateAgentRequest{Title: "Builder", WorkspaceTitle: "Unfinished work", Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: worktrees}}
			if _, err := e.app.CreateAgent(ctx, request); err == nil {
				t.Fatal("invalid source setup succeeded")
			}
			dashboard, err := e.app.Store.Dashboard(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(dashboard.Workspaces) != 1 || dashboard.Workspaces[0].ID != e.workspace.ID || len(dashboard.Repositories) != 1 || dashboard.Repositories[0].ID != e.repository.ID || len(dashboard.Agents) != 0 || len(dashboard.Worktrees) != 0 {
				t.Fatalf("failed submission changed durable resources: %+v", dashboard)
			}
			mirrors, err := os.ReadDir(filepath.Join(e.config.StateDir, "repositories"))
			if err != nil || len(mirrors) != 1 || mirrors[0].Name() != filepath.Base(e.repository.MirrorPath) {
				t.Fatalf("mirrors after failure = %v, %v", mirrors, err)
			}
			if after := runAppGitOutput(t, e.repository.MirrorPath, "worktree", "list", "--porcelain"); after != before {
				t.Fatalf("existing mirror retained failed worktrees: %s", after)
			}
			request.Placement.Worktrees = []AgentPlacementWorktreeRequest{{RepositoryURL: newURL}}
			if _, err := e.app.CreateAgent(ctx, request); err != nil {
				t.Fatalf("corrected submission failed: %v", err)
			}
		})
	}
}

func TestAgentFormResolvesExistingSourcesWithoutNewResources(t *testing.T) {
	e := newWorktreeTestEnvironment(t)
	ctx := t.Context()
	url := (&url.URL{Scheme: "file", Path: e.source}).String()
	agent, err := e.app.CreateAgent(ctx, CreateAgentRequest{Title: "Builder", WorkspaceTitle: "domain", Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryURL: url}}}})
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := e.app.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worktree, ok := dashboard.PrimaryWorktree(agent)
	if !ok || agent.WorkspaceID != e.workspace.ID || worktree.RepositoryID != e.repository.ID || len(dashboard.Workspaces) != 1 || len(dashboard.Repositories) != 1 {
		t.Fatalf("existing resource lookup = %+v, %+v", agent, worktree)
	}
	creator := e.createAgent(t, "Parent", AgentPlacementRequest{Type: "directory"})
	for _, request := range []CreateAgentRequest{
		{Title: "Child", CreatedByAgentID: creator.ID, WorkspaceTitle: "Other", Placement: AgentPlacementRequest{Type: "directory"}},
		{Title: "Child", CreatedByAgentID: creator.ID, WorkspaceID: e.workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryURL: url}}}},
	} {
		if _, err := e.app.CreateAgent(ctx, request); err == nil {
			t.Fatal("agent-created request accepted user-only resource input")
		}
	}
	if _, err := e.app.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Domain"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.CreateAgent(ctx, CreateAgentRequest{Title: "Ambiguous", WorkspaceTitle: "Domain", Placement: AgentPlacementRequest{Type: "directory"}}); err == nil {
		t.Fatal("ambiguous workspace name selected a workspace")
	}
	if _, err := e.app.CreateAgent(ctx, CreateAgentRequest{Title: "Explicit", WorkspaceID: e.workspace.ID, Placement: AgentPlacementRequest{Type: "directory"}}); err != nil {
		t.Fatalf("explicit workspace selection failed: %v", err)
	}
}
