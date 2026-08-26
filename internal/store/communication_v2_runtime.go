package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
	backup, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return "", err
	}
	defer func() { _ = backup.Close() }()
	var integrity string
	if err := backup.QueryRowContext(ctx, `pragma integrity_check`).Scan(&integrity); err != nil {
		return "", fmt.Errorf("verify communication backup: %w", err)
	}
	if integrity != "ok" {
		return "", fmt.Errorf("verify communication backup: integrity check returned %q", integrity)
	}
	var rows int
	if err := backup.QueryRowContext(ctx, `select count(*) from communication_protocol_state where singleton=1`).Scan(&rows); err != nil || rows != 1 {
		if err == nil {
			err = fmt.Errorf("protocol state row is missing")
		}
		return "", fmt.Errorf("verify communication backup: %w", err)
	}
	verified = true
	return path, nil
}

func (s *Store) AgentRuntimeProtocolGenerationMatches(ctx context.Context, agentID, runtimeID string, generation int) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from agent_runtime_protocol_generations where agent_id=? and runtime_id=? and generation=?`, agentID, runtimeID, generation).Scan(&count)
	return count == 1, err
}

// RegisteredCommunicationRuntimeCount returns bounded barrier progress.
func (s *Store) RegisteredCommunicationRuntimeCount(ctx context.Context, generation int) (running, registered int, err error) {
	err = s.db.QueryRowContext(ctx, `select
  (select count(*) from agents where runtime_id<>''),
  (select count(*) from agents join agent_runtime_protocol_generations registration on registration.agent_id=agents.id and registration.runtime_id=agents.runtime_id where agents.runtime_id<>'' and registration.generation=?)`, generation).Scan(&running, &registered)
	return
}
