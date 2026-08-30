package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

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

func TestDirectMessageAPIRejectsInvalidPromptWithoutCommunicationState(t *testing.T) {
	for _, protocol := range []struct {
		name string
		v2   bool
	}{
		{name: "v1"},
		{name: "v2", v2: true},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			application := communicationRuntimeTestApp(t)
			if protocol.v2 {
				if _, err := application.UpgradeCommunicationV2(t.Context(), CommunicationUpgradeRequest{Generation: 2, IdleTimeout: time.Second, BarrierTimeout: time.Second}); err != nil {
					t.Fatal(err)
				}
			}
			putCommunicationAgent(t, application, "direct-target")
			if err := application.Store.SetAgentPresentation(t.Context(), "direct-target", "background"); err != nil {
				t.Fatal(err)
			}
			modelStarts := 0
			application.backgroundStart = func(_ context.Context, _ model.Agent) error {
				modelStarts++
				return nil
			}
			server := NewServer(application)
			for _, test := range []struct {
				name   string
				prompt string
				want   string
			}{
				{name: "whitespace", prompt: " \t\n", want: "message text is required"},
				{name: "byte limit", prompt: " " + strings.Repeat("a", model.AgentMessagePromptByteLimit+1) + " ", want: "byte limit"},
			} {
				t.Run(test.name, func(t *testing.T) {
					body, err := json.Marshal(map[string]string{"text": test.prompt})
					if err != nil {
						t.Fatal(err)
					}
					request := httptest.NewRequest(http.MethodPost, "/v1/agents/direct-target/messages", bytes.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Idempotency-Key", protocol.name+":"+test.name)
					response := httptest.NewRecorder()
					server.http.Handler.ServeHTTP(response, request)
					if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.want) {
						t.Fatalf("invalid prompt response = %d: %s", response.Code, response.Body.String())
					}
					state, err := application.Store.DurableState(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if len(state.Messages) != 0 || len(state.AgentOperations) != 0 || len(state.AgentOperationAttempts) != 0 ||
						len(state.AgentMessageResults) != 0 || len(state.AgentInboxReceipts) != 0 || len(state.AgentOperationJoins) != 0 ||
						len(state.AgentPiLocalEvents) != 0 || len(state.AgentCoordinationMessageMeta) != 0 || len(state.AgentCoordinationSendReceipts) != 0 ||
						len(state.AgentTodoLinkIntents) != 0 || len(state.AgentTodoSettlementEvents) != 0 {
						t.Fatalf("invalid prompt created durable communication state: %#v", state)
					}
					ready, err := application.Store.CoordinationReadyAgentIDs(t.Context())
					if err != nil || len(ready) != 0 {
						t.Fatalf("invalid prompt created a wake = %#v, %v", ready, err)
					}
					target, err := application.Store.Agent(t.Context(), "direct-target")
					if err != nil || target.Status != "stopped" || modelStarts != 0 {
						t.Fatalf("invalid prompt started the model = agent %#v, starts %d, error %v", target, modelStarts, err)
					}
				})
			}
		})
	}
}

func TestCompanionAgentViewAcceptsHashedMessageID(t *testing.T) {
	application := companionTestApp(t, "runtime")
	messageID := "message:" + strings.Repeat("a", 64)
	message := model.AgentMessage{ID: messageID, TargetAgentID: "agent", Prompt: "Visible phone message", Status: "queued", CreatedAt: 10, UpdatedAt: 10}
	if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	server := NewServer(application)
	request := httptest.NewRequest(http.MethodGet, "/v1/companion/agents/agent?message="+url.QueryEscape(messageID), nil)
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("companion agent status = %d: %s", response.Code, response.Body.String())
	}
	var state CompanionAgentState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 1 || state.Messages[0].ID != messageID || state.Messages[0].Prompt != "Visible phone message" {
		t.Fatalf("companion agent messages = %#v", state.Messages)
	}
}

func TestAgentOperationsEndpointIsReadOnlyAndVersioned(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	request := httptest.NewRequest(http.MethodGet, "/v1/agents/agent/operations", nil)
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("operations status = %d: %s", response.Code, response.Body.String())
	}
	var projection model.AgentOperations
	if err := json.Unmarshal(response.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Version != 1 || projection.Agent.ID != "agent" || projection.Workspace.ID != "ws" {
		t.Fatalf("operations projection = %#v", projection)
	}
	if strings.Contains(response.Body.String(), "runtime") || strings.Contains(response.Body.String(), "session") || strings.Contains(response.Body.String(), "/source") {
		t.Fatalf("operations response exposed private runtime data: %s", response.Body.String())
	}
	workspaceRequest := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws/operations", nil)
	workspaceResponse := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(workspaceResponse, workspaceRequest)
	if workspaceResponse.Code != http.StatusNotFound {
		t.Fatalf("workspace operations endpoint status = %d, want 404", workspaceResponse.Code)
	}
}

func TestRuntimeStopDoesNotWaitForExclusiveRepositoryOperation(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	server.repositoryGate.Lock()
	defer server.repositoryGate.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/stop", bytes.NewBufferString(`{"runtimeId":"runtime","error":""}`))
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
		{name: "registered runtime", body: `{"agentId":"agent","runtimeId":"runtime","requestId":"tool-call","args":{}}`, want: http.StatusOK},
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
			"agentId": "agent", "runtimeId": runtimeID, "requestId": fmt.Sprintf("progress-tool-%s-%s-%s-%d", runtimeID, current, eventID, currentAttempt), "currentMessageId": current, "currentAttempt": currentAttempt,
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
	if response := call("runtime", "", "checkpoint", claimed.Attempt); response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"error":"report_progress requires an active delivery"`) {
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
	renderer := &contextualActivityRecordingRenderer{err: fmt.Errorf("renderer unavailable")}
	application.Renderer = renderer
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
	message := model.AgentMessage{ID: "delegated", SenderAgentID: root.ID, TargetAgentID: child.ID, Kind: "request", Act: "request", ResultMode: "notify", Status: "queued", ProcessingDeadlineAt: now + 60_000, RootMessageID: "delegated", RunID: "delegated", CreatedAt: now, UpdatedAt: now}
	if err := application.Store.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	server := NewServer(application)
	call := func(runtimeID string, project bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"runtimeId": runtimeID, "projectContextualActivity": project})
		request := httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/delegated-status", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	if response := call("other", true); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong runtime status = %d: %s", response.Code, response.Body.String())
	}
	response := call("runtime", true)
	if response.Code != http.StatusOK {
		t.Fatalf("delegated status = %d: %s", response.Code, response.Body.String())
	}
	var value struct {
		ActiveAgents   int `json:"activeDelegatedAgents"`
		ActiveRequests int `json:"activeDelegatedRequests"`
		Waiting        int `json:"waitingJoinedWork"`
		ActiveWork     int `json:"activeDelegatedWork"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.ActiveAgents != 1 || value.ActiveRequests != 1 || value.Waiting != 0 || value.ActiveWork != 2 {
		t.Fatalf("delegated status = %#v", value)
	}
	if len(renderer.active) != 1 || renderer.active[0] != 2 {
		t.Fatalf("contextual renderer calls = %#v", renderer.active)
	}
	if err := application.Store.SetAgentRuntimeStatus(t.Context(), root.ID, "runtime", "running", ""); err != nil {
		t.Fatal(err)
	}
	if response := call("runtime", true); response.Code != http.StatusOK {
		t.Fatalf("active Pi delegated status = %d: %s", response.Code, response.Body.String())
	}
	if len(renderer.active) != 1 {
		t.Fatalf("contextual state competed with active Pi: %#v", renderer.active)
	}
}

type contextualActivityRecordingRenderer struct {
	active []int
	err    error
}

func (r *contextualActivityRecordingRenderer) Name() string    { return "recording" }
func (r *contextualActivityRecordingRenderer) Context() string { return "default" }
func (r *contextualActivityRecordingRenderer) OpenTerminal(context.Context, model.Workspace, model.Worktree, string, []string) (string, error) {
	return "", nil
}
func (r *contextualActivityRecordingRenderer) OpenAgent(context.Context, model.Workspace, model.Worktree, model.Agent, []string, bool) (string, string, bool, error) {
	return "", "", false, nil
}
func (r *contextualActivityRecordingRenderer) CloseAgent(context.Context, model.Agent) error {
	return nil
}
func (r *contextualActivityRecordingRenderer) ReportAgent(context.Context, model.Agent, string, string) error {
	return nil
}
func (r *contextualActivityRecordingRenderer) ReportContextualActivity(_ context.Context, _ model.Agent, active int) error {
	r.active = append(r.active, active)
	return r.err
}

func TestRuntimeWireAliasesSupportAnOpenOlderExtension(t *testing.T) {
	application := companionTestApp(t, "runtime")
	application.legacyRuntimeTools = map[string]string{"agent": "runtime"}
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
	claim := call("/v1/runtime/agents/agent/claim", `{"runtimeId":"runtime"}`)
	if claim.Code != http.StatusOK {
		t.Fatalf("legacy claim = %d: %s", claim.Code, claim.Body.String())
	}
	complete := call("/v1/runtime/agents/agent/messages/message/complete", `{"runtimeId":"runtime","response":"done","error":""}`)
	if complete.Code != http.StatusOK {
		t.Fatalf("legacy completion = %d: %s", complete.Code, complete.Body.String())
	}
	stored, err := application.Store.AgentMessage(t.Context(), message.ID)
	if err != nil || stored.Status != "completed" || stored.Response != "done" {
		t.Fatalf("legacy completed message = %#v, %v", stored, err)
	}
	aliasMessage := model.AgentMessage{ID: "alias-message", TargetAgentID: "agent", Prompt: "more work", Status: "queued", CreatedAt: now + 1, UpdatedAt: now + 1}
	if err := application.Store.PutAgentMessage(t.Context(), aliasMessage); err != nil {
		t.Fatal(err)
	}
	aliasClaim := call("/v1/runtime/agents/agent/claim", `{"runtimeId":"runtime","claimKey":"old-claim"}`)
	if aliasClaim.Code != http.StatusOK {
		t.Fatalf("claimKey alias = %d: %s", aliasClaim.Code, aliasClaim.Body.String())
	}
	tool := call("/v1/runtime/tools/list_agents", `{"agentId":"agent","toolCallId":"old-tool","args":{}}`)
	if tool.Code != http.StatusOK {
		t.Fatalf("legacy runtime tool = %d: %s", tool.Code, tool.Body.String())
	}
}

func TestRuntimeWorkspaceCreationIsRejectedWithoutMutation(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/create_workspace", bytes.NewBufferString(`{"agentId":"agent","runtimeId":"runtime","requestId":"create-workspace","args":{"title":"Agent work"}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), "available only to users") {
		t.Fatalf("workspace creation response = %d: %s", response.Code, response.Body.String())
	}
	dashboard, err := application.Store.Dashboard(t.Context())
	if err != nil || len(dashboard.Workspaces) != 1 || dashboard.Workspaces[0].ID != "ws" {
		t.Fatalf("workspaces after rejected tool = %#v, %v", dashboard.Workspaces, err)
	}
}

func TestRuntimeMutationUsesDurableRequestReceipt(t *testing.T) {
	application := companionTestApp(t, "runtime")
	application.backgroundStart = func(context.Context, model.Agent) error { return nil }
	server := NewServer(application)
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/create_agent", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	body := `{"agentId":"agent","runtimeId":"runtime","requestId":"create-1","args":{"title":"New worker"}}`
	first := call(body)
	second := call(body)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("idempotent runtime mutation = first %d %s, second %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	dashboard, err := application.Store.Dashboard(t.Context())
	if err != nil || len(dashboard.Workspaces) != 1 || len(dashboard.Agents) != 2 {
		t.Fatalf("dashboard after retry = %#v, %v", dashboard, err)
	}
	for _, agent := range dashboard.Agents {
		if agent.ID != "agent" && agent.WorkspaceID != "ws" {
			t.Fatalf("created agent used another workspace: %#v", agent)
		}
	}
	conflict := call(`{"agentId":"agent","runtimeId":"runtime","requestId":"create-1","args":{"title":"Different worker"}}`)
	if conflict.Code == http.StatusOK {
		t.Fatalf("conflicting runtime receipt succeeded: %s", conflict.Body.String())
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
	body := `{"agentId":"agent","runtimeId":"runtime","requestId":"await-many","args":{"message_ids":["failed-message","completed-message"],"return_when":"all","timeout_seconds":30}}`
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
