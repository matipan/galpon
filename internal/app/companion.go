package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	companionweb "github.com/matipan/galpon/internal/companion/web"
	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

type CompanionBackend interface {
	Dashboard(context.Context) (model.Dashboard, error)
	Agent(context.Context, string) (model.AgentView, error)
	SendCompanion(context.Context, string, string, string) (model.AgentMessage, error)
	CreateAgentFromSource(context.Context, CreateAgentFromSourceRequest, string) (CreateAgentFromSourceResult, error)
}

type CompanionWorkspace struct {
	ID        string           `json:"id"`
	Title     string           `json:"title"`
	Status    string           `json:"status"`
	CreatedAt int64            `json:"createdAt"`
	UpdatedAt int64            `json:"updatedAt"`
	Agents    []CompanionAgent `json:"agents"`
}

type CompanionAgent struct {
	ID               string `json:"id"`
	WorkspaceID      string `json:"workspaceId"`
	Title            string `json:"title"`
	Role             string `json:"role,omitempty"`
	Status           string `json:"status"`
	CanCopyPlacement bool   `json:"canCopyPlacement"`
	WorkspaceTitle   string `json:"workspaceTitle,omitempty"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
}

type CompanionMessage struct {
	ID            string `json:"id"`
	SenderAgentID string `json:"senderAgentId,omitempty"`
	TargetAgentID string `json:"targetAgentId"`
	Prompt        string `json:"prompt"`
	Status        string `json:"status"`
	Response      string `json:"response,omitempty"`
	Failed        bool   `json:"failed,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	UpdatedAt     int64  `json:"updatedAt"`
}

type CompanionBootstrap struct {
	Cursor     int64                `json:"cursor"`
	Workspaces []CompanionWorkspace `json:"workspaces"`
}

type CompanionAgentDetail struct {
	Cursor   int64                     `json:"cursor"`
	Agent    CompanionAgent            `json:"agent"`
	Timeline []model.ConversationEvent `json:"timeline"`
}

type CompanionCreateResult struct {
	Agent          CompanionAgent   `json:"agent"`
	InitialMessage CompanionMessage `json:"initialMessage"`
	StartPending   bool             `json:"startPending"`
}

type CompanionServer struct {
	store   *store.Store
	backend CompanionBackend
	origin  string
	http    *http.Server
	Logger  *log.Logger
}

func NewCompanionServer(st *store.Store, backend CompanionBackend, allowedOrigin string) *CompanionServer {
	s := &CompanionServer{store: st, backend: backend, origin: strings.TrimSpace(allowedOrigin)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/bootstrap", s.bootstrap)
	mux.HandleFunc("GET /api/v1/agents/{id}", s.agent)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("POST /api/v1/agents/{id}/messages", s.sendMessage)
	mux.HandleFunc("POST /api/v1/agents", s.createAgent)
	mux.Handle("/", http.FileServer(http.FS(companionweb.Assets)))
	s.http = &http.Server{Handler: companionHeaders(mux), ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *CompanionServer) Serve(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid companion listen address: %w", err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("companion listener must use 127.0.0.1")
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return err
	}
	err = s.http.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *CompanionServer) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func companionHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *CompanionServer) bootstrap(w http.ResponseWriter, r *http.Request) {
	dashboard, err := s.backend.Dashboard(r.Context())
	if err != nil {
		s.internalError(w, http.StatusBadGateway, "could not read Galpon state", err)
		return
	}
	sequence, err := s.store.CompanionSequence(r.Context())
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not read companion sequence", err)
		return
	}
	out := CompanionBootstrap{Cursor: sequence, Workspaces: []CompanionWorkspace{}}
	workspaceIndex := make(map[string]int, len(dashboard.Workspaces))
	for _, workspace := range dashboard.Workspaces {
		workspaceIndex[workspace.ID] = len(out.Workspaces)
		out.Workspaces = append(out.Workspaces, safeWorkspace(workspace))
	}
	for _, agent := range dashboard.Agents {
		if index, ok := workspaceIndex[agent.WorkspaceID]; ok {
			out.Workspaces[index].Agents = append(out.Workspaces[index].Agents, safeAgent(agent))
		}
	}
	companionJSON(w, http.StatusOK, out)
}

func (s *CompanionServer) agent(w http.ResponseWriter, r *http.Request) {
	view, err := s.backend.Agent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	events, err := s.store.ConversationEvents(r.Context(), view.Agent.ID)
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not read the conversation", err)
		return
	}
	sequence, err := s.store.CompanionSequence(r.Context())
	if err != nil {
		s.internalError(w, http.StatusInternalServerError, "could not read companion sequence", err)
		return
	}
	timeline := append([]model.ConversationEvent(nil), events...)
	lastTimelineSequence := int64(0)
	for _, event := range events {
		if event.Sequence > lastTimelineSequence {
			lastTimelineSequence = event.Sequence
		}
	}
	for _, message := range view.Messages {
		if message.Status != "queued" && message.Status != "delivered" {
			continue
		}
		represented := false
		for _, event := range events {
			if strings.Contains(event.Content, message.ID) {
				represented = true
				break
			}
		}
		if !represented {
			lastTimelineSequence++
			timeline = append(timeline, model.ConversationEvent{Sequence: lastTimelineSequence, AgentID: view.Agent.ID, EventID: "delivery:" + message.ID, Kind: "delivery_" + message.Status, Role: "user", Content: message.Prompt, CreatedAt: message.CreatedAt})
		}
	}
	agent := safeAgent(view.Agent)
	dashboard, dashboardErr := s.backend.Dashboard(r.Context())
	if dashboardErr == nil {
		if workspace, ok := dashboard.Workspace(view.Agent.WorkspaceID); ok {
			agent.WorkspaceTitle = workspace.Title
		}
	} else {
		s.logError("read workspace title", dashboardErr)
	}
	companionJSON(w, http.StatusOK, CompanionAgentDetail{Cursor: sequence, Agent: agent, Timeline: timeline})
}

func (s *CompanionServer) sendMessage(w http.ResponseWriter, r *http.Request) {
	key, ok := s.checkMutation(w, r)
	if !ok {
		return
	}
	var in struct {
		Prompt string `json:"prompt"`
	}
	if !decodeCompanion(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Prompt) == "" {
		companionError(w, http.StatusUnprocessableEntity, "prompt is required")
		return
	}
	message, err := s.backend.SendCompanion(r.Context(), r.PathValue("id"), in.Prompt, key)
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	companionJSON(w, http.StatusOK, safeMessage(message))
}

func (s *CompanionServer) createAgent(w http.ResponseWriter, r *http.Request) {
	key, ok := s.checkMutation(w, r)
	if !ok {
		return
	}
	var in CreateAgentFromSourceRequest
	if !decodeCompanion(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.SourceAgentID) == "" || strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Prompt) == "" {
		companionError(w, http.StatusUnprocessableEntity, "sourceAgentId, title, and prompt are required")
		return
	}
	result, err := s.backend.CreateAgentFromSource(r.Context(), in, key)
	if err != nil {
		s.companionBackendError(w, err)
		return
	}
	companionJSON(w, http.StatusOK, CompanionCreateResult{Agent: safeAgent(result.Agent), InitialMessage: safeMessage(result.InitialMessage), StartPending: result.StartPending})
}

func (s *CompanionServer) checkMutation(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.origin == "" || strings.TrimSpace(r.Header.Get("Origin")) != s.origin {
		companionError(w, http.StatusForbidden, "request origin is not allowed")
		return "", false
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		companionError(w, http.StatusPreconditionRequired, "a valid Idempotency-Key is required")
		return "", false
	}
	return key, true
}

func (s *CompanionServer) events(w http.ResponseWriter, r *http.Request) {
	afterText := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if afterText == "" {
		afterText = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	after := int64(0)
	var err error
	if afterText != "" {
		after, err = strconv.ParseInt(afterText, 10, 64)
		if err != nil || after < 0 {
			companionError(w, http.StatusBadRequest, "after must be a non-negative integer")
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		companionError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	poll := time.NewTicker(250 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		events, err := s.store.CompanionEventsAfter(r.Context(), after, 100)
		if err != nil {
			s.logError("stream companion events", err)
			return
		}
		for _, event := range events {
			data, _ := json.Marshal(event)
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
				return
			}
			after = event.Sequence
		}
		if len(events) > 0 {
			flusher.Flush()
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func decodeCompanion(w http.ResponseWriter, r *http.Request, value any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		companionError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		companionError(w, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		companionError(w, http.StatusBadRequest, "request body must contain one JSON value")
		return false
	}
	return true
}

func safeWorkspace(value model.Workspace) CompanionWorkspace {
	return CompanionWorkspace{ID: value.ID, Title: value.Title, Status: value.Status, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, Agents: []CompanionAgent{}}
}

func safeAgent(value model.Agent) CompanionAgent {
	return CompanionAgent{ID: value.ID, WorkspaceID: value.WorkspaceID, Title: value.Title, Role: value.Role, Status: value.Status, CanCopyPlacement: value.Placement.Type == "worktrees" && len(value.Placement.Worktrees) > 0, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func safeMessage(value model.AgentMessage) CompanionMessage {
	return CompanionMessage{ID: value.ID, SenderAgentID: value.SenderAgentID, TargetAgentID: value.TargetAgentID, Prompt: value.Prompt, Status: value.Status, Response: value.Response, Failed: value.Status == "failed", CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt}
}

func (s *CompanionServer) companionBackendError(w http.ResponseWriter, err error) {
	s.logError("companion backend request", err)
	var apiError *APIError
	if errors.As(err, &apiError) {
		if apiError.StatusCode == http.StatusNotFound {
			companionError(w, http.StatusNotFound, "resource not found")
			return
		}
		if strings.Contains(apiError.Message, "Idempotency-Key") || strings.Contains(apiError.Message, "manual review") {
			companionError(w, http.StatusConflict, apiError.Message)
			return
		}
		switch apiError.Message {
		case "agent title and prompt are required", "prompt is required":
			companionError(w, http.StatusUnprocessableEntity, apiError.Message)
			return
		case "source agent does not have a managed worktree placement":
			companionError(w, http.StatusUnprocessableEntity, "source agent cannot copy a managed placement")
			return
		}
	}
	companionError(w, http.StatusBadGateway, "the Galpon daemon could not complete the request")
}

func (s *CompanionServer) internalError(w http.ResponseWriter, status int, message string, err error) {
	s.logError(message, err)
	companionError(w, status, message)
}

func (s *CompanionServer) logError(operation string, err error) {
	if s.Logger != nil {
		s.Logger.Printf("%s: %v", operation, err)
	}
}

func companionError(w http.ResponseWriter, status int, message string) {
	companionJSON(w, status, map[string]string{"error": message})
}

func companionJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
