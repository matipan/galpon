package factory

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	"github.com/matipan/galpon/internal/app"
	"github.com/matipan/galpon/internal/model"
)

type fakeGitHub struct{}

func (fakeGitHub) Issue(context.Context, string, string) (Issue, error) { return Issue{}, nil }
func (fakeGitHub) FindPullRequest(context.Context, string, string) (PullRequest, error) {
	return PullRequest{}, nil
}
func (fakeGitHub) CreatePullRequest(context.Context, string, string, string, string) (PullRequest, error) {
	return PullRequest{}, nil
}
func (fakeGitHub) PullRequest(context.Context, string, int) (PullRequest, error) {
	return PullRequest{}, nil
}
func (fakeGitHub) Merge(context.Context, string, int) error { return nil }

func TestReviewApprovalMustBeFinalProtocolLine(t *testing.T) {
	if !reviewApproved("No findings.\nFACTORY_REVIEW: APPROVED\n") {
		t.Fatal("valid approval was rejected")
	}
	if reviewApproved("FACTORY_REVIEW: APPROVED\nFACTORY_REVIEW: CHANGES_REQUESTED") {
		t.Fatal("non-final approval was accepted")
	}
}

func TestReconcilerPersistsPlannerResult(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetStage(ctx, order.ID, StagePlanning, "active"); err != nil {
		t.Fatal(err)
	}
	client, closeServer := fakeGalpon(t)
	defer closeServer()
	r := &Reconciler{Store: store, Galpon: client, GitHub: fakeGitHub{}}
	r.Tick(ctx)
	runs, err := store.Runs(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Kind != "planner" {
		t.Fatalf("planner was not launched: %#v", runs)
	}
	r.Tick(ctx)
	got, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != StagePlanApproval || got.Status != "waiting" || got.Plan != "Plan result" {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestReconcilerPreparesTestingGuideBeforeHumanDecision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetDeveloper(ctx, order.ID, "planner-agent"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetCommit(ctx, order.ID, "commit-one"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetStage(ctx, order.ID, StageHumanTest, "waiting"); err != nil {
		t.Fatal(err)
	}
	client, closeServer := fakeGalpon(t)
	defer closeServer()
	r := &Reconciler{Store: store, Galpon: client, GitHub: fakeGitHub{}}

	r.Tick(ctx)
	preparing, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preparing.Stage != StageHumanTest || preparing.Status != "active" {
		t.Fatalf("feature did not wait for testing guide: %#v", preparing)
	}

	r.Tick(ctx)
	ready, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Stage != StageHumanTest || ready.Status != "waiting" {
		t.Fatalf("feature was not released for human testing: %#v", ready)
	}
	if len(runs) != 1 || runs[0].Kind != "test-guide" || runs[0].Commit != "commit-one" || runs[0].Status != "completed" || runs[0].Result != "Revised plan result" {
		t.Fatalf("testing guide run = %#v", runs)
	}
}

func fakeGalpon(t *testing.T) (*app.Client, func()) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "galpon.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	agentID := "planner-agent"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/integrations/factory/agents", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(app.CreateFactoryAgentResult{Agent: model.Agent{ID: agentID, Status: "running"}, InitialMessage: model.AgentMessage{ID: "message", TargetAgentID: agentID, Status: "queued"}})
	})
	mux.HandleFunc("POST /v1/agents/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(model.AgentMessage{ID: "feedback", TargetAgentID: agentID, Status: "queued"})
	})
	mux.HandleFunc("GET /v1/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(model.AgentView{Agent: model.Agent{ID: agentID, RuntimeID: "runtime", Status: "running"}, Messages: []model.AgentMessage{
			{ID: "message", TargetAgentID: agentID, Status: "completed", Response: "Plan result"},
			{ID: "feedback", TargetAgentID: agentID, Status: "completed", Response: "Revised plan result"},
		}})
	})
	mux.HandleFunc("POST /v1/runtime/agents/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"stopped": true})
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	return app.NewClient(socket), func() { _ = server.Close(); _ = listener.Close() }
}
