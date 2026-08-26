package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	if projection.Work[0].Activity != nil || projection.Activity == nil || projection.Activity.Version != 1 || len(projection.Activity.Facts) != 1 || projection.Activity.Facts[0].Category != "tool: read" || projection.Activity.Facts[0].Source != "observed" {
		t.Fatalf("separate observed activity lane = item %#v, lane %#v", projection.Work[0].Activity, projection.Activity)
	}
	if projection.Summary.ReportedBlockers != 1 || projection.Summary.StaleObservations != 1 {
		t.Fatalf("summary = %#v", projection.Summary)
	}
	workerFound := false
	for _, agent := range projection.Agents {
		if agent.ID == "worker" {
			workerFound = true
			if agent.CurrentDelivery == nil || agent.CurrentDelivery.WorkID != root.ID || agent.CurrentDelivery.Activity != nil {
				t.Fatalf("worker delivery = %#v", agent.CurrentDelivery)
			}
		}
	}
	if !workerFound {
		t.Fatal("worker was not in the runtime matrix")
	}
	for _, agent := range projection.Agents {
		if agent.ID == "reviewer" {
			if agent.CurrentDelivery != nil {
				t.Fatalf("stale delivery became current: %#v", agent.CurrentDelivery)
			}
			if agent.ObservedDelivery == nil || agent.ObservedDelivery.Observation.Lease != "stale" {
				t.Fatalf("stale lease was lost from runtime facts: %#v", agent.ObservedDelivery)
			}
		}
	}
	if projection.Queue.InboundClaimed != 2 || projection.Queue.InboundClaimedFresh != 1 {
		t.Fatalf("queue facts = %#v", projection.Queue)
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

func TestWorkspaceOperationsKeepsQueuedStaleAndTerminalCheckpointsHistorical(t *testing.T) {
	for _, state := range []struct {
		name, status, lease, priority string
	}{
		{name: "queued", status: "queued", lease: "none", priority: "queued"},
		{name: "stale", status: "delivered", lease: "stale", priority: "stale_observation"},
		{name: "terminal", status: "completed", lease: "none", priority: "recent_completion"},
	} {
		t.Run(state.name, func(t *testing.T) {
			s := testStore(t)
			workFixture(t, s)
			message := activeWorkMessage("historical-checkpoint", "captain", "worker", "", "historical-checkpoint", "historical-run", "notify", 0)
			if err := s.PutAgentMessage(t.Context(), message); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.PutWorkProgress(t.Context(), "worker", "worker-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "blocked", Version: 1, Phase: "blocked", Summary: "Historical report", Blocker: "Historical blocker"}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UnixMilli()
			switch state.status {
			case "queued":
				_, _ = s.db.Exec(`update agent_messages set status='queued',runtime_id='',lease_expires_at=0,updated_at=? where id=?`, now, message.ID)
			case "delivered":
				_, _ = s.db.Exec(`update agent_messages set lease_expires_at=?,updated_at=? where id=?`, now-1, now, message.ID)
			case "completed":
				_, _ = s.db.Exec(`update agent_messages set status='completed',lease_expires_at=0,completed_at=?,updated_at=? where id=?`, now, now, message.ID)
			}
			projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
			if err != nil || len(projection.Work) != 1 {
				t.Fatalf("projection = %#v, %v", projection, err)
			}
			item := projection.Work[0]
			if item.Checkpoint != nil || item.Priority != state.priority || projection.Summary.ReportedBlockers != 0 {
				t.Fatalf("historical checkpoint classified current work: %#v, summary %#v", item, projection.Summary)
			}
			if !slices.ContainsFunc(item.Timeline, func(event model.WorkTimelineEvent) bool {
				return event.Source == "reported" && event.Label == "Historical report"
			}) {
				t.Fatalf("historical report missing from timeline: %#v", item.Timeline)
			}
			for _, agent := range projection.Agents {
				if agent.ID == "worker" && agent.CurrentDelivery != nil {
					t.Fatalf("%s work became current delivery: %#v", state.name, agent.CurrentDelivery)
				}
			}
		})
	}
}

func TestWorkspaceOperationsPrioritizesWorkOutsideRuntimeMatrix(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	for index := 0; index < OperationsMaxAgents+4; index++ {
		putWorkAgent(t, s, fmt.Sprintf("matrix-%03d", index), fmt.Sprintf("Matrix %03d", index), fmt.Sprintf("matrix-runtime-%03d", index))
	}
	putWorkAgent(t, s, "outside-matrix", "Outside matrix", "outside-runtime")
	if _, err := s.db.Exec(`update agents set status='stopped',updated_at=1 where id='outside-matrix'`); err != nil {
		t.Fatal(err)
	}
	message := activeWorkMessage("outside-work", "captain", "outside-matrix", "", "outside-work", "outside-run", "notify", 0)
	message.RuntimeID = "outside-runtime"
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PutWorkProgress(t.Context(), "outside-matrix", "outside-runtime", 1, model.WorkProgressEvent{MessageID: message.ID, EventID: "outside-blocker", Version: 1, Phase: "blocked", Summary: "Global blocker", Blocker: "Choose globally"}); err != nil {
		t.Fatal(err)
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Agents) != OperationsMaxAgents || projection.Truncation.AgentsOmitted == 0 || slices.ContainsFunc(projection.Agents, func(agent model.OperationsAgent) bool { return agent.ID == "outside-matrix" }) {
		t.Fatalf("runtime matrix bound = agents %d truncation %#v", len(projection.Agents), projection.Truncation)
	}
	if len(projection.Work) == 0 || projection.Work[0].ID != message.ID || projection.Work[0].Priority != "reported_blocker" {
		t.Fatalf("global work priority lost outside matrix: %#v", projection.Work)
	}
}

func TestWorkspaceOperationsReportsUnknownSourceOmissionsTruthfully(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	for index := 0; index < OperationsSourceScanLimit+2; index++ {
		message := model.AgentMessage{ID: fmt.Sprintf("source-%03d", index), SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: fmt.Sprintf("source-%03d", index), RunID: fmt.Sprintf("run-%03d", index), Prompt: "private", Status: "queued", CreatedAt: now + int64(index), UpdatedAt: now + int64(index)}
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Truncation.SourceTruncated || projection.Truncation.RootsOmissionExact || projection.Truncation.ItemsOmissionExact || projection.Truncation.TimelineOmissionExact || projection.Summary.WorkCountsExact || projection.Activity == nil || !projection.Activity.Truncation.Truncated || projection.Activity.Truncation.OmissionExact {
		t.Fatalf("unknown source omission was reported as exact: %#v, summary %#v", projection.Truncation, projection.Summary)
	}
}

func TestWorkspaceOperationsFencesAndRedactsSafeActivity(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	message := activeWorkMessage("activity-fence", "captain", "worker", "", "activity-fence", "activity-fence-run", "notify", 0)
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into conversation_events(agent_id,event_id,runtime_id,runtime_seq,kind,content,tool_name,created_at) values(?,?,?,?,?,?,?,?)`, "worker", "old-attempt", "worker-runtime", 1, "tool_execution_start", "private old output", "read", message.ClaimedAt-1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into conversation_events(agent_id,event_id,runtime_id,runtime_seq,kind,content,tool_name,created_at) values(?,?,?,?,?,?,?,?)`, "worker", "wrong-runtime", "wrong-runtime", 2, "tool_execution_start", "private wrong output", "write", message.ClaimedAt+2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutConversationEvents(t.Context(), "worker", "worker-runtime", []model.ConversationEvent{{EventID: "current-runtime", RuntimeSeq: 3, Kind: "tool_execution_end", Content: "private path secret output", ToolName: "read", CreatedAt: message.ClaimedAt + 3}}); err != nil {
		t.Fatal(err)
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil || projection.Activity == nil || len(projection.Activity.Facts) != 1 {
		t.Fatalf("activity lane = %#v, %v", projection.Activity, err)
	}
	fact := projection.Activity.Facts[0]
	if fact.Category != "tool: read" || fact.Status != "completed" || fact.Source != "observed" {
		t.Fatalf("activity fact = %#v", fact)
	}
	encoded, _ := json.Marshal(projection.Activity)
	for _, forbidden := range []string{"private", "path", "secret", "output", "wrong-runtime", "worker-runtime", "old-attempt", "agentTitle", "workTitle"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("activity exposed %q: %s", forbidden, encoded)
		}
	}
	if _, err := s.db.Exec(`update agent_messages set lease_expires_at=? where id=?`, time.Now().Add(-time.Second).UnixMilli(), message.ID); err != nil {
		t.Fatal(err)
	}
	projection, err = s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil || projection.Activity != nil && len(projection.Activity.Facts) != 0 {
		t.Fatalf("stale delivery retained current activity: %#v, %v", projection.Activity, err)
	}
}

func TestWorkspaceOperationsBoundsActivityLaneWithExactOmissions(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	for index := 0; index < OperationsMaxActivityFacts+2; index++ {
		agentID := fmt.Sprintf("activity-agent-%02d", index)
		runtimeID := fmt.Sprintf("activity-runtime-%02d", index)
		putWorkAgent(t, s, agentID, fmt.Sprintf("Activity agent %02d", index), runtimeID)
		message := activeWorkMessage(fmt.Sprintf("activity-work-%02d", index), "captain", agentID, "", fmt.Sprintf("activity-work-%02d", index), fmt.Sprintf("activity-run-%02d", index), "notify", 0)
		message.RuntimeID = runtimeID
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutConversationEvents(t.Context(), agentID, runtimeID, []model.ConversationEvent{{EventID: "read", RuntimeSeq: 1, Kind: "tool_execution_start", ToolName: "read", CreatedAt: message.ClaimedAt + 1}}); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil || projection.Activity == nil {
		t.Fatalf("activity lane = %#v, %v", projection.Activity, err)
	}
	if len(projection.Activity.Facts) != OperationsMaxActivityFacts || !projection.Activity.Truncation.Truncated || projection.Activity.Truncation.FactsOmitted != 2 || !projection.Activity.Truncation.OmissionExact {
		t.Fatalf("activity truncation = %#v", projection.Activity)
	}
}

func TestWorkspaceOperationsShowsResultReadyBeforeQueueProjection(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "ready-only", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: "ready-only", RunID: "ready-only-run", Prompt: "private", Status: "completed", NotificationState: "pending", CompletedAt: now, CreatedAt: now - 1, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if err := s.PutLifecycleEvent(t.Context(), model.LifecycleEvent{ID: "message-result:ready-only", EventType: "message.result", SubjectAgentID: "worker", RecipientAgentID: "captain", MessageID: message.ID, Payload: "private result", Status: "pending", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil || len(projection.Work) != 1 || projection.Work[0].Result == nil || projection.Work[0].Result.Stage != "result_ready" || projection.Queue.ResultsReady != 1 {
		t.Fatalf("result-ready fact = work %#v queue %#v err %v", projection.Work, projection.Queue, err)
	}
}

func TestWorkspaceOperationsShowsQueuedUnconsumedResultFacts(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	message := activeWorkMessage("result-ready", "captain", "worker", "", "result-ready", "result-ready-run", "notify", 0)
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAgentMessage(t.Context(), message.ID, "worker", "worker-runtime", 1, "done", ""); err != nil {
		t.Fatal(err)
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil || len(projection.Work) != 1 {
		t.Fatalf("projection = %#v, %v", projection, err)
	}
	if projection.Work[0].Result == nil || projection.Work[0].Result.Stage != "delivery_queued" || projection.Queue.ResultDeliveries != 1 || projection.Queue.InboundQueued != 1 {
		t.Fatalf("queued unconsumed result facts = result %#v queue %#v", projection.Work[0].Result, projection.Queue)
	}
	claimed, err := s.ClaimAgentMessage(t.Context(), "captain", "captain-runtime", "result-claim")
	if err != nil || claimed == nil || claimed.Kind != "result" {
		t.Fatalf("claim result = %#v, %v", claimed, err)
	}
	projection, err = s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil || projection.Work[0].Result == nil || projection.Work[0].Result.Stage != "delivery_claimed" || projection.Queue.ResultClaims != 1 {
		t.Fatalf("claimed result facts = result %#v queue %#v err %v", projection.Work[0].Result, projection.Queue, err)
	}
}

func TestWorkspaceOperationsSanitizesStoredWorkspaceTitleFallback(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	if _, err := s.db.Exec(`update workstreams set title=? where id='work-ws'`, "Bad\n\u202eTitle"); err != nil {
		t.Fatal(err)
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(projection.Workspace.Title, "\n\r") || strings.Contains(projection.Workspace.Title, "\u202e") || projection.Workspace.Title != "BadTitle" {
		t.Fatalf("workspace fallback title = %q", projection.Workspace.Title)
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
