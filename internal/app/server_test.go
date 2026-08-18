package app

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRuntimeStopDoesNotWaitForExclusiveRepositoryOperation(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	server.repositoryGate.Lock()
	defer server.repositoryGate.Unlock()
	request := httptest.NewRequest(http.MethodPost, "/v1/runtime/agents/agent/stop", bytes.NewBufferString(`{"runtimeId":"runtime","error":""}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		server.http.Handler.ServeHTTP(response, request)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("runtime stop waited for the repository gate")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("runtime stop status = %d: %s", response.Code, response.Body.String())
	}
}

func TestRuntimeToolRequiresRegisteredRuntime(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	for _, test := range []struct {
		name string
		body string
		want int
	}{
		{name: "missing runtime", body: `{"agentId":"agent","args":{}}`, want: http.StatusUnauthorized},
		{name: "wrong runtime", body: `{"agentId":"agent","runtimeId":"other","args":{}}`, want: http.StatusUnauthorized},
		{name: "registered runtime", body: `{"agentId":"agent","runtimeId":"runtime","requestId":"tool-call","args":{}}`, want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/list_agents", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			server.http.Handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestRuntimeMutationUsesDurableRequestReceipt(t *testing.T) {
	application := companionTestApp(t, "runtime")
	server := NewServer(application)
	call := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/runtime/tools/create_workspace", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}
	body := `{"agentId":"agent","runtimeId":"runtime","requestId":"create-1","args":{"title":"New work"}}`
	first := call(body)
	second := call(body)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() {
		t.Fatalf("idempotent runtime mutation = first %d %s, second %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	dashboard, err := application.Store.Dashboard(t.Context())
	if err != nil || len(dashboard.Workspaces) != 2 {
		t.Fatalf("workspaces after retry = %#v, %v", dashboard.Workspaces, err)
	}
	conflict := call(`{"agentId":"agent","runtimeId":"runtime","requestId":"create-1","args":{"title":"Different work"}}`)
	if conflict.Code == http.StatusOK {
		t.Fatalf("conflicting runtime receipt succeeded: %s", conflict.Body.String())
	}
}

func TestShutdownWaitsForRepositoryOperation(t *testing.T) {
	s := &Server{http: &http.Server{}, done: make(chan struct{})}
	s.repositoryGate.RLock()
	response := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		s.shutdown(response, httptest.NewRequest(http.MethodPost, "/v1/shutdown", nil))
		close(returned)
	}()

	deadline := time.Now().Add(time.Second)
	for !s.draining.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !s.draining.Load() {
		t.Fatal("server did not start draining")
	}
	select {
	case <-returned:
		t.Fatal("shutdown returned while a repository operation was active")
	default:
	}

	s.repositoryGate.RUnlock()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not continue after the repository operation finished")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("shutdown status = %d", response.Code)
	}
}
