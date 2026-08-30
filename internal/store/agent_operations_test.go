package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestAgentOperationsShowsReceivedAndDelegatedWorkWithCurrentFirst(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	received := activeWorkMessage("agent-ops-received", "captain", "worker", "", "agent-ops-received", "agent-ops-received-run", "notify", 0)
	delegated := activeWorkMessage("agent-ops-delegated", "worker", "reviewer", received.ID, received.ID, received.RunID, "join", 1)
	failed := model.AgentMessage{
		ID: "agent-ops-failed", SenderAgentID: "worker", TargetAgentID: "reviewer", Kind: "request", Act: "request", ResultMode: "notify",
		RootMessageID: "agent-ops-failed", RunID: "agent-ops-failed-run", Status: "failed", TerminalReason: "failed",
		CreatedAt: now - 20, UpdatedAt: now - 10, CompletedAt: now - 10,
	}
	unrelated := activeWorkMessage("agent-ops-unrelated", "captain", "reviewer", "", "agent-ops-unrelated", "agent-ops-unrelated-run", "notify", 0)
	for _, message := range []model.AgentMessage{received, delegated, failed, unrelated} {
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}

	projection, err := s.AgentOperations(t.Context(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Agent.ID != "worker" || projection.Workspace.ID != "work-ws" {
		t.Fatalf("identity = %#v, workspace %#v", projection.Agent, projection.Workspace)
	}
	if len(projection.Current) != 2 {
		t.Fatalf("current work = %#v", projection.Current)
	}
	directions := map[string]string{}
	for _, item := range projection.Current {
		directions[item.ID] = item.Direction
	}
	if directions[received.ID] != "received" || directions[delegated.ID] != "delegated" {
		t.Fatalf("directions = %#v", directions)
	}
	if len(projection.Attention) != 1 || projection.Attention[0].ID != failed.ID || projection.Attention[0].Direction != "delegated" {
		t.Fatalf("attention = %#v", projection.Attention)
	}
	if len(projection.RecentResults) != 1 || projection.RecentResults[0].ID != failed.ID {
		t.Fatalf("recent results = %#v", projection.RecentResults)
	}
	if projection.Summary.Received != 1 || projection.Summary.Delegated != 2 || projection.Summary.Current != 2 || projection.Summary.NeedsAttention != 1 || projection.Summary.Results != 1 || projection.Summary.Failures != 1 {
		t.Fatalf("summary = %#v", projection.Summary)
	}
	if projection.Queue.InboundClaimed != 1 || projection.Queue.InboundClaimedFresh != 1 {
		t.Fatalf("selected-agent queue = %#v", projection.Queue)
	}
	encoded, _ := json.Marshal(projection)
	if strings.Contains(string(encoded), unrelated.ID) || strings.Contains(string(encoded), "runtime") || strings.Contains(string(encoded), "session") {
		t.Fatalf("agent projection exposed unrelated or private data: %s", encoded)
	}
}

func TestAgentOperationsIncludesCrossWorkspaceReceivedWork(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	if err := s.PutWorkspace(t.Context(), model.Workspace{ID: "other-ws", Title: "Other", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAgent(t.Context(), model.Agent{
		ID: "external", WorkspaceID: "other-ws", Title: "External", Presentation: "background",
		Placement: model.AgentPlacement{Type: "none"}, Kind: "pi", Status: "idle", CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
	received := activeWorkMessage("cross-workspace-received", "external", "worker", "", "cross-workspace-received", "cross-workspace-run", "notify", 0)
	if err := s.PutAgentMessage(t.Context(), received); err != nil {
		t.Fatal(err)
	}

	projection, err := s.AgentOperations(t.Context(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Workspace.ID != "work-ws" || len(projection.Current) != 1 || projection.Current[0].ID != received.ID || projection.Current[0].Direction != "received" {
		t.Fatalf("cross-workspace received work = %#v", projection)
	}
}

func TestAgentOperationsIsNotStarvedByUnrelatedWorkspaceRoots(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	for index := 0; index < OperationsMaxRoots+8; index++ {
		id := fmt.Sprintf("unrelated-%03d", index)
		message := activeWorkMessage(id, "captain", "reviewer", "", id, id+"-run", "notify", 0)
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	selected := activeWorkMessage("selected-delegation", "worker", "reviewer", "", "selected-delegation", "selected-run", "notify", 0)
	if err := s.PutAgentMessage(t.Context(), selected); err != nil {
		t.Fatal(err)
	}

	projection, err := s.AgentOperations(t.Context(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Current) != 1 || projection.Current[0].ID != selected.ID || projection.Current[0].Direction != "delegated" {
		t.Fatalf("selected current work = %#v", projection.Current)
	}
	if projection.Truncation.SourceTruncated {
		t.Fatalf("unrelated workspace roots truncated the agent view: %#v", projection.Truncation)
	}
}

func TestAgentOperationsBoundsOlderResultsAndCoordination(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	for index := 0; index < AgentOperationsMaxRecentResults+2; index++ {
		id := strings.Repeat("x", 1) + string(rune('A'+index))
		message := model.AgentMessage{
			ID: "bounded-" + id, SenderAgentID: "worker", TargetAgentID: "reviewer", Kind: "request", Act: "request", ResultMode: "notify",
			RootMessageID: "bounded-" + id, RunID: "bounded-run-" + id, Status: "completed", CreatedAt: now - int64(index+2), UpdatedAt: now - int64(index+1), CompletedAt: now - int64(index+1),
		}
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := s.AgentOperations(t.Context(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.RecentResults) != AgentOperationsMaxRecentResults || projection.Truncation.RecentResultsOmitted != 2 || !projection.Truncation.Truncated {
		t.Fatalf("result bound = %d, truncation %#v", len(projection.RecentResults), projection.Truncation)
	}
}
