package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func taskTestApp(t *testing.T) (*App, model.AgentOperation) {
	t.Helper()
	a := communicationRuntimeTestApp(t)
	if _, err := a.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-parent", "task-child"} {
		putCommunicationAgent(t, a, id)
		registerCommunicationRuntime(t, a, id, id+"-runtime")
	}
	op, err := a.RegisterDirectOperation(t.Context(), "task-parent", DirectOperationRequest{RuntimeID: "task-parent-runtime", UserEntryID: "task-entry", ProtocolGeneration: 2})
	if err != nil {
		t.Fatal(err)
	}
	return a, op
}

func callTaskTool(t *testing.T, a *App, name, operationID string, attempt int, args map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"agentId": "task-parent", "runtimeId": "task-parent-runtime", "protocolGeneration": 2,
		"operationId": operationID, "operationAttempt": attempt, "requestId": "test-" + name, "args": args})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/"+name, bytes.NewReader(body))
	r.SetPathValue("name", name)
	w := httptest.NewRecorder()
	(&Server{app: a}).runtimeTool(w, r)
	return w
}

func TestRuntimeObservationsIgnoreStaleOperationAndDoNotConsume(t *testing.T) {
	a, op := taskTestApp(t)
	message, _, err := a.QueueCoordinationMessage(t.Context(), "task-parent", "task-parent-runtime", op.ID, op.Attempt, 2, "task-child", "work", "task-send", "request", "notify", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	delivery, err := a.ClaimCoordinationOperation(t.Context(), "task-child", "task-child-runtime", "child-claim", 2)
	if err != nil || delivery == nil {
		t.Fatalf("claim = %#v, %v", delivery, err)
	}
	if err := a.StartCoordinationOperation(t.Context(), "task-child", "task-child-runtime", delivery.Operation.ID, delivery.Operation.Attempt); err != nil {
		t.Fatal(err)
	}
	w := callTaskTool(t, a, "read_message", "missing-operation", 99, map[string]any{"message_id": message.ID})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"running"`) {
		t.Fatalf("running read = %d %s", w.Code, w.Body)
	}
	if _, err := a.SettleCoordinationOperation(t.Context(), "task-child", "task-child-runtime", delivery.Operation.ID, delivery.Operation.Attempt, "durable answer", ""); err != nil {
		t.Fatal(err)
	}
	before, err := a.Store.DurableState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_message", "await_agent", "read_message", "await_agent"} {
		w = callTaskTool(t, a, name, "", 0, map[string]any{"message_id": message.ID})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"response":"durable answer"`) {
			t.Fatalf("%s = %d %s", name, w.Code, w.Body)
		}
		for _, internal := range []string{"resultMode", "receiptId", "notificationState", "runId", "leaseExpiresAt"} {
			if strings.Contains(w.Body.String(), `"`+internal+`"`) {
				t.Fatalf("tool exposes %s: %s", internal, w.Body)
			}
		}
	}
	after, err := a.Store.DurableState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("observation mutated durable state")
	}
}

func TestTaskWaitUsesOneRealTimeoutAndKeepsWork(t *testing.T) {
	a, op := taskTestApp(t)
	var ids []string
	for _, prompt := range []string{"first", "second"} {
		message, _, err := a.QueueCoordinationMessage(t.Context(), "task-parent", "task-parent-runtime", op.ID, op.Attempt, 2, "task-child", prompt, prompt, "request", "notify", 0, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, message.ID)
	}
	started := time.Now()
	w := callTaskTool(t, a, "await_agents", "stale", 42, map[string]any{"message_ids": ids, "return_when": "all", "timeout_seconds": 1})
	elapsed := time.Since(started)
	if w.Code != http.StatusOK || elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("bounded wait took %s: %d %s", elapsed, w.Code, w.Body)
	}
	var result model.AgentWaitManyResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "timeout" || len(result.Outcomes) != 2 {
		t.Fatalf("wait = %#v", result)
	}
	for index, outcome := range result.Outcomes {
		if outcome.MessageID != ids[index] || outcome.WaitStatus != "timeout" || outcome.Status != "queued" {
			t.Fatalf("outcome %d = %#v", index, outcome)
		}
	}
	a.waitMu.Lock()
	defer a.waitMu.Unlock()
	if len(a.waits) != 0 {
		t.Fatalf("wait left dependency edges: %#v", a.waits)
	}
}

func TestProgressIneligibilityIsNotAnOperationError(t *testing.T) {
	a, op := taskTestApp(t)
	for _, value := range []struct {
		id      string
		attempt int
	}{{"", 0}, {"missing", 1}, {op.ID, op.Attempt + 1}, {op.ID, op.Attempt}} {
		w := callTaskTool(t, a, "report_progress", value.id, value.attempt, map[string]any{"phase": "working", "summary": "Checking tests"})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"recorded":false`) || strings.Contains(w.Body.String(), "sql:") {
			t.Fatalf("optional progress = %d %s", w.Code, w.Body)
		}
	}
	w := callTaskTool(t, a, "send_agent", "missing", 1, map[string]any{"agent": "task-child", "prompt": "work"})
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "sql:") {
		t.Fatalf("stale mutation = %d %s", w.Code, w.Body)
	}
}

func TestRuntimeProgressDefaultsAreSafeAndReplayable(t *testing.T) {
	a, parent := taskTestApp(t)
	if _, _, err := a.QueueCoordinationMessage(t.Context(), "task-parent", "task-parent-runtime", parent.ID, parent.Attempt, 2, "task-child", "report progress", "progress-defaults", "request", "notify", 0, ""); err != nil {
		t.Fatal(err)
	}
	delivery, err := a.ClaimCoordinationOperation(t.Context(), "task-child", "task-child-runtime", "progress-claim", 2)
	if err != nil || delivery == nil {
		t.Fatalf("claim = %#v, %v", delivery, err)
	}
	if err := a.StartCoordinationOperation(t.Context(), "task-child", "task-child-runtime", delivery.Operation.ID, delivery.Operation.Attempt); err != nil {
		t.Fatal(err)
	}
	const callID = "call_progress_123|fc_0123456789abcdef0123456789abcdef"
	const generatedID = "progress:188875c3ec62cf1b:f3fc0581a0fec167:02b02bc8329b8574:f4b65344f63bf4b2"
	for _, test := range []struct {
		explicit, want string
		inserted       bool
	}{{"", generatedID, true}, {"", generatedID, false}, {"manual-checkpoint", "manual-checkpoint", true}} {
		args := map[string]any{"phase": "working", "summary": "Checking progress defaults"}
		if test.explicit != "" {
			args["event_id"] = test.explicit
		}
		body, err := json.Marshal(map[string]any{
			"agentId": "task-child", "runtimeId": "task-child-runtime", "protocolGeneration": 2,
			"operationId": delivery.Operation.ID, "operationAttempt": delivery.Operation.Attempt,
			"requestId": callID, "args": args,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/report_progress", bytes.NewReader(body))
		r.SetPathValue("name", "report_progress")
		w := httptest.NewRecorder()
		(&Server{app: a}).runtimeTool(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("progress defaults = %d %s", w.Code, w.Body)
		}
		var result struct {
			Recorded bool                    `json:"recorded"`
			Inserted bool                    `json:"inserted"`
			Progress model.WorkProgressEvent `json:"progress"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Recorded || result.Inserted != test.inserted || result.Progress.EventID != test.want || result.Progress.Version != 1 {
			t.Fatalf("progress replay = %#v", result)
		}
		if _, err := model.ValidateWorkProgress(result.Progress); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTodoLinkedTaskDispatchDoesNotWaitForParentSettlement(t *testing.T) {
	a, op := taskTestApp(t)
	message, _, err := a.QueueCoordinationMessage(t.Context(), "task-parent", "task-parent-runtime", op.ID, op.Attempt, 2, "task-child", "start now", "todo-send", "request", "", 12, "complete_on_success")
	if err != nil {
		t.Fatal(err)
	}
	if message.ResultMode != "join" {
		t.Fatalf("TODO changed routing: %s", message.ResultMode)
	}
	delivery, err := a.ClaimCoordinationOperation(t.Context(), "task-child", "task-child-runtime", "todo-child", 2)
	if err != nil || delivery == nil || delivery.Message == nil || delivery.Message.ID != message.ID {
		t.Fatalf("TODO blocked initial work: %#v, %v", delivery, err)
	}
	parent, err := a.Store.AgentOperation(t.Context(), op.ID)
	if err != nil || parent.State != "running" {
		t.Fatalf("parent already settled: %#v, %v", parent, err)
	}
}
