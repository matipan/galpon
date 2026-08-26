package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestWorkspaceOperationsDeduplicatesCausalRootsAndSeparatesReports(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	root := activeWorkMessage("operations-root", "captain", "worker", "", "operations-root", "operations-run", "notify", 0)
	child := activeWorkMessage("operations-child", "worker", "reviewer", root.ID, root.ID, root.RunID, "join", 1)
	child.LeaseExpiresAt = now - 1
	for _, message := range []model.AgentMessage{root, child} {
		if err := s.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.PutWorkProgress(context.Background(), "worker", "worker-runtime", 1, model.WorkProgressEvent{
		MessageID: root.ID, EventID: "operations-blocker", Version: 1, Phase: "blocked",
		Summary: "Waiting for a product choice", Blocker: "Choose the safe label",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutConversationEvents(context.Background(), "worker", "worker-runtime", []model.ConversationEvent{{
		EventID: "operations-activity", RuntimeSeq: 1, Kind: "tool_execution_start", ToolName: "read", Content: "private path and output", CreatedAt: root.ClaimedAt + 1,
	}}); err != nil {
		t.Fatal(err)
	}

	projection, err := s.WorkspaceOperations(context.Background(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Version != 1 || projection.Workspace.Title != "Work" {
		t.Fatalf("projection identity = %#v", projection)
	}
	if len(projection.Work) != 1 || projection.Work[0].ID != root.ID || len(projection.Work[0].Children) != 1 {
		t.Fatalf("causal roots were duplicated: %#v", projection.Work)
	}
	if projection.Work[0].Priority != "reported_blocker" || projection.Work[0].Checkpoint == nil || projection.Work[0].Checkpoint.Source != "reported" {
		t.Fatalf("reported blocker priority = %#v", projection.Work[0])
	}
	if projection.Work[0].Observation.Source != "observed" || projection.Work[0].Observation.State != "started" || projection.Work[0].Observation.LeaseObservedAt == 0 {
		t.Fatalf("observed delivery facts = %#v", projection.Work[0].Observation)
	}
	if projection.Work[0].Activity == nil || projection.Work[0].Activity.Category != "tool: read" || projection.Work[0].Activity.Source != "observed" {
		t.Fatalf("observed activity = %#v", projection.Work[0].Activity)
	}
	if projection.Summary.ReportedBlockers != 1 || projection.Summary.StaleObservations != 1 {
		t.Fatalf("summary = %#v", projection.Summary)
	}
	workerFound := false
	for _, agent := range projection.Agents {
		if agent.ID == "worker" {
			workerFound = true
			if agent.CurrentDelivery == nil || agent.CurrentDelivery.WorkID != root.ID || agent.CurrentDelivery.Activity == nil || agent.CurrentDelivery.Activity.Category != "tool: read" {
				t.Fatalf("worker delivery = %#v", agent.CurrentDelivery)
			}
		}
	}
	if !workerFound {
		t.Fatal("worker was not in the runtime matrix")
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private prompt must not enter projection", "private path and output", "worker-runtime", "reviewer-runtime", "sessionPath", "runtimeId", "stuck"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("operations projection exposed %q: %s", secret, encoded)
		}
	}
}

func TestWorkspaceOperationsUsesDeterministicPriorityOrder(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	completed := model.AgentMessage{ID: "completed", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: "completed", RunID: "completed-run", Status: "completed", Prompt: "private", CreatedAt: now, UpdatedAt: now, CompletedAt: now}
	queued := completed
	queued.ID, queued.RootMessageID, queued.RunID = "queued", "queued", "queued-run"
	queued.Status, queued.CompletedAt = "queued", 0
	for _, message := range []model.AgentMessage{completed, queued} {
		if err := s.PutAgentMessage(context.Background(), message); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := s.WorkspaceOperations(context.Background(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Work) != 2 || projection.Work[0].ID != queued.ID || projection.Work[0].Priority != "queued" || projection.Work[1].Priority != "recent_completion" {
		t.Fatalf("priority order = %#v", projection.Work)
	}
	if projection.Truncation.MaxAgents != OperationsMaxAgents || projection.Truncation.MaxItems != OperationsMaxItems {
		t.Fatalf("bounds = %#v", projection.Truncation)
	}
}
