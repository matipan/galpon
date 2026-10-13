package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/matipan/galpon/internal/model"
)

func TestUnstartedFailureRetiresOnlyItsRequestReceipt(t *testing.T) {
	s, agents := communicationV2Store(t)
	for n := range 2 {
		id := fmt.Sprintf("request-%d", n)
		now := time.Now().UnixMilli()
		message := model.AgentMessage{ID: id, TargetAgentID: "b", Kind: "request", Act: "request", ResultMode: "notify", Prompt: "Work", Status: "queued", CreatedAt: now, UpdatedAt: now}
		if _, _, err := s.AdmitCoordinationMessage(t.Context(), CoordinationSendAdmission{Message: message}); err != nil {
			t.Fatal(err)
		}
		op, err := s.ClaimAgentOperation(t.Context(), "b", agents["b"].RuntimeID, "claim-"+id)
		if err != nil || op == nil || op.ParentMessageID != id {
			t.Fatalf("next request could not start: %#v, %v", op, err)
		}
		if n == 1 {
			if err := s.StartAgentOperation(t.Context(), op.ID, "b", op.RuntimeID, op.Attempt); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SettleAgentOperation(t.Context(), op.ID, "b", op.RuntimeID, op.Attempt, "Done", ""); err != nil {
				t.Fatal(err)
			}
			break
		}
		if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "unseen-control", AgentID: "b", OperationID: op.ID, Kind: "control", State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SettleAgentOperation(t.Context(), op.ID, "b", op.RuntimeID, op.Attempt, "", "Input was rejected"); err != nil {
			t.Fatal(err)
		}
		request, err := s.AgentInboxReceipt(t.Context(), "request:"+id)
		if err != nil || request.State != "abandoned" || request.PresentedAt != 0 || request.AcknowledgedAt != 0 {
			t.Fatalf("unsaved input was not retired honestly: %#v, %v", request, err)
		}
		control, err := s.AgentInboxReceipt(t.Context(), "unseen-control")
		if err != nil || control.State != "pending" || control.OperationID != "" {
			t.Fatalf("unseen control was lost: %#v, %v", control, err)
		}
		receiptOp, receipt, err := s.ClaimAgentInboxReceiptOperation(t.Context(), "b", op.RuntimeID, "next-duty")
		if err != nil || receipt == nil || receipt.ID != control.ID {
			t.Fatalf("retired request became a receipt operation: %#v, %v", receipt, err)
		}
		taken, _, err := s.TakeOperationReceipts(t.Context(), receiptOp.ID, "b", op.RuntimeID, receiptOp.Attempt, "show-control", 1024)
		if err != nil || len(taken) != 1 {
			t.Fatalf("control delivery: %#v, %v", taken, err)
		}
		if err := s.MarkAgentInboxReceiptPresented(t.Context(), control.ID, receiptOp.ID, op.RuntimeID, receiptOp.Attempt, "show-control"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SettleAgentOperation(t.Context(), receiptOp.ID, "b", op.RuntimeID, receiptOp.Attempt, "Seen", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUnseenReceiptCanBeHandledAfterAnUnstartedFailure(t *testing.T) {
	s, agents := communicationV2Store(t)
	runtimeID := agents["a"].RuntimeID
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: "notice", AgentID: "a", Kind: "control", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	first, _, err := s.ClaimAgentInboxReceiptOperation(t.Context(), "a", runtimeID, "first")
	if err != nil || first == nil {
		t.Fatalf("first claim: %#v, %v", first, err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), first.ID, "a", runtimeID, first.Attempt, "", "Writer stopped before input"); err != nil {
		t.Fatal(err)
	}
	next, receipt, err := s.ClaimAgentInboxReceiptOperation(t.Context(), "a", runtimeID, "second")
	if err != nil || next == nil || receipt == nil || receipt.ID != "notice" {
		t.Fatalf("unseen duty could not be retried: %#v, %#v, %v", next, receipt, err)
	}
	retry, err := s.ClaimAgentOperation(t.Context(), "a", runtimeID, "second")
	if err != nil || retry == nil || retry.ID != next.ID || retry.Attempt != next.Attempt {
		t.Fatalf("claim retry changed ownership: %#v, %v", retry, err)
	}
	bindingRetry, sameReceipt, err := s.ClaimAgentInboxReceiptOperation(t.Context(), "a", runtimeID, "second")
	if err != nil || bindingRetry == nil || bindingRetry.ID != next.ID || sameReceipt == nil || sameReceipt.ID != receipt.ID {
		t.Fatalf("receipt claim retry changed its binding: %#v, %#v, %v", bindingRetry, sameReceipt, err)
	}
	taken, _, err := s.TakeOperationReceipts(t.Context(), next.ID, "a", runtimeID, next.Attempt, "show", 1024)
	if err != nil || len(taken) != 1 {
		t.Fatalf("receipt delivery: %#v, %v", taken, err)
	}
	if err := s.MarkAgentInboxReceiptPresented(t.Context(), receipt.ID, next.ID, runtimeID, next.Attempt, "show"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SettleAgentOperation(t.Context(), next.ID, "a", runtimeID, next.Attempt, "Seen", ""); err != nil {
		t.Fatal(err)
	}
	if op, _, err := s.ClaimAgentInboxReceiptOperation(t.Context(), "a", runtimeID, "empty"); err != nil || op != nil {
		t.Fatalf("completed receipt was offered again: %#v, %v", op, err)
	}
}

func TestTerminalMessageCannotReplayADetachedRequestReceipt(t *testing.T) {
	s, agents := communicationV2Store(t)
	now := time.Now().UnixMilli()
	message := model.AgentMessage{ID: "finished", TargetAgentID: "b", Act: "request", Prompt: "Work", Status: "failed", Error: "Input rejected", CompletedAt: now, CreatedAt: now, UpdatedAt: now}
	if err := s.PutAgentMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	receiptID := "request:" + message.ID
	if err := s.PutAgentInboxReceipt(t.Context(), model.AgentInboxReceipt{ID: receiptID, AgentID: "b", MessageID: message.ID, Kind: "request", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAgentOperation(t.Context(), model.AgentOperation{ID: "receipt-operation:" + receiptID, AgentID: "b", Kind: "direct", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `update agent_operations set state='failed',last_error='The saved native input for this operation is unavailable' where id=?`, "receipt-operation:"+receiptID); err != nil {
		t.Fatal(err)
	}
	ready, err := s.CoordinationReadyAgentIDs(t.Context())
	if err != nil || len(ready) != 0 {
		t.Fatalf("terminal request woke an agent: %v, %v", ready, err)
	}
	for n := range 2 {
		op, receipt, err := s.ClaimAgentInboxReceiptOperation(t.Context(), "b", agents["b"].RuntimeID, fmt.Sprintf("reopen-%d", n))
		if err != nil || op != nil || receipt != nil {
			t.Fatalf("terminal request replay: %#v, %#v, %v", op, receipt, err)
		}
	}
	saved, err := s.AgentInboxReceipt(t.Context(), receiptID)
	if err != nil || saved.State != "abandoned" || saved.PresentedAt != 0 || saved.AcknowledgedAt != 0 {
		t.Fatalf("terminal receipt retirement: %#v, %v", saved, err)
	}
}
