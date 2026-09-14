package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestResultObservationConflictRecoversWithoutLosingCompletion(t *testing.T) {
	a, parent := taskTestApp(t)
	message, _, err := a.QueueCoordinationMessage(t.Context(), "task-parent", "task-parent-runtime", parent.ID, parent.Attempt, 2, "task-child", "bounded work", "observation-recovery", "request", "join", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	child, err := a.ClaimCoordinationOperation(t.Context(), "task-child", "task-child-runtime", "child-claim", 2)
	if err != nil || child == nil {
		t.Fatalf("child claim = %#v, %v", child, err)
	}
	if err := a.StartCoordinationOperation(t.Context(), "task-child", "task-child-runtime", child.Operation.ID, child.Operation.Attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SettleCoordinationOperation(t.Context(), "task-child", "task-child-runtime", child.Operation.ID, child.Operation.Attempt, "saved child result", ""); err != nil {
		t.Fatal(err)
	}
	observe := func(attempt int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"runtimeId": "task-parent-runtime", "attempt": attempt,
			"toolCallId": "call_read|fc_result", "messageIds": []string{message.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/task-parent/operations/"+parent.ID+"/observe-results", bytes.NewReader(body))
		r.SetPathValue("id", "task-parent")
		r.SetPathValue("operationID", parent.ID)
		w := httptest.NewRecorder()
		(&Server{app: a}).observeResults(w, r)
		return w
	}
	// A restart or expired lease releases ownership but preserves the result.
	if err := a.Store.RecoverAgentCoordinationState(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, err := a.Store.DurableState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	w := observe(parent.Attempt)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"error":"result observation no longer belongs to this operation attempt"`) {
		t.Fatalf("stale observation contract = %d %s", w.Code, w.Body)
	}
	after, err := a.Store.DurableState(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rejected observation changed durable state: %v", err)
	}
	registerCommunicationRuntime(t, a, "task-parent", "task-parent-runtime")
	resumed, err := a.ClaimCoordinationOperation(t.Context(), "task-parent", "task-parent-runtime", "recovered-parent", 2)
	if err != nil || resumed == nil || resumed.Operation.ID != parent.ID || resumed.Operation.Attempt != parent.Attempt+1 {
		t.Fatalf("recovered parent = %#v, %v", resumed, err)
	}
	if err := a.StartCoordinationOperation(t.Context(), "task-parent", "task-parent-runtime", parent.ID, resumed.Operation.Attempt); err != nil {
		t.Fatal(err)
	}
	w = observe(resumed.Operation.Attempt)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"recorded":true`) {
		t.Fatalf("replayed observation = %d %s", w.Code, w.Body)
	}
	settled, err := a.SettleCoordinationOperation(t.Context(), "task-parent", "task-parent-runtime", parent.ID, resumed.Operation.Attempt, "saved final response", "")
	if err != nil || settled.Parked || settled.Operation.State != "settled" {
		t.Fatalf("saved completion = %#v, %v", settled, err)
	}
	result, err := a.Store.ReadCoordinationTask(t.Context(), message.ID, "task-parent")
	if err != nil || result.Response != "saved child result" || result.Attempt != 1 {
		t.Fatalf("child result changed or repeated: %#v, %v", result, err)
	}
}
