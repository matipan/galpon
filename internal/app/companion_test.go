package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

type fakeCompanionBackend struct {
	dashboard       model.Dashboard
	dashboardErr    error
	dashboardHook   func()
	agentHook       func()
	view            model.AgentView
	messagePageIDs  []string
	hasMoreMessages bool
	messageBefore   string
	sent            int
	created         int
}

func (f *fakeCompanionBackend) CompanionDashboard(context.Context) (model.Dashboard, error) {
	if f.dashboardHook != nil {
		f.dashboardHook()
	}
	return f.dashboard, f.dashboardErr
}
func (f *fakeCompanionBackend) CompanionAgent(context.Context, string, []string, string, bool) (CompanionAgentState, error) {
	if f.agentHook != nil {
		f.agentHook()
	}
	workspaceTitle := ""
	if workspace, ok := f.dashboard.Workspace(f.view.Agent.WorkspaceID); ok {
		workspaceTitle = workspace.Title
	}
	return CompanionAgentState{
		Agent: f.view.Agent, Messages: f.view.Messages, WorkspaceTitle: workspaceTitle,
		HasMoreMessages: f.hasMoreMessages, MessageBefore: f.messageBefore, MessagePageIDs: f.messagePageIDs,
	}, nil
}
func (f *fakeCompanionBackend) SendCompanion(_ context.Context, id, prompt, _ string) (model.AgentMessage, error) {
	f.sent++
	return model.AgentMessage{ID: "message", TargetAgentID: id, Prompt: prompt, Status: "queued", CreatedAt: 3, UpdatedAt: 3}, nil
}
func (f *fakeCompanionBackend) CreateAgentFromSource(_ context.Context, in CreateAgentFromSourceRequest, _ string) (CreateAgentFromSourceResult, error) {
	f.created++
	return CreateAgentFromSourceResult{Agent: model.Agent{ID: "new", WorkspaceID: "ws", Title: in.Title, Role: in.Role, Placement: model.AgentPlacement{Type: "worktrees", Worktrees: []model.AgentWorktree{{WorktreeID: "new-wt"}}}, Status: "stopped"}, InitialMessage: model.AgentMessage{ID: "initial", TargetAgentID: "new", Prompt: in.Prompt, Status: "queued"}, StartPending: true}, nil
}

func TestCompanionHidesInternalErrorsAndLogsThemLocally(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{dashboardErr: errors.New("sqlite failed at /private/galpon.db")}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	var logs bytes.Buffer
	server.Logger = log.New(&logs, "", 0)
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "/private") || !strings.Contains(response.Body.String(), "could not read Galpon state") {
		t.Fatalf("public error = %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "/private/galpon.db") {
		t.Fatalf("local log = %q", logs.String())
	}
}

func TestCompanionServesEmbeddedFrontendAssets(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := NewCompanionServer(st, &fakeCompanionBackend{}, "http://127.0.0.1:8420")
	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: "Galpón Companion"},
		{path: "/app.mjs", contentType: "text/javascript", contains: "CompanionAPI"},
		{path: "/detail-state.mjs", contentType: "text/javascript", contains: "mergeRefreshedDetail"},
		{path: "/styles.css", contentType: "text/css", contains: "Tokyo Night"},
	} {
		response := httptest.NewRecorder()
		serveCompanion(server, response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), test.contentType) || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("GET %s = %d, %q, %q", test.path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/not-a-client-route", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown asset status = %d", response.Code)
	}
}

func TestCompanionBootstrapAndAgentUseSafeNestedDTOs(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{dashboard: model.Dashboard{
		Repositories: []model.Repository{{ID: "repo", Title: "Repository", SourcePath: "/secret/repository"}},
		Workspaces:   []model.Workspace{{ID: "ws", Title: "Work", Status: "active", CreatedAt: 1, UpdatedAt: 2}},
		Agents: []model.Agent{
			{ID: "agent", WorkspaceID: "ws", Title: "Worker", Role: "reviewer", Status: "running", SessionPath: "/secret/session.jsonl", RuntimeID: "secret-runtime", Placement: model.AgentPlacement{Type: "worktrees", CWD: "/secret", Worktrees: []model.AgentWorktree{{WorktreeID: "wt"}}}, CreatedAt: 1, UpdatedAt: 2},
			{ID: "cwd", WorkspaceID: "ws", Title: "Unmanaged", Status: "idle", Placement: model.AgentPlacement{Type: "none", CWD: "/private/path"}},
		},
	}}
	backend.view = model.AgentView{Agent: backend.dashboard.Agents[0], Messages: []model.AgentMessage{{ID: "delivery", TargetAgentID: "agent", Prompt: "do work", Status: "queued", RuntimeID: "private", CreatedAt: 5, UpdatedAt: 5}}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")

	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{"secret-runtime", "/secret", "sessionPath", "rendererId"} {
		if strings.Contains(body, secret) {
			t.Fatalf("bootstrap leaked %q: %s", secret, body)
		}
	}
	var bootstrap CompanionBootstrap
	if err := json.Unmarshal(response.Body.Bytes(), &bootstrap); err != nil {
		t.Fatal(err)
	}
	if len(bootstrap.Repositories) != 1 || bootstrap.Repositories[0].Title != "Repository" || len(bootstrap.Workspaces) != 1 || len(bootstrap.Workspaces[0].Agents) != 2 || !bootstrap.Workspaces[0].Agents[0].CanCopyPlacement || bootstrap.Workspaces[0].Agents[1].CanCopyPlacement {
		t.Fatalf("bootstrap = %#v", bootstrap)
	}

	response = httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Agent.WorkspaceTitle != "Work" || len(detail.Timeline) != 1 || detail.Timeline[0].Kind != "delivery_queued" || detail.Timeline[0].EventID != "delivery:delivery:prompt" {
		t.Fatalf("agent detail = %#v", detail)
	}

	backend.view.Messages[0].Status = "completed"
	backend.view.Messages[0].Response = "finished before mirroring was enabled"
	backend.view.Messages[0].UpdatedAt = 6
	response = httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 2 || detail.Timeline[0].Kind != "delivery_completed" || detail.Timeline[1].Content != "finished before mirroring was enabled" {
		t.Fatalf("completed fallback timeline = %#v", detail.Timeline)
	}
}

func TestCompanionAgentResponseHasFinalEncodedSizeBound(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	messages := make([]model.AgentMessage, 0, 100)
	for index := 0; index < 100; index++ {
		messages = append(messages, model.AgentMessage{
			ID: "message-" + strconv.Itoa(index), TargetAgentID: "agent", Prompt: "work", Status: "completed",
			Response: strings.Repeat("\x01", 64<<10), CreatedAt: int64(index + 1), UpdatedAt: int64(index + 1),
		})
	}
	backend := &fakeCompanionBackend{view: model.AgentView{Agent: model.Agent{ID: "agent", WorkspaceID: "ws"}, Messages: messages}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("agent response = %d: %s", response.Code, response.Body.String())
	}
	if response.Body.Len() > 4<<20 {
		t.Fatalf("encoded agent response is %d bytes", response.Body.Len())
	}
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if !detail.HasMore || detail.MessageBefore == "" {
		t.Fatalf("bounded synthetic history has no next cursor: %#v", detail)
	}
}

func TestCompanionBoundKeepsMessageRetryCursorWhenAllPromptsAreDropped(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	workspace := model.Workspace{ID: "bounded-ws", Title: "Bounded", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "bounded-agent", WorkspaceID: workspace.ID, Title: "Agent", Status: "stopped", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutAgent(context.Background(), agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAgentRuntime(context.Background(), agent.ID, "runtime", "session", "/session"); err != nil {
		t.Fatal(err)
	}
	events := make([]model.ConversationEvent, 0, 100)
	for index := 1; index <= 100; index++ {
		events = append(events, model.ConversationEvent{
			EventID: "large-" + strconv.Itoa(index), RuntimeSeq: int64(index), Kind: "assistant_text_delta",
			Content: strings.Repeat("\x01", 64<<10), CreatedAt: int64(100 + index),
		})
	}
	if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", events); err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{
		view: model.AgentView{Agent: agent, Messages: []model.AgentMessage{{ID: "message", TargetAgentID: agent.ID, Prompt: "old prompt", Status: "queued", CreatedAt: 1, UpdatedAt: 1}}},
	}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID, nil))
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	promptPresent := slices.ContainsFunc(detail.Timeline, func(event model.ConversationEvent) bool { return event.EventID == "delivery:message:prompt" })
	if promptPresent || !detail.MessageHasMore || detail.MessageBefore == "" || !detail.ConversationHasMore {
		t.Fatalf("bounded stream cursors = %#v", detail)
	}
}

func TestCompanionAgentUsesMessagePageWhenBothCursorsArePresent(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{view: model.AgentView{
		Agent:    model.Agent{ID: "agent", WorkspaceID: "ws"},
		Messages: []model.AgentMessage{{ID: "unrelated", TargetAgentID: "agent", Prompt: "older durable prompt", Status: "completed", CreatedAt: 5, UpdatedAt: 5}},
	}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	path := "/api/v1/agents/agent?before=9&messageBefore=10.cursor"
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, path, nil))
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 1 || detail.Timeline[0].Content != "older durable prompt" {
		t.Fatalf("simultaneous cursor timeline = %#v", detail.Timeline)
	}
}

func TestMergeSyntheticTimelinePreservesPiStreamOrder(t *testing.T) {
	conversation := []model.ConversationEvent{
		{Sequence: 1, Kind: "assistant_message_start", CreatedAt: 100},
		{Sequence: 2, Kind: "assistant_text_delta", CreatedAt: 200},
		{Sequence: 3, Kind: "assistant_message_end", CreatedAt: 100},
	}
	timeline := mergeSyntheticTimeline(conversation, []model.ConversationEvent{{Kind: "delivery_queued", CreatedAt: 150}})
	positions := map[string]int{}
	for index, event := range timeline {
		positions[event.Kind] = index
	}
	if positions["assistant_message_start"] >= positions["assistant_text_delta"] || positions["assistant_text_delta"] >= positions["assistant_message_end"] {
		t.Fatalf("Pi stream order changed: %#v", timeline)
	}
}

func TestBoundPublicTimelineKeepsRealResumeBoundary(t *testing.T) {
	events := []model.ConversationEvent{{Sequence: 7, EventID: "event-7", Kind: "assistant_message_end", Content: "real"}}
	for index := 0; index < 10; index++ {
		id := "delivery:message-" + strconv.Itoa(index)
		events = append(events,
			model.ConversationEvent{EventID: id + ":prompt", Kind: "delivery_completed", Content: strings.Repeat("\x01", 64<<10)},
			model.ConversationEvent{EventID: id + ":response", Kind: "assistant_message_end", Content: strings.Repeat("\x01", 64<<10)},
		)
	}
	kept, dropped := boundPublicTimeline(events, 1<<20)
	if len(dropped) == 0 || !slices.ContainsFunc(kept, func(event model.ConversationEvent) bool { return event.Sequence == 7 }) {
		t.Fatalf("bounded timeline lost resume boundary: kept %#v, dropped %d", kept, len(dropped))
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 1<<20 {
		t.Fatalf("bounded timeline = %d bytes", len(encoded))
	}
}

func TestCompanionHistoryPageDoesNotDuplicateGloballyMirroredResponse(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	workspace := model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: workspace.ID, Title: "Worker", Status: "stopped", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutAgent(context.Background(), agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAgentRuntime(context.Background(), agent.ID, "runtime", "session", "/session"); err != nil {
		t.Fatal(err)
	}
	messageID := "5ef108a9-5194-4b33-a829-f514eeda8e4d"
	conversation := []model.ConversationEvent{
		{EventID: "user", RuntimeSeq: 1, Kind: "user_message", Role: "user", Content: "[delivery " + messageID + "]\ndo work", CreatedAt: 10},
		{EventID: "answer", RuntimeSeq: 2, Kind: "assistant_message_end", Role: "assistant", Content: "done\n", CreatedAt: 20},
	}
	for index := 3; index <= 101; index++ {
		conversation = append(conversation, model.ConversationEvent{EventID: "event-" + strconv.Itoa(index), RuntimeSeq: int64(index), Kind: "lifecycle", Content: "busy", CreatedAt: int64(20 + index)})
	}
	if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", conversation); err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{view: model.AgentView{
		Agent:    agent,
		Messages: []model.AgentMessage{{ID: messageID, TargetAgentID: agent.ID, Prompt: "do work", Response: "done", Status: "completed", CreatedAt: 10, UpdatedAt: 20}},
	}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")

	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID, nil))
	var latest CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if !latest.HasMore || latest.Before == 0 {
		t.Fatalf("latest page boundary = %#v", latest)
	}
	allEvents := append([]model.ConversationEvent(nil), latest.Timeline...)
	mirrored := slices.Contains(latest.MirroredDeliveryResponses, messageID)
	page := latest
	for page.ConversationHasMore {
		response = httptest.NewRecorder()
		path := "/api/v1/agents/" + agent.ID + "?before=" + strconv.FormatInt(page.Before, 10)
		serveCompanion(server, response, httptest.NewRequest(http.MethodGet, path, nil))
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		allEvents = append(page.Timeline, allEvents...)
		mirrored = mirrored || slices.Contains(page.MirroredDeliveryResponses, messageID)
	}
	responses := 0
	for _, event := range allEvents {
		if event.Kind == "assistant_message_end" && strings.TrimSpace(event.Content) == "done" {
			responses++
		}
	}
	if responses != 1 || !mirrored {
		t.Fatalf("mirrored response appeared %d times across pages, mirrored %v", responses, mirrored)
	}
}

func TestCompanionRejectsUnexpectedHostFunnelAndTailscaleIdentity(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := NewCompanionServer(st, &fakeCompanionBackend{}, "https://galpon.example.test")
	server.TailscaleUser = "owner@example.test"

	request := httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusMisdirectedRequest {
		t.Fatalf("unexpected host status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	request.Host = server.host
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing Tailscale identity status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	request.Host = server.host
	request.Header.Set("Tailscale-User-Login", server.TailscaleUser)
	request.Header.Set("Tailscale-Funnel-Request", "true")
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("Funnel status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	request.Host = server.host
	request.Header.Set("Tailscale-User-Login", server.TailscaleUser)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site API status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil)
	request.Host = server.host
	request.Header.Set("Tailscale-User-Login", server.TailscaleUser)
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authorized Tailscale status = %d: %s", response.Code, response.Body.String())
	}
}

func TestCompanionMutationsRequireExactOriginAndIdempotencyKey(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{}
	server := NewCompanionServer(st, backend, "https://galpon.example.test")

	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/messages", bytes.NewBufferString(`{"prompt":"continue"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example.test")
	request.Header.Set("Idempotency-Key", "key")
	response := httptest.NewRecorder()
	serveCompanion(server, response, request)
	if response.Code != http.StatusForbidden || backend.sent != 0 {
		t.Fatalf("wrong-origin response = %d, sent = %d", response.Code, backend.sent)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/messages", bytes.NewBufferString(`{"prompt":"continue"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://galpon.example.test")
	response = httptest.NewRecorder()
	serveCompanion(server, response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing-key status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/messages", bytes.NewBufferString(`{"prompt":"continue"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://galpon.example.test")
	request.Header.Set("Idempotency-Key", "key")
	response = httptest.NewRecorder()
	serveCompanion(server, response, request)
	if response.Code != http.StatusOK || backend.sent != 1 {
		t.Fatalf("valid response = %d, sent = %d: %s", response.Code, backend.sent, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" || response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("browser headers = %#v", response.Header())
	}
}

func TestCompanionSnapshotCursorPrecedesOverlappingProjectionMutation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	first, err := st.AppendCompanionEvent(context.Background(), "invalidate", "first", "ws")
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{}
	backend.dashboardHook = func() {
		backend.dashboardHook = nil
		if _, err := st.AppendCompanionEvent(context.Background(), "invalidate", "overlap", "ws"); err != nil {
			t.Fatal(err)
		}
	}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil))
	var snapshot CompanionBootstrap
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Cursor != first.Sequence {
		t.Fatalf("snapshot cursor = %d, want pre-projection %d", snapshot.Cursor, first.Sequence)
	}
	events, err := st.CompanionEventsAfter(context.Background(), snapshot.Cursor, 10)
	if err != nil || len(events) != 1 || events[0].AgentID != "overlap" {
		t.Fatalf("replay after snapshot = %#v, %v", events, err)
	}
}

func TestCompanionAgentCursorPrecedesOverlappingProjectionMutation(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	first, err := st.AppendCompanionEvent(context.Background(), "invalidate", "first", "ws")
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{view: model.AgentView{Agent: model.Agent{ID: "agent", WorkspaceID: "ws"}}}
	backend.agentHook = func() {
		backend.agentHook = nil
		if _, err := st.AppendCompanionEvent(context.Background(), "invalidate", "overlap", "ws"); err != nil {
			t.Fatal(err)
		}
	}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	var snapshot CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Cursor != first.Sequence {
		t.Fatalf("agent cursor = %d, want pre-projection %d", snapshot.Cursor, first.Sequence)
	}
	events, err := st.CompanionEventsAfter(context.Background(), snapshot.Cursor, 10)
	if err != nil || len(events) != 1 || events[0].AgentID != "overlap" {
		t.Fatalf("agent replay after snapshot = %#v, %v", events, err)
	}
}

func TestCompanionSSEReplaysAndPrefersLastEventID(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	first, err := st.AppendCompanionEvent(context.Background(), "invalidate", "old", "ws")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.AppendCompanionEvent(context.Background(), "invalidate", "agent", "ws")
	if err != nil {
		t.Fatal(err)
	}
	server := NewCompanionServer(st, &fakeCompanionBackend{}, "http://127.0.0.1:8420")
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/events?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", strconvFormat(first.Sequence))
	request.Host = server.host
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	data, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "id: "+strconvFormat(second.Sequence)) || !strings.Contains(data, `"agentId":"agent"`) || strings.Contains(data, `"agentId":"old"`) {
		t.Fatalf("SSE event = %q", data)
	}
}

func TestCompanionSSEResetsCursorBeyondRetainedRange(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	server := NewCompanionServer(st, &fakeCompanionBackend{}, "http://127.0.0.1:8420")
	httpServer := httptest.NewServer(server.http.Handler)
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/events?after=999", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = server.host
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := readSSEEvent(bufio.NewReader(response.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(data, "id: 0") || !strings.Contains(data, "event: reset") {
		t.Fatalf("SSE reset = %q", data)
	}
}

func serveCompanion(server *CompanionServer, response *httptest.ResponseRecorder, request *http.Request) {
	request.Host = server.host
	server.http.Handler.ServeHTTP(response, request)
}

func readSSEEvent(reader *bufio.Reader) (string, error) {
	var out strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		out.WriteString(line)
		if line == "\n" {
			return out.String(), nil
		}
	}
}

func strconvFormat(value int64) string { return strconv.FormatInt(value, 10) }
