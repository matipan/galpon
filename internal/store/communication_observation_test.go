package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func observationStore(t *testing.T) (*Store, map[string]model.Agent) {
	t.Helper()
	s, agents := communicationV2Store(t)
	enableProjectionV2(t, s)
	for _, agent := range agents {
		if err := s.RegisterAgentProtocolGeneration(t.Context(), agent.ID, agent.RuntimeID, 2); err != nil {
			t.Fatal(err)
		}
	}
	return s, agents
}

func TestTaskObservationIsRepeatableAndSeparateFromAcknowledgement(t *testing.T) {
	for _, mode := range []string{"join", "notify"} {
		t.Run(mode, func(t *testing.T) {
			s, agents := observationStore(t)
			parent, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "parent", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "run", ProtocolGeneration: 2})
			if err != nil {
				t.Fatal(err)
			}
			parent = claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, parent.ID)
			message, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{
				Message:         model.AgentMessage{ID: "task", SenderAgentID: "a", TargetAgentID: "b", Prompt: "bounded work", ResultMode: mode},
				SourceOperation: parent.ID, OperationAttempt: parent.Attempt, JoinDeadlineAt: time.Now().Add(time.Minute).UnixMilli(),
			})
			if err != nil {
				t.Fatal(err)
			}
			child := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation:"+message.ID)
			running, err := s.ReadCoordinationTask(t.Context(), message.ID, "a")
			if err != nil || running.Status != "running" || running.Attempt != child.Attempt {
				t.Fatalf("running observation = %#v, %v", running, err)
			}
			if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "exact result", ""); err != nil {
				t.Fatal(err)
			}
			before, err := s.DurableState(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				got, err := s.ReadCoordinationTask(t.Context(), message.ID, "a")
				if err != nil || got.Status != "completed" || got.Response != "exact result" {
					t.Fatalf("completed observation = %#v, %v", got, err)
				}
			}
			after, err := s.DurableState(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("read changed durable state")
			}
			if _, err := s.ReadCoordinationTask(t.Context(), message.ID, "c"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unrelated participant read = %v", err)
			}
			if err := s.ObserveAgentResults(t.Context(), "a", "wrong-runtime", parent.ID, parent.Attempt, "tool", []string{message.ID}); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unfenced observation = %v", err)
			}
			for range 2 {
				if err := s.ObserveAgentResults(t.Context(), "a", agents["a"].RuntimeID, parent.ID, parent.Attempt, "tool", []string{message.ID}); err != nil {
					t.Fatal(err)
				}
			}
			settled, err := s.SettleAgentOperation(t.Context(), parent.ID, "a", agents["a"].RuntimeID, parent.Attempt, "integrated", "")
			if err != nil || settled.Parked || settled.Operation.State != "settled" {
				t.Fatalf("observed result caused extra turn: %#v, %v", settled, err)
			}
			if mode == "join" {
				join, err := s.AgentOperationJoin(t.Context(), parent.ID, message.ID)
				if err != nil || join.State != "acknowledged" {
					t.Fatalf("join = %#v, %v", join, err)
				}
			}
			got, err := s.ReadCoordinationTask(t.Context(), message.ID, "a")
			if err != nil || got.Response != "exact result" {
				t.Fatalf("result lost after acknowledgement: %#v, %v", got, err)
			}
		})
	}
}

func TestObservationDoesNotTakeAnotherOperationsReceipt(t *testing.T) {
	s, agents := observationStore(t)
	if _, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "owner", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "owner", ProtocolGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	owner := claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, "owner")
	message, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: model.AgentMessage{ID: "other-task", SenderAgentID: "a", TargetAgentID: "b", Prompt: "work"}, SourceOperation: owner.ID, OperationAttempt: owner.Attempt, JoinDeadlineAt: time.Now().Add(time.Minute).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), owner.ID, "a", agents["a"].RuntimeID, owner.Attempt, "waiting", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "observer", AgentID: "a", Kind: "direct", State: "ready", CausalRunID: "observer", ProtocolGeneration: 2}); err != nil {
		t.Fatal(err)
	}
	observer := claimAndStartOperation(t, s, "a", agents["a"].RuntimeID, "observer")
	child := claimAndStartOperation(t, s, "b", agents["b"].RuntimeID, "operation:"+message.ID)
	if _, err := s.SettleAgentOperation(t.Context(), child.ID, "b", agents["b"].RuntimeID, child.Attempt, "result", ""); err != nil {
		t.Fatal(err)
	}
	before, err := s.DurableState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ObserveAgentResults(t.Context(), "a", agents["a"].RuntimeID, observer.ID, observer.Attempt, "tool", []string{message.ID}); err != nil {
		t.Fatal(err)
	}
	after, err := s.DurableState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.AgentInboxReceipts, after.AgentInboxReceipts) {
		t.Fatal("observation stole another operation's receipt")
	}
}
