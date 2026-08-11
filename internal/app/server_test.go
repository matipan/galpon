package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
