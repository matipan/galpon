package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type Server struct {
	app            *App
	http           *http.Server
	listener       net.Listener
	done           chan struct{}
	stop           sync.Once
	repositoryGate sync.RWMutex
	draining       atomic.Bool
}

func NewServer(app *App) *Server {
	s := &Server{app: app, done: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/dashboard", s.dashboard)
	mux.HandleFunc("POST /v1/repositories", s.repositories)
	mux.HandleFunc("DELETE /v1/repositories/{id}", s.deleteResource("repository"))
	mux.HandleFunc("POST /v1/repositories/{id}/remotes", s.repositoryRemotes)
	mux.HandleFunc("POST /v1/workspaces", s.workspaces)
	mux.HandleFunc("DELETE /v1/workspaces/{id}", s.deleteResource("workspace"))
	mux.HandleFunc("POST /v1/workspaces/{id}/archive", s.archiveWorkspace)
	mux.HandleFunc("POST /v1/workspaces/{id}/renderer", s.renderer)
	mux.HandleFunc("POST /v1/worktrees", s.worktrees)
	mux.HandleFunc("POST /v1/agents", s.agents)
	mux.HandleFunc("DELETE /v1/agents/{id}", s.deleteResource("agent"))
	mux.HandleFunc("GET /v1/agents/{id}", s.agent)
	mux.HandleFunc("DELETE /v1/worktrees/{id}", s.deleteResource("worktree"))
	mux.HandleFunc("POST /v1/cleanup", s.cleanup)
	mux.HandleFunc("POST /v1/agents/{id}/open", s.openAgent)
	mux.HandleFunc("POST /v1/agents/{id}/messages", s.messages)
	mux.HandleFunc("POST /v1/runtime/agents/{id}/register", s.registerRuntime)
	mux.HandleFunc("POST /v1/runtime/agents/{id}/status", s.runtimeStatus)
	mux.HandleFunc("POST /v1/runtime/agents/{id}/stop", s.stopRuntime)
	mux.HandleFunc("POST /v1/runtime/agents/{id}/claim", s.claimMessage)
	mux.HandleFunc("POST /v1/runtime/agents/{id}/messages/{messageID}/complete", s.completeMessage)
	mux.HandleFunc("POST /v1/runtime/tools/{name}", s.runtimeTool)
	mux.HandleFunc("POST /v1/shutdown", s.shutdown)
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *Server) Serve(socket string) error {
	if err := os.MkdirAll(filepathDir(socket), 0o700); err != nil {
		return err
	}
	if existing, err := net.DialTimeout("unix", socket, 150*time.Millisecond); err == nil {
		existing.Close()
		return fmt.Errorf("Galpon is already running")
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		return err
	}
	s.listener = listener
	err = s.http.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Wait() { <-s.done }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	value, err := s.app.Store.Dashboard(r.Context())
	respond(w, value, err)
}

func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	if !s.beginRepositoryOperation(w) {
		return
	}
	defer s.repositoryGate.RUnlock()
	var in AddRepositoryRequest
	if !decode(w, r, &in) {
		return
	}
	value, reused, err := s.app.AddRepository(r.Context(), in)
	respond(w, map[string]any{"repository": value, "reused": reused}, err)
}
func (s *Server) repositoryRemotes(w http.ResponseWriter, r *http.Request) {
	if !s.beginRepositoryOperation(w) {
		return
	}
	defer s.repositoryGate.RUnlock()
	var in struct {
		Name        string `json:"name"`
		FetchURL    string `json:"fetchUrl"`
		PushURL     string `json:"pushUrl"`
		PushDefault bool   `json:"pushDefault"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.AddRepositoryRemote(r.Context(), r.PathValue("id"), model.RepositoryRemote{Name: in.Name, FetchURL: in.FetchURL, PushURL: in.PushURL}, in.PushDefault)
	respond(w, value, err)
}
func (s *Server) deleteResource(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.beginExclusiveOperation(w) {
			return
		}
		defer s.repositoryGate.Unlock()
		value, err := s.app.DeleteResource(r.Context(), kind, r.PathValue("id"))
		respond(w, value, err)
	}
}
func (s *Server) cleanup(w http.ResponseWriter, r *http.Request) {
	if !s.beginExclusiveOperation(w) {
		return
	}
	defer s.repositoryGate.Unlock()
	value, err := s.app.Cleanup(r.Context())
	respond(w, value, err)
}
func (s *Server) workspaces(w http.ResponseWriter, r *http.Request) {
	var in CreateWorkspaceRequest
	if !decode(w, r, &in) {
		return
	}
	ws, err := s.app.CreateWorkspace(r.Context(), in)
	respond(w, ws, err)
}
func (s *Server) archiveWorkspace(w http.ResponseWriter, r *http.Request) {
	err := s.app.Store.ArchiveWorkspace(r.Context(), r.PathValue("id"))
	respond(w, map[string]any{"archived": err == nil}, err)
}
func (s *Server) renderer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Renderer string `json:"renderer"`
		Context  string `json:"context"`
		ID       string `json:"id"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.app.Store.SetRenderer(r.Context(), r.PathValue("id"), in.Renderer, in.Context, in.ID)
	respond(w, map[string]any{"saved": err == nil}, err)
}
func (s *Server) worktrees(w http.ResponseWriter, r *http.Request) {
	if !s.beginRepositoryOperation(w) {
		return
	}
	defer s.repositoryGate.RUnlock()
	var in CreateWorktreeRequest
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.CreateWorktree(r.Context(), in)
	respond(w, value, err)
}
func (s *Server) agents(w http.ResponseWriter, r *http.Request) {
	var in CreateAgentRequest
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.CreateAgent(r.Context(), in)
	respond(w, value, err)
}
func (s *Server) agent(w http.ResponseWriter, r *http.Request) {
	value, err := s.app.Store.AgentView(r.Context(), r.PathValue("id"))
	respond(w, value, err)
}
func (s *Server) openAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Focus bool `json:"focus"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.OpenAgent(r.Context(), r.PathValue("id"), in.Focus)
	respond(w, value, err)
}
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.QueueAgentMessage(r.Context(), "", r.PathValue("id"), in.Text)
	respond(w, value, err)
}
func (s *Server) registerRuntime(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID   string `json:"runtimeId"`
		SessionID   string `json:"sessionId"`
		SessionPath string `json:"sessionPath"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.app.RegisterRuntime(r.Context(), r.PathValue("id"), in.RuntimeID, in.SessionID, in.SessionPath)
	respond(w, map[string]any{"registered": err == nil}, err)
}
func (s *Server) runtimeStatus(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Status    string `json:"status"`
		Error     string `json:"error"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.app.SetRuntimeStatus(r.Context(), r.PathValue("id"), in.RuntimeID, in.Status, in.Error)
	respond(w, map[string]any{"saved": err == nil}, err)
}
func (s *Server) stopRuntime(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Error     string `json:"error"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.app.StopRuntime(r.Context(), r.PathValue("id"), in.RuntimeID, in.Error)
	respond(w, map[string]any{"stopped": err == nil}, err)
}
func (s *Server) claimMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.ClaimMessage(r.Context(), r.PathValue("id"), in.RuntimeID)
	respond(w, map[string]any{"message": value}, err)
}
func (s *Server) completeMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RuntimeID string `json:"runtimeId"`
		Response  string `json:"response"`
		Error     string `json:"error"`
	}
	if !decode(w, r, &in) {
		return
	}
	err := s.app.CompleteMessage(r.Context(), r.PathValue("id"), r.PathValue("messageID"), in.RuntimeID, in.Response, in.Error)
	respond(w, map[string]any{"completed": err == nil}, err)
}
func (s *Server) runtimeTool(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("name") == "cleanup_created_agents" {
		if !s.beginExclusiveOperation(w) {
			return
		}
		defer s.repositoryGate.Unlock()
	}
	var in struct {
		AgentID string         `json:"agentId"`
		Args    map[string]any `json:"args"`
	}
	if !decode(w, r, &in) {
		return
	}
	value, err := s.app.handleAgentTool(r.Context(), in.AgentID, r.PathValue("name"), in.Args)
	respond(w, value, err)
}
func (s *Server) shutdown(w http.ResponseWriter, _ *http.Request) {
	s.stop.Do(func() {
		s.draining.Store(true)
		s.repositoryGate.Lock()
		defer s.repositoryGate.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"stopping": true})
		go func() {
			time.Sleep(50 * time.Millisecond)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := s.http.Shutdown(shutdownCtx); err != nil {
				_ = s.http.Close()
			}
			close(s.done)
		}()
	})
}

func (s *Server) beginRepositoryOperation(w http.ResponseWriter) bool {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Galpon is stopping; retry after it starts"})
		return false
	}
	s.repositoryGate.RLock()
	if s.draining.Load() {
		s.repositoryGate.RUnlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Galpon is stopping; retry after it starts"})
		return false
	}
	return true
}

func (s *Server) beginExclusiveOperation(w http.ResponseWriter) bool {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Galpon is stopping; retry after it starts"})
		return false
	}
	s.repositoryGate.Lock()
	if s.draining.Load() {
		s.repositoryGate.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Galpon is stopping; retry after it starts"})
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		status := http.StatusInternalServerError
		if IsNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSONStatus(w, status, map[string]any{"error": err.Error()})
}
func writeJSON(w http.ResponseWriter, status int, value any) { writeJSONStatus(w, status, value) }
func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func filepathDir(path string) string {
	index := strings.LastIndex(path, string(os.PathSeparator))
	if index <= 0 {
		return "."
	}
	return path[:index]
}
