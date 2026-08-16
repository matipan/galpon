package app

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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
	request := CreateAgentFromSourceRequest{SourceAgentID: source.ID, Title: "Phone agent", Role: "implementer", Prompt: "Do the next task"}
	first, err := application.CreateAgentFromSource(ctx, "create-key", request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.CreateAgentFromSource(ctx, "create-key", request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.ID != second.Agent.ID || first.Agent.ContextAgentID != "" || first.Agent.Placement.PrimaryWorktreeID == source.Placement.PrimaryWorktreeID || first.Agent.Placement.Worktrees[0].Mode != "private" || first.InitialMessage.Prompt != request.Prompt || first.StartPending {
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

	body = []byte(`{"runtimeId":"other","events":[{"eventId":"bad","runtimeSeq":8,"kind":"agent_start","createdAt":` + intString(now) + `}]}`)
	request = httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/conversation-events", bytes.NewReader(body))
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code < 400 {
		t.Fatal("ingestion accepted an unregistered runtime")
	}
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
