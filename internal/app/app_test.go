package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
)

func TestCheckpointMovesDurableStateAndExactDirtyWorktree(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceConfig := config.Config{StateDir: filepath.Join(root, "source-state"), Socket: filepath.Join(root, "source-state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	source, err := Open(ctx, sourceConfig, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}

	repository, _, err := source.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "checkpoint-source")})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := source.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Move me"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := source.CreateAgent(ctx, CreateAgentRequest{Title: "Builder", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := source.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	worktree, ok := dashboard.PrimaryWorktree(agent)
	if !ok {
		t.Fatal("agent has no worktree")
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "README.md"), []byte("staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAppGit(t, worktree.Path, "add", "README.md")
	if err := os.WriteFile(filepath.Join(worktree.Path, "README.md"), []byte("working\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "notes.txt"), []byte("untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, ".gitignore"), []byte("ignored.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "ignored.txt"), []byte("local only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := runAppGitOutput(t, worktree.Path, "status", "--porcelain=v1")

	sessionPath := filepath.Join(sourceConfig.StateDir, "agents", agent.ID, "sessions", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("{\"type\":\"session\",\"id\":%q,\"cwd\":%q}\n", agent.ID, worktree.Path)
	if err := os.WriteFile(sessionPath, []byte(session), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := source.Store.RegisterAgentRuntime(ctx, agent.ID, "runtime", agent.SessionID, sessionPath); err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(root, "state.checkpoint")
	if _, err := source.CreateCheckpoint(ctx, checkpointPath, "test passphrase", true); err == nil || !strings.Contains(err.Error(), "is active") {
		t.Fatalf("active checkpoint error = %v", err)
	}
	if err := source.StopRuntime(ctx, agent.ID, "runtime", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateCheckpoint(ctx, filepath.Join(root, "local-remote.checkpoint"), "test passphrase", false); err == nil || !strings.Contains(err.Error(), "uses local push remote") {
		t.Fatalf("local remote checkpoint error = %v", err)
	}
	if err := source.Store.SetAgentRenderer(ctx, agent.ID, "herdr", "old", "pane-old"); err != nil {
		t.Fatal(err)
	}
	if err := source.Store.SetRenderer(ctx, workspace.ID, "herdr", "old", "workspace-old"); err != nil {
		t.Fatal(err)
	}
	message, err := source.enqueueAgentMessage(ctx, "", agent.ID, "Continue after restore")
	if err != nil {
		t.Fatal(err)
	}
	discarded, err := source.CreateAgent(ctx, CreateAgentRequest{Title: "Discarded", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	discardedWorktree, err := source.Store.Worktree(ctx, discarded.Placement.PrimaryWorktreeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.DeleteResource(ctx, "agent", discarded.ID); err != nil {
		t.Fatal(err)
	}

	created, err := source.CreateCheckpoint(ctx, checkpointPath, "test passphrase", true)
	if err != nil {
		t.Fatal(err)
	}
	if created.Resources.Agents != 1 || created.Resources.Worktrees != 1 || created.GitRefs != 1 || created.DirtyWorktrees != 1 || created.IgnoredFiles != 1 || len(created.Worktrees) != 1 || created.Worktrees[0].Ref == "" {
		t.Fatalf("checkpoint result = %#v", created)
	}
	if _, err := source.Store.Agent(ctx, discarded.ID); !IsNotFound(err) {
		t.Fatalf("discarded agent became visible: %v", err)
	}
	if _, err := os.Stat(discardedWorktree.Path); err != nil {
		t.Fatalf("checkpoint cleaned a hidden worktree: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	targetConfig := config.Config{StateDir: filepath.Join(root, "target-state"), Socket: filepath.Join(root, "target-state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	target, err := Open(ctx, targetConfig, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, target)
	restored, err := target.RestoreCheckpoint(ctx, checkpointPath, "test passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != created.ID || restored.Resources != created.Resources || restored.GitRefs != 1 {
		t.Fatalf("restore result = %#v", restored)
	}
	restoredDashboard, err := target.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restoredDashboard.Agents) != 1 || len(restoredDashboard.Worktrees) != 1 || len(restoredDashboard.Workspaces) != 1 {
		t.Fatalf("restored dashboard = %#v", restoredDashboard)
	}
	if restoredDashboard.Workspaces[0].RendererID != "" || restoredDashboard.Workspaces[0].Renderer != "" {
		t.Fatalf("restored workspace kept renderer state: %#v", restoredDashboard.Workspaces[0])
	}
	restoredAgent := restoredDashboard.Agents[0]
	restoredWorktree := restoredDashboard.Worktrees[0]
	if restoredAgent.ID != agent.ID || restoredAgent.Status != "stopped" || restoredAgent.RuntimeID != "" || restoredAgent.RendererID != "" {
		t.Fatalf("restored agent = %#v", restoredAgent)
	}
	if got := runAppGitOutput(t, restoredWorktree.Path, "status", "--porcelain=v1"); got != statusBefore {
		t.Fatalf("restored status:\n%s\nwant:\n%s", got, statusBefore)
	}
	if data, err := os.ReadFile(filepath.Join(restoredWorktree.Path, "README.md")); err != nil || string(data) != "working\n" {
		t.Fatalf("restored README = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(restoredWorktree.Path, "ignored.txt")); !os.IsNotExist(err) {
		t.Fatalf("ignored file was restored: %v", err)
	}
	restoredView, err := target.Store.AgentView(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restoredView.Agent.SessionPath == sessionPath || !strings.HasPrefix(restoredView.Agent.SessionPath, targetConfig.StateDir) {
		t.Fatalf("restored session path = %s", restoredView.Agent.SessionPath)
	}
	sessionData, err := os.ReadFile(restoredView.Agent.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sessionData), restoredWorktree.Path) || strings.Contains(string(sessionData), worktree.Path) {
		t.Fatalf("restored session header = %s", sessionData)
	}
	if len(restoredView.Messages) != 1 || restoredView.Messages[0].ID != message.ID || restoredView.Messages[0].Status != "queued" {
		t.Fatalf("restored messages = %#v", restoredView.Messages)
	}
	if _, err := target.RestoreCheckpoint(ctx, checkpointPath, "test passphrase"); err == nil || !strings.Contains(err.Error(), "empty Galpon state") {
		t.Fatalf("second restore error = %v", err)
	}
}

func TestStandaloneWorktreeCreatesWorkspaceAndSurvivesSharedAgentDeletion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

	repository, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "human-work")})
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.CreateWorktree(ctx, CreateWorktreeRequest{WorkspaceTitle: "Fix it myself", RepositoryID: repository.ID, Remote: repository.DefaultRemote, Ref: repository.DefaultBranch, FetchFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.Workspace.Title != "Fix it myself" || created.Worktree.WorkspaceID != created.Workspace.ID || created.Worktree.Lifecycle != "workspace" {
		t.Fatalf("created worktree = %#v", created)
	}
	if _, err := os.Stat(filepath.Join(created.Worktree.Path, "README.md")); err != nil {
		t.Fatalf("managed worktree: %v", err)
	}
	second, err := application.CreateWorktree(ctx, CreateWorktreeRequest{WorkspaceID: created.Workspace.ID, RepositoryID: repository.ID, Ref: repository.DefaultBranch})
	if err != nil {
		t.Fatal(err)
	}
	if second.Workspace.ID != created.Workspace.ID || second.Worktree.ID == created.Worktree.ID || second.Worktree.Lifecycle != "workspace" {
		t.Fatalf("existing workspace result = %#v", second)
	}
	dashboard, err := application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Workspaces) != 1 || len(dashboard.Agents) != 0 || len(dashboard.Worktrees) != 2 {
		t.Fatalf("standalone dashboard = %#v", dashboard)
	}

	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Optional helper", WorkspaceID: created.Workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{SourceWorktreeID: created.Worktree.ID, Mode: "share"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Placement.PrimaryWorktreeID != created.Worktree.ID {
		t.Fatalf("shared placement = %#v", agent.Placement)
	}
	deleted, err := application.DeleteResource(ctx, "agent", agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Hidden.Agents != 1 || deleted.Hidden.Worktrees != 0 {
		t.Fatalf("agent deletion removed standalone worktree: %#v", deleted)
	}
	dashboard, err = application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Agents) != 0 || len(dashboard.Worktrees) != 2 {
		t.Fatalf("standalone worktree did not survive: %#v", dashboard)
	}
	if _, ok := dashboard.Worktree(created.Worktree.ID); !ok {
		t.Fatalf("shared standalone worktree did not survive: %#v", dashboard.Worktrees)
	}
}

func TestDeleteResourceClosesDirectAndCascadedAgentViews(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test-renderer", context: "test-context"}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Delete views"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "First", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Second", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []model.Agent{first, second} {
		if err := application.Store.SetAgentRenderer(ctx, agent.ID, renderer.Name(), renderer.Context(), "pane-"+agent.ID); err != nil {
			t.Fatal(err)
		}
		if err := application.Store.RegisterAgentRuntime(ctx, agent.ID, "runtime-"+agent.ID, agent.SessionID, filepath.Join(root, agent.ID+".jsonl")); err != nil {
			t.Fatal(err)
		}
	}
	if err := application.RequestAgentFinish(ctx, first.ID, "wrong-runtime"); err == nil {
		t.Fatal("finish accepted the wrong runtime")
	}
	if err := application.RequestAgentFinish(ctx, first.ID, "runtime-"+first.ID); err != nil {
		t.Fatalf("request finish: %v", err)
	}

	deleted, err := application.DeleteResource(ctx, "agent", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Hidden.Agents != 1 || !slices.Equal(renderer.closed, []string{first.ID}) {
		t.Fatalf("direct deletion = %#v, closed = %v", deleted, renderer.closed)
	}
	deleted, err = application.DeleteResource(ctx, "workspace", workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Hidden.Workspaces != 1 || deleted.Hidden.Agents != 1 || !slices.Equal(renderer.closed, []string{first.ID, second.ID}) {
		t.Fatalf("workspace deletion = %#v, closed = %v", deleted, renderer.closed)
	}
	plan, err := application.Store.DeletedCleanupPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Agents) != 2 {
		t.Fatalf("deleted agents = %#v", plan.Agents)
	}
	for _, agent := range plan.Agents {
		if agent.RuntimeID != "" || agent.Status != "stopped" {
			t.Fatalf("deleted agent still runs: %#v", agent)
		}
	}
}

func TestDeleteResourceKeepsAgentVisibleWhenItsViewCannotClose(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test-renderer", context: "test-context", closeErr: fmt.Errorf("close failed")}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Keep visible"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Still here", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Store.SetAgentRenderer(ctx, agent.ID, renderer.Name(), renderer.Context(), "pane-"+agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.DeleteResource(ctx, "agent", agent.ID); err == nil || !strings.Contains(err.Error(), "close terminal view") {
		t.Fatalf("delete error = %v", err)
	}
	if _, err := application.Store.Agent(ctx, agent.ID); err != nil {
		t.Fatalf("agent was hidden after close failed: %v", err)
	}
}

func TestAgentPlacementSupportsPrivateCopiesSharingSecondariesAndNoWorktree(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

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

func TestCreateAgentToolQueuesInitialPromptBeforeStarting(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test-renderer", context: "test-context"}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Prompted creation"})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Creator", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.handleAgentTool(ctx, creator.ID, "create_agent", map[string]any{
		"title": "Worker", "workspace": workspace.ID, "cwd": root, "prompt": "  Inspect the failure  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := created.(CreateAgentToolResult)
	if !ok {
		t.Fatalf("created agent result = %#v", created)
	}
	if result.CreatedByAgentID != creator.ID || result.Status != "starting" {
		t.Fatalf("created agent = %#v", result.Agent)
	}
	if result.InitialMessage == nil {
		t.Fatalf("created agent has no initial message: %#v", result)
	}
	message := *result.InitialMessage
	if message.SenderAgentID != creator.ID || message.TargetAgentID != result.ID || message.Prompt != "Inspect the failure" || message.Status != "queued" {
		t.Fatalf("initial message = %#v", message)
	}
	stored, err := application.Store.AgentMessage(ctx, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored != message {
		t.Fatalf("stored message = %#v, want %#v", stored, message)
	}
	if !slices.Equal(renderer.opened, []string{result.ID}) {
		t.Fatalf("started agents = %v", renderer.opened)
	}
	encoded := JSON(result)
	if !strings.Contains(encoded, `"initialMessage"`) || !strings.Contains(encoded, `"id":"`+message.ID+`"`) {
		t.Fatalf("tool result JSON = %s", encoded)
	}
}

func TestCleanupAgentsRemovesOnlySelectedAgentsAndRequiresCompleteSubtrees(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test-renderer", context: "test-context"}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

	repository, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "lineage")})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Lineage"})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Creator", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	createdChild, err := application.handleAgentTool(ctx, creator.ID, "create_agent", map[string]any{"title": "Child", "workspace": workspace.ID, "placement_agent": creator.ID})
	if err != nil {
		t.Fatal(err)
	}
	childResult, ok := createdChild.(CreateAgentToolResult)
	if !ok || childResult.CreatedByAgentID != creator.ID {
		t.Fatalf("created child = %#v", createdChild)
	}
	child := childResult.Agent
	createdGrandchild, err := application.handleAgentTool(ctx, child.ID, "create_agent", map[string]any{"title": "Grandchild", "workspace": workspace.ID, "cwd": root})
	if err != nil {
		t.Fatal(err)
	}
	grandchildResult, ok := createdGrandchild.(CreateAgentToolResult)
	if !ok || grandchildResult.CreatedByAgentID != child.ID {
		t.Fatalf("created grandchild = %#v", createdGrandchild)
	}
	grandchild := grandchildResult.Agent
	createdSibling, err := application.handleAgentTool(ctx, creator.ID, "create_agent", map[string]any{"title": "Keep me", "workspace": workspace.ID, "cwd": root})
	if err != nil {
		t.Fatal(err)
	}
	siblingResult, ok := createdSibling.(CreateAgentToolResult)
	if !ok || siblingResult.CreatedByAgentID != creator.ID {
		t.Fatalf("created sibling = %#v", createdSibling)
	}
	sibling := siblingResult.Agent
	unrelated, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Unrelated", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Pending manual cleanup", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.DeleteResource(ctx, "agent", pending.ID); err != nil {
		t.Fatal(err)
	}

	dashboard, err := application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	creatorWorktree, ok := dashboard.PrimaryWorktree(creator)
	if !ok {
		t.Fatal("creator worktree not found")
	}
	childWorktree, ok := dashboard.PrimaryWorktree(child)
	if !ok {
		t.Fatal("child worktree not found")
	}
	for index, agent := range []model.Agent{child, grandchild} {
		paneID := "pane-" + agent.ID
		if err := application.Store.SetAgentRenderer(ctx, agent.ID, renderer.Name(), renderer.Context(), paneID); err != nil {
			t.Fatal(err)
		}
		sessionPath := filepath.Join(cfg.StateDir, "agents", agent.ID, "sessions", "session.jsonl")
		if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sessionPath, []byte("session\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := application.Store.RegisterAgentRuntime(ctx, agent.ID, fmt.Sprintf("runtime-%d", index), agent.SessionID, sessionPath); err != nil {
			t.Fatal(err)
		}
	}
	message, err := application.enqueueAgentMessage(ctx, creator.ID, child.ID, "Consumed result")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := application.handleAgentTool(ctx, creator.ID, "cleanup_agents", map[string]any{"agent_ids": []any{unrelated.ID}}); err == nil || !strings.Contains(err.Error(), "was not created by") {
		t.Fatalf("unrelated cleanup error = %v", err)
	}
	if _, err := application.handleAgentTool(ctx, creator.ID, "cleanup_agents", map[string]any{"agent_ids": []any{child.ID}}); err == nil || !strings.Contains(err.Error(), grandchild.ID) {
		t.Fatalf("incomplete subtree error = %v", err)
	}
	if len(renderer.closed) != 0 {
		t.Fatalf("validation closed agents: %v", renderer.closed)
	}

	cleanupValue, err := application.handleAgentTool(ctx, creator.ID, "cleanup_agents", map[string]any{"agent_ids": []any{child.ID, grandchild.ID}})
	if err != nil {
		t.Fatal(err)
	}
	cleaned, ok := cleanupValue.(model.AgentCleanupResult)
	if !ok || cleaned.Removed.Agents != 2 || cleaned.Removed.Worktrees != 1 || cleaned.ClosedViews != 2 {
		t.Fatalf("cleanup result = %#v", cleanupValue)
	}
	if !slices.Equal(renderer.closed, []string{grandchild.ID, child.ID}) {
		t.Fatalf("closed agents = %v", renderer.closed)
	}
	for _, path := range []string{childWorktree.Path, filepath.Join(cfg.StateDir, "agents", child.ID), filepath.Join(cfg.StateDir, "agents", grandchild.ID)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cleaned path remains %s: %v", path, err)
		}
	}
	if _, err := application.Store.AgentMessage(ctx, message.ID); !IsNotFound(err) {
		t.Fatalf("cleaned message remains: %v", err)
	}
	if _, err := os.Stat(creatorWorktree.Path); err != nil {
		t.Fatalf("creator worktree was removed: %v", err)
	}
	for _, agent := range []model.Agent{creator, sibling, unrelated} {
		if _, err := application.Store.Agent(ctx, agent.ID); err != nil {
			t.Fatalf("surviving agent %s was removed: %v", agent.Title, err)
		}
	}
	plan, err := application.Store.DeletedCleanupPlan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Agents) != 1 || plan.Agents[0].ID != pending.ID {
		t.Fatalf("targeted cleanup changed pending deletions: %#v", plan.Agents)
	}

	singleValue, err := application.handleAgentTool(ctx, creator.ID, "cleanup_agents", map[string]any{"agent_ids": []any{sibling.ID}})
	if err != nil {
		t.Fatal(err)
	}
	single, ok := singleValue.(model.AgentCleanupResult)
	if !ok || single.Removed.Agents != 1 || len(single.Agents) != 1 || single.Agents[0].ID != sibling.ID {
		t.Fatalf("single-agent cleanup result = %#v", singleValue)
	}
	if _, err := application.Store.Agent(ctx, sibling.ID); !IsNotFound(err) {
		t.Fatalf("single selected agent remains: %v", err)
	}
	if _, err := application.Store.Agent(ctx, unrelated.ID); err != nil {
		t.Fatalf("single-agent cleanup removed unrelated agent: %v", err)
	}
}

type cleanupRenderer struct {
	name     string
	context  string
	opened   []string
	closed   []string
	closeErr error
}

func (r *cleanupRenderer) Name() string    { return r.name }
func (r *cleanupRenderer) Context() string { return r.context }
func (r *cleanupRenderer) OpenTerminal(context.Context, model.Workspace, model.Worktree, string, []string) (string, error) {
	return "workspace", nil
}
func (r *cleanupRenderer) OpenAgent(_ context.Context, _ model.Workspace, _ model.Worktree, agent model.Agent, _ []string, _ bool) (string, string, bool, error) {
	r.opened = append(r.opened, agent.ID)
	return "workspace", "pane", false, nil
}
func (r *cleanupRenderer) CloseAgent(_ context.Context, agent model.Agent) error {
	r.closed = append(r.closed, agent.ID)
	return r.closeErr
}
func (r *cleanupRenderer) ReportAgent(context.Context, model.Agent, string, string) error { return nil }

func TestCleanupRemovesDeletedManagedStateAndAllowsRepositoryReadd(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

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

func closeTestApp(t *testing.T, application *App) {
	t.Helper()
	if err := application.Close(); err != nil {
		t.Errorf("close app: %v", err)
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
	_ = runAppGitOutput(t, cwd, args...)
}

func runAppGitOutput(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = cwd
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
