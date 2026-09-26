package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrAgentSessionResetBlocked reports Galpón work that still belongs to the
// agent's current Pi conversation.
var ErrAgentSessionResetBlocked = errors.New("agent has active Galpón work in its current Pi session")

// ErrAgentSessionResetConflict reports that the caller does not own the
// current runtime or that the current Pi session changed.
var ErrAgentSessionResetConflict = errors.New("agent runtime or Pi session changed before the session reset")

// AgentSessionResetBlocker returns a short description of the first active
// Galpón work item that would lose its Pi conversation after a session reset.
// An empty description means that a reset is safe.
func (s *Store) AgentSessionResetBlocker(ctx context.Context, agentID string) (string, error) {
	return agentSessionResetBlocker(ctx, s.db, agentID)
}

func agentSessionResetBlocker(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, agentID string) (string, error) {
	var operations int
	// A ready operation with an attempt resumes an earlier conversation turn.
	// A ready direct operation belongs to a user prompt in this session. Fresh
	// inbound requests have no attempt yet and can start in the new session.
	if err := query.QueryRowContext(ctx, `select count(*) from agent_operations where agent_id=? and (state in ('claimed','running','waiting','settling') or (state='ready' and (attempt>0 or kind='direct')))`, agentID).Scan(&operations); err != nil {
		return "", err
	}
	if operations != 0 {
		return fmt.Sprintf("%d active Galpón operation(s)", operations), nil
	}
	var deliveries int
	if err := query.QueryRowContext(ctx, `select count(*) from agent_messages where target_agent_id=? and status='delivered'`, agentID).Scan(&deliveries); err != nil {
		return "", err
	}
	if deliveries != 0 {
		return fmt.Sprintf("%d delivered Galpón message(s)", deliveries), nil
	}
	return "", nil
}

// ResetAgentSession moves an agent from its current Pi session to a new Pi
// session in the same runtime. The update succeeds only for the runtime that
// owns the agent, only while the stored session path still matches
// previousSessionPath, and only when no active Galpón work belongs to the
// previous conversation.
func (s *Store) ResetAgentSession(ctx context.Context, id, runtimeID, previousSessionPath, sessionID, sessionPath string, generation int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	currentGeneration, cutoverComplete, maintenance, err := protocolState(ctx, tx)
	if err != nil {
		return err
	}
	if maintenance {
		return fmt.Errorf("communication protocol is in maintenance mode")
	}
	if cutoverComplete && generation != currentGeneration {
		return fmt.Errorf("communication protocol generation %d is stale; current generation is %d", generation, currentGeneration)
	}
	var owned int
	if err := tx.QueryRowContext(ctx, `select count(*) from agents where id=? and runtime_id=? and session_path=?`, id, runtimeID, previousSessionPath).Scan(&owned); err != nil {
		return err
	}
	if owned != 1 {
		return ErrAgentSessionResetConflict
	}
	blocker, err := agentSessionResetBlocker(ctx, tx, id)
	if err != nil {
		return err
	}
	if blocker != "" {
		return fmt.Errorf("%w: %s", ErrAgentSessionResetBlocked, blocker)
	}
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update agents set session_id=?,session_path=?,updated_at=? where id=? and runtime_id=? and session_path=?`, sessionID, sessionPath, now, id, runtimeID, previousSessionPath); err != nil {
		return err
	}
	return tx.Commit()
}
