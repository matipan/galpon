package nativeagent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

type recordedCall struct {
	Path string
	Body map[string]any
}

type daemonFixture struct {
	mu       sync.Mutex
	calls    []recordedCall
	delivery *app.CoordinationOperationDelivery
}

func (f *daemonFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{r.URL.Path, body})
	switch {
	case strings.HasSuffix(r.URL.Path, "/claim"):
		_ = json.NewEncoder(w).Encode(map[string]any{"delivery": f.delivery})
		f.delivery = nil
	case strings.HasSuffix(r.URL.Path, "/take"):
		_ = json.NewEncoder(w).Encode(app.CoordinationReceiptBatch{})
	case strings.HasSuffix(r.URL.Path, "/operations/direct"):
		entry, _ := body["userEntryId"].(string)
		runtime, _ := body["runtimeId"].(string)
		_ = json.NewEncoder(w).Encode(model.AgentOperation{ID: "direct:" + entry, AgentID: "agent", Kind: "direct", State: "running", UserEntryID: entry, RuntimeID: runtime, Attempt: 1, ProtocolGeneration: 3})
	default:
		_, _ = w.Write([]byte("{}\n"))
	}
}

func (f *daemonFixture) matching(suffix string) []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls []recordedCall
	for _, call := range f.calls {
		if strings.HasSuffix(call.Path, suffix) {
			calls = append(calls, call)
		}
	}
	return calls
}

func controllerFixture(t *testing.T) (*runtimeController, *daemonFixture, string) {
	t.Helper()
	root := t.TempDir()
	fixture := &daemonFixture{}
	socket := filepath.Join(root, "daemon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: fixture, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	controller := &runtimeController{client: app.NewClient(socket), agent: model.Agent{ID: "agent", Kind: "claude"}, runtimeID: "current-runtime", generation: 3,
		journalPath: filepath.Join(root, "journal.json"), sessionSaved: true, recovery: make(map[string]*runtimeJob), lastRenew: time.Now()}
	source := filepath.Join(root, "native.jsonl")
	controller.transcript = newTranscript("claude", source, filepath.Join(root, "managed.jsonl"))
	return controller, fixture, source
}

func (f *daemonFixture) settled(operationID string) []recordedCall {
	var calls []recordedCall
	for _, call := range f.matching("/settle") {
		if strings.Contains(call.Path, "/operations/"+operationID+"/") {
			calls = append(calls, call)
		}
	}
	return calls
}

func userRecord(id, parent, prompt string) map[string]any {
	return map[string]any{"type": "user", "uuid": id, "parentUuid": parent, "promptId": id + "-prompt", "message": map[string]any{"role": "user", "content": prompt}}
}

// localCommandRecords returns the records that Claude saves for a command that
// runs without a model turn.
func localCommandRecords(id, parent, command string) []map[string]any {
	prompt := id + "-prompt"
	return []map[string]any{
		{"type": "user", "uuid": id + "-caveat", "parentUuid": parent, "promptId": prompt, "isMeta": true, "message": map[string]any{"role": "user", "content": "<local-command-caveat>The command below was run directly in Claude Code, not sent to you as a request.</local-command-caveat>"}},
		{"type": "user", "uuid": id, "parentUuid": id + "-caveat", "promptId": prompt, "origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": command}},
	}
}

func finalRecord(id, parent, text string) map[string]any {
	return map[string]any{"type": "assistant", "uuid": id, "parentUuid": parent, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}, "stop_reason": "end_turn"}}
}

func observationCallRecord(id, parent, callID string) map[string]any {
	return map[string]any{"type": "assistant", "uuid": id, "parentUuid": parent, "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": callID, "name": "mcp__galpon__galpon_read_message", "input": map[string]any{"message_id": "message:child"}}}}}
}

func TestRuntimeAcknowledgesOnlySavedNativeEvidence(t *testing.T) {
	controller, daemon, source := controllerFixture(t)
	operation := model.AgentOperation{ID: "operation", AgentID: "agent", Kind: "inbound", RuntimeID: controller.runtimeID, Attempt: 3, ProtocolGeneration: 3}
	job := &runtimeJob{Operation: operation, Token: "delivery-token", NativeID: "request-prompt", SentAt: time.Now().UnixMilli(),
		Receipts:     app.CoordinationReceiptBatch{Receipts: []model.AgentInboxReceipt{{ID: "result-receipt", State: "claimed"}}},
		Observations: []observation{{Marker: "result-marker", MessageIDs: []string{"message:child"}}}}
	controller.jobs = []*runtimeJob{job}
	step := func() {
		t.Helper()
		controller.mu.Lock()
		err := controller.step(t.Context())
		controller.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	step()
	if len(daemon.matching("/start")) != 0 || len(daemon.matching("/present")) != 0 {
		t.Fatal("transport admission presented an unsaved delivery")
	}
	appendRecord(t, source, userRecord("request", "", "Work "+deliveryMarker(job.Token)), true)
	step()
	if len(daemon.matching("/start")) != 1 || len(daemon.matching("/present")) != 1 {
		t.Fatal("saved delivery did not present its receipts")
	}
	if len(daemon.matching("/observe-results")) != 0 {
		t.Fatal("unsaved tool output was acknowledged")
	}
	appendRecord(t, source, map[string]any{"type": "user", "uuid": "unrelated-tool", "parentUuid": "request", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "shell-call", "content": receiptPrefix + "result-marker]"}}}}, true)
	step()
	if len(daemon.matching("/observe-results")) != 0 {
		t.Fatal("a marker from an unrelated tool acknowledged a Galpon result")
	}
	appendRecord(t, source, observationCallRecord("read-call", "unrelated-tool", "read-child"), true)
	appendRecord(t, source, map[string]any{"type": "user", "uuid": "tool-output", "parentUuid": "read-call", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "read-child", "content": receiptPrefix + "result-marker]"}}}}, true)
	step()
	if len(daemon.matching("/observe-results")) != 1 {
		t.Fatal("saved tool output did not record result observation")
	}
	if err := controller.event(nativeEvent{Kind: "finish", ID: "request-prompt", Final: "answer"}); err != nil {
		t.Fatal(err)
	}
	appendRecord(t, source, userRecord("human", "tool-output", "Another question"), true)
	appendRecord(t, source, finalRecord("human-final", "human", "answer"), true)
	step()
	if len(daemon.matching("/settle")) != 0 {
		t.Fatal("another turn's identical final text settled the assignment")
	}
	appendRecord(t, source, finalRecord("request-final", "tool-output", "answer"), true)
	step()
	settlements := daemon.matching("/settle")
	if len(settlements) != 1 || settlements[0].Body["response"] != "answer" || settlements[0].Body["attempt"] != float64(3) {
		t.Fatalf("settlement = %#v", settlements)
	}
}

func TestCodexSavedMCPResultAcknowledgement(t *testing.T) {
	content := []any{map[string]any{"type": "text", "text": receiptPrefix + "result-marker]"}}
	success := map[string]any{"Ok": map[string]any{"content": content}}
	for _, test := range []struct {
		name, server, tool, turn string
		result                   map[string]any
		want                     int
	}{
		{"saved await result", "galpon", "galpon_await_agent", "work", success, 1},
		{"other server", "other", "galpon_await_agent", "work", success, 0},
		{"other tool", "galpon", "galpon_list_agents", "work", success, 0},
		{"other turn", "galpon", "galpon_await_agent", "other", success, 0},
		{"tool error", "galpon", "galpon_await_agent", "work", map[string]any{"Ok": map[string]any{"content": content, "isError": true}}, 0},
		{"transport error", "galpon", "galpon_await_agent", "work", map[string]any{"Err": receiptPrefix + "result-marker]"}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, daemon, source := controllerFixture(t)
			controller.agent.Kind, controller.transcript.kind = "codex", "codex"
			controller.jobs = []*runtimeJob{{
				Operation: model.AgentOperation{ID: "operation", AgentID: "agent", Kind: "direct", RuntimeID: controller.runtimeID, Attempt: 1, ProtocolGeneration: 3},
				NativeID:  "work", Started: true,
				Observations: []observation{{Marker: "result-marker", MessageIDs: []string{"message:child"}}},
			}}
			appendRecord(t, source, map[string]any{"type": "response_item", "payload": map[string]any{
				"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Ask Reviewer for its last PR"}},
				"internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "work"},
			}}, true)
			appendRecord(t, source, map[string]any{"type": "event_msg", "payload": map[string]any{
				"type": "mcp_tool_call_end", "call_id": "exec-call", "turn_id": test.turn,
				"invocation": map[string]any{"server": test.server, "tool": test.tool, "arguments": map[string]any{"message_id": "message:child"}},
				"result":     test.result,
			}}, true)
			for range 2 {
				if err := controller.step(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			calls := daemon.matching("/observe-results")
			if len(calls) != test.want {
				t.Fatalf("result acknowledgements = %d, want %d", len(calls), test.want)
			}
			if test.want == 1 {
				body := calls[0].Body
				ids, _ := body["messageIds"].([]any)
				if body["toolCallId"] != "result-marker" || len(ids) != 1 || ids[0] != "message:child" {
					t.Fatalf("wrong result acknowledged: %#v", body)
				}
			}
		})
	}
}

func TestRuntimeRecoversSavedCompletionWithTheCurrentAttempt(t *testing.T) {
	controller, daemon, source := controllerFixture(t)
	appendRecord(t, source, userRecord("request", "", "Work "+deliveryMarker("saved-token")), true)
	appendRecord(t, source, finalRecord("request-final", "request", "saved answer"), true)
	if err := controller.transcript.refresh(); err != nil {
		t.Fatal(err)
	}
	controller.recovery["operation"] = &runtimeJob{Operation: model.AgentOperation{ID: "operation", RuntimeID: "dead-runtime", Attempt: 1}, Token: "saved-token", RootID: "request", NativeID: "request-prompt", Terminal: true, Final: "saved answer"}
	daemon.delivery = &app.CoordinationOperationDelivery{Operation: model.AgentOperation{ID: "operation", AgentID: "agent", Kind: "inbound", RuntimeID: controller.runtimeID, Attempt: 2, ProtocolGeneration: 3}}
	if err := controller.claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.step(context.Background()); err != nil {
		t.Fatal(err)
	}
	settlements := daemon.matching("/settle")
	if len(settlements) != 1 || settlements[0].Body["runtimeId"] != "current-runtime" || settlements[0].Body["attempt"] != float64(2) || settlements[0].Body["response"] != "saved answer" {
		t.Fatalf("recovery reused stale ownership or lost the saved answer: %#v", settlements)
	}
}

func TestRecoveredTerminalInputIsNeverSentAgain(t *testing.T) {
	for _, test := range []struct {
		name              string
		records           []map[string]any
		response, failure string
	}{
		{"saved answer", []map[string]any{finalRecord("terminal-final", "terminal", "Saved answer")}, "Saved answer", ""},
		{"unfinished turn", nil, "", "does not send terminal input again"},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, daemon, source := controllerFixture(t)
			writer := &fenceDriver{}
			controller.driver = writer
			appendRecord(t, source, userRecord("terminal", "", "Review the planner"), true)
			for _, record := range test.records {
				appendRecord(t, source, record, true)
			}
			if err := controller.transcript.refresh(); err != nil {
				t.Fatal(err)
			}
			daemon.delivery = &app.CoordinationOperationDelivery{Operation: model.AgentOperation{ID: "direct:terminal", AgentID: "agent", Kind: "direct", UserEntryID: "claude:terminal", RuntimeID: controller.runtimeID, Attempt: 2, ProtocolGeneration: 3}}
			if err := controller.claim(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := controller.step(t.Context()); err != nil {
				t.Fatal(err)
			}
			settled := daemon.settled("direct:terminal")
			if writer.submitted != 0 || len(settled) != 1 || settled[0].Body["attempt"] != float64(2) || settled[0].Body["response"] != test.response || !strings.Contains(settled[0].Body["error"].(string), test.failure) {
				t.Fatalf("recovered terminal input was sent again or settled wrongly: submitted=%d, %#v", writer.submitted, settled)
			}
		})
	}
}
