package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestServerReportsFactoryAPIVersion(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	server := NewServer(store, &Reconciler{Store: store})
	request := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	response := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(response, request)
	var health struct {
		APIVersion int `json:"apiVersion"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.APIVersion != APIVersion {
		t.Fatalf("API version = %d, want %d", health.APIVersion, APIVersion)
	}
}

func TestServerCreateListAndApprovePlan(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	reconciler := &Reconciler{Store: store}
	server := NewServer(store, reconciler)
	httpServer := httptest.NewServer(server.HTTP.Handler)
	defer httpServer.Close()
	body := []byte(`{"title":"Feature","request":"Do it","repositoryId":"repo","repositoryPath":"/repo","workspaceId":"workspace"}`)
	response, err := http.Post(httpServer.URL+"/v1/work-orders", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", response.StatusCode)
	}
	var order WorkOrder
	if err = json.NewDecoder(response.Body).Decode(&order); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err = store.SetStage(context.Background(), order.ID, StagePlanApproval, "waiting"); err != nil {
		t.Fatal(err)
	}
	response, err = http.Post(httpServer.URL+"/v1/work-orders/"+order.ID+"/actions", "application/json", bytes.NewBufferString(`{"action":"approve_plan"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("action status %d", response.StatusCode)
	}
	updated, err := store.Get(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Stage != StageImplementation || updated.Status != "active" {
		t.Fatalf("unexpected transition: %#v", updated)
	}
	response, err = http.Get(httpServer.URL + "/v1/work-orders")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err = json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(snapshot.WorkOrders) != 1 {
		t.Fatalf("unexpected list: %#v", snapshot)
	}
}

func TestServerReturnsRunsForQueueAndSelectedFeature(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: "agent", Kind: "planner", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, &Reconciler{Store: store})
	for _, path := range []string{"/v1/work-orders", "/v1/work-orders/" + order.ID} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if path != "/v1/work-orders" {
			request.SetPathValue("id", order.ID)
		}
		response := httptest.NewRecorder()
		server.HTTP.Handler.ServeHTTP(response, request)
		var snapshot Snapshot
		if err = json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Runs) != 1 || snapshot.Runs[0].AgentID != "agent" {
			t.Fatalf("%s runs = %#v", path, snapshot.Runs)
		}
	}
}

func TestServerRequiresTestingGuideBeforeHumanResult(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetDeveloper(ctx, order.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetCommit(ctx, order.ID, "commit-one"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetStage(ctx, order.ID, StageHumanTest, "waiting"); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, &Reconciler{Store: store})
	postPass := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/work-orders/"+order.ID+"/actions", bytes.NewBufferString(`{"action":"test_pass"}`))
		request.SetPathValue("id", order.ID)
		response := httptest.NewRecorder()
		server.HTTP.Handler.ServeHTTP(response, request)
		return response
	}
	if response := postPass(); response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("testing handoff is not available")) {
		t.Fatalf("test without guide = %d: %s", response.Code, response.Body.String())
	}
	if err = store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: "developer", Kind: "test-guide", Commit: "commit-one", Status: "completed", Result: "Run the CLI and inspect its output."}); err != nil {
		t.Fatal(err)
	}
	if response := postPass(); response.Code != http.StatusOK {
		t.Fatalf("test with guide = %d: %s", response.Code, response.Body.String())
	}
	updated, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Stage != StageReview || updated.Status != "active" {
		t.Fatalf("test result did not start review: %#v", updated)
	}
}

func TestServerUpdatesBlockedBriefAndResumes(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Fail(ctx, order.ID, context.Canceled); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, &Reconciler{Store: store})
	request := httptest.NewRequest(http.MethodPost, "/v1/work-orders/"+order.ID+"/actions", bytes.NewBufferString(`{"action":"update_brief","note":"Add OAuth login with GitHub"}`))
	request.SetPathValue("id", order.ID)
	response := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	updated, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Request != "Add OAuth login with GitHub" || updated.Status != "active" || updated.LastError != "" {
		t.Fatalf("updated feature = %#v", updated)
	}
}

func TestServerStopsAndResumesActiveAgent(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetStage(ctx, order.ID, StageImplementation, "active"); err != nil {
		t.Fatal(err)
	}
	if err = store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: "planner-agent", Kind: "developer", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	client, closeServer := fakeGalpon(t)
	defer closeServer()
	server := NewServer(store, &Reconciler{Store: store, Galpon: client})
	postAction := func(action string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/work-orders/"+order.ID+"/actions", bytes.NewBufferString(`{"action":"`+action+`"}`))
		request.SetPathValue("id", order.ID)
		response := httptest.NewRecorder()
		server.HTTP.Handler.ServeHTTP(response, request)
		return response
	}
	if response := postAction("stop_agent"); response.Code != http.StatusOK {
		t.Fatalf("stop status %d: %s", response.Code, response.Body.String())
	}
	blocked, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != "blocked" || blocked.LastError != "agent stopped by operator" {
		t.Fatalf("stopped feature = %#v", blocked)
	}
	if response := postAction("retry"); response.Code != http.StatusOK {
		t.Fatalf("retry status %d: %s", response.Code, response.Body.String())
	}
	resumed, err := store.Get(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != "active" || runs[len(runs)-1].Status != "running" || runs[len(runs)-1].Result != "feedback" {
		t.Fatalf("resumed feature = %#v, runs = %#v", resumed, runs)
	}
}

func TestServerDeletesFeatureRunsAndHistory(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: "planner-agent", Kind: "planner", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err = store.Event(ctx, order.ID, "approval", "Plan ready"); err != nil {
		t.Fatal(err)
	}
	client, closeServer := fakeGalpon(t)
	defer closeServer()
	server := NewServer(store, &Reconciler{Store: store, Galpon: client})
	request := httptest.NewRequest(http.MethodDelete, "/v1/work-orders/"+order.ID, nil)
	request.SetPathValue("id", order.ID)
	response := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("delete status %d: %s", response.Code, response.Body.String())
	}
	if _, err = store.Get(ctx, order.ID); err == nil {
		t.Fatal("deleted feature still exists")
	}
	runs, err := store.Runs(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 || len(events) != 0 {
		t.Fatalf("delete left runs or events: runs=%#v events=%#v", runs, events)
	}
}

func TestPlanReviewAnnotationsContinueInExistingPlanner(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	order, err := store.Create(ctx, CreateRequest{Title: "Feature", Request: "Build it", RepositoryID: "repo", WorkspaceID: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetPlan(ctx, order.ID, "Original plan"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetStage(ctx, order.ID, StagePlanApproval, "waiting"); err != nil {
		t.Fatal(err)
	}
	if err = store.PutRun(ctx, AgentRun{WorkOrderID: order.ID, AgentID: "planner-agent", Kind: "planner", Status: "completed", Result: "Original plan"}); err != nil {
		t.Fatal(err)
	}
	client, closeServer := fakeGalpon(t)
	defer closeServer()
	reconciler := &Reconciler{Store: store, Galpon: client, GitHub: fakeGitHub{}}
	server := NewServer(store, reconciler)
	request := httptest.NewRequest(http.MethodPost, "/v1/work-orders/"+order.ID+"/actions", bytes.NewBufferString(`{"action":"review_plan","note":"Revise the plan from these annotations."}`))
	response := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		updated, getErr := store.Get(ctx, order.ID)
		if getErr == nil && updated.Stage == StagePlanApproval && updated.Plan == "Revised plan result" {
			runs, runErr := store.Runs(ctx, order.ID)
			if runErr != nil {
				t.Fatal(runErr)
			}
			if len(runs) != 2 || runs[1].Kind != "planner-feedback" || runs[1].AgentID != "planner-agent" {
				t.Fatalf("planner runs = %#v", runs)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	updated, _ := store.Get(ctx, order.ID)
	t.Fatalf("revised plan did not return for approval: %#v", updated)
}

func TestServerRejectsInvalidTransition(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	order, err := store.Create(context.Background(), CreateRequest{Title: "Feature", Request: "x", RepositoryID: "r", WorkspaceID: "w"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, &Reconciler{Store: store})
	request := httptest.NewRequest(http.MethodPost, "/v1/work-orders/"+order.ID+"/actions", bytes.NewBufferString(`{"action":"approve_plan"}`))
	request.SetPathValue("id", order.ID)
	response := httptest.NewRecorder()
	server.HTTP.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status %d", response.Code)
	}
}
