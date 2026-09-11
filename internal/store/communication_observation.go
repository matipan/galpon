package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matipan/galpon/internal/model"
)

// ReadCoordinationTask observes a stable assignment handle. Operations and
// immutable results, not the legacy message delivery projection, own its state.
// Reading does not claim, present, or acknowledge a result receipt.
func (s *Store) ReadCoordinationTask(ctx context.Context, id, agentID string) (model.AgentMessage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return model.AgentMessage{}, err
	}
	defer func() { _ = tx.Rollback() }()
	message, err := scanAgentMessage(tx.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where id=? and (sender_agent_id=? or target_agent_id=?)`, id, agentID, agentID))
	if err != nil {
		return message, err
	}
	images, err := loadMessageImages(ctx, tx, message.ID, true)
	if err != nil {
		return message, err
	}
	message.Images = imagePointer(images)
	operation, err := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where parent_message_id=? and kind='inbound'`, id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return message, err
	}
	if err == nil {
		message.Attempt = operation.Attempt
		message.UpdatedAt = max(message.UpdatedAt, operation.UpdatedAt)
		message.RuntimeID = operation.RuntimeID
		message.LeaseExpiresAt = operation.LeaseExpiresAt
		switch operation.State {
		case "ready":
			message.Status = "queued"
		case "claimed", "running", "settling":
			message.Status = "running"
		case "waiting":
			message.Status = "waiting"
		case "settled":
			message.Status = "completed"
		case "failed", "expired", "canceled":
			message.Status = "failed"
			message.Error = operation.LastError
			message.TerminalReason = operation.TerminalReason
		}
	}
	result, err := scanAgentMessageResult(tx.QueryRowContext(ctx, `select id,message_id,status,response,error,terminal_reason,legacy_state,created_at,protocol_generation from agent_message_results where message_id=?`, id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return message, err
	}
	if err == nil {
		message.Status = result.Status
		if result.Status == "expired" || result.Status == "canceled" {
			message.Status = "failed"
		}
		message.Response, message.Error = result.Response, result.Error
		message.TerminalReason = result.TerminalReason
		message.CompletedAt = result.CreatedAt
		message.UpdatedAt = max(message.UpdatedAt, result.CreatedAt)
	}
	return message, tx.Commit()
}

// ObserveAgentResults is a runtime acknowledgement of tool results already
// saved in the Pi conversation. It is deliberately separate from read/await.
// Only this operation's receipts or unbound notifications can be presented;
// another operation's causal result must never be consumed by observation.
func (s *Store) ObserveAgentResults(ctx context.Context, agentID, runtimeID, operationID string, attempt int, toolCallID string, messageIDs []string) error {
	if strings.TrimSpace(toolCallID) == "" || len(toolCallID) > 200 || len(messageIDs) < 1 || len(messageIDs) > 16 {
		return fmt.Errorf("a tool call ID and between 1 and 16 result handles are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := fenceOperationMutation(ctx, tx, operationID, agentID, runtimeID, attempt)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	seen := make(map[string]bool, len(messageIDs))
	for _, id := range messageIDs {
		if id == "" || seen[id] {
			return fmt.Errorf("result handles must be nonempty and unique")
		}
		seen[id] = true
		var sender string
		if err := tx.QueryRowContext(ctx, `select sender_agent_id from agent_messages where id=? and (sender_agent_id=? or target_agent_id=?)`, id, agentID, agentID).Scan(&sender); err != nil {
			return err
		}
		if sender != agentID {
			continue // A recipient can inspect its own work, but cannot take the sender's result.
		}
		if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set operation_id=?,state='presented',runtime_id=?,pi_tool_request_id=?,attempt=attempt+1,operation_attempt=?,claimed_at=?,presented_at=?,lease_expires_at=?,updated_at=?
			where agent_id=? and message_id=? and kind in ('result','blocker') and result_id is not null
			and state='pending' and eligible=1 and protocol_generation=? and (operation_id=? or operation_id is null)
			and exists(select 1 from agent_message_results where id=agent_inbox_receipts.result_id)`,
			operationID, runtimeID, toolCallID, attempt, now, now, operation.LeaseExpiresAt, now,
			agentID, id, operation.ProtocolGeneration, operationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
