package store

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestOperationRenewalKeepsInboundReceiptAlive(t *testing.T) {
	for _, generation := range []int{2, 3} {
		for _, started := range []bool{false, true} {
			t.Run(fmt.Sprintf("generation_%d/started_%t", generation, started), func(t *testing.T) {
				s, agents := communicationV2Store(t)
				if _, err := s.db.Exec(`update communication_protocol_state set generation=?,cutover_complete=1`, generation); err != nil {
					t.Fatal(err)
				}
				for _, agent := range agents {
					if err := s.RegisterAgentProtocolGeneration(t.Context(), agent.ID, agent.RuntimeID, generation); err != nil {
						t.Fatal(err)
					}
				}
				message, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{
					Message:   model.AgentMessage{ID: "long-request", SenderAgentID: "a", TargetAgentID: "b", Prompt: "long work", ResultMode: "notify"},
					RuntimeID: agents["a"].RuntimeID,
				})
				if err != nil {
					t.Fatal(err)
				}
				op, err := s.ClaimAgentOperation(t.Context(), "b", agents["b"].RuntimeID, "long-request-claim")
				if err != nil || op == nil {
					t.Fatalf("claim = %#v, %v", op, err)
				}
				if started {
					if err := s.StartAgentOperation(t.Context(), op.ID, op.AgentID, op.RuntimeID, op.Attempt); err != nil {
						t.Fatal(err)
					}
				}
				// Simulate a heartbeat halfway through the original two-minute lease.
				originalDeadline := time.Now().Add(coordinationLease / 2).UnixMilli()
				for _, table := range []string{"agent_operations", "agent_inbox_receipts"} {
					if _, err := s.db.Exec(`update `+table+` set claimed_at=?,lease_expires_at=?`, originalDeadline-coordinationLease.Milliseconds(), originalDeadline); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.RenewAgentOperationLease(t.Context(), op.ID, op.AgentID, op.RuntimeID, op.Attempt); err != nil {
					t.Fatal(err)
				}
				renewed, err := s.AgentOperation(t.Context(), op.ID)
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := s.AgentInboxReceipt(t.Context(), "request:"+message.ID)
				if err != nil || receipt.LeaseExpiresAt != renewed.LeaseExpiresAt || receipt.LeaseExpiresAt <= originalDeadline {
					t.Fatalf("receipt lease was not renewed with the operation: %#v, operation %#v, %v", receipt, renewed, err)
				}
				recoverLeasesAt(t, s, originalDeadline+1)
				current, err := s.AgentOperation(t.Context(), op.ID)
				if err != nil || current.State != renewed.State || current.Attempt != op.Attempt || current.RuntimeID != op.RuntimeID {
					t.Fatalf("active work was recovered at its old receipt deadline: %#v, %v", current, err)
				}
				if _, err := s.SettleAgentOperation(t.Context(), op.ID, op.AgentID, op.RuntimeID, op.Attempt, "long work finished once", ""); err != nil {
					t.Fatal(err)
				}
				result, err := s.ReadCoordinationTask(t.Context(), message.ID, "a")
				if err != nil || result.Status != "completed" || result.Response != "long work finished once" || result.Attempt != 1 {
					t.Fatalf("long result = %#v, %v", result, err)
				}
			})
		}
	}
}

func recoverLeasesAt(t *testing.T, s *Store, now int64) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := recoverExpiredCoordinationLeases(t.Context(), tx, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func renewalOperation(t *testing.T, s *Store, id string) model.AgentOperation {
	t.Helper()
	if _, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: id, AgentID: "a", Kind: "direct", State: "ready", CausalRunID: id}); err != nil {
		t.Fatal(err)
	}
	return claimAndStartOperation(t, s, "a", "a-runtime", id)
}

func TestOperationRenewalOnlyExtendsOwnedActiveReceipts(t *testing.T) {
	s, _ := communicationV2Store(t)
	op := renewalOperation(t, s, "owner")
	other := renewalOperation(t, s, "other")
	oldLease := time.Now().Add(time.Minute).UnixMilli()
	for _, test := range []struct {
		name, kind, state, operation, agent, runtime string
		attempt                                      int
		renew                                        bool
	}{
		{"request", "request", "claimed", op.ID, "a", op.RuntimeID, op.Attempt, true},
		{"result", "result", "presented", op.ID, "a", op.RuntimeID, op.Attempt, true},
		{"blocker", "blocker", "claimed", op.ID, "a", op.RuntimeID, op.Attempt, true},
		{"control", "control", "presented", op.ID, "a", op.RuntimeID, op.Attempt, true},
		{"other-operation", "control", "claimed", other.ID, "a", op.RuntimeID, op.Attempt, false},
		{"old-runtime", "control", "claimed", op.ID, "a", "old-runtime", op.Attempt, false},
		{"old-attempt", "control", "presented", op.ID, "a", op.RuntimeID, op.Attempt - 1, false},
		{"pending", "control", "pending", op.ID, "a", op.RuntimeID, op.Attempt, false},
		{"acknowledged", "control", "acknowledged", op.ID, "a", op.RuntimeID, op.Attempt, false},
		{"abandoned", "control", "abandoned", op.ID, "a", op.RuntimeID, op.Attempt, false},
	} {
		if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: test.name, AgentID: test.agent, OperationID: test.operation, Kind: test.kind}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`update agent_inbox_receipts set state=?,runtime_id=?,operation_attempt=?,lease_expires_at=? where id=?`, test.state, test.runtime, test.attempt, oldLease, test.name); err != nil {
			t.Fatal(err)
		}
		if err := s.RenewAgentOperationLease(t.Context(), op.ID, op.AgentID, op.RuntimeID, op.Attempt); err != nil {
			t.Fatal(err)
		}
		receipt, err := s.AgentInboxReceipt(t.Context(), test.name)
		if err != nil || receipt.State != test.state || receipt.RuntimeID != test.runtime || receipt.OperationAttempt != test.attempt {
			t.Fatalf("%s ownership changed: %#v, %v", test.name, receipt, err)
		}
		if test.renew && receipt.LeaseExpiresAt <= oldLease || !test.renew && receipt.LeaseExpiresAt != oldLease {
			t.Fatalf("%s renewal scope: got %d, original %d, renew %t", test.name, receipt.LeaseExpiresAt, oldLease, test.renew)
		}
	}
}

func TestOperationRenewalRejectsStaleOwnershipAndRollsBack(t *testing.T) {
	for _, name := range []string{"agent", "runtime", "attempt", "expired-operation", "expired-receipt", "deadline", "receipt-write-failure"} {
		t.Run(name, func(t *testing.T) {
			s, _ := communicationV2Store(t)
			op := renewalOperation(t, s, "owner")
			if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "receipt", AgentID: op.AgentID, OperationID: op.ID, Kind: "control"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`update agent_inbox_receipts set state='presented',runtime_id=?,operation_attempt=?,lease_expires_at=?`, op.RuntimeID, op.Attempt, time.Now().Add(time.Minute).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			agent, runtime, attempt := op.AgentID, op.RuntimeID, op.Attempt
			switch name {
			case "agent":
				agent = "b"
			case "runtime":
				runtime = "old-runtime"
			case "attempt":
				attempt++
			case "expired-operation", "expired-receipt", "deadline":
				query := `update agent_operations set lease_expires_at=1`
				switch name {
				case "expired-receipt":
					query = `update agent_inbox_receipts set lease_expires_at=1`
				case "deadline":
					query = `update agent_operations set deadline_at=1`
				}
				if _, err := s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			case "receipt-write-failure":
				if _, err := s.db.Exec(`create trigger reject_receipt_renewal before update of lease_expires_at on agent_inbox_receipts begin select raise(abort,'test receipt write failure'); end`); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.DurableState(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = s.RenewAgentOperationLease(t.Context(), op.ID, agent, runtime, attempt)
			if name == "receipt-write-failure" {
				if err == nil {
					t.Fatal("receipt write failure was ignored")
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("stale renewal = %v", err)
			}
			after, err := s.DurableState(t.Context())
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected renewal changed durable state: %v", err)
			}
		})
	}
}

func TestOperationRenewalExtendsOwnedTodoLeases(t *testing.T) {
	for _, kind := range []string{"link", "settlement-pending", "settlement-applied"} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired_%t", kind, expired), func(t *testing.T) {
				s, _ := communicationV2Store(t)
				op := renewalOperation(t, s, "todo-owner")
				message, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{
					Message:         model.AgentMessage{ID: "todo-work", SenderAgentID: "a", TargetAgentID: "b", Prompt: "work", ResultMode: "notify"},
					SourceOperation: op.ID, OperationAttempt: op.Attempt, RuntimeID: op.RuntimeID, TodoID: 1,
				})
				if err != nil {
					t.Fatal(err)
				}
				intent, err := s.ClaimAgentTodoLinkIntent(t.Context(), "todo:"+message.ID, op.AgentID, op.RuntimeID, "link-claim", op.Attempt)
				if err != nil {
					t.Fatal(err)
				}
				table, id := "todo_link_intents", intent.ID
				if kind != "link" {
					if err := s.ApplyAgentTodoLinkIntent(t.Context(), intent.ID, op.AgentID, op.RuntimeID, op.Attempt); err != nil {
						t.Fatal(err)
					}
					child := claimAndStartOperation(t, s, "b", "b-runtime", "operation:"+message.ID)
					if _, err := s.SettleAgentOperation(t.Context(), child.ID, child.AgentID, child.RuntimeID, child.Attempt, "done", ""); err != nil {
						t.Fatal(err)
					}
					event, err := s.ClaimAgentTodoSettlementEvent(t.Context(), op.AgentID, op.RuntimeID, "settlement-claim")
					if err != nil {
						t.Fatal(err)
					}
					op, err = s.AgentOperation(t.Context(), event.OperationID)
					if err != nil {
						t.Fatal(err)
					}
					if kind == "settlement-applied" {
						if err := s.ApplyAgentTodoSettlementEvent(t.Context(), event.ID, op.AgentID, op.RuntimeID, op.Attempt, `{"status":"completed"}`); err != nil {
							t.Fatal(err)
						}
					}
					table, id = "todo_settlement_events", event.ID
				}
				oldLease := time.Now().Add(time.Minute).UnixMilli()
				if expired {
					oldLease = 1
				}
				if _, err := s.db.Exec(`update `+table+` set lease_expires_at=? where id=?`, oldLease, id); err != nil {
					t.Fatal(err)
				}
				before, err := s.DurableState(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = s.RenewAgentOperationLease(t.Context(), op.ID, op.AgentID, op.RuntimeID, op.Attempt)
				if expired {
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("expired TODO renewal = %v", err)
					}
					after, err := s.DurableState(t.Context())
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("expired TODO was revived: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				renewed, err := s.AgentOperation(t.Context(), op.ID)
				if err != nil {
					t.Fatal(err)
				}
				var lease int64
				if err := s.db.QueryRow(`select lease_expires_at from `+table+` where id=?`, id).Scan(&lease); err != nil || lease != renewed.LeaseExpiresAt || lease <= oldLease {
					t.Fatalf("TODO lease = %d, operation lease %d, %v", lease, renewed.LeaseExpiresAt, err)
				}
				recoverLeasesAt(t, s, oldLease+1)
				current, err := s.AgentOperation(t.Context(), op.ID)
				if err != nil || current.State != renewed.State || current.Attempt != op.Attempt {
					t.Fatalf("TODO work recovered at old lease deadline: %#v, %v", current, err)
				}
			})
		}
	}
}
