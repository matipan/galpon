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
	if strings.Contains(string(encoded), "directOperations") {
		t.Fatalf("pre-cutover v1 JSON added direct operation fields: %s", encoded)
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

func enableProjectionV2(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`update communication_protocol_state set generation=2,pending_generation=2,cutover_complete=1,maintenance=0 where singleton=1`); err != nil {
		t.Fatal(err)
	}
}

func putProjectionV2Fixture(t *testing.T, s *Store) {
	t.Helper()
	enableProjectionV2(t, s)
	now := time.Now().UnixMilli()
	waiting := model.AgentMessage{ID: "v2-waiting-message", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "v2-waiting-message", RunID: "v2-waiting-run", Prompt: "private waiting prompt", Status: "queued", CreatedAt: now - 20, UpdatedAt: now - 20}
	ready := model.AgentMessage{ID: "v2-ready-message", SenderAgentID: "captain", TargetAgentID: "reviewer", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "v2-ready-message", RunID: "v2-ready-run", Prompt: "private ready prompt", Response: "private result body", Status: "completed", NotificationState: "pending", CompletedAt: now - 10, CreatedAt: now - 19, UpdatedAt: now - 10}
	legacy := model.AgentMessage{ID: "v2-legacy-message", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: "v2-legacy-message", RunID: "v2-legacy-run", Prompt: "private legacy prompt", Response: "private legacy result", Status: "completed", NotificationState: "suppressed", CompletedAt: now - 8, CreatedAt: now - 18, UpdatedAt: now - 8}
	for _, message := range []model.AgentMessage{waiting, ready, legacy} {
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	statements := []string{
		`insert into agent_operations(id,agent_id,kind,state,parent_message_id,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('private-waiting-operation','worker','inbound','waiting','v2-waiting-message','v2-waiting-run',1,1,2,2)`,
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('private-source-operation','captain','direct','ready','v2-ready-run',2,1,10,2)`,
		`insert into agent_operations(id,agent_id,kind,state,parent_message_id,causal_run_id,attempt,created_at,updated_at,settled_at,protocol_generation) values('private-target-operation','reviewer','inbound','settled','v2-ready-message','v2-ready-run',1,1,9,9,2)`,
		`insert into agent_operations(id,agent_id,kind,state,parent_message_id,causal_run_id,attempt,created_at,updated_at,settled_at,protocol_generation) values('private-legacy-operation','worker','inbound','settled','v2-legacy-message','v2-legacy-run',1,1,8,8,2)`,
		`insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values('v2-ready-message','private-source-operation','safe-hash',1)`,
		`insert into agent_message_results(id,message_id,status,response,created_at,protocol_generation) values('private-ready-result','v2-ready-message','completed','private immutable result',9,2)`,
		`insert into agent_message_results(id,message_id,status,response,legacy_state,created_at,protocol_generation) values('private-legacy-result','v2-legacy-message','completed','private suppressed result','legacy_suppressed_unknown',8,2)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,deadline_at,created_at,updated_at,protocol_generation) values('private-ready-join','private-source-operation','v2-ready-message','ready',100,1,10,2)`,
		`insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,kind,state,eligible,created_at,updated_at,protocol_generation) values('private-waiting-receipt','worker','private-waiting-operation','v2-waiting-message','request','presented',1,1,2,2)`,
		`insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values('private-result-pending','captain','private-source-operation','v2-ready-message','private-ready-result','result','pending',1,1,10,2)`,
		`insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values('private-result-claimed','captain','private-source-operation','v2-ready-message','private-ready-result','result','claimed',1,1,10,2)`,
		`insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values('private-result-presented','captain','private-source-operation','v2-ready-message','private-ready-result','result','presented',1,1,10,2)`,
		`insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values('private-result-acknowledged','captain','private-source-operation','v2-ready-message','private-ready-result','result','acknowledged',1,1,10,2)`,
		`insert into todo_link_intents(id,message_id,operation_id,todo_id,policy,state,created_at,applied_at,protocol_generation) values('private-todo-intent','v2-ready-message','private-source-operation',7,'complete_on_success','applied',1,9,2)`,
		`insert into todo_settlement_events(id,intent_id,result_id,agent_id,operation_id,state,snapshot,created_at,protocol_generation) values('private-todo-event','private-todo-intent','private-ready-result','captain','private-source-operation','pending','private TODO snapshot',10,2)`,
		`update agent_messages set completed_at=1,updated_at=1 where id in ('v2-ready-message','v2-legacy-message')`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatalf("v2 fixture statement failed: %v\n%s", err, statement)
		}
	}
}

func findProjectionWork(items []model.WorkItem, id string) *model.WorkItem {
	for index := range items {
		if items[index].ID == id {
			return &items[index]
		}
		if found := findProjectionWork(items[index].Children, id); found != nil {
			return found
		}
	}
	return nil
}

func TestWorkspaceOperationsProjectsAuthoritativeCommunicationV2State(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	putProjectionV2Fixture(t, s)
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Version != 2 || projection.Summary.WaitingWork != 1 || projection.Summary.ResumeQueued != 1 || projection.Summary.TodoPending != 1 || projection.Summary.TodoApplied != 1 || projection.Summary.LegacySuppressedUnknown != 1 {
		t.Fatalf("v2 summary = %#v", projection.Summary)
	}
	if projection.Queue.ResultsReady != 1 || projection.Queue.ReceiptsClaimed != 1 || projection.Queue.ReceiptsPresented != 2 || projection.Queue.ReceiptsAcknowledged != 1 {
		t.Fatalf("v2 queue = %#v", projection.Queue)
	}
	waiting := findProjectionWork(projection.Work, "v2-waiting-message")
	ready := findProjectionWork(projection.Work, "v2-ready-message")
	legacy := findProjectionWork(projection.Work, "v2-legacy-message")
	if waiting == nil || waiting.Observation.State != "waiting" || !coordinationFact(waiting.Coordination, "target_operation", "waiting") || !coordinationFact(waiting.Coordination, "request_receipt", "presented") {
		t.Fatalf("waiting work = %#v", waiting)
	}
	for _, fact := range [][2]string{{"join", "ready"}, {"result_delivery", "ready"}, {"resume", "queued"}, {"result_receipt", "claimed"}, {"result_receipt", "presented"}, {"result_receipt", "acknowledged"}, {"todo_link", "applied"}, {"todo_settlement", "pending"}} {
		if ready == nil || !coordinationFact(ready.Coordination, fact[0], fact[1]) {
			t.Fatalf("ready work omitted %v: %#v", fact, ready)
		}
	}
	if ready.Result == nil || ready.Result.Stage != "result_ready" || legacy == nil || legacy.Result == nil || legacy.Result.Stage != "legacy_suppressed_unknown" {
		t.Fatalf("v2 results = ready %#v legacy %#v", ready, legacy)
	}
	encoded, _ := json.Marshal(projection)
	for _, private := range []string{"private waiting prompt", "private ready prompt", "private immutable result", "private TODO snapshot", "private-source-operation", "private-result-pending", "private-todo-intent"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("projection exposed %q: %s", private, encoded)
		}
	}
}

func TestCommunicationProjectionBindsJoinsAndCountsUniqueOperations(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	enableProjectionV2(t, s)
	now := time.Now().UnixMilli()
	messages := []model.AgentMessage{
		{ID: "shared-one", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "shared-one", RunID: "shared-run", Status: "completed", CompletedAt: now, CreatedAt: now - 10, UpdatedAt: now},
		{ID: "shared-two", SenderAgentID: "captain", TargetAgentID: "reviewer", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "shared-two", RunID: "shared-run", Status: "completed", CompletedAt: now, CreatedAt: now - 9, UpdatedAt: now},
		{ID: "unrelated-join", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "unrelated-join", RunID: "unrelated-run", Status: "completed", CompletedAt: now, CreatedAt: now - 8, UpdatedAt: now},
		{ID: "waiting-one", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "waiting-one", RunID: "waiting-run", Status: "completed", CompletedAt: now, CreatedAt: now - 7, UpdatedAt: now},
		{ID: "waiting-two", SenderAgentID: "captain", TargetAgentID: "reviewer", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "waiting-two", RunID: "waiting-run", Status: "completed", CompletedAt: now, CreatedAt: now - 6, UpdatedAt: now},
	}
	for _, message := range messages {
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	for index, message := range messages {
		statement := fmt.Sprintf(`insert into agent_operations(id,agent_id,kind,state,parent_message_id,causal_run_id,attempt,created_at,updated_at,settled_at,protocol_generation) values('target-%d','%s','inbound','settled','%s','%s',1,1,10,10,2)`, index, message.TargetAgentID, message.ID, message.RunID)
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatalf("target fixture failed: %v\n%s", err, statement)
		}
	}
	statements := []string{
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('shared-source','captain','direct','ready','shared-run',2,1,10,2)`,
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('false-source','captain','direct','ready','unrelated-run',0,1,10,2)`,
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('other-source','captain','direct','running','other-run',1,1,10,2)`,
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('waiting-source','captain','direct','waiting','waiting-run',1,1,10,2)`,
		`insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values('shared-one','shared-source','hash-1',1)`,
		`insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values('shared-two','shared-source','hash-2',1)`,
		`insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values('unrelated-join','false-source','hash-3',1)`,
		`insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values('waiting-one','waiting-source','hash-4',1)`,
		`insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values('waiting-two','waiting-source','hash-5',1)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,created_at,updated_at,protocol_generation) values('shared-join-1','shared-source','shared-one','ready',1,10,2)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,created_at,updated_at,protocol_generation) values('shared-join-2','shared-source','shared-two','ready',1,10,2)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,created_at,updated_at,protocol_generation) values('false-own-join','false-source','unrelated-join','open',1,10,2)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,created_at,updated_at,protocol_generation) values('false-other-join','other-source','unrelated-join','ready',1,10,2)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,created_at,updated_at,protocol_generation) values('waiting-join-1','waiting-source','waiting-one','open',1,10,2)`,
		`insert into agent_operation_joins(id,operation_id,message_id,state,created_at,updated_at,protocol_generation) values('waiting-join-2','waiting-source','waiting-two','open',1,10,2)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatalf("fixture failed: %v\n%s", err, statement)
		}
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Summary.ResumeQueued != 1 {
		t.Fatalf("unique resume operations = %d, want 1", projection.Summary.ResumeQueued)
	}
	if projection.Summary.WaitingWork != 1 {
		t.Fatalf("unique waiting operations = %d, want 1", projection.Summary.WaitingWork)
	}
	for _, id := range []string{"shared-one", "shared-two"} {
		item := findProjectionWork(projection.Work, id)
		if item == nil || !coordinationFact(item.Coordination, "resume", "queued") {
			t.Fatalf("shared source resume missing from %s: %#v", id, item)
		}
	}
	unrelated := findProjectionWork(projection.Work, "unrelated-join")
	if unrelated == nil || coordinationFact(unrelated.Coordination, "resume", "queued") {
		t.Fatalf("unrelated ready join caused a false resume: %#v", unrelated)
	}
	if !coordinationFact(unrelated.Coordination, "join", "open") || !coordinationFact(unrelated.Coordination, "join", "ready") {
		t.Fatalf("multiple join states were not projected: %#v", unrelated)
	}
}

func TestCommunicationProjectionIncludesSafeDirectOperations(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	enableProjectionV2(t, s)
	statements := []string{
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,lease_expires_at,created_at,updated_at,protocol_generation) values('private-direct-running','captain','direct','running','direct-running-run',1,9999999999999,1,10,2)`,
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('private-direct-waiting','captain','direct','waiting','direct-waiting-run',1,1,11,2)`,
		`insert into agent_operations(id,agent_id,kind,state,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('private-direct-ready','captain','direct','ready','direct-ready-run',2,1,12,2)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.DirectOperations) != 3 || projection.Summary.WaitingWork != 1 || projection.Summary.ResumeQueued != 1 {
		t.Fatalf("workspace direct operation facts = %#v summary %#v", projection.DirectOperations, projection.Summary)
	}
	work, err := s.AgentWork(t.Context(), "captain", false)
	if err != nil || len(work.DirectOperations) != 3 {
		t.Fatalf("agent direct operation facts = %#v, %v", work.DirectOperations, err)
	}
	encoded, _ := json.Marshal(struct {
		Workspace []model.DirectOperationFact `json:"workspace"`
		Agent     []model.DirectOperationFact `json:"agent"`
	}{projection.DirectOperations, work.DirectOperations})
	for _, private := range []string{"private-direct-running", "private-direct-waiting", "private-direct-ready", "direct-running-run"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("safe direct operation facts exposed %q: %s", private, encoded)
		}
	}
	if !slices.ContainsFunc(projection.DirectOperations, func(fact model.DirectOperationFact) bool {
		return fact.State == "started" && fact.Lease == "fresh" && fact.ObservedAt == 10
	}) {
		t.Fatalf("fresh direct operation state was not preserved: %#v", projection.DirectOperations)
	}
}

func TestCommunicationProjectionAggregatesHighCardinalityFacts(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	enableProjectionV2(t, s)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "aggregate-message", SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "join", RootMessageID: "aggregate-message", RunID: "aggregate-run", Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into agent_operations(id,agent_id,kind,state,parent_message_id,causal_run_id,attempt,created_at,updated_at,protocol_generation) values('aggregate-target','worker','inbound','ready','aggregate-message','aggregate-run',0,1,1,2)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 200; index++ {
		if _, err := s.db.Exec(`insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?, 'captain',null,'aggregate-message','result','pending',1,1,?,2)`, fmt.Sprintf("aggregate-receipt-%d", index), index+1); err != nil {
			t.Fatal(err)
		}
	}
	projection, err := s.WorkspaceOperations(t.Context(), "work-ws")
	item := findProjectionWork(projection.Work, message.ID)
	if err != nil || item == nil || item.Coordination == nil || len(item.Coordination.Facts) > workCoordinationFactLimit {
		t.Fatalf("bounded aggregate projection = %#v, %v", item, err)
	}
	var receiptCount, deliveryCount int
	for _, fact := range item.Coordination.Facts {
		if fact.Kind == "result_receipt" && fact.State == "pending" {
			receiptCount = fact.Count
		}
		if fact.Kind == "result_delivery" && fact.State == "ready" {
			deliveryCount = fact.Count
		}
	}
	if receiptCount != 200 || deliveryCount != 200 {
		t.Fatalf("SQL aggregate counts = receipts %d deliveries %d", receiptCount, deliveryCount)
	}
}

func TestCommunicationProjectionAggregatesTODOFactsPerMessage(t *testing.T) {
	s := testStore(t)
	workFixture(t, s)
	enableProjectionV2(t, s)
	for index, id := range []string{"todo-fact-one", "todo-fact-two"} {
		message := model.AgentMessage{ID: id, SenderAgentID: "captain", TargetAgentID: "worker", Kind: "request", Act: "request", ResultMode: "notify", RootMessageID: id, RunID: id, Status: "completed", CompletedAt: int64(index + 2), CreatedAt: 1, UpdatedAt: int64(index + 2)}
		if err := s.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
		statements := []string{
			fmt.Sprintf(`insert into agent_message_results(id,message_id,status,created_at,protocol_generation) values('result-%[1]s','%[1]s','completed',2,2)`, id),
			fmt.Sprintf(`insert into todo_link_intents(id,message_id,todo_id,policy,state,created_at,applied_at,protocol_generation) values('intent-%[1]s','%[1]s',%[2]d,'annotate','applied',1,2,2)`, id, index+1),
			fmt.Sprintf(`insert into todo_settlement_events(id,intent_id,result_id,agent_id,state,snapshot,created_at,applied_at,protocol_generation) values('settlement-%[1]s','intent-%[1]s','result-%[1]s','captain','applied','private snapshot',2,3,2)`, id),
		}
		for _, statement := range statements {
			if _, err := s.db.Exec(statement); err != nil {
				t.Fatalf("TODO fact fixture failed: %v\n%s", err, statement)
			}
		}
	}
	communication, err := loadWorkCommunication(t.Context(), s.db, []string{"todo-fact-one", "todo-fact-two"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"todo-fact-one", "todo-fact-two"} {
		facts := buildWorkCoordination("completed", 3, communication[id])
		if !coordinationFact(facts, "todo_link", "applied") || !coordinationFact(facts, "todo_settlement", "applied") {
			t.Fatalf("message %s lost SQL-aggregated TODO facts: %#v", id, facts)
		}
		encoded, _ := json.Marshal(facts)
		if strings.Contains(string(encoded), "private snapshot") {
			t.Fatalf("message %s exposed its TODO snapshot: %s", id, encoded)
		}
	}
}

func TestCommunicationProjectionQueriesUseMessageFirstIndexes(t *testing.T) {
	s := testStore(t)
	queries := []struct {
		name, query, table string
	}{
		{"operations", `select parent_message_id,id from agent_operations where parent_message_id in ('message') and parent_message_id<>''`, "agent_operations"},
		{"joins", `select message_id,state from agent_operation_joins where message_id in ('message') group by message_id,state`, "agent_operation_joins"},
		{"receipts", `select message_id,kind,state,eligible,count(*) from agent_inbox_receipts where message_id in ('message') group by message_id,kind,state,eligible`, "agent_inbox_receipts"},
		{"results", `select message_id,status from agent_message_results where message_id in ('message')`, "agent_message_results"},
		{"todo links", `select message_id,state from todo_link_intents where message_id in ('message') group by message_id,state`, "todo_link_intents"},
		{"todo settlements", `select intent.message_id,event.state from todo_link_intents intent join todo_settlement_events event on event.intent_id=intent.id where intent.message_id in ('message') group by intent.message_id,event.state`, "todo_settlement_events"},
	}
	for _, test := range queries {
		t.Run(test.name, func(t *testing.T) {
			rows, err := s.db.Query(`explain query plan ` + test.query)
			if err != nil {
				t.Fatal(err)
			}
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(details, "\n")
			if strings.Contains(plan, "SCAN "+test.table) {
				t.Fatalf("full table scan in plan:\n%s", plan)
			}
		})
	}
}
