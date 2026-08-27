package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CommunicationIdleState is an authoritative, bounded summary used by the
// coordinated terminal upgrade. It does not contain durable IDs or content.
type CommunicationIdleState struct {
	DeliveredMessages int `json:"deliveredMessages"`
	ActiveOperations  int `json:"activeOperations"`
	BusyRuntimes      int `json:"busyRuntimes"`
}

type CommunicationUnregisteredRuntime struct {
	AgentID    string `json:"agentId"`
	AgentTitle string `json:"agentTitle"`
	RuntimeID  string `json:"runtimeId"`
}

type CommunicationRuntimeRecoveryResult struct {
	AgentID          string `json:"agentId"`
	RuntimeID        string `json:"runtimeId"`
	Generation       int    `json:"generation"`
	Deliveries       int    `json:"deliveries"`
	Operations       int    `json:"operations"`
	Receipts         int    `json:"receipts"`
	TodoLinks        int    `json:"todoLinks"`
	TodoSettlements  int    `json:"todoSettlements"`
	RecoveredAt      int64  `json:"recoveredAt"`
	AlreadyRecovered bool   `json:"alreadyRecovered"`
}

func (v CommunicationIdleState) Safe() bool {
	return v.DeliveredMessages == 0 && v.ActiveOperations == 0 && v.BusyRuntimes == 0
}

// CommunicationDrainState is the durable pre-cutover maintenance phase. The
// daemon rejects new sends and claims while old fenced completions drain.
func (s *Store) CommunicationDrainState(ctx context.Context) (generation int, draining bool, err error) {
	var value int
	err = s.db.QueryRowContext(ctx, `select pending_generation,draining from communication_protocol_state where singleton=1`).Scan(&generation, &value)
	return generation, value != 0, err
}

func (s *Store) CommunicationRecoveryPending(ctx context.Context) (bool, error) {
	var value int
	err := s.db.QueryRowContext(ctx, `select recovery_pending from communication_protocol_state where singleton=1`).Scan(&value)
	return value != 0, err
}

func (s *Store) MarkCommunicationRecoveryComplete(ctx context.Context, generation int) error {
	result, err := s.db.ExecContext(ctx, `update communication_protocol_state set recovery_pending=0,updated_at=? where singleton=1 and generation=? and cutover_complete=1 and maintenance=0 and recovery_pending=1`, time.Now().UnixMilli(), generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var pending int
		if err := s.db.QueryRowContext(ctx, `select recovery_pending from communication_protocol_state where singleton=1 and generation=? and cutover_complete=1 and maintenance=0`, generation).Scan(&pending); err != nil || pending != 0 {
			return fmt.Errorf("communication recovery is not ready to complete")
		}
	}
	return nil
}

func (s *Store) BeginCommunicationDrain(ctx context.Context, generation int) error {
	if generation <= 1 {
		return fmt.Errorf("new protocol generation is required")
	}
	result, err := s.db.ExecContext(ctx, `update communication_protocol_state set draining=1,pending_generation=?,updated_at=? where singleton=1 and cutover_complete=0 and maintenance=0 and (draining=0 or pending_generation=?)`, generation, time.Now().UnixMilli(), generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("communication drain could not start for generation %d", generation)
	}
	return nil
}

// PromoteCommunicationDrain atomically seals database writes after v1 work is
// idle. Backup and semantic migration run only after this transition.
func (s *Store) PromoteCommunicationDrain(ctx context.Context, generation int) error {
	result, err := s.db.ExecContext(ctx, `update communication_protocol_state set draining=0,maintenance=1,updated_at=? where singleton=1 and cutover_complete=0 and maintenance=0 and draining=1 and pending_generation=?`, time.Now().UnixMilli(), generation)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("communication drain is not ready for generation %d", generation)
	}
	return nil
}

// CommunicationSafeIdle reads all process-owned communication activity from
// the database. Agent runtime status is the authoritative Pi invocation state.
func (s *Store) CommunicationSafeIdle(ctx context.Context) (CommunicationIdleState, error) {
	var out CommunicationIdleState
	err := s.db.QueryRowContext(ctx, `select
  (select count(*) from agent_messages where status='delivered'),
  (select count(*) from agent_operations where state in ('claimed','running','settling')),
  (select count(*) from agents where runtime_id<>'' and status in ('starting','running'))`).Scan(
		&out.DeliveredMessages, &out.ActiveOperations, &out.BusyRuntimes,
	)
	return out, err
}

// CreateVerifiedCommunicationBackup creates a complete SQLite backup and
// verifies both integrity and the protocol state row. The returned path is for
// local terminal diagnostics only and must not be used in read projections.
func (s *Store) CreateVerifiedCommunicationBackup(ctx context.Context, generation int) (string, error) {
	if generation <= 1 {
		return "", fmt.Errorf("new protocol generation is required")
	}
	directory := filepath.Join(s.stateDir, "backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, fmt.Sprintf("communication-v2-generation-%d-%d.db", generation, time.Now().UnixMilli()))
	quoted := strings.ReplaceAll(path, "'", "''")
	if _, err := s.db.ExecContext(ctx, "vacuum into '"+quoted+"'"); err != nil {
		return "", fmt.Errorf("create communication backup: %w", err)
	}
	verified := false
	defer func() {
		if !verified {
			_ = os.Remove(path)
		}
	}()
	if err := verifyCommunicationBackup(ctx, path, generation); err != nil {
		return "", err
	}
	verified = true
	return path, nil
}

// VerifiedCommunicationBackup finds and verifies a pre-migration backup from
// an earlier upgrade attempt. It never treats a post-cutover database as a
// migration backup.
func (s *Store) VerifiedCommunicationBackup(ctx context.Context, generation int) (bool, error) {
	pattern := filepath.Join(s.stateDir, "backups", fmt.Sprintf("communication-v2-generation-%d-*.db", generation))
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return false, err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, path := range paths {
		if err := verifyCommunicationBackup(ctx, path, generation); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func verifyCommunicationBackup(ctx context.Context, path string, generation int) error {
	backup, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return err
	}
	defer func() { _ = backup.Close() }()
	var integrity string
	if err := backup.QueryRowContext(ctx, `pragma integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("verify communication backup: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("verify communication backup: integrity check returned %q", integrity)
	}
	var current, pending, complete, maintenance int
	if err := backup.QueryRowContext(ctx, `select generation,pending_generation,cutover_complete,maintenance from communication_protocol_state where singleton=1`).Scan(&current, &pending, &complete, &maintenance); err != nil {
		return fmt.Errorf("verify communication backup: %w", err)
	}
	if current >= generation || pending != generation || complete != 0 || maintenance != 1 {
		return fmt.Errorf("verify communication backup: protocol state is not the pre-migration generation %d state", generation)
	}
	return nil
}

func (s *Store) AgentRuntimeProtocolGenerationMatches(ctx context.Context, agentID, runtimeID string, generation int) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from agent_runtime_protocol_generations where agent_id=? and runtime_id=? and generation=?`, agentID, runtimeID, generation).Scan(&count)
	return count == 1, err
}

// ReconcileAgentOperationOwnership returns only caller-supplied operation IDs
// that still belong to the agent and have a daemon-authoritative nonterminal
// state. Input order is preserved. Missing, foreign, and terminal operations
// are intentionally indistinguishable to the Pi-local caller.
func (s *Store) ReconcileAgentOperationOwnership(ctx context.Context, agentID string, operationIDs []string) ([]string, error) {
	if len(operationIDs) == 0 {
		return []string{}, nil
	}
	marks := make([]string, len(operationIDs))
	args := make([]any, 0, len(operationIDs)+1)
	args = append(args, agentID)
	for index, id := range operationIDs {
		marks[index] = "?"
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `select id from agent_operations where agent_id=? and state in ('ready','claimed','running','waiting','settling') and id in (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ownedSet := make(map[string]struct{}, len(operationIDs))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ownedSet[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	owned := make([]string, 0, len(ownedSet))
	for _, id := range operationIDs {
		if _, ok := ownedSet[id]; ok {
			owned = append(owned, id)
		}
	}
	return owned, nil
}

// RegisteredCommunicationRuntimeCount returns bounded barrier progress.
func (s *Store) RegisteredCommunicationRuntimeCount(ctx context.Context, generation int) (running, registered int, err error) {
	err = s.db.QueryRowContext(ctx, `select
  (select count(*) from agents where runtime_id<>''),
  (select count(*) from agents join agent_runtime_protocol_generations registration on registration.agent_id=agents.id and registration.runtime_id=agents.runtime_id where agents.runtime_id<>'' and registration.generation=?)`, generation).Scan(&running, &registered)
	return
}

// UnregisteredCommunicationRuntimes returns a bounded set of exact durable
// identities for operator diagnostics. It does not inspect operating-system
// processes or renderer views.
func (s *Store) UnregisteredCommunicationRuntimes(ctx context.Context, generation, limit int) (expected, registered int, values []CommunicationUnregisteredRuntime, omitted int, err error) {
	if limit < 1 {
		return 0, 0, nil, 0, fmt.Errorf("runtime diagnostic limit must be positive")
	}
	expected, registered, err = s.RegisteredCommunicationRuntimeCount(ctx, generation)
	if err != nil {
		return 0, 0, nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `select id,title,runtime_id from agents where runtime_id<>'' and not exists (
  select 1 from agent_runtime_protocol_generations registration
  where registration.agent_id=agents.id and registration.runtime_id=agents.runtime_id and registration.generation=?
) order by lower(title),id limit ?`, generation, limit+1)
	if err != nil {
		return 0, 0, nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value CommunicationUnregisteredRuntime
		if err := rows.Scan(&value.AgentID, &value.AgentTitle, &value.RuntimeID); err != nil {
			return 0, 0, nil, 0, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, nil, 0, err
	}
	missing := expected - registered
	if len(values) > limit {
		values = values[:limit]
	}
	if missing > len(values) {
		omitted = missing - len(values)
	}
	return expected, registered, values, omitted, nil
}

// RecoverUnregisteredCommunicationRuntime is the bounded operator recovery for
// one exact stale runtime at the generation registration barrier. It requeues
// only work fenced by both the agent and runtime IDs. The audit row makes an
// exact retry idempotent.
func (s *Store) RecoverUnregisteredCommunicationRuntime(ctx context.Context, agentID, runtimeID string) (CommunicationRuntimeRecoveryResult, error) {
	out := CommunicationRuntimeRecoveryResult{AgentID: agentID, RuntimeID: runtimeID}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	generation, _, complete, maintenance, err := protocolStateDetails(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Generation = generation
	err = tx.QueryRowContext(ctx, `select deliveries,operations,receipts,todo_links,todo_settlements,recovered_at from communication_runtime_recoveries where agent_id=? and runtime_id=? and generation=?`, agentID, runtimeID, generation).Scan(
		&out.Deliveries, &out.Operations, &out.Receipts, &out.TodoLinks, &out.TodoSettlements, &out.RecoveredAt,
	)
	if err == nil {
		out.AlreadyRecovered = true
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if !complete || !maintenance {
		return out, fmt.Errorf("communication runtime recovery requires the active registration barrier")
	}
	var title string
	if err := tx.QueryRowContext(ctx, `select title from agents where id=? and runtime_id=?`, agentID, runtimeID).Scan(&title); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, fmt.Errorf("agent and runtime identity do not match")
		}
		return out, err
	}
	var registered int
	if err := tx.QueryRowContext(ctx, `select count(*) from agent_runtime_protocol_generations where agent_id=? and runtime_id=? and generation=?`, agentID, runtimeID, generation).Scan(&registered); err != nil {
		return out, err
	}
	if registered != 0 {
		return out, fmt.Errorf("runtime is registered for communication protocol generation %d", generation)
	}
	writer := "runtime_recovery"
	writerResult, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer=?,updated_at=? where singleton=1 and generation=? and cutover_complete=1 and maintenance=1 and maintenance_writer=''`, writer, time.Now().UnixMilli(), generation)
	if err != nil {
		return out, err
	}
	if count, err := writerResult.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("communication maintenance has another internal writer")
	}
	now := time.Now().UnixMilli()
	out.RecoveredAt = now
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='recovered',terminal_reason='operator_runtime_recovery',finished_at=?,updated_at=? where runtime_id=? and state in ('claimed','running') and exists(select 1 from agent_operations operation where operation.id=agent_operation_attempts.operation_id and operation.agent_id=?)`, now, now, runtimeID, agentID); err != nil {
		return out, err
	}
	result, err := tx.ExecContext(ctx, `update agent_operations set state='ready',runtime_id='',claim_key='',lease_expires_at=0,last_error='operator recovered stale runtime ownership',updated_at=? where agent_id=? and runtime_id=? and state in ('claimed','running','settling')`, now, agentID, runtimeID)
	if err != nil {
		return out, err
	}
	if out.Operations, err = rowsAffected(result); err != nil {
		return out, err
	}
	result, err = tx.ExecContext(ctx, `update agent_messages set status='queued',notification_state=case when kind='result' then 'pending' else notification_state end,terminal_reason='',runtime_id='',claim_key='',lease_expires_at=0,last_error='operator recovered stale runtime delivery',updated_at=? where target_agent_id=? and runtime_id=? and status='delivered'`, now, agentID, runtimeID)
	if err != nil {
		return out, err
	}
	if out.Deliveries, err = rowsAffected(result); err != nil {
		return out, err
	}
	result, err = tx.ExecContext(ctx, `update agent_inbox_receipts set state='pending',runtime_id='',claim_key='',pi_tool_request_id='',lease_expires_at=0,operation_attempt=0,updated_at=? where agent_id=? and runtime_id=? and state in ('claimed','presented')`, now, agentID, runtimeID)
	if err != nil {
		return out, err
	}
	if out.Receipts, err = rowsAffected(result); err != nil {
		return out, err
	}
	result, err = tx.ExecContext(ctx, `update todo_link_intents set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='operator recovered stale runtime TODO link' where runtime_id=? and state='pending' and (exists(select 1 from agent_operations operation where operation.id=todo_link_intents.operation_id and operation.agent_id=?) or operation_id is null and exists(select 1 from agent_messages message where message.id=todo_link_intents.message_id and message.sender_agent_id=?))`, runtimeID, agentID, agentID)
	if err != nil {
		return out, err
	}
	if out.TodoLinks, err = rowsAffected(result); err != nil {
		return out, err
	}
	result, err = tx.ExecContext(ctx, `update todo_settlement_events set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='operator recovered stale runtime TODO settlement' where agent_id=? and runtime_id=? and state in ('pending','applied') and acknowledged_at=0`, agentID, runtimeID)
	if err != nil {
		return out, err
	}
	if out.TodoSettlements, err = rowsAffected(result); err != nil {
		return out, err
	}
	agentResult, err := tx.ExecContext(ctx, `update agents set status='stopped',runtime_id='',last_error='',updated_at=? where id=? and runtime_id=?`, now, agentID, runtimeID)
	if err != nil {
		return out, err
	}
	if count, err := agentResult.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("agent and runtime identity changed during recovery")
	}
	if _, err := tx.ExecContext(ctx, `delete from agent_runtime_launches where agent_id=? and runtime_id=?`, agentID, runtimeID); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `delete from agent_runtime_protocol_generations where agent_id=? and runtime_id=?`, agentID, runtimeID); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `insert into communication_runtime_recoveries(agent_id,runtime_id,generation,deliveries,operations,receipts,todo_links,todo_settlements,recovered_at) values(?,?,?,?,?,?,?,?,?)`, agentID, runtimeID, generation, out.Deliveries, out.Operations, out.Receipts, out.TodoLinks, out.TodoSettlements, now); err != nil {
		return out, err
	}
	clearResult, err := tx.ExecContext(ctx, `update communication_protocol_state set maintenance_writer='',updated_at=? where singleton=1 and maintenance_writer=?`, now, writer)
	if err != nil {
		return out, err
	}
	if count, err := clearResult.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("communication maintenance writer could not be cleared")
	}
	return out, tx.Commit()
}

func rowsAffected(result sql.Result) (int, error) {
	count, err := result.RowsAffected()
	return int(count), err
}
