package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type Server struct {
	Store      *Store
	Reconciler *Reconciler
	HTTP       *http.Server
	listener   net.Listener
}

func NewServer(store *Store, reconciler *Reconciler) *Server {
	s := &Server{Store: store, Reconciler: reconciler}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]any{"ok": true, "apiVersion": APIVersion})
	})
	mux.HandleFunc("GET /v1/work-orders", s.list)
	mux.HandleFunc("POST /v1/work-orders", s.create)
	mux.HandleFunc("GET /v1/work-orders/{id}", s.get)
	mux.HandleFunc("DELETE /v1/work-orders/{id}", s.deleteWorkOrder)
	mux.HandleFunc("POST /v1/work-orders/{id}/actions", s.action)
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, map[string]bool{"stopping": true})
		go func() { _ = s.Shutdown(context.Background()) }()
	})
	s.HTTP = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}
func (s *Server) Serve(socket string) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	if c, err := net.DialTimeout("unix", socket, 100*time.Millisecond); err == nil {
		_ = c.Close()
		return fmt.Errorf("factory is already running")
	}
	_ = os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	s.listener = l
	if err := os.Chmod(socket, 0o600); err != nil {
		return err
	}
	err = s.HTTP.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *Server) Shutdown(ctx context.Context) error { return s.HTTP.Shutdown(ctx) }
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	orders, err := s.Store.List(r.Context())
	if err != nil {
		problem(w, err)
		return
	}
	runs, err := s.Store.RunningRuns(r.Context())
	if err != nil {
		problem(w, err)
		return
	}
	write(w, 200, Snapshot{WorkOrders: orders, Runs: runs})
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	order, err := s.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, err)
		return
	}
	events, err := s.Store.Events(r.Context(), order.ID)
	if err != nil {
		problem(w, err)
		return
	}
	runs, err := s.Store.Runs(r.Context(), order.ID)
	if err != nil {
		problem(w, err)
		return
	}
	write(w, 200, Snapshot{WorkOrders: []WorkOrder{order}, Events: events, Runs: runs})
}
func (s *Server) deleteWorkOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var err error
	if s.Reconciler != nil {
		err = s.Reconciler.Delete(r.Context(), id)
	} else {
		err = s.Store.Delete(r.Context(), id)
	}
	if err != nil {
		problem(w, err)
		return
	}
	write(w, http.StatusOK, map[string]bool{"deleted": true})
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var in CreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		problem(w, err)
		return
	}
	if in.Title == "" && in.IssueURL == "" {
		problem(w, fmt.Errorf("title or issue URL is required"))
		return
	}
	if in.RepositoryID == "" || in.WorkspaceID == "" {
		problem(w, fmt.Errorf("repository and workspace are required"))
		return
	}
	order, err := s.Store.Create(r.Context(), in)
	if err != nil {
		problem(w, err)
		return
	}
	write(w, http.StatusCreated, order)
	s.trigger()
}
func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	order, err := s.Store.Get(ctx, r.PathValue("id"))
	if err != nil {
		problem(w, err)
		return
	}
	var in ActionRequest
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&in); err != nil {
		problem(w, err)
		return
	}
	switch in.Action {
	case "approve_plan":
		if order.Stage != StagePlanApproval {
			err = fmt.Errorf("plan is not waiting for approval")
		} else {
			err = s.Store.SetStage(ctx, order.ID, StageImplementation, "active")
		}
	case "review_plan":
		if order.Stage != StagePlanApproval {
			err = fmt.Errorf("plan is not waiting for review annotations")
		} else if strings.TrimSpace(in.Note) == "" || len(in.Note) > 64<<10 {
			err = fmt.Errorf("plan annotations must contain at most 64 KiB")
		} else {
			var planner AgentRun
			var found bool
			var runs []AgentRun
			runs, err = s.Store.Runs(ctx, order.ID)
			if err == nil {
				for index := len(runs) - 1; index >= 0; index-- {
					if runs[index].Kind == "planner" || runs[index].Kind == "planner-feedback" {
						planner, found = runs[index], true
						break
					}
				}
				if !found {
					err = fmt.Errorf("planner agent is not available")
				}
			}
			if err == nil {
				var message model.AgentMessage
				message, err = s.Reconciler.Galpon.Send(ctx, planner.AgentID, in.Note)
				if err == nil {
					err = s.Store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: planner.AgentID, Kind: "planner-feedback", Commit: message.ID, Status: "running", Result: message.ID})
				}
			}
			if err == nil {
				err = s.Store.SetStage(ctx, order.ID, StagePlanning, "active")
			}
		}
	case "test_pass":
		if order.Stage != StageHumanTest || order.Status != "waiting" {
			err = fmt.Errorf("implementation is not waiting for testing")
		} else if err = s.requireTestGuide(ctx, order); err == nil {
			err = s.Store.SetStage(ctx, order.ID, StageReview, "active")
		}
	case "test_fail":
		if order.Stage != StageHumanTest || order.Status != "waiting" {
			err = fmt.Errorf("implementation is not waiting for testing")
		} else if err = s.requireTestGuide(ctx, order); err == nil {
			err = s.Store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: order.DeveloperID, Kind: "review-human", Commit: order.Commit, Status: "completed", Result: "Human test failed:\n" + in.Note})
			if err == nil {
				err = s.Store.SetStage(ctx, order.ID, StageReviewFixes, "active")
			}
		}
	case "approve_merge":
		if order.Stage != StageWaitingForApproval {
			err = fmt.Errorf("pull request is not waiting for merge approval")
		} else {
			var view model.AgentView
			view, err = s.Reconciler.Galpon.Agent(ctx, order.DeveloperID)
			var path string
			if err == nil {
				var commit string
				commit, path, err = agentCommit(ctx, view)
				if err == nil && commit != order.Commit {
					err = fmt.Errorf("the implementation commit changed; test and review it again")
				}
			}
			if err == nil {
				var pr PullRequest
				pr, err = s.Reconciler.GitHub.PullRequest(ctx, path, order.PRNumber)
				if err == nil && pr.Checks != "success" && pr.Checks != "none" {
					err = fmt.Errorf("pull request checks have not passed")
				}
			}
			if err == nil {
				err = s.Reconciler.GitHub.Merge(ctx, path, order.PRNumber)
			}
			if err == nil {
				_ = s.Store.Event(ctx, order.ID, "merge", "Merge approved")
			}
		}
	case "update_brief":
		if order.Stage != StageIntake || order.Status != "blocked" {
			err = fmt.Errorf("feature brief is not waiting for a fix")
		} else if strings.TrimSpace(in.Note) == "" || len(in.Note) > 128<<10 {
			err = fmt.Errorf("feature brief must contain at most 128 KiB")
		} else {
			err = s.Store.SetRequest(ctx, order.ID, strings.TrimSpace(in.Note))
		}
	case "stop_agent":
		var run AgentRun
		var found bool
		var runs []AgentRun
		runs, err = s.Store.Runs(ctx, order.ID)
		if err == nil {
			for index := len(runs) - 1; index >= 0; index-- {
				if runs[index].Status == "running" {
					run, found = runs[index], true
					break
				}
			}
			if !found {
				err = fmt.Errorf("no agent is currently running")
			}
		}
		if err == nil {
			var view model.AgentView
			view, err = s.Reconciler.Galpon.Agent(ctx, run.AgentID)
			if err == nil && view.Agent.RuntimeID == "" {
				err = fmt.Errorf("agent runtime is not available")
			}
			if err == nil {
				err = s.Reconciler.Galpon.StopRuntime(ctx, run.AgentID, view.Agent.RuntimeID, "stopped by Factory operator")
			}
			if err == nil {
				run.Status = "stopped"
				err = s.Store.PutRun(ctx, run)
			}
			if err == nil {
				err = s.Store.Fail(ctx, order.ID, fmt.Errorf("agent stopped by operator"))
			}
		}
	case "retry":
		var stopped *AgentRun
		var runs []AgentRun
		runs, err = s.Store.Runs(ctx, order.ID)
		if err == nil {
			for index := len(runs) - 1; index >= 0; index-- {
				if runs[index].Status == "stopped" {
					stopped = &runs[index]
					break
				}
			}
		}
		if err == nil && stopped != nil {
			var message model.AgentMessage
			message, err = s.Reconciler.Galpon.Send(ctx, stopped.AgentID, "Continue the Factory task from where it stopped. Complete the assigned work and report the final result.")
			if err == nil {
				stopped.Status = "running"
				stopped.Result = message.ID
				err = s.Store.PutRun(ctx, *stopped)
			}
		}
		if err == nil {
			_, err = s.Store.db.ExecContext(ctx, `UPDATE work_orders SET status='active',last_error='',updated_at=? WHERE id=?`, time.Now().UnixMilli(), order.ID)
		}
	case "cancel":
		_, err = s.Store.db.ExecContext(ctx, `UPDATE work_orders SET status='cancelled',updated_at=? WHERE id=?`, time.Now().UnixMilli(), order.ID)
	default:
		err = fmt.Errorf("unknown action %q", in.Action)
	}
	if err != nil {
		problem(w, err)
		return
	}
	_ = s.Store.Event(ctx, order.ID, "action", in.Action)
	updated, err := s.Store.Get(ctx, order.ID)
	if err != nil {
		problem(w, err)
		return
	}
	write(w, 200, updated)
	s.trigger()
}
func (s *Server) requireTestGuide(ctx context.Context, order WorkOrder) error {
	runs, err := s.Store.Runs(ctx, order.ID)
	if err != nil {
		return err
	}
	for index := len(runs) - 1; index >= 0; index-- {
		run := runs[index]
		if run.Kind == "test-guide" && run.Commit == order.Commit && run.Status == "completed" && strings.TrimSpace(run.Result) != "" {
			return nil
		}
	}
	return fmt.Errorf("the ready-to-use testing handoff is not available for commit %s", short(order.Commit))
}

func (s *Server) trigger() {
	if s.Reconciler != nil && s.Reconciler.Store != nil && s.Reconciler.Galpon != nil {
		go s.Reconciler.Tick(context.Background())
	}
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, err error) {
	write(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}
