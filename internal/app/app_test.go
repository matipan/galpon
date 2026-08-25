package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
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
	if err := source.StopRuntime(ctx, agent.ID, "runtime", "", ""); err != nil {
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
	message, _, err := source.enqueueAgentMessageIdempotent(ctx, "", agent.ID, "Continue after restore", "checkpoint-send")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Store.RegisterAgentRuntime(ctx, agent.ID, "progress-runtime", agent.SessionID, sessionPath); err != nil {
		t.Fatal(err)
	}
	claimed, err := source.Store.ClaimAgentMessage(ctx, agent.ID, "progress-runtime", "checkpoint-progress-claim")
	if err != nil || claimed == nil || claimed.ID != message.ID {
		t.Fatalf("claim checkpoint progress message = %#v, %v", claimed, err)
	}
	if _, inserted, err := source.ReportWorkProgress(ctx, agent.ID, "progress-runtime", message.ID, claimed.Attempt, model.WorkProgressEvent{Version: 1, EventID: "checkpoint-progress", Phase: "working", Summary: "Verified portable progress"}); err != nil || !inserted {
		t.Fatalf("report checkpoint progress = %v, inserted %v", err, inserted)
	}
	if err := source.StopRuntime(ctx, agent.ID, "progress-runtime", ""); err != nil {
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
	if len(restoredView.Messages) != 1 || restoredView.Messages[0].ID != message.ID || restoredView.Messages[0].Status != "queued" || restoredView.Messages[0].IdempotencyKey != "checkpoint-send" {
		t.Fatalf("restored messages = %#v", restoredView.Messages)
	}
	restoredProgress, err := target.Store.WorkProgressEvents(ctx, message.ID)
	if err != nil || len(restoredProgress) != 1 || restoredProgress[0].RuntimeID != "restored" || restoredProgress[0].Summary != "Verified portable progress" {
		t.Fatalf("restored progress = %#v, %v", restoredProgress, err)
	}
	if _, err := target.RestoreCheckpoint(ctx, checkpointPath, "test passphrase"); err == nil || !strings.Contains(err.Error(), "empty Galpon state") {
		t.Fatalf("second restore error = %v", err)
	}
}

func TestCreateAgentWithoutPlacementUsesManagedDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)

	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Coordination"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.CreateAgent(ctx, CreateAgentRequest{Title: strings.Repeat("a", store.WorkTitleLimit+1), WorkspaceID: workspace.ID}); err != nil {
		t.Fatalf("agent creation rejected a Companion-valid title: %v", err)
	}
	if _, err := application.CreateAgent(ctx, CreateAgentRequest{Title: strings.Repeat("a", 121), WorkspaceID: workspace.ID}); err == nil {
		t.Fatal("agent creation accepted an over-limit title")
	}
	if _, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "misleading\u202etitle", WorkspaceID: workspace.ID}); err == nil {
		t.Fatal("agent creation accepted a bidirectional title")
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Captain", WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	wantDirectory := filepath.Join(cfg.StateDir, "agents", agent.ID, "workspace")
	if agent.Placement.Type != "none" || agent.Placement.CWD != wantDirectory {
		t.Fatalf("managed placement = %#v, want %s", agent.Placement, wantDirectory)
	}
	if info, err := os.Stat(wantDirectory); err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("managed directory = %#v, %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(wantDirectory, "coordination.md"), []byte("plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := application.DeleteResource(ctx, "agent", agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, "agents", agent.ID)); !os.IsNotExist(err) {
		t.Fatalf("managed agent directory survived cleanup: %v", err)
	}
}

func TestCheckpointRestoresManagedAgentDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceConfig := config.Config{StateDir: filepath.Join(root, "source-state"), Socket: filepath.Join(root, "source-state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	source, err := Open(ctx, sourceConfig, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := source.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Portable coordination"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := source.CreateAgent(ctx, CreateAgentRequest{Title: "Chief of staff", WorkspaceID: workspace.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent.Placement.CWD, "brief.txt"), []byte("delegate carefully\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpointPath := filepath.Join(root, "managed-directory.checkpoint")
	created, err := source.CreateCheckpoint(ctx, checkpointPath, "test passphrase", true)
	if err != nil {
		t.Fatal(err)
	}
	if created.Resources.Agents != 1 || created.UnmanagedDirectories != 0 {
		t.Fatalf("checkpoint result = %#v", created)
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
	if restored.Resources.Agents != 1 || restored.UnmanagedDirectories != 0 {
		t.Fatalf("restore result = %#v", restored)
	}
	stored, err := target.Store.Agent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantDirectory := filepath.Join(targetConfig.StateDir, "agents", agent.ID, "workspace")
	if stored.Placement.CWD != wantDirectory {
		t.Fatalf("restored managed directory = %s, want %s", stored.Placement.CWD, wantDirectory)
	}
	if data, err := os.ReadFile(filepath.Join(wantDirectory, "brief.txt")); err != nil || string(data) != "delegate carefully\n" {
		t.Fatalf("restored managed file = %q, %v", data, err)
	}
}

func TestCheckpointRestoresUnmanagedAgentDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sourceConfig := config.Config{StateDir: filepath.Join(root, "source-state"), Socket: filepath.Join(root, "source-state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	source, err := Open(ctx, sourceConfig, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}

	workspace, err := source.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Unmanaged work"})
	if err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "external-agent-directory")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "not-checkpointed.txt"), []byte("external data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent, err := source.CreateAgent(ctx, CreateAgentRequest{Title: "External agent", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: cwd}})
	if err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sourceConfig.StateDir, "agents", agent.ID, "sessions", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, []byte(fmt.Sprintf("{\"type\":\"session\",\"id\":%q,\"cwd\":%q}\n", agent.ID, cwd)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := source.Store.RegisterAgentRuntime(ctx, agent.ID, "runtime", agent.SessionID, sessionPath); err != nil {
		t.Fatal(err)
	}
	if err := source.StopRuntime(ctx, agent.ID, "runtime", "", ""); err != nil {
		t.Fatal(err)
	}

	checkpointPath := filepath.Join(root, "unmanaged.checkpoint")
	created, err := source.CreateCheckpoint(ctx, checkpointPath, "test passphrase", true)
	if err != nil {
		t.Fatal(err)
	}
	if created.Resources.Agents != 1 || created.Resources.Worktrees != 0 || created.GitRefs != 0 || created.UnmanagedDirectories != 1 {
		t.Fatalf("checkpoint result = %#v", created)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(cwd); err != nil {
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
	if restored.Resources.Agents != 1 || restored.UnmanagedDirectories != 1 {
		t.Fatalf("restore result = %#v", restored)
	}
	view, err := target.Store.AgentView(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Agent.Placement.Type != "none" || view.Agent.Placement.CWD != cwd {
		t.Fatalf("restored placement = %#v", view.Agent.Placement)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("restored unmanaged directory is not empty: %#v", entries)
	}
	sessionData, err := os.ReadFile(view.Agent.SessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sessionData), cwd) {
		t.Fatalf("restored session header = %s", sessionData)
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
	if err := application.RequestAgentFinish(ctx, first.ID, "wrong-runtime", ""); err == nil {
		t.Fatal("finish accepted the wrong runtime")
	}
	if err := application.RequestAgentFinish(ctx, first.ID, "runtime-"+first.ID, ""); err != nil {
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

	var backgroundStarted []string
	application.backgroundStart = func(_ context.Context, agent model.Agent) error {
		backgroundStarted = append(backgroundStarted, agent.ID)
		return nil
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Prompted creation"})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Creator", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.handleAgentTool(ctx, creator.ID, "create_agent", map[string]any{
		"title": "Worker", "workspace": workspace.ID, "prompt": "  Inspect the failure  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, ok := created.(CreateAgentToolResult)
	if !ok {
		t.Fatalf("created agent result = %#v", created)
	}
	if result.CreatedByAgentID != creator.ID || result.Presentation != "background" || result.Status != "starting" {
		t.Fatalf("created agent = %#v", result.Agent)
	}
	wantDirectory := filepath.Join(cfg.StateDir, "agents", result.ID, "workspace")
	if result.Placement.Type != "none" || result.Placement.CWD != wantDirectory {
		t.Fatalf("created managed placement = %#v, want %s", result.Placement, wantDirectory)
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
	if len(renderer.opened) != 0 || !slices.Equal(backgroundStarted, []string{result.ID}) {
		t.Fatalf("renderer opened = %v, background started = %v", renderer.opened, backgroundStarted)
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

func TestBackgroundAgentRunsWithoutRendererAndPromotesAfterProcessExit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fakePi := filepath.Join(root, "fake-pi")
	if err := os.WriteFile(fakePi, []byte("#!/bin/sh\ncat >/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	renderer := &cleanupRenderer{name: "test-renderer", context: "test-context"}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: fakePi, PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Background work"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Worker", WorkspaceID: workspace.ID, Presentation: "background", Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := application.StartBackgroundAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Presentation != "background" || started.RendererID != "" || len(renderer.opened) != 0 {
		t.Fatalf("background start = %#v, renderer opened = %v", started, renderer.opened)
	}
	if _, err := application.OpenAgent(ctx, agent.ID, true); err == nil || !strings.Contains(err.Error(), "wait until it is idle") {
		t.Fatalf("running background promotion error = %v", err)
	}
	if err := application.Store.SetAgentStatus(ctx, agent.ID, "idle", ""); err != nil {
		t.Fatal(err)
	}
	promoted, err := application.OpenAgent(ctx, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Presentation != "foreground" || promoted.RendererID == "" || !slices.Equal(renderer.opened, []string{agent.ID}) {
		t.Fatalf("promoted agent = %#v, renderer opened = %v", promoted, renderer.opened)
	}
}

func TestFailedPromotionKeepsAgentInBackground(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test-renderer", context: "test-context", openErr: errors.New("Herdr unavailable")}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Failed promotion"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Worker", WorkspaceID: workspace.ID, Presentation: "background", Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.OpenAgent(ctx, agent.ID, true); err == nil || !strings.Contains(err.Error(), "Herdr unavailable") {
		t.Fatalf("promotion error = %v", err)
	}
	stored, err := application.Store.Agent(ctx, agent.ID)
	if err != nil || stored.Presentation != "background" || stored.RendererID != "" {
		t.Fatalf("failed promotion changed agent = %#v, %v", stored, err)
	}
	started := 0
	application.backgroundStart = func(context.Context, model.Agent) error { started++; return nil }
	if _, err := application.StartBackgroundAgent(ctx, agent.ID); err != nil || started != 1 {
		t.Fatalf("restart after failed promotion = started %d, %v", started, err)
	}
}

func TestQueuedMessageRetriesTemporaryAgentStartFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Retry start"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Worker", WorkspaceID: workspace.ID, Presentation: "background", Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	starts := 0
	application.backgroundStart = func(context.Context, model.Agent) error {
		mu.Lock()
		defer mu.Unlock()
		starts++
		if starts == 1 {
			return errors.New("temporary start failure")
		}
		return nil
	}
	message, err := application.QueueAgentMessage(ctx, "", agent.ID, "work")
	if err != nil || message.Status != "queued" {
		t.Fatalf("queued message = %#v, %v", message, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := starts
		mu.Unlock()
		if count >= 2 {
			stored, readErr := application.Store.Agent(ctx, agent.ID)
			if readErr != nil || stored.Status != "starting" {
				t.Fatalf("retried agent = %#v, %v", stored, readErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent start was not retried")
}

func TestBackgroundAgentCancelsHeadlessDialogRequests(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	responsePath := filepath.Join(root, "response.json")
	t.Setenv("GALPON_TEST_RPC_RESPONSE", responsePath)
	fakePi := filepath.Join(root, "fake-pi")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"type\":\"extension_ui_request\",\"id\":\"dialog-1\",\"method\":\"confirm\"}'\nIFS= read -r response\nprintf '%s\\n' \"$response\" > \"$GALPON_TEST_RPC_RESPONSE\"\ncat >/dev/null\n"
	if err := os.WriteFile(fakePi, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: fakePi, PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Headless dialog"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Worker", WorkspaceID: workspace.ID, Presentation: "background", Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.StartBackgroundAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(responsePath)
		if err == nil {
			var response map[string]any
			if json.Unmarshal(data, &response) != nil || response["id"] != "dialog-1" || response["cancelled"] != true {
				t.Fatalf("dialog response = %s", data)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background dialog was not cancelled")
}

func TestCreatorCleanupCannotRemovePromotedAgent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Protected work"})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Creator", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Promoted", WorkspaceID: workspace.ID, CreatedByAgentID: creator.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Store.SetAgentPresentation(ctx, child.ID, "foreground"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.CleanupAgents(ctx, creator.ID, []string{child.ID}); err == nil || !strings.Contains(err.Error(), "protected from creator cleanup") {
		t.Fatalf("promoted cleanup error = %v", err)
	}
	if _, err := application.Store.Agent(ctx, child.ID); err != nil {
		t.Fatalf("promoted agent was removed: %v", err)
	}
	parent, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Background parent", WorkspaceID: workspace.ID, CreatedByAgentID: creator.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	promotedChild, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Promoted child", WorkspaceID: workspace.ID, CreatedByAgentID: parent.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Store.SetAgentPresentation(ctx, promotedChild.ID, "foreground"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.CleanupAgents(ctx, creator.ID, []string{parent.ID}); err == nil || !strings.Contains(err.Error(), promotedChild.ID) {
		t.Fatalf("ancestor cleanup with promoted descendant error = %v", err)
	}
	if _, err := application.Store.Agent(ctx, parent.ID); err != nil {
		t.Fatalf("blocked ancestor was removed: %v", err)
	}
}

func TestConcurrentOpenAgentStartsPiOnce(t *testing.T) {
	renderer := newBlockingStartRenderer()
	application, agent := newLifecycleTestAgent(t, renderer)
	defer renderer.release()

	runConcurrentAgentCalls(t, renderer, 32, func(_ int) error {
		_, err := application.OpenAgent(context.Background(), agent.ID, true)
		return err
	})
}

func TestConcurrentQueueAgentMessageStartsPiOnce(t *testing.T) {
	renderer := newBlockingStartRenderer()
	application, agent := newLifecycleTestAgent(t, renderer)
	defer renderer.release()

	const calls = 32
	runConcurrentAgentCalls(t, renderer, calls, func(index int) error {
		_, err := application.QueueAgentMessage(context.Background(), "", agent.ID, fmt.Sprintf("work item %d", index))
		return err
	})
	messages, err := application.Store.AgentMessages(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != calls {
		t.Fatalf("queued messages = %d, want %d", len(messages), calls)
	}
}

func newLifecycleTestAgent(t *testing.T, renderer *blockingStartRenderer) (*App, model.Agent) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(context.Background(), cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestApp(t, application) })
	workspace, err := application.CreateWorkspace(context.Background(), CreateWorkspaceRequest{Title: "Lifecycle serialization"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := application.CreateAgent(context.Background(), CreateAgentRequest{Title: "Worker", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "none", CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	return application, agent
}

func runConcurrentAgentCalls(t *testing.T, renderer *blockingStartRenderer, count int, call func(int) error) {
	t.Helper()
	start := make(chan struct{})
	errors := make(chan error, count)
	var ready sync.WaitGroup
	var calls sync.WaitGroup
	ready.Add(count)
	calls.Add(count)
	for index := 0; index < count; index++ {
		go func(callIndex int) {
			defer calls.Done()
			ready.Done()
			<-start
			errors <- call(callIndex)
		}(index)
	}
	ready.Wait()
	close(start)

	select {
	case <-renderer.firstOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("first renderer open did not start")
	}
	// The first renderer call stays blocked here. Every other call must wait on
	// the agent lifecycle lock instead of making another start decision from the
	// same stopped agent snapshot.
	time.Sleep(50 * time.Millisecond)
	if opens, starts, reports := renderer.counts(); opens != 1 || starts != 1 || reports != 0 {
		t.Fatalf("renderer effects while first open is blocked = opens %d, starts %d, reports %d", opens, starts, reports)
	}

	renderer.release()
	done := make(chan struct{})
	go func() {
		calls.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent agent calls did not finish")
	}
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if opens, starts, reports := renderer.counts(); opens != count || starts != 1 || reports != 1 {
		t.Fatalf("renderer effects = opens %d, starts %d, reports %d; want opens %d, starts 1, reports 1", opens, starts, reports, count)
	}
}

type blockingStartRenderer struct {
	mu          sync.Mutex
	firstOpen   chan struct{}
	releaseOpen chan struct{}
	releaseOnce sync.Once
	opens       int
	starts      int
	reports     int
}

func newBlockingStartRenderer() *blockingStartRenderer {
	return &blockingStartRenderer{firstOpen: make(chan struct{}), releaseOpen: make(chan struct{})}
}

func (r *blockingStartRenderer) Name() string    { return "blocking" }
func (r *blockingStartRenderer) Context() string { return "test" }
func (r *blockingStartRenderer) OpenTerminal(context.Context, model.Workspace, model.Worktree, string, []string) (string, error) {
	return "workspace-view", nil
}
func (r *blockingStartRenderer) OpenAgent(_ context.Context, _ model.Workspace, _ model.Worktree, agent model.Agent, _ []string, _ bool) (string, string, bool, error) {
	r.mu.Lock()
	r.opens++
	first := r.opens == 1
	paneID := ""
	if agent.Renderer == r.Name() && agent.RendererContext == r.Context() {
		paneID = agent.RendererID
	}
	newPane := paneID == ""
	start := newPane || (agent.Status != "running" && agent.Status != "starting" && agent.Status != "idle")
	if start {
		r.starts++
	}
	r.mu.Unlock()
	if first {
		close(r.firstOpen)
		<-r.releaseOpen
	}
	return "workspace-view", "agent-view", start, nil
}
func (r *blockingStartRenderer) CloseAgent(context.Context, model.Agent) error { return nil }
func (r *blockingStartRenderer) ReportAgent(_ context.Context, _ model.Agent, status, _ string) error {
	if status == "starting" {
		r.mu.Lock()
		r.reports++
		r.mu.Unlock()
	}
	return nil
}
func (r *blockingStartRenderer) release() {
	r.releaseOnce.Do(func() { close(r.releaseOpen) })
}
func (r *blockingStartRenderer) counts() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opens, r.starts, r.reports
}

type cleanupRenderer struct {
	name     string
	context  string
	opened   []string
	closed   []string
	openErr  error
	closeErr error
}

func (r *cleanupRenderer) Name() string    { return r.name }
func (r *cleanupRenderer) Context() string { return r.context }
func (r *cleanupRenderer) OpenTerminal(context.Context, model.Workspace, model.Worktree, string, []string) (string, error) {
	return "workspace", nil
}
func (r *cleanupRenderer) OpenAgent(_ context.Context, _ model.Workspace, _ model.Worktree, agent model.Agent, _ []string, _ bool) (string, string, bool, error) {
	r.opened = append(r.opened, agent.ID)
	if r.openErr != nil {
		return "", "", false, r.openErr
	}
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
	if err := application.StopRuntime(ctx, agent.ID, "runtime", "", ""); err != nil {
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

func TestAwaitAgentMessageUsesCompletionNotification(t *testing.T) {
	application := companionTestApp(t, "caller-runtime")
	ctx := context.Background()
	target := putWaitTarget(t, application, "target", "target-runtime")
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "notify-message", SenderAgentID: "agent", TargetAgentID: target.ID, Kind: "request", Prompt: "work", Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgentMessage(ctx, message); err != nil {
		t.Fatal(err)
	}

	result := make(chan model.AgentMessage, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := application.AwaitAgentMessage(context.Background(), message.ID)
		result <- value
		errs <- err
	}()
	waitForMessageWaiter(t, application, message.ID)
	claimed, err := application.Store.ClaimAgentMessage(ctx, target.ID, target.RuntimeID, "notify-claim")
	if err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if err := application.CompleteMessage(ctx, target.ID, message.ID, target.RuntimeID, "", claimed.Attempt, "done", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-result:
		if err := <-errs; err != nil || value.Status != "completed" || value.Response != "done" {
			t.Fatalf("await = %#v, %v", value, err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion notification did not wake the waiter")
	}
}

func TestAwaitAgentsAnyReturnsOrderedPartialTypedOutcomes(t *testing.T) {
	application := companionTestApp(t, "caller-runtime")
	ctx := context.Background()
	now := time.Now().UnixMilli()
	ids := []string{"wait-first", "wait-second", "wait-third"}
	targets := make([]model.Agent, len(ids))
	for index, id := range ids {
		targets[index] = putWaitTarget(t, application, fmt.Sprintf("wait-target-%d", index), fmt.Sprintf("wait-runtime-%d", index))
		message := model.AgentMessage{ID: id, SenderAgentID: "agent", TargetAgentID: targets[index].ID, Kind: "request", Prompt: "work", Status: "queued", CreatedAt: now + int64(index), UpdatedAt: now + int64(index)}
		if err := application.Store.PutAgentMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}

	results := make(chan model.AgentWaitManyResult, 1)
	errs := make(chan error, 1)
	go func() {
		value, err := application.awaitAgentToolMessages(context.Background(), "agent", ids, "any", time.Second)
		results <- value
		errs <- err
	}()
	for _, id := range ids {
		waitForMessageWaiter(t, application, id)
	}
	claimed, err := application.Store.ClaimAgentMessage(ctx, targets[1].ID, targets[1].RuntimeID, "any-claim")
	if err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if err := application.CompleteMessage(ctx, targets[1].ID, ids[1], targets[1].RuntimeID, "", claimed.Attempt, "second done", ""); err != nil {
		t.Fatal(err)
	}

	select {
	case value := <-results:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if value.Status != "completed" || value.ReturnWhen != "any" || value.Completed != 1 || value.Total != 3 {
			t.Fatalf("wait result = %#v", value)
		}
		for index, outcome := range value.Outcomes {
			if outcome.ID != ids[index] {
				t.Fatalf("outcome order = %#v", value.Outcomes)
			}
		}
		if value.Outcomes[0].WaitStatus != "pending" || value.Outcomes[1].WaitStatus != "completed" || value.Outcomes[1].MessageStatus != "completed" || value.Outcomes[1].Attempt != 1 || value.Outcomes[1].TargetRuntimeStatus != "running" || value.Outcomes[2].WaitStatus != "pending" {
			t.Fatalf("typed outcomes = %#v", value.Outcomes)
		}
	case <-time.After(time.Second):
		t.Fatal("any wait did not return after one completion")
	}
	for _, id := range []string{ids[0], ids[2]} {
		message, err := application.Store.AgentMessage(ctx, id)
		if err != nil || message.Status != "queued" {
			t.Fatalf("unfinished work %s was changed: %#v, %v", id, message, err)
		}
	}
}

func TestAwaitAgentsTimeoutAndValidation(t *testing.T) {
	application := companionTestApp(t, "caller-runtime")
	target := putWaitTarget(t, application, "timeout-target", "timeout-runtime")
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "timeout-message", SenderAgentID: "agent", TargetAgentID: target.ID, Kind: "request", Prompt: "work", Status: "delivered", RuntimeID: target.RuntimeID, Attempt: 2, CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	value, err := application.awaitAgentToolMessages(t.Context(), "agent", []string{message.ID}, "all", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	outcome := value.Outcomes[0]
	if value.Status != "timeout" || outcome.WaitStatus != "timeout" || outcome.MessageStatus != "delivered" || outcome.Attempt != 2 || outcome.WaitError == nil || outcome.WaitError.Kind != "timeout" {
		t.Fatalf("timeout result = %#v", value)
	}
	stored, err := application.Store.AgentMessage(t.Context(), message.ID)
	if err != nil || stored.Status != "delivered" {
		t.Fatalf("timeout canceled work: %#v, %v", stored, err)
	}
	if _, err := application.awaitAgentToolMessages(t.Context(), "agent", []string{message.ID, message.ID}, "all", time.Second); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := application.awaitAgentToolMessages(t.Context(), "agent", []string{message.ID}, "first", time.Second); err == nil || !strings.Contains(err.Error(), "any or all") {
		t.Fatalf("return_when error = %v", err)
	}
	tooMany := make([]string, 17)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("message-%d", index)
	}
	if _, err := application.awaitAgentToolMessages(t.Context(), "agent", tooMany, "all", time.Second); err == nil || !strings.Contains(err.Error(), "between 1 and 16") {
		t.Fatalf("message limit error = %v", err)
	}
}

func TestAgentWaitBatchRejectsCycleAtomically(t *testing.T) {
	application := &App{}
	finish, err := application.beginAgentWait("agent-b", model.AgentMessage{ID: "b-a", TargetAgentID: "agent-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	if err := application.replaceAgentWaits("agent-a", "batch", []model.AgentMessage{{ID: "a-c", TargetAgentID: "agent-c"}, {ID: "a-b", TargetAgentID: "agent-b"}}); err == nil || !strings.Contains(err.Error(), "agent-a -> agent-b -> agent-a") {
		t.Fatalf("batch cycle error = %v", err)
	}
	application.waitMu.Lock()
	defer application.waitMu.Unlock()
	if len(application.waits["agent-a"]) != 0 {
		t.Fatalf("rejected batch registered partial waits: %#v", application.waits)
	}
}

func TestAgentWaitBatchReplacementRemovesSettledEdges(t *testing.T) {
	application := &App{}
	if err := application.replaceAgentWaits("agent-a", "batch", []model.AgentMessage{{ID: "a-b", TargetAgentID: "agent-b"}, {ID: "a-c", TargetAgentID: "agent-c"}}); err != nil {
		t.Fatal(err)
	}
	if err := application.replaceAgentWaits("agent-a", "batch", []model.AgentMessage{{ID: "a-c", TargetAgentID: "agent-c"}}); err != nil {
		t.Fatal(err)
	}
	finish, err := application.beginAgentWait("agent-b", model.AgentMessage{ID: "b-a", TargetAgentID: "agent-a"})
	if err != nil {
		t.Fatalf("settled edge caused a false cycle: %v", err)
	}
	finish()
}

func TestAgentWaitRegistrationsDoNotReplaceConcurrentEdges(t *testing.T) {
	application := &App{}
	if err := application.replaceAgentWaits("agent-a", "first", []model.AgentMessage{{ID: "same", TargetAgentID: "agent-b"}}); err != nil {
		t.Fatal(err)
	}
	if err := application.replaceAgentWaits("agent-a", "second", []model.AgentMessage{{ID: "same", TargetAgentID: "agent-c"}}); err != nil {
		t.Fatal(err)
	}
	if len(application.waits["agent-a"]) != 2 {
		t.Fatalf("concurrent edges = %#v", application.waits)
	}
	if err := application.replaceAgentWaits("agent-a", "first", nil); err != nil {
		t.Fatal(err)
	}
	if len(application.waits["agent-a"]) != 1 {
		t.Fatalf("removing one wait changed another: %#v", application.waits)
	}
	if err := application.replaceAgentWaits("agent-a", "second", nil); err != nil {
		t.Fatal(err)
	}
	if application.waits["agent-a"] != nil {
		t.Fatalf("wait edges were not removed: %#v", application.waits)
	}
}

func putWaitTarget(t *testing.T, application *App, id, runtimeID string) model.Agent {
	t.Helper()
	now := time.Now().UnixMilli()
	target := model.Agent{ID: id, WorkspaceID: "ws", Title: id, Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()}, Kind: "pi", Status: "idle", SessionID: id, RuntimeID: runtimeID, CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgent(t.Context(), target, nil); err != nil {
		t.Fatal(err)
	}
	return target
}

func waitForMessageWaiter(t *testing.T, application *App, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		application.messageWaiterMu.Lock()
		count := len(application.messageWaiters[id])
		application.messageWaiterMu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("message waiter for %s was not registered", id)
}
