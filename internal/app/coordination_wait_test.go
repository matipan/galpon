package app

import (
	"testing"
	"time"
)

func TestCoordinationWaitWakesIdleRuntimeForClaimableWork(t *testing.T) {
	a, parent := taskTestApp(t)
	// A runtime without an observation receives the current state at once.
	initial, err := a.WaitCoordinationChange(t.Context(), "task-child", "", time.Second)
	if err != nil || !initial.Changed || initial.Fingerprint == "" {
		t.Fatalf("initial wait = %#v, %v", initial, err)
	}
	quiet, err := a.WaitCoordinationChange(t.Context(), "task-child", initial.Fingerprint, 600*time.Millisecond)
	if err != nil || quiet.Changed {
		t.Fatalf("unchanged idle wait = %#v, %v", quiet, err)
	}

	woke := make(chan CoordinationWake, 1)
	failed := make(chan error, 1)
	go func() {
		value, err := a.WaitCoordinationChange(t.Context(), "task-child", initial.Fingerprint, 10*time.Second)
		if err != nil {
			failed <- err
			return
		}
		woke <- value
	}()
	time.Sleep(100 * time.Millisecond)
	if _, _, err := a.QueueCoordinationMessage(t.Context(), "task-parent", "task-parent-runtime", parent.ID, parent.Attempt, 2, "task-child", "bounded work", "wake-child", "request", "join", 0, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failed:
		t.Fatal(err)
	case value := <-woke:
		if !value.Changed || value.Fingerprint == initial.Fingerprint {
			t.Fatalf("assignment wake = %#v", value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a new assignment did not wake the idle runtime")
	}
	// The wake is only a signal. The normal fenced claim receives the work.
	claimed, err := a.ClaimCoordinationOperation(t.Context(), "task-child", "task-child-runtime", "woken-claim", 2)
	if err != nil || claimed == nil {
		t.Fatalf("claim after wake = %#v, %v", claimed, err)
	}
}
