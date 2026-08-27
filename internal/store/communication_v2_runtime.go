package store

import (
	"context"
	"database/sql"
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
