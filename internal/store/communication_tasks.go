package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

const coordinationTaskUpdateKind = "coordination_task_update"

type coordinationTaskUpdateRecord struct {
	MessageID string `json:"messageId"`
	Text      string `json:"text"`
	Status    string `json:"status"`
}

// UpdateCoordinationTask appends an update to one queued, unclaimed assignment.
// The sender's current operation fences the mutation. The update ID is stable
// and sender-scoped: an exact retry returns its first status, while reuse for a
// different operation, message, or text fails. Running work is not interrupted.
func (s *Store) UpdateCoordinationTask(ctx context.Context, messageID, senderID, runtimeID, operationID string, operationAttempt int, updateID, text string) (string, error) {
	messageID = strings.TrimSpace(messageID)
	senderID = strings.TrimSpace(senderID)
	runtimeID = strings.TrimSpace(runtimeID)
	operationID = strings.TrimSpace(operationID)
	updateID = strings.TrimSpace(updateID)
	text = strings.TrimSpace(text)
	if messageID == "" || senderID == "" || runtimeID == "" || operationID == "" || operationAttempt <= 0 {
		return "", fmt.Errorf("coordination task update ownership is required")
	}
	if updateID == "" || len(updateID) > 200 {
		return "", fmt.Errorf("a valid coordination task update ID is required")
	}
	if text == "" {
		return "", fmt.Errorf("coordination task update text is required")
	}
	if len(text) > model.AgentMessagePromptByteLimit {
		return "", fmt.Errorf("coordination task update text exceeds the %d-byte limit", model.AgentMessagePromptByteLimit)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	source, err := fenceOperationMutation(ctx, tx, operationID, senderID, runtimeID, operationAttempt)
	if err != nil {
		return "", err
	}

	eventID := "coordination-update:" + updateID
	var existingOperationID, existingKind, existingPayload string
	var existingAttempt int
	err = tx.QueryRowContext(ctx, `select coalesce(operation_id,''),operation_attempt,kind,payload from agent_pi_local_events where agent_id=? and event_id=?`, senderID, eventID).Scan(&existingOperationID, &existingAttempt, &existingKind, &existingPayload)
	if err == nil {
		var record coordinationTaskUpdateRecord
		if existingKind != coordinationTaskUpdateKind || existingOperationID != operationID || existingAttempt <= 0 || json.Unmarshal([]byte(existingPayload), &record) != nil || record.MessageID != messageID || record.Text != text {
			return "", fmt.Errorf("coordination task update ID was already used for different work")
		}
		return record.Status, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	var messageStatus, prompt, taskState, receiptState string
	var receiptAttempt, taskAttempt, generation int
	err = tx.QueryRowContext(ctx, `select message.status,message.prompt,operation.state,receipt.state,receipt.attempt,operation.attempt,operation.protocol_generation
from agent_messages message
join agent_operations operation on operation.parent_message_id=message.id and operation.agent_id=message.target_agent_id
join agent_inbox_receipts receipt on receipt.operation_id=operation.id and receipt.message_id=message.id and receipt.kind='request'
where message.id=? and message.sender_agent_id=? and message.kind='request'`, messageID, senderID).Scan(&messageStatus, &prompt, &taskState, &receiptState, &receiptAttempt, &taskAttempt, &generation)
	if err != nil {
		return "", err
	}
	if generation != source.ProtocolGeneration {
		return "", fmt.Errorf("coordination task update protocol generation does not match its operation")
	}

	status := "updated"
	if messageStatus == "completed" || messageStatus == "failed" || taskState == "settled" || taskState == "failed" || taskState == "canceled" || taskState == "expired" {
		status = "already_completed"
	} else if messageStatus != "queued" || taskState != "ready" || receiptState != "pending" || receiptAttempt != 0 || taskAttempt != 0 {
		status = "already_started"
	} else {
		updatedPrompt := prompt + "\n\nTask update:\n" + text
		if len(updatedPrompt) > model.AgentMessagePromptByteLimit {
			return "", fmt.Errorf("updated coordination task text exceeds the %d-byte limit", model.AgentMessagePromptByteLimit)
		}
		result, err := tx.ExecContext(ctx, `update agent_messages set prompt=?,updated_at=? where id=? and sender_agent_id=? and status='queued'
  and exists(select 1 from agent_operations operation join agent_inbox_receipts receipt on receipt.operation_id=operation.id
    where operation.parent_message_id=agent_messages.id and operation.state='ready' and operation.attempt=0 and receipt.kind='request' and receipt.state='pending' and receipt.attempt=0)`, updatedPrompt, time.Now().UnixMilli(), messageID, senderID)
		if err != nil {
			return "", err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if count != 1 {
			return "", sql.ErrNoRows
		}
	}

	payload, err := json.Marshal(coordinationTaskUpdateRecord{MessageID: messageID, Text: text, Status: status})
	if err != nil {
		return "", err
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `insert into agent_pi_local_events(id,agent_id,operation_id,operation_attempt,event_id,kind,state,payload,created_at,acknowledged_at,protocol_generation) values(?,?,?,?,?,?, 'acknowledged',?,?,?,?)`, uuid.NewString(), senderID, operationID, operationAttempt, eventID, coordinationTaskUpdateKind, string(payload), now, now, source.ProtocolGeneration); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return status, nil
}
