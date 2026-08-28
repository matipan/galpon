package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
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
	lastPrompt      string
	lastImages      []model.ImageAttachment
	created         int
	operations      model.WorkspaceOperations
}

func (f *fakeCompanionBackend) CompanionDashboard(context.Context) (model.Dashboard, error) {
	if f.dashboardHook != nil {
		f.dashboardHook()
	}
	return f.dashboard, f.dashboardErr
}
func (f *fakeCompanionBackend) WorkspaceOperations(_ context.Context, id string) (model.WorkspaceOperations, error) {
	value := f.operations
	if value.Version == 0 {
		value = model.WorkspaceOperations{Version: 1, Workspace: model.OperationsWorkspace{ID: id, Title: "Work"}, Agents: []model.OperationsAgent{}, Work: []model.WorkItem{}, Timeline: []model.OperationsTimelineFact{}}
	}
	return value, nil
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
	f.lastPrompt = prompt
	return model.AgentMessage{ID: "message", TargetAgentID: id, Prompt: prompt, Status: "queued", CreatedAt: 3, UpdatedAt: 3}, nil
}
func (f *fakeCompanionBackend) SendCompanionImages(_ context.Context, id, prompt, _ string, images []model.ImageAttachment) (model.AgentMessage, error) {
	f.sent++
	f.lastPrompt = prompt
	f.lastImages = images
	result := []model.ImageAttachment{{ID: "stored-image", Name: images[0].Name, MimeType: "image/png", Size: int64(len(images[0].Data)), Data: images[0].Data}}
	return model.AgentMessage{ID: "message", TargetAgentID: id, Prompt: prompt, Images: &result, Status: "queued", CreatedAt: 3, UpdatedAt: 3}, nil
}

type fakeAudioTranscriber struct {
	transcript string
	err        error
	received   string
	language   string
}

func (f *fakeAudioTranscriber) Transcribe(_ context.Context, audio io.Reader, language string) (string, error) {
	content, _ := io.ReadAll(audio)
	f.received = string(content)
	f.language = language
	return f.transcript, f.err
}
func (f *fakeCompanionBackend) CreateAgentFromSource(_ context.Context, in CreateAgentFromSourceRequest, _ string) (CreateAgentFromSourceResult, error) {
	f.created++
	return CreateAgentFromSourceResult{Agent: model.Agent{ID: "new", WorkspaceID: "ws", Title: in.Title, Role: in.Role, Placement: model.AgentPlacement{Type: "worktrees", Worktrees: []model.AgentWorktree{{WorktreeID: "new-wt"}}}, Status: "stopped"}, InitialMessage: model.AgentMessage{ID: "initial", TargetAgentID: "new", Prompt: in.Prompt, Status: "queued"}, StartPending: true}, nil
}

func TestCompanionServesReadOnlyWorkspaceOperations(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{operations: model.WorkspaceOperations{
		Version: 1, Workspace: model.OperationsWorkspace{ID: "ws", Title: "Work"},
		Agents: []model.OperationsAgent{}, Work: []model.WorkItem{}, Timeline: []model.OperationsTimelineFact{},
	}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/ws/operations", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("operations status = %d: %s", response.Code, response.Body.String())
	}
	var value model.WorkspaceOperations
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || value.Version != 1 || value.Workspace.ID != "ws" {
		t.Fatalf("operations = %#v, %v", value, err)
	}
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
		cache       string
	}{
		{path: "/", contentType: "text/html", contains: "Galpón Companion", cache: "no-cache"},
		{path: "/app.mjs", contentType: "text/javascript", contains: "CompanionAPI", cache: "no-cache"},
		{path: "/detail-state.mjs", contentType: "text/javascript", contains: "mergeRefreshedDetail", cache: "no-cache"},
		{path: "/companion-state.mjs", contentType: "text/javascript", contains: "readAgentDraft", cache: "no-cache"},
		{path: "/rich-text.mjs", contentType: "text/javascript", contains: "renderRichText", cache: "no-cache"},
		{path: "/performance.mjs", contentType: "text/javascript", contains: "createPerformanceTracker", cache: "no-cache"},
		{path: "/styles.css", contentType: "text/css", contains: "Tokyo Night", cache: "no-cache"},
		{path: "/manifest.webmanifest", contentType: "application/manifest+json", contains: "Galpón Companion", cache: "no-cache"},
		{path: "/icon.svg", contentType: "image/svg+xml", contains: "#c099ff", cache: "no-cache"},
	} {
		response := httptest.NewRecorder()
		serveCompanion(server, response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), test.contentType) || !strings.Contains(response.Body.String(), test.contains) || response.Header().Get("Cache-Control") != test.cache {
			t.Fatalf("GET %s = %d, content-type %q, cache %q, body %q", test.path, response.Code, response.Header().Get("Content-Type"), response.Header().Get("Cache-Control"), response.Body.String())
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
			{ID: "child", WorkspaceID: "ws", Title: "Delegated reviewer", CreatedByAgentID: "agent", Presentation: "background", Status: "idle", Placement: model.AgentPlacement{Type: "none", CWD: "/private/child"}},
			{ID: "grandchild", WorkspaceID: "ws", Title: "Nested delegate", CreatedByAgentID: "child", Presentation: "background", Status: "stopped", Placement: model.AgentPlacement{Type: "none", CWD: "/private/grandchild"}},
		},
	}}
	backend.view = model.AgentView{Agent: backend.dashboard.Agents[0], Messages: []model.AgentMessage{{ID: "delivery", TargetAgentID: "agent", Prompt: "do work", Status: "queued", RuntimeID: "private", IdempotencyKey: "client-request", CreatedAt: 5, UpdatedAt: 5}}}
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
	delegated := bootstrap.Workspaces[0].Agents[0].DelegatedAgents
	if len(delegated) != 1 || delegated[0].ID != "child" || len(delegated[0].DelegatedAgents) != 1 || delegated[0].DelegatedAgents[0].ID != "grandchild" {
		t.Fatalf("delegated tree = %#v", delegated)
	}

	response = httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Agent.WorkspaceTitle != "Work" || len(detail.DelegatedAgents) != 1 || detail.DelegatedAgents[0].ID != "child" || len(detail.Timeline) != 1 || detail.Timeline[0].Kind != "delivery_queued" || detail.Timeline[0].EventID != "delivery:delivery:prompt" || detail.Timeline[0].ClientRequestID != "client-request" {
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

func TestCompanionReplacesHashedDeliveryEnvelopeWithCleanUserMessage(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	workspace := model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: workspace.ID, Title: "Worker", Status: "running", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutAgent(context.Background(), agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAgentRuntime(context.Background(), agent.ID, "runtime", "session", "/session"); err != nil {
		t.Fatal(err)
	}
	messageID := "message:" + strings.Repeat("a", 64)
	envelope := "Work request [delivery " + messageID + "]:\n\nVisible phone message\n\n---\n\nDelivery instructions: internal protocol text"
	if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", []model.ConversationEvent{{
		EventID: "pi-prompt", RuntimeSeq: 1, Kind: "user_message", Role: "user", Content: envelope, CreatedAt: 10,
	}}); err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{
		dashboard: model.Dashboard{Workspaces: []model.Workspace{workspace}, Agents: []model.Agent{agent}},
		view: model.AgentView{Agent: agent, Messages: []model.AgentMessage{{
			ID: messageID, TargetAgentID: agent.ID, Prompt: "Visible phone message", Status: "queued", CreatedAt: 10, UpdatedAt: 10,
		}}},
	}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID, nil))
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 1 {
		t.Fatalf("delivery timeline = %#v", detail.Timeline)
	}
	prompt := detail.Timeline[0]
	if prompt.EventID != "delivery:"+messageID+":prompt" || prompt.Sequence != 1 || prompt.Kind != "delivery_queued" || prompt.Content != "Visible phone message" {
		t.Fatalf("clean delivery prompt = %#v", prompt)
	}
}

func TestCompanionOmitsUnmirroredResultConsumedByAwait(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{view: model.AgentView{
		Agent: model.Agent{ID: "agent", WorkspaceID: "ws"},
		Messages: []model.AgentMessage{{
			ID: "result:5ef108a9-5194-4b33-a829-f514eeda8e4d", SenderAgentID: "reviewer", SenderTitle: "Parity reviewer", TargetAgentID: "agent", Kind: "result",
			Prompt: "completed delegated result", Response: "consumed by await", Status: "completed", NotificationState: "suppressed", CreatedAt: 10, UpdatedAt: 20,
		}},
	}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 0 {
		t.Fatalf("await-consumed result appeared as a user turn: %#v", detail.Timeline)
	}
	backend.view.Messages[0].NotificationState = "completed"
	response = httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 0 {
		t.Fatalf("legacy await-consumed result appeared as a user turn: %#v", detail.Timeline)
	}
	backend.view.Messages[0].Response = "normal result"
	response = httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 2 || !detail.Timeline[0].IsAgentDelivery || detail.Timeline[0].DeliveryKind != "result" || detail.Timeline[0].DeliverySenderTitle != "Parity reviewer" {
		t.Fatalf("normal unmirrored result lost its bot delivery fallback: %#v", detail.Timeline)
	}
	backend.view.Messages[0].Status = "queued"
	backend.view.Messages[0].NotificationState = "pending"
	backend.view.Messages[0].Response = ""
	response = httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 1 || detail.Timeline[0].Kind != "delivery_queued" {
		t.Fatalf("queued result fallback = %#v", detail.Timeline)
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

func TestCompanionBoundKeepsQueuedPromptAtStableTail(t *testing.T) {
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
	if !promptPresent || detail.MessageHasMore || detail.MessageBefore != "" || !detail.ConversationHasMore {
		t.Fatalf("bounded stable tail = prompt %v, messageMore %v, messageBefore %q, conversationMore %v", promptPresent, detail.MessageHasMore, detail.MessageBefore, detail.ConversationHasMore)
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
	if len(timeline) != 4 || timeline[0].Kind != "assistant_message_start" || timeline[1].Kind != "assistant_text_delta" || timeline[2].Kind != "assistant_message_end" || timeline[3].Kind != "delivery_queued" {
		t.Fatalf("synthetic delivery changed Pi stream position: %#v", timeline)
	}
}

func TestTrimConversationMovesDetachedLeadingResponseToOlderPage(t *testing.T) {
	events := []model.ConversationEvent{
		{Sequence: 7, Kind: "assistant_text_delta", Role: "assistant", Content: "detached"},
		{Sequence: 8, Kind: "assistant_message_end", Role: "assistant", Content: "detached"},
		{Sequence: 9, Kind: "user_message", Role: "user", Content: "next prompt"},
		{Sequence: 10, Kind: "assistant_message_end", Role: "assistant", Content: "next answer"},
	}
	trimmed, changed := trimConversationToFirstUser(events)
	if !changed || len(trimmed) != 2 || trimmed[0].Sequence != 9 {
		t.Fatalf("trimmed conversation = %#v, changed %v", trimmed, changed)
	}
	toolOnly := events[:2]
	if retained, changed := trimConversationToFirstUser(toolOnly); changed || len(retained) != 2 {
		t.Fatalf("conversation without a user boundary was trimmed: %#v, %v", retained, changed)
	}
}

func TestEqualResponsesClaimDifferentConversationEvents(t *testing.T) {
	claimed := make(map[int64]bool)
	first, ok := claimLastResponseSequence([]int64{10, 20}, claimed)
	if !ok || first != 20 {
		t.Fatalf("first response sequence = %d, %v", first, ok)
	}
	second, ok := claimLastResponseSequence([]int64{10, 20}, claimed)
	if !ok || second != 10 {
		t.Fatalf("second response sequence = %d, %v", second, ok)
	}
	if _, ok := claimLastResponseSequence([]int64{10, 20}, claimed); ok {
		t.Fatal("claimed response sequence was reused")
	}
}

func TestConversationWindowDetectsMissingStreamStart(t *testing.T) {
	if !conversationWindowStartsMidStream([]model.ConversationEvent{{Kind: "assistant_text_delta"}}) {
		t.Fatal("assistant delta without a start was accepted as complete context")
	}
	completeAssistant := []model.ConversationEvent{{Kind: "assistant_message_start"}, {Kind: "assistant_text_delta"}, {Kind: "assistant_message_end"}}
	if conversationWindowStartsMidStream(completeAssistant) {
		t.Fatal("complete assistant stream requested older context")
	}
	if !conversationWindowNeedsOlderContext(completeAssistant) {
		t.Fatal("a valid rolling page without its user boundary was accepted as stable context")
	}
	completeTurn := append([]model.ConversationEvent{{Kind: "user_message", Role: "user"}}, completeAssistant...)
	if conversationWindowNeedsOlderContext(completeTurn) {
		t.Fatal("a complete user turn requested older context")
	}
	if !conversationWindowStartsMidStream([]model.ConversationEvent{{Kind: "tool_execution_update", ToolCallID: "call"}}) {
		t.Fatal("tool update without a start was accepted as complete context")
	}
	if !conversationWindowStartsMidStream([]model.ConversationEvent{{Kind: "tool_execution_end", ToolCallID: "call"}}) {
		t.Fatal("tool end without a start was accepted as complete context")
	}
}

func TestCompanionLatestPageKeepsOneStableToolTurnAcrossRollingCutoffs(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	workspace := model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "agent", WorkspaceID: workspace.ID, Title: "Worker", Status: "running", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutAgent(context.Background(), agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAgentRuntime(context.Background(), agent.ID, "runtime", "session", "/session"); err != nil {
		t.Fatal(err)
	}
	events := []model.ConversationEvent{{EventID: "prompt", RuntimeSeq: 1, Kind: "user_message", Role: "user", Content: "inspect", CreatedAt: 1}}
	for tool := 1; tool <= 11; tool++ {
		callID := "call-" + strconv.Itoa(tool)
		for _, event := range []model.ConversationEvent{
			{Kind: "assistant_message_start", Role: "assistant"},
			{Kind: "assistant_message_end", Role: "assistant"},
			{Kind: "tool_execution_start", Role: "tool", ToolName: "read", ToolCallID: callID},
			{Kind: "tool_execution_update", Role: "tool", ToolName: "read", ToolCallID: callID},
			{Kind: "tool_execution_end", Role: "tool", ToolName: "read", ToolCallID: callID},
		} {
			sequence := int64(len(events) + 1)
			event.EventID = "event-" + strconv.FormatInt(sequence, 10)
			event.RuntimeSeq = sequence
			event.CreatedAt = sequence
			events = append(events, event)
		}
	}
	backend := &fakeCompanionBackend{view: model.AgentView{Agent: agent}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	inserted := 0
	for _, checkpoint := range []struct {
		end       int
		wantTools int
	}{{end: 51, wantTools: 10}, {end: 54, wantTools: 11}, {end: 56, wantTools: 11}} {
		if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", events[inserted:checkpoint.end]); err != nil {
			t.Fatal(err)
		}
		inserted = checkpoint.end
		response := httptest.NewRecorder()
		serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID, nil))
		var detail CompanionAgentDetail
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		toolCount := 0
		for _, event := range detail.Timeline {
			if event.Kind == "tool_execution_start" {
				toolCount++
			}
		}
		if toolCount != checkpoint.wantTools || len(detail.Timeline) == 0 || detail.Timeline[0].Role != "user" {
			t.Fatalf("checkpoint %d returned %d tools from %#v, want one turn with %d tools", checkpoint.end, toolCount, detail.Timeline, checkpoint.wantTools)
		}
	}

	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID+"?after=1", nil))
	var catchup CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &catchup); err != nil {
		t.Fatal(err)
	}
	if !catchup.CatchupHasMore || catchup.CatchupAfter != 41 || len(catchup.Timeline) != companionConversationPageSize || catchup.Timeline[0].Sequence != 2 || catchup.Timeline[len(catchup.Timeline)-1].Sequence != 41 {
		t.Fatalf("forward catchup page = after %d, more %v, timeline %#v", catchup.CatchupAfter, catchup.CatchupHasMore, catchup.Timeline)
	}

	futureMessageID := "11111111-1111-4111-8111-111111111111"
	future := make([]model.ConversationEvent, 0, 44)
	for sequence := int64(57); sequence < 100; sequence++ {
		future = append(future, model.ConversationEvent{EventID: "event-" + strconv.FormatInt(sequence, 10), RuntimeSeq: sequence, Kind: "lifecycle", Content: "busy", CreatedAt: sequence})
	}
	future = append(future, model.ConversationEvent{EventID: "future-prompt", RuntimeSeq: 100, Kind: "user_message", Role: "user", Content: "[delivery " + futureMessageID + "] future", CreatedAt: 100})
	if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", future); err != nil {
		t.Fatal(err)
	}
	backend.view.Messages = []model.AgentMessage{{ID: futureMessageID, TargetAgentID: agent.ID, Prompt: "future", Status: "delivered", CreatedAt: 100, UpdatedAt: 100}}
	response = httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID+"?after=56", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &catchup); err != nil {
		t.Fatal(err)
	}
	anchor := slices.IndexFunc(catchup.Timeline, func(event model.ConversationEvent) bool {
		return event.EventID == "delivery:"+futureMessageID+":prompt"
	})
	if !catchup.CatchupHasMore || catchup.CatchupAfter != 96 || anchor < 0 || catchup.Timeline[anchor].Sequence != 100 || !catchup.Timeline[anchor].IsAnchor {
		t.Fatalf("future anchor changed contiguous catchup cursor: after %d, more %v, timeline %#v", catchup.CatchupAfter, catchup.CatchupHasMore, catchup.Timeline)
	}
}

func TestMirroredDeliveryPromptKeepsItsPiSequence(t *testing.T) {
	resultID := "result:17123b86-4213-4c8b-829a-e9ce266e614f"
	resultEvents := []model.ConversationEvent{{Sequence: 9, Kind: "user_message", Content: "[delivery " + resultID + "] internal result"}}
	if ids := conversationDeliveryIDs(resultEvents); !slices.Equal(ids, []string{resultID}) {
		t.Fatalf("result delivery IDs = %#v", ids)
	}
	resultConversation, resultReplaced := replaceMirroredDeliveryPrompts(resultEvents, []model.AgentMessage{{
		ID: resultID, SenderAgentID: "reviewer", SenderTitle: "Parity reviewer", TargetAgentID: "agent", Kind: "result", Prompt: "visible result", Status: "delivered",
	}}, "agent")
	if !resultReplaced[resultID] || resultConversation[0].EventID != "delivery:"+resultID+":prompt" || resultConversation[0].Content != "visible result" || !resultConversation[0].IsAgentDelivery || resultConversation[0].DeliverySenderTitle != "Parity reviewer" || resultConversation[0].DeliveryKind != "result" {
		t.Fatalf("mirrored result replacement = %#v, %#v", resultConversation, resultReplaced)
	}
	batchFirst := "8f7b64c1-956d-4fd8-bb88-ce75cb27f22f"
	batchSecond := "67c51f16-6120-4bc6-8b63-0ac634389bab"
	batchEvents := []model.ConversationEvent{{Sequence: 8, Kind: "user_message", Content: "[delivery " + batchFirst + "] first\n[delivery " + batchSecond + "] second"}}
	batchConversation, batchReplaced := replaceMirroredDeliveryPrompts(batchEvents, []model.AgentMessage{
		{ID: batchFirst, TargetAgentID: "agent", Prompt: "first prompt", Status: "completed"},
		{ID: batchSecond, TargetAgentID: "agent", Prompt: "second prompt", Status: "completed"},
	}, "agent")
	if !batchReplaced[batchFirst] || !batchReplaced[batchSecond] || !strings.Contains(batchConversation[0].Content, "first prompt\n\n---\n\nsecond prompt") {
		t.Fatalf("mirrored batch replacement = %#v, %#v", batchConversation, batchReplaced)
	}
	messageID := "message:" + strings.Repeat("b", 64)
	cursor := companionMessageCursor(200, messageID)
	cursorCreatedAt, cursorMessageID, cursorErr := parseCompanionMessageCursor(cursor)
	if cursorErr != nil || cursorCreatedAt != 200 || cursorMessageID != messageID {
		t.Fatalf("hashed delivery cursor = %d, %q, %v", cursorCreatedAt, cursorMessageID, cursorErr)
	}
	events := []model.ConversationEvent{
		{Sequence: 10, EventID: "event-10", Kind: "user_message", Role: "user", Content: "Message [delivery " + messageID + "]: internal envelope", CreatedAt: 200},
		{Sequence: 11, EventID: "event-11", Kind: "tool_execution_start", ToolCallID: "call", CreatedAt: 300},
	}
	messages := []model.AgentMessage{{ID: messageID, TargetAgentID: "agent", Prompt: "visible prompt", Status: "completed", CreatedAt: 100}}
	conversation, replaced := replaceMirroredDeliveryPrompts(events, messages, "agent")
	if !replaced[messageID] || len(conversation) != 2 {
		t.Fatalf("replacement = %#v, %#v", conversation, replaced)
	}
	prompt := conversation[0]
	if prompt.Sequence != 10 || prompt.EventID != "delivery:"+messageID+":prompt" || prompt.Kind != "delivery_completed" || prompt.Content != "visible prompt" {
		t.Fatalf("prompt replacement = %#v", prompt)
	}
	if conversation[1].Kind != "tool_execution_start" {
		t.Fatalf("action moved before prompt: %#v", conversation)
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

func TestBoundPublicTimelineDoesNotSplitSharedSequence(t *testing.T) {
	events := []model.ConversationEvent{
		{Sequence: 1, EventID: "delivery:direct:prompt", Content: strings.Repeat("a", 300)},
		{Sequence: 1, EventID: "delivery:bot:prompt", Content: strings.Repeat("b", 300)},
		{Sequence: 2, EventID: "event-2", Content: strings.Repeat("c", 300)},
	}
	kept, dropped := boundPublicTimeline(events, 1000)
	if len(kept) != 1 || kept[0].Sequence != 2 || len(dropped) != 2 || dropped[0].Sequence != 1 || dropped[1].Sequence != 1 {
		t.Fatalf("shared sequence was split: kept %#v, dropped %#v", kept, dropped)
	}
}

func TestCompanionBatchUsesOneSyntheticFallbackResponse(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	workspace := model.Workspace{ID: "batch-ws", Title: "Work", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "batch-agent", WorkspaceID: workspace.ID, Title: "Worker", Status: "stopped", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutAgent(context.Background(), agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAgentRuntime(context.Background(), agent.ID, "runtime", "session", "/session"); err != nil {
		t.Fatal(err)
	}
	firstID := "5ef108a9-5194-4b33-a829-f514eeda8e4d"
	secondID := "2c3ef933-e9eb-47d6-a762-a87cf6677ae0"
	if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", []model.ConversationEvent{{
		EventID: "batch-prompt", RuntimeSeq: 1, Kind: "user_message", Role: "user",
		Content: "[delivery " + firstID + "] first\n[delivery " + secondID + "] second", CreatedAt: 10,
	}}); err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{view: model.AgentView{
		Agent: agent,
		Messages: []model.AgentMessage{
			{ID: firstID, TargetAgentID: agent.ID, Prompt: "first", Response: "batch done", Status: "completed", CreatedAt: 10, UpdatedAt: 20},
			{ID: secondID, SenderAgentID: "worker", SenderTitle: "Worker bot", TargetAgentID: agent.ID, Kind: "request", Prompt: "second", Response: "batch done", Status: "completed", CreatedAt: 10, UpdatedAt: 20},
		},
	}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	readDetail := func() CompanionAgentDetail {
		response := httptest.NewRecorder()
		serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID, nil))
		var detail CompanionAgentDetail
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	detail := readDetail()
	if len(detail.Timeline) != 3 || detail.Timeline[0].Role != "user" || detail.Timeline[0].IsAgentDelivery || detail.Timeline[1].Role != "user" || !detail.Timeline[1].IsAgentDelivery || detail.Timeline[1].DeliverySenderTitle != "Worker bot" || detail.Timeline[2].Role != "assistant" || detail.Timeline[2].Content != "batch done" {
		t.Fatalf("completed mixed batch fallback = %#v", detail.Timeline)
	}
	if detail.Timeline[0].Sequence != 1 || detail.Timeline[1].Sequence != 1 {
		t.Fatalf("mixed batch did not keep its shared sequence: %#v", detail.Timeline)
	}
	for index := range backend.view.Messages {
		backend.view.Messages[index].Status = "failed"
		backend.view.Messages[index].Response = ""
	}
	detail = readDetail()
	if len(detail.Timeline) != 3 || detail.Timeline[2].Role != "assistant" || !detail.Timeline[2].IsError || detail.Timeline[2].Content != "The agent could not complete this request." {
		t.Fatalf("failed mixed batch fallback = %#v", detail.Timeline)
	}
}

func TestCompanionDoesNotAnchorPromptWhenItsLeadingResponseWasTrimmed(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	workspace := model.Workspace{ID: "trim-ws", Title: "Work", Status: "active", CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutWorkspace(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	agent := model.Agent{ID: "trim-agent", WorkspaceID: workspace.ID, Title: "Worker", Status: "stopped", Placement: model.AgentPlacement{Type: "none"}, CreatedAt: 1, UpdatedAt: 1}
	if err := st.PutAgent(context.Background(), agent, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterAgentRuntime(context.Background(), agent.ID, "runtime", "session", "/session"); err != nil {
		t.Fatal(err)
	}
	messageID := "5ef108a9-5194-4b33-a829-f514eeda8e4d"
	events := []model.ConversationEvent{
		{EventID: "old-prompt", RuntimeSeq: 1, Kind: "user_message", Role: "user", Content: "[delivery " + messageID + "] old", CreatedAt: 10},
		{EventID: "old-answer", RuntimeSeq: 2, Kind: "assistant_message_end", Role: "assistant", Content: "done", CreatedAt: 20},
		{EventID: "next-prompt", RuntimeSeq: 3, Kind: "user_message", Role: "user", Content: "next", CreatedAt: 30},
		{EventID: "next-answer", RuntimeSeq: 4, Kind: "assistant_message_end", Role: "assistant", Content: "next done", CreatedAt: 40},
	}
	for index := 5; index <= 41; index++ {
		events = append(events, model.ConversationEvent{EventID: "later-" + strconv.Itoa(index), RuntimeSeq: int64(index), Kind: "lifecycle", Content: "later", CreatedAt: int64(40 + index)})
	}
	if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", events); err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{view: model.AgentView{
		Agent: agent,
		Messages: []model.AgentMessage{{
			ID: messageID, TargetAgentID: agent.ID, Prompt: "old", Response: "done", Status: "completed", CreatedAt: 10, UpdatedAt: 20,
		}},
	}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID, nil))
	var detail CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Before != 3 || len(detail.Timeline) == 0 || detail.Timeline[0].Sequence != 3 {
		t.Fatalf("trimmed latest page boundary = %d, timeline %#v", detail.Before, detail.Timeline)
	}
	if slices.ContainsFunc(detail.Timeline, func(event model.ConversationEvent) bool {
		return strings.HasPrefix(event.EventID, "delivery:"+messageID+":")
	}) {
		t.Fatalf("trimmed response left a detached completed prompt: %#v", detail.Timeline)
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
	batchedMessageID := "2c3ef933-e9eb-47d6-a762-a87cf6677ae0"
	activeMessageID := "229e59b6-a8b7-47f8-8e93-e185498189c0"
	conversation := []model.ConversationEvent{
		{EventID: "user", RuntimeSeq: 1, Kind: "user_message", Role: "user", Content: "[delivery " + messageID + "]\ndo work\n[delivery " + batchedMessageID + "]\ndo more", CreatedAt: 10},
		{EventID: "answer", RuntimeSeq: 2, Kind: "assistant_message_end", Role: "assistant", Content: "done\n", CreatedAt: 20},
		{EventID: "active-user", RuntimeSeq: 3, Kind: "user_message", Role: "user", Content: "[delivery " + activeMessageID + "]\nkeep working", CreatedAt: 30},
	}
	for index := 4; index <= 102; index++ {
		conversation = append(conversation, model.ConversationEvent{EventID: "event-" + strconv.Itoa(index), RuntimeSeq: int64(index), Kind: "lifecycle", Content: "busy", CreatedAt: int64(20 + index)})
	}
	if _, err := st.PutConversationEvents(context.Background(), agent.ID, "runtime", conversation); err != nil {
		t.Fatal(err)
	}
	backend := &fakeCompanionBackend{view: model.AgentView{
		Agent: agent,
		Messages: []model.AgentMessage{
			{ID: messageID, TargetAgentID: agent.ID, Prompt: "do work", Response: "done", Status: "completed", CreatedAt: 10, UpdatedAt: 20},
			{ID: batchedMessageID, TargetAgentID: agent.ID, Prompt: "do more", Response: "done", Status: "completed", CreatedAt: 10, UpdatedAt: 20},
			{ID: activeMessageID, TargetAgentID: agent.ID, Prompt: "keep working", Status: "delivered", CreatedAt: 30, UpdatedAt: 30},
		},
	}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")

	response := httptest.NewRecorder()
	serveCompanion(server, response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+agent.ID, nil))
	var latest CompanionAgentDetail
	if err := json.Unmarshal(response.Body.Bytes(), &latest); err != nil {
		t.Fatal(err)
	}
	if !latest.HasMore || latest.Before != 3 {
		t.Fatalf("latest page did not start at its stable user boundary: before %d", latest.Before)
	}
	if slices.ContainsFunc(latest.Timeline, func(event model.ConversationEvent) bool {
		return strings.HasPrefix(event.EventID, "delivery:"+messageID+":") || strings.HasPrefix(event.EventID, "delivery:"+batchedMessageID+":")
	}) {
		t.Fatalf("older mirrored delivery was detached onto latest page: %#v", latest.Timeline)
	}
	activePrompt := slices.IndexFunc(latest.Timeline, func(event model.ConversationEvent) bool {
		return event.EventID == "delivery:"+activeMessageID+":prompt"
	})
	if activePrompt < 0 || latest.Timeline[activePrompt].Sequence != 3 || latest.Timeline[activePrompt].IsAnchor {
		t.Fatalf("active prompt was not retained at its durable boundary: %#v", latest.Timeline)
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
	prompts := 0
	responses := 0
	canonicalResponse := false
	for _, event := range allEvents {
		if event.EventID == "delivery:"+messageID+":prompt" {
			prompts++
		}
		if event.Kind == "assistant_message_end" && strings.TrimSpace(event.Content) == "done" {
			responses++
			canonicalResponse = canonicalResponse || event.EventID == "delivery:"+messageID+":response" || event.EventID == "delivery:"+batchedMessageID+":response"
		}
	}
	batchPromptVisible := slices.ContainsFunc(allEvents, func(event model.ConversationEvent) bool {
		return strings.Contains(event.Content, "do work\n\n---\n\ndo more")
	})
	if prompts != 1 || responses != 1 || !mirrored || !canonicalResponse || !batchPromptVisible {
		t.Fatalf("mirrored delivery across pages = prompts %d, responses %d, mirrored %v, canonical response %v, batch prompt %v", prompts, responses, mirrored, canonicalResponse, batchPromptVisible)
	}
	seenIDs := make(map[string]bool)
	seenSequences := make(map[int64]bool)
	for _, event := range allEvents {
		if seenIDs[event.EventID] {
			continue
		}
		seenIDs[event.EventID] = true
		if event.Sequence > 0 {
			seenSequences[event.Sequence] = true
		}
	}
	for sequence := int64(1); sequence <= 102; sequence++ {
		if !seenSequences[sequence] {
			t.Fatalf("conversation sequence %d was skipped across pages", sequence)
		}
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
	if response.Header().Get("Access-Control-Allow-Origin") != "" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(response.Header().Get("Permissions-Policy"), "microphone=(self)") {
		t.Fatalf("browser headers = %#v", response.Header())
	}
}

func TestCompanionAcceptsMultipartImageMessage(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{}
	server := NewCompanionServer(st, backend, "https://galpon.example.test")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("prompt", "show this"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("images", "pixel.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(testPNG(t)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/messages", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", "https://galpon.example.test")
	request.Header.Set("Idempotency-Key", "image-key")
	response := httptest.NewRecorder()
	serveCompanion(server, response, request)
	if response.Code != http.StatusOK || backend.lastPrompt != "show this" || len(backend.lastImages) != 1 || backend.lastImages[0].Name != "pixel.png" {
		t.Fatalf("multipart response = %d %s; backend = %#v", response.Code, response.Body.String(), backend)
	}
	var message CompanionMessage
	if err := json.Unmarshal(response.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if len(message.Images) != 1 || message.Images[0].Data != "" || message.Images[0].URL != "/api/v1/images/stored-image" {
		t.Fatalf("public images = %#v", message.Images)
	}
}

func TestCompanionTranscribesAndSendsAudioMessage(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{}
	transcriber := &fakeAudioTranscriber{transcript: "Check the failing test"}
	server := NewCompanionServer(st, backend, "https://galpon.example.test")
	server.audioTranscriber = transcriber

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("audio", "message.webm")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("recorded audio"))
	image, err := form.CreateFormFile("images", "context.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = image.Write(testPNG(t))
	_ = form.WriteField("language", "es")
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/audio-messages", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Origin", "https://galpon.example.test")
	request.Header.Set("Idempotency-Key", "voice-key")
	response := httptest.NewRecorder()
	serveCompanion(server, response, request)

	if response.Code != http.StatusOK || backend.sent != 1 || backend.lastPrompt != transcriber.transcript || len(backend.lastImages) != 1 || backend.lastImages[0].Name != "context.png" {
		t.Fatalf("audio response = %d, sent = %d, prompt = %q, images = %#v: %s", response.Code, backend.sent, backend.lastPrompt, backend.lastImages, response.Body.String())
	}
	if transcriber.received != "recorded audio" || transcriber.language != "es" {
		t.Fatalf("transcriber received %q with language %q", transcriber.received, transcriber.language)
	}
	var result CompanionAudioResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Transcript != transcriber.transcript || result.Message.Prompt != transcriber.transcript || len(result.Message.Images) != 1 || result.Language != "es" {
		t.Fatalf("audio result = %#v", result)
	}
}

func TestCompanionRejectsUnsupportedAudioLanguage(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{}
	transcriber := &fakeAudioTranscriber{transcript: "unused"}
	server := NewCompanionServer(st, backend, "https://galpon.example.test")
	server.audioTranscriber = transcriber

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("audio", "message.webm")
	_, _ = part.Write([]byte("recorded audio"))
	_ = form.WriteField("language", "fr")
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/audio-messages", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Origin", "https://galpon.example.test")
	request.Header.Set("Idempotency-Key", "voice-key")
	response := httptest.NewRecorder()
	serveCompanion(server, response, request)

	if response.Code != http.StatusUnprocessableEntity || backend.sent != 0 || transcriber.received != "" {
		t.Fatalf("language response = %d, sent = %d, transcribed = %q: %s", response.Code, backend.sent, transcriber.received, response.Body.String())
	}
}

func TestCleanVoxtypeTranscriptRemovesAudioProgress(t *testing.T) {
	output := "Loading audio file: \"/tmp/recording.wav\"\nAudio format: 16000 Hz, 1 channel(s), Int\nProcessing 136320 samples (8.52s)...\n\nTesting the audio message.\nSecond line.\n"
	if got, want := cleanVoxtypeTranscript(output), "Testing the audio message.\nSecond line."; got != want {
		t.Fatalf("clean transcript = %q, want %q", got, want)
	}
	plain := "First paragraph.\n\nSecond paragraph."
	if got := cleanVoxtypeTranscript(plain); got != plain {
		t.Fatalf("plain transcript changed to %q", got)
	}
}

func TestCompanionRejectsAudioWhenNoSpeechIsDetected(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	backend := &fakeCompanionBackend{}
	server := NewCompanionServer(st, backend, "https://galpon.example.test")
	server.audioTranscriber = &fakeAudioTranscriber{err: errCompanionAudioEmpty}

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("audio", "message.webm")
	_, _ = part.Write([]byte("silence"))
	_ = form.Close()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/audio-messages", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Origin", "https://galpon.example.test")
	request.Header.Set("Idempotency-Key", "voice-key")
	response := httptest.NewRecorder()
	serveCompanion(server, response, request)

	if response.Code != http.StatusUnprocessableEntity || backend.sent != 0 || !strings.Contains(response.Body.String(), "no speech") {
		t.Fatalf("empty audio response = %d, sent = %d: %s", response.Code, backend.sent, response.Body.String())
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
