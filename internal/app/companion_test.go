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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

type fakeCompanionBackend struct {
	dashboard    model.Dashboard
	dashboardErr error
	view         model.AgentView
	sent         int
	created      int
}

func (f *fakeCompanionBackend) Dashboard(context.Context) (model.Dashboard, error) {
	return f.dashboard, f.dashboardErr
}
func (f *fakeCompanionBackend) Agent(context.Context, string) (model.AgentView, error) {
	return f.view, nil
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
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil))
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
		{path: "/styles.css", contentType: "text/css", contains: "Tokyo Night"},
	} {
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), test.contentType) || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("GET %s = %d, %q, %q", test.path, response.Code, response.Header().Get("Content-Type"), response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/not-a-client-route", nil))
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
		Workspaces: []model.Workspace{{ID: "ws", Title: "Work", Status: "active", CreatedAt: 1, UpdatedAt: 2}},
		Agents: []model.Agent{
			{ID: "agent", WorkspaceID: "ws", Title: "Worker", Role: "reviewer", Status: "running", SessionPath: "/secret/session.jsonl", RuntimeID: "secret-runtime", Placement: model.AgentPlacement{Type: "worktrees", CWD: "/secret", Worktrees: []model.AgentWorktree{{WorktreeID: "wt"}}}, CreatedAt: 1, UpdatedAt: 2},
			{ID: "cwd", WorkspaceID: "ws", Title: "Unmanaged", Status: "idle", Placement: model.AgentPlacement{Type: "none", CWD: "/private/path"}},
		},
	}}
	backend.view = model.AgentView{Agent: backend.dashboard.Agents[0], Messages: []model.AgentMessage{{ID: "delivery", TargetAgentID: "agent", Prompt: "do work", Status: "queued", RuntimeID: "private", CreatedAt: 5, UpdatedAt: 5}}}
	server := NewCompanionServer(st, backend, "http://127.0.0.1:8420")

	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap", nil))
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
	if len(bootstrap.Workspaces) != 1 || len(bootstrap.Workspaces[0].Agents) != 2 || !bootstrap.Workspaces[0].Agents[0].CanCopyPlacement || bootstrap.Workspaces[0].Agents[1].CanCopyPlacement {
		t.Fatalf("bootstrap = %#v", bootstrap)
	}

	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
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
	server.http.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent", nil))
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Timeline) != 2 || detail.Timeline[0].Kind != "delivery_completed" || detail.Timeline[1].Content != "finished before mirroring was enabled" {
		t.Fatalf("completed fallback timeline = %#v", detail.Timeline)
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
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || backend.sent != 0 {
		t.Fatalf("wrong-origin response = %d, sent = %d", response.Code, backend.sent)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/messages", bytes.NewBufferString(`{"prompt":"continue"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://galpon.example.test")
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing-key status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/agents/agent/messages", bytes.NewBufferString(`{"prompt":"continue"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://galpon.example.test")
	request.Header.Set("Idempotency-Key", "key")
	response = httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || backend.sent != 1 {
		t.Fatalf("valid response = %d, sent = %d: %s", response.Code, backend.sent, response.Body.String())
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "" || response.Header().Get("Content-Security-Policy") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("browser headers = %#v", response.Header())
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
