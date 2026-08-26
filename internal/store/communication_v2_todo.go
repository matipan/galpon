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

const todoIntentColumns = `id,message_id,coalesce(operation_id,''),todo_id,policy,state,runtime_id,claim_key,attempt,operation_attempt,lease_expires_at,last_error,created_at,applied_at,protocol_generation`
const todoIntentListColumns = `intent.id,intent.message_id,coalesce(intent.operation_id,''),intent.todo_id,intent.policy,intent.state,intent.runtime_id,intent.claim_key,intent.attempt,intent.operation_attempt,intent.lease_expires_at,intent.last_error,intent.created_at,intent.applied_at,intent.protocol_generation`
const todoEventColumns = `id,intent_id,result_id,agent_id,coalesce(operation_id,''),state,snapshot,runtime_id,claim_key,attempt,operation_attempt,lease_expires_at,last_error,created_at,applied_at,acknowledged_at,protocol_generation`

func scanTodoIntent(row rowScanner) (model.AgentTodoLinkIntent, error) {
	var value model.AgentTodoLinkIntent
	err := row.Scan(&value.ID, &value.MessageID, &value.OperationID, &value.TodoID, &value.Policy, &value.State, &value.RuntimeID, &value.ClaimKey, &value.Attempt, &value.OperationAttempt, &value.LeaseExpiresAt, &value.LastError, &value.CreatedAt, &value.AppliedAt, &value.ProtocolGeneration)
	return value, err
}

func scanTodoEvent(row rowScanner) (model.AgentTodoSettlementEvent, error) {
	var value model.AgentTodoSettlementEvent
	err := row.Scan(&value.ID, &value.IntentID, &value.ResultID, &value.AgentID, &value.OperationID, &value.State, &value.Snapshot, &value.RuntimeID, &value.ClaimKey, &value.Attempt, &value.OperationAttempt, &value.LeaseExpiresAt, &value.LastError, &value.CreatedAt, &value.AppliedAt, &value.AcknowledgedAt, &value.ProtocolGeneration)
	return value, err
}

func (s *Store) AgentTodoLinkIntents(ctx context.Context, agentID string) ([]model.AgentTodoLinkIntent, error) {
	rows, err := s.db.QueryContext(ctx, `select `+todoIntentListColumns+` from todo_link_intents intent join agent_messages message on message.id=intent.message_id where message.sender_agent_id=? or message.target_agent_id=? order by intent.created_at,intent.id`, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.AgentTodoLinkIntent
	for rows.Next() {
		value, err := scanTodoIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) ClaimAgentTodoLinkIntent(ctx context.Context, intentID, agentID, runtimeID, claimKey string, operationAttempt int) (model.AgentTodoLinkIntent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AgentTodoLinkIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := scanTodoIntent(tx.QueryRowContext(ctx, `select `+todoIntentColumns+` from todo_link_intents where id=?`, intentID))
	if err != nil {
		return value, err
	}
	now := time.Now().UnixMilli()
	if value.OperationID == "" {
		var senderID string
		if err := tx.QueryRowContext(ctx, `select sender_agent_id from agent_messages where id=?`, value.MessageID).Scan(&senderID); err != nil {
			return value, err
		}
		if senderID != agentID {
			return value, sql.ErrNoRows
		}
		if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, value.ProtocolGeneration); err != nil {
			return value, err
		}
		operationID := "todo-link-operation:" + value.ID
		lease := now + coordinationLease.Milliseconds()
		operation := model.AgentOperation{ID: operationID, AgentID: agentID, Kind: "direct", State: "claimed", CausalRunID: operationID, RuntimeID: runtimeID, ClaimKey: claimKey, Attempt: 1, ClaimedAt: now, LeaseExpiresAt: lease, CreatedAt: now, UpdatedAt: now, ProtocolGeneration: value.ProtocolGeneration}
		if _, err := tx.ExecContext(ctx, `insert into agent_operations(`+operationColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationValues(operation)...); err != nil {
			return value, err
		}
		if _, err := tx.ExecContext(ctx, `insert into agent_operation_attempts(id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at) values(?,?,?,?,?,'claimed',?,?)`, operationID+":1", operationID, 1, runtimeID, claimKey, now, now); err != nil {
			return value, err
		}
		value.OperationID, operationAttempt = operationID, 1
		if _, err := tx.ExecContext(ctx, `update todo_link_intents set operation_id=? where id=? and operation_id is null`, operationID, value.ID); err != nil {
			return value, err
		}
	}
	if _, err := fenceOperationMutation(ctx, tx, value.OperationID, agentID, runtimeID, operationAttempt); err != nil {
		return value, err
	}
	if value.RuntimeID != "" {
		if value.RuntimeID == runtimeID && value.ClaimKey == claimKey && value.OperationAttempt == operationAttempt && value.LeaseExpiresAt > now {
			return value, nil
		}
		return value, sql.ErrNoRows
	}
	result, err := tx.ExecContext(ctx, `update todo_link_intents set runtime_id=?,claim_key=?,attempt=attempt+1,operation_attempt=?,lease_expires_at=?,last_error='' where id=? and state='pending' and runtime_id=''`, runtimeID, claimKey, operationAttempt, now+coordinationLease.Milliseconds(), intentID)
	if err != nil {
		return value, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return value, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return value, err
	}
	value.RuntimeID, value.ClaimKey, value.OperationAttempt, value.LeaseExpiresAt = runtimeID, claimKey, operationAttempt, now+coordinationLease.Milliseconds()
	value.Attempt++
	return value, nil
}

func (s *Store) ApplyAgentTodoLinkIntent(ctx context.Context, intentID, agentID, runtimeID string, operationAttempt int) error {
	return s.finishTodoIntent(ctx, intentID, agentID, runtimeID, operationAttempt, true, "")
}

func (s *Store) FailAgentTodoLinkIntent(ctx context.Context, intentID, agentID, runtimeID string, operationAttempt int, failure string) error {
	if failure == "" {
		return fmt.Errorf("TODO link failure is required")
	}
	return s.finishTodoIntent(ctx, intentID, agentID, runtimeID, operationAttempt, false, failure)
}

func (s *Store) finishTodoIntent(ctx context.Context, intentID, agentID, runtimeID string, operationAttempt int, applied bool, failure string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := scanTodoIntent(tx.QueryRowContext(ctx, `select `+todoIntentColumns+` from todo_link_intents where id=?`, intentID))
	if err != nil {
		return err
	}
	if _, err := fenceOperationMutation(ctx, tx, value.OperationID, agentID, runtimeID, operationAttempt); err != nil {
		return err
	}
	if value.RuntimeID != runtimeID || value.OperationAttempt != operationAttempt || value.State != "pending" {
		return sql.ErrNoRows
	}
	now := time.Now().UnixMilli()
	state := "failed"
	if applied {
		state = "applied"
	}
	if _, err := tx.ExecContext(ctx, `update todo_link_intents set state=?,runtime_id='',claim_key='',lease_expires_at=0,last_error=?,applied_at=case when ? then ? else applied_at end where id=? and state='pending' and runtime_id=? and operation_attempt=?`, state, failure, applied, now, intentID, runtimeID, operationAttempt); err != nil {
		return err
	}
	if applied {
		if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set eligible=1,updated_at=? where message_id=? and kind='request' and state='pending'`, now, value.MessageID); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,operation_id,message_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?, 'blocker','pending',1,?,?,?)`, "todo-link-failed:"+intentID, agentID, value.OperationID, value.MessageID, now, now, value.ProtocolGeneration); err != nil {
			return err
		}
	}
	if strings.HasPrefix(value.OperationID, "todo-link-operation:") {
		operationState, attemptState, reason := "settled", "settled", ""
		if !applied {
			operationState, attemptState, reason = "failed", "failed", "todo_failed"
		}
		if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state=?,terminal_reason=?,finished_at=?,updated_at=? where operation_id=? and attempt=? and state in ('claimed','running')`, attemptState, reason, now, now, value.OperationID, operationAttempt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operations set state=?,runtime_id='',claim_key='',lease_expires_at=0,terminal_reason=?,last_error=?,settled_at=?,updated_at=? where id=? and attempt=? and state in ('claimed','running')`, operationState, reason, failure, now, now, value.OperationID, operationAttempt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AgentTodoSettlementEvents(ctx context.Context, agentID string) ([]model.AgentTodoSettlementEvent, error) {
	rows, err := s.db.QueryContext(ctx, `select `+todoEventColumns+` from todo_settlement_events where agent_id=? order by created_at,id`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.AgentTodoSettlementEvent
	for rows.Next() {
		value, err := scanTodoEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) ClaimAgentTodoSettlementEvent(ctx context.Context, agentID, runtimeID, claimKey string) (model.AgentTodoSettlementEvent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AgentTodoSettlementEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if claimKey != "" {
		value, err := scanTodoEvent(tx.QueryRowContext(ctx, `select `+todoEventColumns+` from todo_settlement_events where agent_id=? and claim_key=?`, agentID, claimKey))
		if err == nil {
			if value.RuntimeID != runtimeID || value.LeaseExpiresAt <= now || value.State != "pending" {
				return value, sql.ErrNoRows
			}
			if _, err := fenceOperationMutation(ctx, tx, value.OperationID, agentID, runtimeID, value.OperationAttempt); err != nil {
				return value, err
			}
			return value, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return model.AgentTodoSettlementEvent{}, err
		}
	}
	value, err := scanTodoEvent(tx.QueryRowContext(ctx, `select `+todoEventColumns+` from todo_settlement_events where agent_id=? and state='pending' and acknowledged_at=0 and runtime_id='' order by created_at,id limit 1`, agentID))
	if err != nil {
		return value, err
	}
	if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, value.ProtocolGeneration); err != nil {
		return value, err
	}
	operationID := "todo-operation:" + value.ID
	lease := now + coordinationLease.Milliseconds()
	operation := model.AgentOperation{ID: operationID, AgentID: agentID, Kind: "direct", State: "claimed", CausalRunID: operationID, RuntimeID: runtimeID, ClaimKey: claimKey, Attempt: 1, ClaimedAt: now, LeaseExpiresAt: lease, CreatedAt: now, UpdatedAt: now, ProtocolGeneration: value.ProtocolGeneration}
	if _, err := tx.ExecContext(ctx, `insert into agent_operations(`+operationColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationValues(operation)...); err != nil {
		return value, err
	}
	if _, err := tx.ExecContext(ctx, `insert into agent_operation_attempts(id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at) values(?,?,?,?,?,'claimed',?,?)`, operationID+":1", operationID, 1, runtimeID, claimKey, now, now); err != nil {
		return value, err
	}
	if _, err := tx.ExecContext(ctx, `update todo_settlement_events set operation_id=?,runtime_id=?,claim_key=?,attempt=attempt+1,operation_attempt=1,lease_expires_at=?,last_error='' where id=? and state='pending' and runtime_id=''`, operationID, runtimeID, claimKey, lease, value.ID); err != nil {
		return value, err
	}
	if err := tx.Commit(); err != nil {
		return value, err
	}
	value.OperationID, value.RuntimeID, value.ClaimKey, value.OperationAttempt, value.LeaseExpiresAt = operationID, runtimeID, claimKey, 1, lease
	value.Attempt++
	return value, nil
}

func (s *Store) ApplyAgentTodoSettlementEvent(ctx context.Context, eventID, agentID, runtimeID string, operationAttempt int, snapshot string) error {
	return s.finishTodoEvent(ctx, eventID, agentID, runtimeID, operationAttempt, "applied", snapshot, "")
}

func (s *Store) FailAgentTodoSettlementEvent(ctx context.Context, eventID, agentID, runtimeID string, operationAttempt int, failure string) error {
	if failure == "" {
		return fmt.Errorf("TODO settlement failure is required")
	}
	return s.finishTodoEvent(ctx, eventID, agentID, runtimeID, operationAttempt, "failed", "", failure)
}

func (s *Store) finishTodoEvent(ctx context.Context, eventID, agentID, runtimeID string, operationAttempt int, state, snapshot, failure string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := scanTodoEvent(tx.QueryRowContext(ctx, `select `+todoEventColumns+` from todo_settlement_events where id=? and agent_id=?`, eventID, agentID))
	if err != nil {
		return err
	}
	if _, err := fenceOperationMutation(ctx, tx, value.OperationID, agentID, runtimeID, operationAttempt); err != nil {
		return err
	}
	if value.RuntimeID != runtimeID || value.OperationAttempt != operationAttempt || value.State != "pending" {
		return sql.ErrNoRows
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update todo_settlement_events set state=?,snapshot=?,last_error=?,applied_at=case when ?='applied' then ? else applied_at end,lease_expires_at=0 where id=? and state='pending' and runtime_id=? and operation_attempt=?`, state, snapshot, failure, state, now, eventID, runtimeID, operationAttempt); err != nil {
		return err
	}
	if state == "failed" {
		if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='failed',terminal_reason='todo_failed',finished_at=?,updated_at=? where operation_id=? and attempt=? and state in ('claimed','running')`, now, now, value.OperationID, operationAttempt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operations set state='failed',runtime_id='',claim_key='',lease_expires_at=0,terminal_reason='failed',last_error=?,settled_at=?,updated_at=? where id=? and attempt=? and state in ('claimed','running')`, failure, now, now, value.OperationID, operationAttempt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AcknowledgeAgentTodoSettlementEvent(ctx context.Context, eventID, agentID, runtimeID string, operationAttempt int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	value, err := scanTodoEvent(tx.QueryRowContext(ctx, `select `+todoEventColumns+` from todo_settlement_events where id=? and agent_id=?`, eventID, agentID))
	if err != nil {
		return err
	}
	if _, err := fenceOperationMutation(ctx, tx, value.OperationID, agentID, runtimeID, operationAttempt); err != nil {
		return err
	}
	if value.RuntimeID != runtimeID || value.OperationAttempt != operationAttempt || value.State != "applied" {
		return sql.ErrNoRows
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update todo_settlement_events set runtime_id='',claim_key='',lease_expires_at=0,acknowledged_at=? where id=? and state='applied' and runtime_id=? and operation_attempt=?`, now, eventID, runtimeID, operationAttempt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='settled',finished_at=?,updated_at=? where operation_id=? and attempt=? and state in ('claimed','running')`, now, now, value.OperationID, operationAttempt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_operations set state='settled',runtime_id='',claim_key='',lease_expires_at=0,settled_at=?,updated_at=? where id=? and attempt=? and state in ('claimed','running')`, now, now, value.OperationID, operationAttempt); err != nil {
		return err
	}
	return tx.Commit()
}
