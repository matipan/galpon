package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/config"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

func TestCreateAgentFromActiveSourceCopiesPrivatePlacementAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test", context: "test"}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	repository, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "source")})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Phone work"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := application.CreateAgent(ctx, CreateAgentRequest{Title: "Source", WorkspaceID: workspace.ID, Placement: AgentPlacementRequest{Type: "worktrees", Worktrees: []AgentPlacementWorktreeRequest{{RepositoryID: repository.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Store.RegisterAgentRuntime(ctx, source.ID, "active-runtime", source.SessionID, filepath.Join(root, "source.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := application.Store.SetAgentRuntimeStatus(ctx, source.ID, "active-runtime", "running", ""); err != nil {
		t.Fatal(err)
	}
	targetWorkspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Phone target"})
	if err != nil {
		t.Fatal(err)
	}
	request := CreateAgentFromSourceRequest{SourceAgentID: source.ID, WorkspaceID: targetWorkspace.ID, Title: "Phone agent", Role: "implementer", Prompt: "Do the next task"}
	first, err := application.CreateAgentFromSource(ctx, "create-key", request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.CreateAgentFromSource(ctx, "create-key", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.ID != second.Agent.ID || first.Agent.WorkspaceID != targetWorkspace.ID || first.Agent.ContextAgentID != "" || first.Agent.Placement.PrimaryWorktreeID == source.Placement.PrimaryWorktreeID || first.Agent.Placement.Worktrees[0].Mode != "private" || first.InitialMessage.Prompt != request.Prompt || first.StartPending {
		t.Fatalf("created result = %#v", first)
	}
	dashboard, err := application.Store.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Agents) != 2 || len(renderer.opened) != 1 || renderer.opened[0] != first.Agent.ID {
		t.Fatalf("dashboard agents = %d, opened = %v", len(dashboard.Agents), renderer.opened)
	}
}

func TestCreateCompanionAgentFromWorkspaceRepositories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	renderer := &cleanupRenderer{name: "test", context: "test"}
	cfg := config.Config{StateDir: filepath.Join(root, "state"), Socket: filepath.Join(root, "state", "galpon.sock"), PiBin: "pi", PiProvider: "test", HerdrBin: "herdr"}
	application, err := Open(ctx, cfg, log.New(io.Discard, "", 0), renderer)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestApp(t, application)
	primary, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "primary")})
	if err != nil {
		t.Fatal(err)
	}
	secondary, _, err := application.AddRepository(ctx, AddRepositoryRequest{Path: createAppRepository(t, root, "secondary")})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := application.CreateWorkspace(ctx, CreateWorkspaceRequest{Title: "Phone work"})
	if err != nil {
		t.Fatal(err)
	}
	request := CreateAgentFromSourceRequest{
		WorkspaceID: workspace.ID, RepositoryIDs: []string{primary.ID, secondary.ID},
		Title: "Phone agent", Role: "implementer", Prompt: "Start the task",
	}
	result, err := application.CreateAgentFromSource(ctx, "repository-create-key", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Agent.Placement.Worktrees) != 2 || result.Agent.Placement.Worktrees[0].Mode != "private" || result.InitialMessage.Prompt != request.Prompt || result.StartPending {
		t.Fatalf("repository launch = %#v", result)
	}
	if len(renderer.opened) != 1 || renderer.opened[0] != result.Agent.ID {
		t.Fatalf("opened agents = %v", renderer.opened)
	}
}

func TestRuntimeConversationIngestionRoute(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	now := time.Now().UnixMilli()
	body := []byte(`{"runtimeId":"runtime","events":[{"eventId":"tool-end","runtimeSeq":7,"kind":"tool_execution_end","piEntryId":"entry","role":"tool","content":"failed safely","toolName":"bash","toolCallId":"call","isError":true,"createdAt":` + intString(now) + `}]}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/conversation-events", bytes.NewReader(body))
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("ingestion status = %d: %s", response.Code, response.Body.String())
	}
	events, err := application.Store.ConversationEvents(context.Background(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "tool_execution_end" || !events[0].IsError || events[0].ToolCallID != "call" {
		t.Fatalf("events = %#v", events)
	}
	inserted, err := application.IngestConversationEvents(context.Background(), "agent", ConversationEventsRequest{
		RuntimeID: "runtime",
		Events: []model.ConversationEvent{{
			EventID: "private-reasoning", RuntimeSeq: 8, Kind: "assistant_reasoning_end", Role: "assistant", Content: "not public", CreatedAt: now,
		}},
	})
	if err != nil || inserted != 0 {
		t.Fatalf("private reasoning ingestion = %d, %v", inserted, err)
	}
	events, err = application.Store.ConversationEvents(context.Background(), "agent")
	if err != nil || len(events) != 1 {
		t.Fatalf("private reasoning was stored: %#v, %v", events, err)
	}
	_, err = application.IngestConversationEvents(context.Background(), "agent", ConversationEventsRequest{
		RuntimeID: "runtime",
		Events:    []model.ConversationEvent{{EventID: "too-large", Kind: "lifecycle", Content: strings.Repeat("x", (64<<10)+1), CreatedAt: now}},
	})
	if err == nil {
		t.Fatal("ingestion accepted conversation content larger than 64 KiB")
	}

	body = []byte(`{"runtimeId":"runtime","events":[{"eventId":"bad-image","runtimeSeq":9,"kind":"user_message","role":"user","images":[{"mimeType":"image/png","data":"not-base64"}],"createdAt":` + intString(now) + `}]}`)
	request = httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/conversation-events", bytes.NewReader(body))
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid image status = %d: %s", response.Code, response.Body.String())
	}

	body = []byte(`{"runtimeId":"other","events":[{"eventId":"bad","runtimeSeq":8,"kind":"agent_start","createdAt":` + intString(now) + `}]}`)
	request = httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/conversation-events", bytes.NewReader(body))
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code < 400 {
		t.Fatal("ingestion accepted an unregistered runtime")
	}
}

func TestCompanionImageMessagePersistsAndClaimsExactData(t *testing.T) {
	application := companionTestApp(t, "runtime")
	data := testPNG(t)
	input := model.ImageAttachment{Name: "pixel.png", Data: base64.StdEncoding.EncodeToString(data)}
	message, err := application.QueueCompanionMessageImages(t.Context(), "image-key", "agent", "look", []model.ImageAttachment{input})
	if err != nil {
		t.Fatal(err)
	}
	if message.Images == nil || len(*message.Images) != 1 || (*message.Images)[0].MimeType != "image/png" || (*message.Images)[0].Data != "" {
		t.Fatalf("queued image receipt = %#v", message.Images)
	}
	retry, err := application.QueueCompanionMessageImages(t.Context(), "image-key", "agent", "look", []model.ImageAttachment{input})
	if err != nil || retry.ID != message.ID {
		t.Fatalf("image retry = %#v, %v", retry, err)
	}
	mutation, err := application.Store.CompanionMutation(t.Context(), "image-key")
	if err != nil || bytes.Contains(mutation.ResponseJSON, []byte(input.Data)) {
		t.Fatalf("image data was copied into the mutation receipt: %v", err)
	}
	public, _, _, _, _, err := application.Store.CompanionAgentMessages(t.Context(), "agent", []string{message.ID}, 0, "", 0)
	if err != nil || len(public) != 1 || public[0].Images == nil || (*public[0].Images)[0].Data != "" || (*public[0].Images)[0].URL == "" {
		t.Fatalf("public message = %#v, %v", public, err)
	}
	claimed, err := application.Store.ClaimAgentMessage(t.Context(), "agent", "runtime", "image-claim")
	if err != nil || claimed == nil || claimed.Images == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	decoded, err := base64.StdEncoding.DecodeString((*claimed.Images)[0].Data)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatalf("claimed image changed: %v", err)
	}
	metadata, stored, err := application.Store.PublicImage(t.Context(), (*claimed.Images)[0].ID)
	if err != nil || metadata.MimeType != "image/png" || !bytes.Equal(stored, data) {
		t.Fatalf("public image = %#v, %v", metadata, err)
	}
	checkpointState, err := application.Store.DurableState(t.Context())
	if err != nil || len(checkpointState.Messages) != 1 || checkpointState.Messages[0].Images == nil || (*checkpointState.Messages[0].Images)[0].Data == "" {
		t.Fatalf("checkpoint images = %#v, %v", checkpointState.Messages, err)
	}
	companion := NewCompanionServer(application.Store, &fakeCompanionBackend{}, "http://127.0.0.1:8420")
	response := httptest.NewRecorder()
	serveCompanion(companion, response, httptest.NewRequest(http.MethodGet, metadata.URL, nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || response.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(response.Body.Bytes(), data) {
		t.Fatalf("image route = %d, %v, %q", response.Code, response.Header(), response.Body.Bytes())
	}
	if _, err := application.Store.SoftDelete(t.Context(), "agent", "agent"); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	serveCompanion(companion, response, httptest.NewRequest(http.MethodGet, metadata.URL, nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("deleted owner image status = %d", response.Code)
	}
}

func TestRuntimeConversationImagesAreSeparatePublicBlobs(t *testing.T) {
	application := companionTestApp(t, "runtime")
	data := testPNG(t)
	event := model.ConversationEvent{
		EventID: "image-event", RuntimeSeq: 1, Kind: "user_message", Role: "user", Content: "screen", CreatedAt: time.Now().UnixMilli(),
		Images: []model.ImageAttachment{{Name: "screen.png", Data: base64.StdEncoding.EncodeToString(data)}},
	}
	inserted, err := application.IngestConversationEvents(t.Context(), "agent", ConversationEventsRequest{RuntimeID: "runtime", Events: []model.ConversationEvent{event}})
	if err != nil || inserted != 1 {
		t.Fatalf("ingest = %d, %v", inserted, err)
	}
	events, err := application.Store.ConversationEvents(t.Context(), "agent")
	if err != nil || len(events) != 1 || len(events[0].Images) != 1 || events[0].Images[0].Data != "" || events[0].Images[0].URL == "" {
		t.Fatalf("public events = %#v, %v", events, err)
	}
	_, stored, err := application.Store.PublicImage(t.Context(), events[0].Images[0].ID)
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored event image changed: %v", err)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	if err := png.Encode(&out, pixel); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestCompanionMessageIdempotencyIsAdmittedByDaemon(t *testing.T) {
	application := companionTestApp(t, "")
	ctx := context.Background()
	first, err := application.QueueCompanionMessage(ctx, "mobile-key", "agent", "continue")
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.QueueCompanionMessage(ctx, "mobile-key", "agent", "continue")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry IDs differ: %s and %s", first.ID, second.ID)
	}
	messages, err := application.Store.AgentMessages(ctx, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	if _, err := application.QueueCompanionMessage(ctx, "mobile-key", "agent", "different"); err == nil {
		t.Fatal("key reuse with a different request was accepted")
	}

	if _, _, err := application.Store.ReserveCompanionMutation(ctx, "crashed-key", "send_message", "unrecoverable-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.QueueCompanionMessage(ctx, "crashed-key", "agent", "new prompt"); err == nil {
		t.Fatal("a pending command was run a second time")
	}
	messages, err = application.Store.AgentMessages(ctx, "agent")
	if err != nil || len(messages) != 1 {
		t.Fatalf("pending retry changed messages: %#v, %v", messages, err)
	}

	if _, err := application.Store.SoftDelete(ctx, "agent", "agent"); err != nil {
		t.Fatal(err)
	}
	replayed, err := application.QueueCompanionMessage(ctx, "mobile-key", "agent", "continue")
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("completed receipt after target removal = %#v, %v", replayed, err)
	}
}

func companionTestApp(t *testing.T, runtimeID string) *App {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Now().UnixMilli()
	if err := st.PutRepository(ctx, model.Repository{ID: "repo", Title: "Repo", SourcePath: "/source", FetchURL: "/source", MirrorPath: "/mirror", DefaultBranch: "main", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutWorkspace(ctx, model.Workspace{ID: "ws", Title: "Work", Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	placement := model.AgentPlacement{Type: "worktrees", PrimaryWorktreeID: "wt", Worktrees: []model.AgentWorktree{{WorktreeID: "wt", Position: 0, Mode: "private"}}}
	agent := model.Agent{ID: "agent", WorkspaceID: "ws", Title: "Worker", Placement: placement, Kind: "pi", Status: "idle", SessionID: "agent", RuntimeID: runtimeID, CreatedAt: now, UpdatedAt: now}
	worktree := model.Worktree{ID: "wt", WorkspaceID: "ws", RepositoryID: "repo", Path: filepath.Join(root, "wt"), Branch: "branch", BaseRef: "main", CreatedAt: now}
	if err := st.PutAgent(ctx, agent, []model.Worktree{worktree}); err != nil {
		t.Fatal(err)
	}
	return &App{Store: st}
}

func intString(value int64) string {
	return strconv.FormatInt(value, 10)
}
