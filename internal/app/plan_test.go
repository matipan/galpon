package app

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestPlanHandoffCreatesForegroundAgentFromFreshMainAndRetries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test", context: "test"}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	if _, err := application.UpgradeCommunicationV2(ctx, CommunicationUpgradeRequest{Generation: 3, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	repoPath := createAppRepository(t, root, "source")
	repository, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: repoPath})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Plan work"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Planner", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	dashboard, _ := application.Store.Dashboard(ctx)
	sourceTree, _ := dashboard.PrimaryWorktree(source)
	if err := os.WriteFile(filepath.Join(sourceTree.Path, "planner-only"), []byte("do not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A stale local branch must not hide the freshly fetched remote branch.
	runAppGit(t, repository.MirrorPath, "update-ref", "refs/heads/main", "refs/remotes/"+repository.DefaultRemote+"/main")
	// Advance main after the managed mirror was created. Launch must fetch it.
	if err := os.WriteFile(filepath.Join(repoPath, "fresh-main"), []byte("new base"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAppGit(t, repoPath, "add", "fresh-main")
	runAppGit(t, repoPath, "commit", "-m", "advance main")
	request := CreatePlanAgentRequest{
		PlanHandoff: PlanHandoff{SourceAgentID: source.ID, RevisionID: uuid.NewString(), Plan: "# Test plan\n\nImplement on fresh main.  \n"},
		Agent:       CreateAgentRequest{Title: "Implementation", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: repository.ID, Ref: "main", FetchFirst: true}}}},
	}
	first, err := application.CreatePlanAgent(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.CreatePlanAgent(ctx, request)
	if err != nil || first.AgentID != second.AgentID {
		t.Fatalf("handoff retry: %#v; %v", second, err)
	}
	agent, err := application.Store.Agent(ctx, first.AgentID)
	if err != nil || agent.Presentation != "foreground" || agent.CreatedByAgentID != "" || agent.ContextAgentID != "" {
		t.Fatalf("handoff created a background/helper/context fork: %#v; %v", agent, err)
	}
	dashboard, _ = application.Store.Dashboard(ctx)
	worktree, _ := dashboard.PrimaryWorktree(agent)
	if _, err := os.Stat(filepath.Join(worktree.Path, "fresh-main")); err != nil {
		t.Fatal("handoff did not fetch fresh main:", err)
	}
	if _, err := os.Stat(filepath.Join(worktree.Path, "planner-only")); !os.IsNotExist(err) {
		t.Fatal("handoff shared or copied the planner's dirty files")
	}
	if len(dashboard.Agents) != 2 || worktree.ID == sourceTree.ID || agent.Placement.Worktrees[0].Mode != "private" {
		t.Fatal("handoff placement or agent count is wrong")
	}
	state, err := application.Store.DurableState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.PlanLaunches) != 1 || !state.PlanLaunches[0].Created || state.PlanLaunches[0].AgentID != agent.ID {
		t.Fatalf("handoff identity is not durable: %#v", state.PlanLaunches)
	}
	messages := 0
	for _, message := range state.Messages {
		if message.TargetAgentID == agent.ID {
			messages++
			if message.SenderAgentID != "" || !strings.Contains(message.Prompt, request.Plan) {
				t.Fatalf("handoff is not an exact direct user prompt: %#v", message)
			}
		}
	}
	if messages != 1 {
		t.Fatalf("initial prompt count = %d", messages)
	}
	changed := request
	changed.Plan += "changed"
	if _, err := application.CreatePlanAgent(ctx, changed); err == nil {
		t.Fatal("changed content reused an approved revision")
	}
	// A checkpoint must retain launch identity, including retry fencing.
	restored, err := store.Open(filepath.Join(t.TempDir(), "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if err := restored.RestoreDurableState(ctx, state); err != nil {
		t.Fatal(err)
	}
	stored, err := restored.ReservePlanLaunch(ctx, model.PlanLaunch{SourceAgentID: source.ID, RevisionID: request.RevisionID, PlanHash: state.PlanLaunches[0].PlanHash, AgentID: uuid.NewString()})
	if err != nil || stored.AgentID != agent.ID || !stored.Created {
		t.Fatalf("checkpoint lost launch identity: %#v; %v", stored, err)
	}
	// A missing remote branch must fail, not fall back to an old local main.
	runAppGit(t, repoPath, "branch", "-m", "main", "renamed")
	missing := request
	missing.RevisionID = uuid.NewString()
	if _, err := application.CreatePlanAgent(ctx, missing); err == nil {
		t.Fatal("a missing remote main silently used stale local main")
	}
}

func TestPlanDirectMessageRetriesKeepAdmissionIdentity(t *testing.T) {
	application := communicationRuntimeTestApp(t)
	putCommunicationAgent(t, application, "implementer")
	if _, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 3, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		message model.AgentMessage
		fresh   bool
		err     error
	}
	results := make(chan result, 8)
	var group sync.WaitGroup
	for index := range 8 {
		group.Go(func() {
			time.Sleep(time.Duration(index) * time.Millisecond)
			message, fresh, err := application.queueDirectCoordinationMessage(t.Context(), "implementer", "Approved plan", "plan-do:revision", nil, 3)
			results <- result{message, fresh, err}
		})
	}
	group.Wait()
	close(results)
	id, deadline, admitted := "", int64(0), 0
	for value := range results {
		if value.err != nil {
			t.Fatal(value.err)
		}
		if id == "" {
			id, deadline = value.message.ID, value.message.QueueDeadlineAt
		}
		if value.message.ID != id || value.message.QueueDeadlineAt != deadline {
			t.Fatal("retry changed admission identity or deadline")
		}
		if value.fresh {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("new admissions = %d", admitted)
	}
	if _, _, err := application.queueDirectCoordinationMessage(t.Context(), "implementer", "Changed plan", "plan-do:revision", nil, 3); err == nil {
		t.Fatal("accepted different work under the same key")
	}
}

func TestPlanHandoffValidation(t *testing.T) {
	base := PlanHandoff{SourceAgentID: uuid.NewString(), RevisionID: uuid.NewString(), Plan: "# Valid\n\nKeep exact text.  \n"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", " \n", strings.Repeat("x", MaxPlanBytes+1), "# Bad\x1b[2J", strings.Repeat("x\n", 2048)} {
		value := base
		value.Plan = text
		if value.Validate() == nil {
			t.Fatalf("accepted invalid plan length %d", len(text))
		}
	}
}
