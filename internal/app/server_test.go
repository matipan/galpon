package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

const testRuntimeCapability = "test-runtime-capability"

func TestDecodeLimitAllowsImageSizedRuntimeRequests(t *testing.T) {
	body := `{"data":"` + strings.Repeat("a", 2<<20) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	response := httptest.NewRecorder()
	var value struct {
		Data string `json:"data"`
	}
	if !decodeLimit(response, request, &value, 3<<20) {
		t.Fatalf("large decode failed: %d %s", response.Code, response.Body.String())
	}
	if len(value.Data) != 2<<20 {
		t.Fatalf("decoded data length = %d", len(value.Data))
	}
}

func TestRuntimeStopDoesNotWaitForExclusiveRepositoryOperation(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	server.repositoryGate.Lock()
	defer server.repositoryGate.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/stop", bytes.NewBufferString(`{"runtimeId":"runtime","capability":"test-runtime-capability","error":""}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		server.http.Handler.ServeHTTP(response, request)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("runtime stop waited for the repository gate")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("runtime stop status = %d: %s", response.Code, response.Body.String())
	}
}

func TestRuntimeToolRequiresRegisteredRuntime(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "missing runtime", body: `{"agentId":"agent","args":{}}`, want: http.StatusUnauthorized},
		{name: "wrong runtime", body: `{"agentId":"agent","runtimeId":"other","args":{}}`, want: http.StatusUnauthorized},
		{name: "registered runtime", body: `{"agentId":"agent","runtimeId":"runtime","capability":"test-runtime-capability","requestId":"tool-call","args":{}}`, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/list_agents", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestReportProgressRuntimeToolRequiresOwnershipAndActiveDelivery(t *testing.T) {
	application := companionTestApp(t, "runtime")
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "progress-delivery", TargetAgentID: "agent", Prompt: "private work", Status: "queued", QueueDeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	claimed, err := application.Store.ClaimAgentMessage(t.Context(), "agent", "runtime", "progress-claim")
	if err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	server := NewServer(application)
	call := func(runtimeID, current, eventID string, currentAttempt int, reserved ...bool) *httptest.ResponseRecorder {
		args := map[string]any{"version": 1, "event_id": eventID, "phase": "working", "summary": "Running safe checks"}
		if len(reserved) > 0 && reserved[0] {
			args["__current_message_id"] = claimed.ID
			args["__current_attempt"] = claimed.Attempt
			args["__runtime_id"] = "runtime"
		}
		body, _ := json.Marshal(map[string]any{
			"agentId": "agent", "runtimeId": runtimeID, "capability": testRuntimeCapability, "requestId": fmt.Sprintf("progress-tool-%s-%s-%s-%d", runtimeID, current, eventID, currentAttempt), "currentMessageId": current, "currentAttempt": currentAttempt,
			"args": args,
		})
		request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/report_progress", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	if response := call("other", claimed.ID, "checkpoint", claimed.Attempt); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong runtime = %d: %s", response.Code, response.Body.String())
	}
	if response := call("runtime", "", "checkpoint", claimed.Attempt); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing delivery = %d: %s", response.Code, response.Body.String())
	}
	if response := call("runtime", "", "checkpoint", claimed.Attempt, true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("forged reserved fields = %d: %s", response.Code, response.Body.String())
	}
	if response := call("runtime", claimed.ID, "wrong-attempt", claimed.Attempt+1); response.Code != http.StatusBadRequest {
		t.Fatalf("wrong attempt = %d: %s", response.Code, response.Body.String())
	}
	response := call("runtime", claimed.ID, "checkpoint", claimed.Attempt)
	if response.Code != http.StatusOK {
		t.Fatalf("progress report = %d: %s", response.Code, response.Body.String())
	}
	if retry := call("runtime", claimed.ID, "checkpoint", claimed.Attempt); retry.Code != http.StatusOK {
		t.Fatalf("exact retry = %d: %s", retry.Code, retry.Body.String())
	}
	events, err := application.Store.WorkProgressEvents(t.Context(), claimed.ID)
	if err != nil || len(events) != 1 || events[0].Summary != "Running safe checks" {
		t.Fatalf("progress events = %#v, %v", events, err)
	}
}

func TestDelegatedStatusRequiresRuntimeAndReturnsActiveCount(t *testing.T) {
	application := companionTestApp(t, "runtime")
	root, err := application.Store.Agent(t.Context(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	child := model.Agent{
		ID: "child", WorkspaceID: root.WorkspaceID, Title: "Child", CreatedByAgentID: root.ID,
		Presentation: "background", Placement: model.AgentPlacement{Type: "none", CWD: t.TempDir()},
		Kind: "pi", Status: "running", SessionID: "child", CreatedAt: now, UpdatedAt: now,
	}
	if err := application.Store.PutAgent(t.Context(), child, nil); err != nil {
		t.Fatal(err)
	}
	server := NewServer(application)
	call := func(runtimeID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"runtimeId": runtimeID, "capability": testRuntimeCapability})
		request := httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/delegated-status", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	if response := call("other"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong runtime status = %d: %s", response.Code, response.Body.String())
	}
	response := call("runtime")
	if response.Code != http.StatusOK {
		t.Fatalf("delegated status = %d: %s", response.Code, response.Body.String())
	}
	var value struct {
		Active int `json:"activeDelegatedAgents"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Active != 1 {
		t.Fatalf("active delegated agents = %d, want 1", value.Active)
	}
}

func TestRuntimeHTTPRejectsBlankAndMigratedUncredentialedOwnership(t *testing.T) {
	application := companionTestApp(t, "")
	server := NewServer(application)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "message", TargetAgentID: "agent", Prompt: "work", Status: "queued", CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	call := func(path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	preparedCapability, err := application.PrepareRuntime(t.Context(), "agent", "prepared-runtime")
	if err != nil {
		t.Fatal(err)
	}
	for _, attack := range []struct{ path, body string }{
		{"/v1/runtime/agents/agent/register", `{"runtimeId":"","capability":"","sessionId":"agent"}`},
		{"/v1/runtime/agents/agent/register", `{"runtimeId":"prepared-runtime","capability":"wrong","sessionId":"agent"}`},
		{"/v1/runtime/agents/agent/status", `{"runtimeId":"","capability":"","status":"idle"}`},
		{"/v1/runtime/agents/agent/claim", `{"runtimeId":"","capability":"","claimId":"blank"}`},
	} {
		response := call(attack.path, attack.body)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("blank runtime attack = %d: %s", response.Code, response.Body.String())
		}
	}
	if err := application.CancelPreparedRuntime(t.Context(), "agent", "prepared-runtime", preparedCapability); err != nil {
		t.Fatal(err)
	}
	stopped, _ := application.Store.Agent(t.Context(), "agent")
	queued, _ := application.Store.AgentMessage(t.Context(), message.ID)
	if stopped.Status != "stopped" || queued.Status != "queued" {
		t.Fatalf("blank attack changed state: %#v %#v", stopped, queued)
	}

	if err := application.Store.RegisterAgentRuntime(t.Context(), "agent", "migrated-runtime", "agent", ""); err != nil {
		t.Fatal(err)
	}
	for _, attack := range []struct{ path, body string }{
		{"/v1/runtime/agents/agent/claim", `{"runtimeId":"migrated-runtime","claimId":"old"}`},
		{"/v1/runtime/tools/list_agents", `{"agentId":"agent","runtimeId":"migrated-runtime","requestId":"old","args":{}}`},
	} {
		response := call(attack.path, attack.body)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("migrated runtime attack = %d: %s", response.Code, response.Body.String())
		}
	}
	if err := application.Store.ReconcileBackgroundRuntimes(t.Context()); err != nil {
		t.Fatal(err)
	}
	revoked, _ := application.Store.Agent(t.Context(), "agent")
	if revoked.RuntimeID != "" || revoked.Status != "stopped" {
		t.Fatalf("migrated runtime was not revoked: %#v", revoked)
	}
}

type discardedResponseWriter struct{ header http.Header }

func (w *discardedResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (*discardedResponseWriter) Write(value []byte) (int, error) { return len(value), nil }
func (*discardedResponseWriter) WriteHeader(int)                 {}

func TestDeliveryCompletionReplaysAfterCommitResponseLoss(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "completion-loss", TargetAgentID: "agent", Status: "queued", Prompt: "work", CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	claimed, err := application.Store.ClaimAgentMessage(t.Context(), "agent", "runtime", "claim")
	if err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	body := `{"runtimeId":"runtime","capability":"test-runtime-capability","attempt":1,"response":"done"}`
	path := "/v1/runtime/agents/agent/messages/completion-loss/complete"
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	server.http.Handler.ServeHTTP(&discardedResponseWriter{}, request)
	retry := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	retry.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, retry)
	if response.Code != http.StatusOK {
		t.Fatalf("completion retry = %d: %s", response.Code, response.Body.String())
	}
	stored, _ := application.Store.AgentMessage(t.Context(), message.ID)
	if stored.Status != "completed" || stored.Response != "done" {
		t.Fatalf("completion = %#v", stored)
	}
}

func TestRuntimeMutationReplaysAfterCommitResponseLoss(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	body := `{"agentId":"agent","runtimeId":"runtime","capability":"test-runtime-capability","requestId":"response-loss","args":{"title":"Response lost"}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/create_workspace", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	server.http.Handler.ServeHTTP(&discardedResponseWriter{}, request)
	retry := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/create_workspace", bytes.NewBufferString(body))
	retry.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, retry)
	if response.Code != http.StatusOK {
		t.Fatalf("response-loss retry = %d: %s", response.Code, response.Body.String())
	}
	dashboard, err := application.Store.Dashboard(t.Context())
	if err != nil || len(dashboard.Workspaces) != 2 {
		t.Fatalf("response-loss mutation duplicated: %#v, %v", dashboard.Workspaces, err)
	}
}

func TestRuntimeMutationUsesDurableRequestReceipt(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/create_workspace", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	body := `{"agentId":"agent","runtimeId":"runtime","capability":"test-runtime-capability","requestId":"create-1","args":{"title":"New work"}}`
	first := call(body)
	second := call(body)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("idempotent runtime mutation = first %d %s, second %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	dashboard, err := application.Store.Dashboard(t.Context())
	if err != nil || len(dashboard.Workspaces) != 2 {
		t.Fatalf("workspaces after retry = %#v, %v", dashboard.Workspaces, err)
	}
	conflict := call(`{"agentId":"agent","runtimeId":"runtime","capability":"test-runtime-capability","requestId":"create-1","args":{"title":"Different work"}}`)
	if conflict.Code == http.StatusOK {
		t.Fatalf("conflicting runtime receipt succeeded: %s", conflict.Body.String())
	}
}

func TestRuntimeMutationReceiptSurvivesDeliveryRetry(t *testing.T) {
	application := companionTestApp(t, "runtime")
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "runtime-retry", TargetAgentID: "agent", Kind: "request", Prompt: "create work", Status: "queued", QueueDeadlineAt: now + 60_000, CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	firstClaim, err := application.Store.ClaimAgentMessage(t.Context(), "agent", "runtime", "first-claim")
	if err != nil || firstClaim == nil {
		t.Fatalf("first claim = %#v, %v", firstClaim, err)
	}
	server := NewServer(application)
	call := func(runtimeID, capability string, attempt int) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{
			"agentId": "agent", "runtimeId": runtimeID, "capability": capability, "requestId": "stable-create",
			"currentMessageId": message.ID, "currentAttempt": attempt, "args": map[string]any{"title": "Recovered work"},
		})
		request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/create_workspace", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	first := call("runtime", testRuntimeCapability, firstClaim.Attempt)
	if first.Code != http.StatusOK {
		t.Fatalf("first mutation = %d: %s", first.Code, first.Body.String())
	}
	if err := application.StopRuntime(t.Context(), "agent", "runtime", testRuntimeCapability, "restart"); err != nil {
		t.Fatal(err)
	}
	capability, err := application.PrepareRuntime(t.Context(), "agent", "replacement-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := application.RegisterRuntime(t.Context(), "agent", "replacement-runtime", capability, "agent", ""); err != nil {
		t.Fatal(err)
	}
	secondClaim, err := application.Store.ClaimAgentMessage(t.Context(), "agent", "replacement-runtime", "second-claim")
	if err != nil || secondClaim == nil || secondClaim.ID != message.ID || secondClaim.Attempt != firstClaim.Attempt+1 {
		t.Fatalf("second claim = %#v, %v", secondClaim, err)
	}
	second := call("replacement-runtime", capability, secondClaim.Attempt)
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() {
		t.Fatalf("replayed mutation = first %d %s, second %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	dashboard, err := application.Store.Dashboard(t.Context())
	if err != nil || len(dashboard.Workspaces) != 2 {
		t.Fatalf("delivery retry duplicated mutation: %#v, %v", dashboard.Workspaces, err)
	}
}

func TestShutdownWaitsForRepositoryOperation(t *testing.T) {
	s := &Server{http: &http.Server{}, done: make(chan struct{})}
	s.repositoryGate.RLock()
	response := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		s.shutdown(response, httptest.NewRequest(http.MethodPost, "/v1/shutdown", nil))
		close(returned)
	}()

	deadline := time.Now().Add(time.Second)
	for !s.draining.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.draining.Load() {
		t.Fatal("server did not start draining")
	}
	select {
	case <-returned:
		t.Fatal("shutdown returned while a repository operation was active")
	default:
	}

	s.repositoryGate.RUnlock()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not continue after the repository operation finished")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("shutdown status = %d", response.Code)
	}
}

func TestRuntimeAwaitAgentsReturnsOrderedTypedOutcomes(t *testing.T) {
	application := companionTestApp(t, "runtime")
	target := putWaitTarget(t, application, "await-target", "await-runtime")
	now := time.Now().UnixMilli()
	messages := []model.AgentMessage{
		{ID: "completed-message", SenderAgentID: "agent", TargetAgentID: target.ID, Kind: "request", Prompt: "one", Status: "completed", Response: "done", Attempt: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "failed-message", SenderAgentID: "agent", TargetAgentID: target.ID, Kind: "request", Prompt: "two", Status: "failed", Error: "worker failed", Attempt: 3, CreatedAt: now + 1, UpdatedAt: now + 1},
	}
	for _, message := range messages {
		if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(application)
	body := `{"agentId":"agent","runtimeId":"runtime","capability":"test-runtime-capability","requestId":"await-many","args":{"message_ids":["failed-message","completed-message"],"return_when":"all","timeout_seconds":30}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/await_agents", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("await agents status = %d: %s", response.Code, response.Body.String())
	}
	var value model.AgentWaitManyResult
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Status != "completed" || value.Completed != 2 || len(value.Outcomes) != 2 || value.Outcomes[0].ID != "failed-message" || value.Outcomes[1].ID != "completed-message" {
		t.Fatalf("await agents response = %#v", value)
	}
	if value.Outcomes[0].WaitStatus != "failed" || value.Outcomes[0].MessageStatus != "failed" || value.Outcomes[0].Attempt != 3 || value.Outcomes[0].WaitError == nil || value.Outcomes[0].WaitError.Kind != "message_failed" || value.Outcomes[0].TargetRuntimeStatus != "idle" {
		t.Fatalf("failed typed outcome = %#v", value.Outcomes[0])
	}
}
