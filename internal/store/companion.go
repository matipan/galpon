package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type CompanionMutation struct {
	Key          string
	Operation    string
	RequestHash  string
	StatusCode   int
	ResponseJSON []byte
	CreatedAt    int64
}

// PutConversationEvents authenticates the active runtime and inserts each Pi
// event at most once. One durable invalidation is emitted for each non-empty
// inserted batch.
func (s *Store) PutConversationEvents(ctx context.Context, agentID, runtimeID string, events []model.ConversationEvent) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var workspaceID, activeRuntime string
	if err := tx.QueryRowContext(ctx, `select workstream_id,runtime_id from agents where id=? and not exists (select 1 from deleted_items where kind='agent' and resource_id=agents.id)`, agentID).Scan(&workspaceID, &activeRuntime); err != nil {
		return 0, err
	}
	if activeRuntime == "" || activeRuntime != runtimeID {
		return 0, errors.New("Pi runtime is not registered for this agent")
	}
	inserted := 0
	for _, event := range events {
		result, err := tx.ExecContext(ctx, `insert or ignore into conversation_events(agent_id,event_id,runtime_id,runtime_seq,kind,pi_entry_id,role,content,tool_name,tool_call_id,is_delta,is_error,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			agentID, event.EventID, runtimeID, event.RuntimeSeq, event.Kind, event.PiEntryID, event.Role, event.Content, event.ToolName, event.ToolCallID, event.IsDelta, event.IsError, event.CreatedAt)
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += int(count)
	}
	if inserted > 0 {
		if _, err := appendCompanionEvent(ctx, tx, "invalidate", agentID, workspaceID); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *Store) ConversationEvents(ctx context.Context, agentID string) ([]model.ConversationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `select sequence,agent_id,event_id,runtime_seq,kind,pi_entry_id,role,content,tool_name,tool_call_id,is_delta,is_error,created_at from conversation_events where agent_id=? order by sequence`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []model.ConversationEvent{}
	for rows.Next() {
		var event model.ConversationEvent
		if err := rows.Scan(&event.Sequence, &event.AgentID, &event.EventID, &event.RuntimeSeq, &event.Kind, &event.PiEntryID, &event.Role, &event.Content, &event.ToolName, &event.ToolCallID, &event.IsDelta, &event.IsError, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) AppendCompanionEvent(ctx context.Context, eventType, agentID, workspaceID string) (model.CompanionEvent, error) {
	return appendCompanionEvent(ctx, s.db, eventType, agentID, workspaceID)
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func appendCompanionEvent(ctx context.Context, exec sqlExecer, eventType, agentID, workspaceID string) (model.CompanionEvent, error) {
	createdAt := time.Now().UnixMilli()
	result, err := exec.ExecContext(ctx, `insert into companion_events(event_type,agent_id,workspace_id,created_at) values(?,?,?,?)`, eventType, agentID, workspaceID, createdAt)
	if err != nil {
		return model.CompanionEvent{}, err
	}
	sequence, err := result.LastInsertId()
	return model.CompanionEvent{Sequence: sequence, Type: eventType, AgentID: agentID, WorkspaceID: workspaceID, CreatedAt: createdAt}, err
}

func (s *Store) CompanionEventsAfter(ctx context.Context, after int64, limit int) ([]model.CompanionEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select sequence,event_type,agent_id,workspace_id,created_at from companion_events where sequence>? order by sequence limit ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []model.CompanionEvent{}
	for rows.Next() {
		var event model.CompanionEvent
		if err := rows.Scan(&event.Sequence, &event.Type, &event.AgentID, &event.WorkspaceID, &event.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) CompanionSequence(ctx context.Context) (int64, error) {
	var sequence int64
	err := s.db.QueryRowContext(ctx, `select coalesce(max(sequence),0) from companion_events`).Scan(&sequence)
	return sequence, err
}

func (s *Store) CompanionMutation(ctx context.Context, key string) (*CompanionMutation, error) {
	var value CompanionMutation
	err := s.db.QueryRowContext(ctx, `select idempotency_key,operation,request_hash,status_code,response_json,created_at from companion_mutations where idempotency_key=?`, key).Scan(
		&value.Key, &value.Operation, &value.RequestHash, &value.StatusCode, &value.ResponseJSON, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &value, err
}

// ReserveCompanionMutation durably admits a command before it has side
// effects. A status code of zero means that a prior process did not finish the
// command. Callers must fail that retry safely instead of running it again.
func (s *Store) ReserveCompanionMutation(ctx context.Context, key, operation, requestHash string) (*CompanionMutation, bool, error) {
	result, err := s.db.ExecContext(ctx, `insert or ignore into companion_mutations(idempotency_key,operation,request_hash,status_code,response_json,created_at) values(?,?,?,0,?,?)`,
		key, operation, requestHash, []byte(`{}`), time.Now().UnixMilli())
	if err != nil {
		return nil, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	value, err := s.CompanionMutation(ctx, key)
	return value, count == 1, err
}

func (s *Store) CompleteCompanionMutation(ctx context.Context, key string, statusCode int, responseJSON []byte) error {
	result, err := s.db.ExecContext(ctx, `update companion_mutations set status_code=?,response_json=? where idempotency_key=? and status_code=0`, statusCode, responseJSON, key)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("companion mutation is not pending")
	}
	return nil
}
