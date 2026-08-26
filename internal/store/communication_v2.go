package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/matipan/galpon/internal/model"
)

const coordinationLease = 2 * time.Minute

// CoordinationSendAdmission is the complete durable input for one protocol v2
// send. Admission writes the message, target receipt, join, TODO intent, and
// idempotency record in one transaction.
type CoordinationSendAdmission struct {
	Message            model.AgentMessage
	SourceOperation    string
	OperationAttempt   int
	RuntimeID          string
	ProtocolGeneration int
	TodoID             int64
	TodoPolicy         string
	JoinDeadlineAt     int64
}

type AgentOperationSettleResult struct {
	Operation model.AgentOperation
	Parked    bool
}

func (s *Store) migrateCommunicationV2() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	const schema = `
create table if not exists agent_operations (
  id text primary key,
  agent_id text not null references agents(id) on delete cascade,
  kind text not null check(kind in ('direct','inbound')),
  state text not null check(state in ('ready','claimed','running','waiting','settling','settled','failed','canceled','expired')),
  parent_message_id text not null default '',
  causal_run_id text not null,
  user_entry_id text not null default '',
  runtime_id text not null default '',
  claim_key text not null default '',
  attempt integer not null default 0 check(attempt >= 0),
  claimed_at integer not null default 0,
  lease_expires_at integer not null default 0,
  deadline_at integer not null default 0,
  terminal_reason text not null default '',
  last_error text not null default '',
  created_at integer not null,
  updated_at integer not null,
  settled_at integer not null default 0,
  completion_digest text not null default '',
  protocol_generation integer not null default 1 check(protocol_generation > 0)
);
create unique index if not exists agent_operations_user_entry on agent_operations(agent_id,user_entry_id) where user_entry_id<>'';
create unique index if not exists agent_operations_claim on agent_operations(agent_id,claim_key) where claim_key<>'';
create unique index if not exists agent_operations_parent_message on agent_operations(parent_message_id) where parent_message_id<>'';
create index if not exists agent_operations_ready on agent_operations(agent_id,state,created_at,id);
create table if not exists agent_operation_attempts (
  id text primary key,
  operation_id text not null references agent_operations(id) on delete cascade,
  attempt integer not null check(attempt > 0),
  runtime_id text not null,
  claim_key text not null default '',
  state text not null check(state in ('claimed','running','parked','settled','failed','recovered')),
  started_at integer not null,
  updated_at integer not null,
  finished_at integer not null default 0,
  terminal_reason text not null default '',
  unique(operation_id,attempt)
);
create table if not exists agent_message_results (
  id text primary key,
  message_id text not null unique references agent_messages(id) on delete cascade,
  status text not null check(status in ('completed','failed','canceled','expired')),
  response text not null default '',
  error text not null default '',
  terminal_reason text not null default '',
  legacy_state text not null default '' check(legacy_state in ('','legacy_suppressed_unknown')),
  created_at integer not null,
  protocol_generation integer not null default 1 check(protocol_generation > 0)
);
create trigger if not exists agent_message_results_immutable_update before update on agent_message_results begin
  select raise(abort,'message results are immutable');
end;
create table if not exists agent_inbox_receipts (
  id text primary key,
  agent_id text not null references agents(id) on delete cascade,
  operation_id text references agent_operations(id) on delete cascade,
  message_id text references agent_messages(id),
  result_id text references agent_message_results(id) on delete cascade,
  kind text not null check(kind in ('request','result','blocker','control')),
  state text not null check(state in ('pending','claimed','presented','acknowledged','abandoned')),
  eligible integer not null default 1 check(eligible in (0,1)),
  runtime_id text not null default '',
  claim_key text not null default '',
  pi_tool_request_id text not null default '',
  attempt integer not null default 0 check(attempt >= 0),
  operation_attempt integer not null default 0 check(operation_attempt >= 0),
  claimed_at integer not null default 0,
  lease_expires_at integer not null default 0,
  presented_at integer not null default 0,
  acknowledged_at integer not null default 0,
  abandoned_at integer not null default 0,
  created_at integer not null,
  updated_at integer not null,
  protocol_generation integer not null default 1 check(protocol_generation > 0)
);
create index if not exists agent_inbox_receipts_pending on agent_inbox_receipts(agent_id,state,eligible,created_at,id);
create unique index if not exists agent_inbox_receipts_agent_claim on agent_inbox_receipts(agent_id,claim_key) where claim_key<>'';
create index if not exists agent_inbox_receipts_operation_tool on agent_inbox_receipts(operation_id,pi_tool_request_id) where pi_tool_request_id<>'';
create table if not exists agent_operation_joins (
  id text primary key,
  operation_id text not null references agent_operations(id) on delete cascade,
  message_id text not null references agent_messages(id) on delete cascade,
  state text not null check(state in ('open','ready','acknowledged','failed','expired','detached','canceled')),
  deadline_at integer not null default 0,
  failure text not null default '',
  created_at integer not null,
  updated_at integer not null,
  resolved_at integer not null default 0,
  protocol_generation integer not null default 1 check(protocol_generation > 0),
  unique(operation_id,message_id)
);
create index if not exists agent_operation_joins_message_state on agent_operation_joins(message_id,state,operation_id);
create table if not exists agent_pi_local_events (
  id text primary key,
  agent_id text not null references agents(id) on delete cascade,
  operation_id text references agent_operations(id) on delete cascade,
  operation_attempt integer not null default 0 check(operation_attempt >= 0),
  event_id text not null,
  kind text not null,
  state text not null check(state in ('pending','acknowledged')),
  payload text not null default '',
  created_at integer not null,
  acknowledged_at integer not null default 0,
  protocol_generation integer not null default 1,
  unique(agent_id,event_id)
);
create table if not exists coordination_message_meta (
  message_id text primary key references agent_messages(id) on delete cascade,
  source_operation_id text not null default '',
  request_hash text not null,
  created_at integer not null
);
create table if not exists coordination_send_receipts (
  sender_agent_id text not null,
  idempotency_key text not null,
  request_hash text not null,
  message_id text not null references agent_messages(id) on delete cascade,
  created_at integer not null,
  primary key(sender_agent_id,idempotency_key)
);
create table if not exists todo_link_intents (
  id text primary key,
  message_id text not null unique references agent_messages(id) on delete cascade,
  operation_id text references agent_operations(id) on delete cascade,
  todo_id integer not null check(todo_id > 0),
  policy text not null check(policy in ('complete_on_success','annotate')),
  state text not null check(state in ('pending','applied','failed')),
  runtime_id text not null default '',
  claim_key text not null default '',
  attempt integer not null default 0,
  operation_attempt integer not null default 0,
  lease_expires_at integer not null default 0,
  last_error text not null default '',
  created_at integer not null,
  applied_at integer not null default 0,
  protocol_generation integer not null default 1 check(protocol_generation > 0)
);
create table if not exists todo_settlement_events (
  id text primary key,
  intent_id text not null unique references todo_link_intents(id) on delete cascade,
  result_id text not null references agent_message_results(id) on delete cascade,
  agent_id text not null references agents(id),
  operation_id text references agent_operations(id),
  state text not null check(state in ('pending','applied','failed')),
  snapshot text not null default '',
  runtime_id text not null default '',
  claim_key text not null default '',
  attempt integer not null default 0,
  operation_attempt integer not null default 0,
  lease_expires_at integer not null default 0,
  last_error text not null default '',
  created_at integer not null,
  applied_at integer not null default 0,
  acknowledged_at integer not null default 0,
  protocol_generation integer not null default 1 check(protocol_generation > 0)
);
create table if not exists communication_protocol_state (
  singleton integer primary key check(singleton=1),
  generation integer not null check(generation > 0),
  cutover_complete integer not null default 0 check(cutover_complete in (0,1)),
  maintenance integer not null default 0 check(maintenance in (0,1)),
  pending_generation integer not null default 0 check(pending_generation >= 0),
  maintenance_writer text not null default '',
  updated_at integer not null
);
insert or ignore into communication_protocol_state(singleton,generation,updated_at) values(1,1,0);
create table if not exists agent_runtime_protocol_generations (
  agent_id text not null references agents(id) on delete cascade,
  runtime_id text not null,
  generation integer not null check(generation > 0),
  registered_at integer not null,
  primary key(agent_id,runtime_id)
);
`
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	for _, column := range []struct{ table, name, definition string }{
		{"agent_operations", "completion_digest", "text not null default ''"},
		{"agent_operations", "protocol_generation", "integer not null default 1"},
		{"agent_message_results", "protocol_generation", "integer not null default 1"},
		{"agent_inbox_receipts", "protocol_generation", "integer not null default 1"},
		{"agent_operation_joins", "protocol_generation", "integer not null default 1"},
		{"agent_pi_local_events", "protocol_generation", "integer not null default 1"},
		{"todo_link_intents", "runtime_id", "text not null default ''"},
		{"todo_link_intents", "claim_key", "text not null default ''"},
		{"todo_link_intents", "attempt", "integer not null default 0"},
		{"todo_link_intents", "operation_attempt", "integer not null default 0"},
		{"todo_link_intents", "lease_expires_at", "integer not null default 0"},
		{"todo_link_intents", "last_error", "text not null default ''"},
		{"todo_link_intents", "protocol_generation", "integer not null default 1"},
		{"todo_settlement_events", "agent_id", "text not null default ''"},
		{"todo_settlement_events", "operation_id", "text not null default ''"},
		{"todo_settlement_events", "runtime_id", "text not null default ''"},
		{"todo_settlement_events", "claim_key", "text not null default ''"},
		{"todo_settlement_events", "attempt", "integer not null default 0"},
		{"todo_settlement_events", "operation_attempt", "integer not null default 0"},
		{"todo_settlement_events", "lease_expires_at", "integer not null default 0"},
		{"todo_settlement_events", "last_error", "text not null default ''"},
		{"todo_settlement_events", "acknowledged_at", "integer not null default 0"},
		{"todo_settlement_events", "protocol_generation", "integer not null default 1"},
		{"communication_protocol_state", "pending_generation", "integer not null default 0"},
		{"communication_protocol_state", "maintenance_writer", "text not null default ''"},
	} {
		if err := ensureTxColumn(tx, column.table, column.name, column.definition); err != nil {
			return err
		}
	}
	for _, table := range []string{"agent_messages", "agent_operations", "agent_operation_attempts", "agent_message_results", "agent_inbox_receipts", "agent_operation_joins", "agent_pi_local_events", "coordination_message_meta", "coordination_send_receipts", "todo_link_intents", "todo_settlement_events"} {
		for _, action := range []string{"insert", "update", "delete"} {
			name := table + "_maintenance_" + action
			statement := fmt.Sprintf(`create trigger if not exists %s before %s on %s when exists(select 1 from communication_protocol_state where singleton=1 and maintenance=1 and maintenance_writer='') begin select raise(abort,'communication protocol is in maintenance mode'); end`, name, action, table)
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`update todo_settlement_events set agent_id=coalesce((select operation.agent_id from todo_link_intents intent join agent_operations operation on operation.id=intent.operation_id where intent.id=todo_settlement_events.intent_id),(select message.sender_agent_id from todo_link_intents intent join agent_messages message on message.id=intent.message_id where intent.id=todo_settlement_events.intent_id),'') where agent_id=''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
create trigger if not exists agent_inbox_receipts_message_insert before insert on agent_inbox_receipts
when new.message_id<>'' and not exists(select 1 from agent_messages where id=new.message_id) begin select raise(abort,'unknown receipt message'); end;
create trigger if not exists agent_inbox_receipts_message_update before update of message_id on agent_inbox_receipts
when new.message_id<>'' and not exists(select 1 from agent_messages where id=new.message_id) begin select raise(abort,'unknown receipt message'); end;
create trigger if not exists coordination_meta_operation_insert before insert on coordination_message_meta
when new.source_operation_id<>'' and not exists(select 1 from agent_operations where id=new.source_operation_id) begin select raise(abort,'unknown source operation'); end;
create trigger if not exists agent_operations_parent_insert before insert on agent_operations
when new.parent_message_id<>'' and not exists(select 1 from agent_messages where id=new.parent_message_id and target_agent_id=new.agent_id and run_id=new.causal_run_id) begin select raise(abort,'invalid operation parent message'); end;
create trigger if not exists coordination_send_sender_insert before insert on coordination_send_receipts
when new.sender_agent_id<>'' and not exists(select 1 from agents where id=new.sender_agent_id) begin select raise(abort,'unknown send receipt sender'); end;
create trigger if not exists todo_event_agent_insert before insert on todo_settlement_events
when new.agent_id<>'' and not exists(select 1 from agents where id=new.agent_id) begin select raise(abort,'unknown TODO event agent'); end;
create trigger if not exists agent_inbox_receipts_operation_insert before insert on agent_inbox_receipts
when new.operation_id is not null and not exists(select 1 from agent_operations where id=new.operation_id and agent_id=new.agent_id) begin select raise(abort,'receipt operation agent mismatch'); end;
create trigger if not exists agent_inbox_receipts_operation_update before update of operation_id on agent_inbox_receipts
when new.operation_id is not null and not exists(select 1 from agent_operations where id=new.operation_id and agent_id=new.agent_id) begin select raise(abort,'receipt operation agent mismatch'); end;
create trigger if not exists agent_inbox_receipts_result_insert before insert on agent_inbox_receipts
when new.result_id is not null and not exists(select 1 from agent_message_results where id=new.result_id and message_id=new.message_id) begin select raise(abort,'receipt result message mismatch'); end;
create trigger if not exists agent_operation_joins_target_insert before insert on agent_operation_joins
when new.state='open' and not exists(select 1 from agent_operations where parent_message_id=new.message_id) begin select raise(abort,'open join has no target operation'); end;
create trigger if not exists coordination_meta_causal_insert before insert on coordination_message_meta
when new.source_operation_id<>'' and not exists(select 1 from agent_operations operation join agent_messages message on message.id=new.message_id where operation.id=new.source_operation_id and operation.causal_run_id=message.run_id) begin select raise(abort,'coordination metadata causal mismatch'); end;
create trigger if not exists coordination_send_hash_insert before insert on coordination_send_receipts
when not exists(select 1 from coordination_message_meta where message_id=new.message_id and request_hash=new.request_hash) begin select raise(abort,'send receipt metadata mismatch'); end;
create trigger if not exists todo_event_result_insert before insert on todo_settlement_events
when not exists(select 1 from todo_link_intents intent join agent_message_results result on result.message_id=intent.message_id where intent.id=new.intent_id and result.id=new.result_id) begin select raise(abort,'TODO event result mismatch'); end;
create trigger if not exists agent_messages_coordination_delete before delete on agent_messages
when exists(select 1 from agent_operations where parent_message_id=old.id)
  or exists(select 1 from agent_message_results where message_id=old.id)
  or exists(select 1 from agent_inbox_receipts where message_id=old.id)
  or exists(select 1 from agent_operation_joins where message_id=old.id)
  or exists(select 1 from coordination_message_meta where message_id=old.id)
  or exists(select 1 from coordination_send_receipts where message_id=old.id)
  or exists(select 1 from todo_link_intents where message_id=old.id)
begin select raise(abort,'delete coordination state before its message'); end;
`); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureTxColumn(tx *sql.Tx, table, name, definition string) error {
	rows, err := tx.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var columnName, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		found = found || columnName == name
	}
	if err := rows.Close(); err != nil || found {
		return err
	}
	_, err = tx.Exec(`alter table ` + table + ` add column ` + name + ` ` + definition)
	return err
}

func normalizeOperation(value model.AgentOperation) model.AgentOperation {
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.Kind == "" {
		value.Kind = "direct"
	}
	if value.State == "" {
		value.State = "ready"
	}
	if value.CausalRunID == "" {
		value.CausalRunID = value.ID
	}
	if value.CreatedAt == 0 {
		value.CreatedAt = time.Now().UnixMilli()
	}
	if value.UpdatedAt == 0 {
		value.UpdatedAt = value.CreatedAt
	}
	return value
}

const operationColumns = `id,agent_id,kind,state,parent_message_id,causal_run_id,user_entry_id,runtime_id,claim_key,attempt,claimed_at,lease_expires_at,deadline_at,terminal_reason,last_error,created_at,updated_at,settled_at,completion_digest,protocol_generation`

func operationValues(value model.AgentOperation) []any {
	return []any{value.ID, value.AgentID, value.Kind, value.State, value.ParentMessageID, value.CausalRunID, value.UserEntryID, value.RuntimeID, value.ClaimKey, value.Attempt, value.ClaimedAt, value.LeaseExpiresAt, value.DeadlineAt, value.TerminalReason, value.LastError, value.CreatedAt, value.UpdatedAt, value.SettledAt, value.CompletionDigest, value.ProtocolGeneration}
}

func scanOperation(row rowScanner) (model.AgentOperation, error) {
	var value model.AgentOperation
	err := row.Scan(&value.ID, &value.AgentID, &value.Kind, &value.State, &value.ParentMessageID, &value.CausalRunID, &value.UserEntryID, &value.RuntimeID, &value.ClaimKey, &value.Attempt, &value.ClaimedAt, &value.LeaseExpiresAt, &value.DeadlineAt, &value.TerminalReason, &value.LastError, &value.CreatedAt, &value.UpdatedAt, &value.SettledAt, &value.CompletionDigest, &value.ProtocolGeneration)
	return value, err
}

func (s *Store) PutAgentOperation(ctx context.Context, value model.AgentOperation) (model.AgentOperation, error) {
	value = normalizeOperation(value)
	if value.AgentID == "" || (value.Kind != "direct" && value.Kind != "inbound") || value.State != "ready" {
		return model.AgentOperation{}, fmt.Errorf("operation has invalid initial fields")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AgentOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	value.ProtocolGeneration, err = requireObjectGeneration(ctx, tx, value.ProtocolGeneration)
	if err != nil {
		return model.AgentOperation{}, err
	}
	_, err = tx.ExecContext(ctx, `insert into agent_operations(`+operationColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationValues(value)...)
	if err == nil {
		return value, tx.Commit()
	}
	if value.UserEntryID == "" {
		return model.AgentOperation{}, err
	}
	existing, readErr := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where agent_id=? and user_entry_id=?`, value.AgentID, value.UserEntryID))
	if readErr != nil {
		return model.AgentOperation{}, err
	}
	if existing.Kind != value.Kind || existing.ParentMessageID != value.ParentMessageID || existing.CausalRunID != value.CausalRunID || existing.ProtocolGeneration != value.ProtocolGeneration {
		return model.AgentOperation{}, fmt.Errorf("operation user entry ID was already used for different work")
	}
	return existing, tx.Commit()
}

func (s *Store) AgentOperation(ctx context.Context, id string) (model.AgentOperation, error) {
	return scanOperation(s.db.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where id=?`, id))
}

// ClaimReadyOperation claims or resumes one operation. The claim key makes an
// exact retry return the same fenced attempt.
func (s *Store) ClaimAgentOperation(ctx context.Context, agentID, runtimeID, claimKey string) (*model.AgentOperation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if claimKey != "" {
		value, lookupErr := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where agent_id=? and claim_key=?`, agentID, claimKey))
		if lookupErr == nil {
			if value.RuntimeID != runtimeID || (value.State != "claimed" && value.State != "running") || value.LeaseExpiresAt <= now || value.DeadlineAt > 0 && value.DeadlineAt <= now {
				return nil, sql.ErrNoRows
			}
			if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, value.ProtocolGeneration); err != nil {
				return nil, err
			}
			return &value, nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, lookupErr
		}
	}
	if err := recoverExpiredCoordinationLeases(ctx, tx, now); err != nil {
		return nil, err
	}
	value, err := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations operation where agent_id=? and state='ready' and (deadline_at=0 or deadline_at>?) and not exists(select 1 from agent_inbox_receipts receipt where receipt.operation_id=operation.id and receipt.kind='request' and receipt.eligible=0) order by created_at,id limit 1`, agentID, now))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, value.ProtocolGeneration); err != nil {
		return nil, err
	}
	lease := now + coordinationLease.Milliseconds()
	result, err := tx.ExecContext(ctx, `update agent_operations set state='claimed',runtime_id=?,claim_key=?,attempt=attempt+1,claimed_at=?,lease_expires_at=?,last_error='',updated_at=? where id=? and state='ready'`, runtimeID, claimKey, now, lease, now, value.ID)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return nil, err
	}
	attempt := value.Attempt + 1
	if _, err := tx.ExecContext(ctx, `insert into agent_operation_attempts(id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at) values(?,?,?,?,?,'claimed',?,?)`, fmt.Sprintf("%s:%d", value.ID, attempt), value.ID, attempt, runtimeID, claimKey, now, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='claimed',runtime_id=?,claim_key=?,attempt=attempt+1,operation_attempt=?,claimed_at=?,lease_expires_at=?,updated_at=? where operation_id=? and kind='request' and state='pending' and eligible=1`, runtimeID, claimKey, attempt, now, lease, now, value.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	value.State, value.RuntimeID, value.ClaimKey = "claimed", runtimeID, claimKey
	value.Attempt++
	value.ClaimedAt, value.LeaseExpiresAt, value.UpdatedAt = now, lease, now
	return &value, nil
}

func (s *Store) StartAgentOperation(ctx context.Context, id, agentID, runtimeID string, attempt int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := fenceOperationMutation(ctx, tx, id, agentID, runtimeID, attempt); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `update agent_operations set state='running',updated_at=? where id=? and agent_id=? and runtime_id=? and attempt=? and state='claimed' and lease_expires_at>?`, now, id, agentID, runtimeID, attempt, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='running',updated_at=? where operation_id=? and attempt=? and runtime_id=? and state='claimed'`, now, id, attempt, runtimeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='presented',presented_at=?,updated_at=? where operation_id=? and kind='request' and state='claimed' and runtime_id=? and operation_attempt=?`, now, now, id, runtimeID, attempt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RenewAgentOperationLease(ctx context.Context, id, agentID, runtimeID string, attempt int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := fenceOperationMutation(ctx, tx, id, agentID, runtimeID, attempt); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `update agent_operations set lease_expires_at=?,updated_at=? where id=? and agent_id=? and runtime_id=? and attempt=? and state in ('claimed','running') and lease_expires_at>? and (deadline_at=0 or deadline_at>?)`, now+coordinationLease.Milliseconds(), now, id, agentID, runtimeID, attempt, now, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// ParkAgentOperation releases runtime ownership while an operation waits.
func (s *Store) ParkAgentOperation(ctx context.Context, id, agentID, runtimeID string, attempt int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := fenceOperationMutation(ctx, tx, id, agentID, runtimeID, attempt); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `update agent_operations set state='waiting',runtime_id='',claim_key='',lease_expires_at=0,updated_at=? where id=? and agent_id=? and runtime_id=? and attempt=? and state in ('claimed','running') and lease_expires_at>? and exists (select 1 from agent_operation_joins where operation_id=? and state='open')`, now, id, agentID, runtimeID, attempt, now, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='parked',finished_at=?,updated_at=? where operation_id=? and attempt=? and runtime_id=? and state in ('claimed','running')`, now, now, id, attempt, runtimeID); err != nil {
		return err
	}
	return tx.Commit()
}

// FailAgentOperation records a fenced terminal failure.
func (s *Store) FailAgentOperation(ctx context.Context, id, agentID, runtimeID string, attempt int, failure string) (AgentOperationSettleResult, error) {
	if strings.TrimSpace(failure) == "" {
		return AgentOperationSettleResult{}, fmt.Errorf("operation failure is required")
	}
	return s.SettleAgentOperation(ctx, id, agentID, runtimeID, attempt, "", failure)
}

func (s *Store) AgentOperationAttempt(ctx context.Context, operationID string, attempt int) (model.AgentOperationAttempt, error) {
	var value model.AgentOperationAttempt
	err := s.db.QueryRowContext(ctx, `select id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at,finished_at,terminal_reason from agent_operation_attempts where operation_id=? and attempt=?`, operationID, attempt).Scan(&value.ID, &value.OperationID, &value.Attempt, &value.RuntimeID, &value.ClaimKey, &value.State, &value.StartedAt, &value.UpdatedAt, &value.FinishedAt, &value.TerminalReason)
	return value, err
}

func fenceOperationMutation(ctx context.Context, tx *sql.Tx, id, agentID, runtimeID string, attempt int) (model.AgentOperation, error) {
	value, err := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where id=? and agent_id=?`, id, agentID))
	if err != nil {
		return value, err
	}
	if value.RuntimeID != runtimeID || value.Attempt != attempt || (value.State != "claimed" && value.State != "running") || value.LeaseExpiresAt <= time.Now().UnixMilli() || value.DeadlineAt > 0 && value.DeadlineAt <= time.Now().UnixMilli() {
		return value, sql.ErrNoRows
	}
	if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, value.ProtocolGeneration); err != nil {
		return value, err
	}
	return value, nil
}

func operationCompletionDigest(status, response, failure string) string {
	sum := sha256.Sum256([]byte(status + "\x00" + response + "\x00" + failure))
	return hex.EncodeToString(sum[:])
}

func coordinationRequestHash(value model.AgentMessage, sourceOperation string, todoID int64, todoPolicy string, deadline int64) (string, error) {
	input := struct {
		Sender, Target, Kind, Act, Mode, ReplyTo, Parent, Root, Run, Prompt, Source, Policy string
		Depth                                                                               int
		TodoID, Deadline, QueueDeadline, ProcessingDeadline                                 int64
		Images                                                                              *[]model.ImageAttachment
	}{
		Sender: value.SenderAgentID, Target: value.TargetAgentID, Kind: value.Kind, Act: value.Act,
		Mode: value.ResultMode, ReplyTo: value.ReplyTo, Parent: value.ParentMessageID, Root: value.RootMessageID,
		Run: value.RunID, Prompt: value.Prompt, Source: sourceOperation, Policy: todoPolicy, Depth: value.Depth,
		TodoID: todoID, Deadline: deadline, QueueDeadline: value.QueueDeadlineAt,
		ProcessingDeadline: value.ProcessingDeadlineAt, Images: value.Images,
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// AdmitCoordinationMessage performs protocol v2 send admission atomically.
func (s *Store) AdmitCoordinationMessage(ctx context.Context, input CoordinationSendAdmission) (model.AgentMessage, bool, error) {
	runProvided := strings.TrimSpace(input.Message.RunID) != ""
	rootProvided := strings.TrimSpace(input.Message.RootMessageID) != ""
	parentProvided := strings.TrimSpace(input.Message.ParentMessageID) != ""
	value := input.Message
	if input.SourceOperation != "" && value.Act != "inform" && value.ResultMode == "" {
		value.ResultMode = "join"
	}
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	value = normalizeAgentMessage(value)
	if value.Status == "" {
		value.Status = "queued"
	}
	if value.CreatedAt == 0 {
		value.CreatedAt = time.Now().UnixMilli()
	}
	if value.UpdatedAt == 0 {
		value.UpdatedAt = value.CreatedAt
	}
	if value.Status != "queued" || value.Kind != "request" {
		return model.AgentMessage{}, false, fmt.Errorf("coordination admission requires a queued request")
	}
	if value.Act == "inform" {
		value.ResultMode = "none"
	}
	if value.ResultMode == "join" && input.SourceOperation == "" {
		return model.AgentMessage{}, false, fmt.Errorf("joined send requires a source operation")
	}
	if value.ResultMode == "join" && input.JoinDeadlineAt <= time.Now().UnixMilli() {
		return model.AgentMessage{}, false, fmt.Errorf("joined send requires a future deadline")
	}
	if input.TodoID > 0 && value.Act == "inform" {
		return model.AgentMessage{}, false, fmt.Errorf("inform does not support a TODO link")
	}
	if input.TodoID > 0 && input.TodoPolicy == "" {
		input.TodoPolicy = "complete_on_success"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	currentGeneration, _, complete, _, err := protocolStateDetails(ctx, tx)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	generation := input.ProtocolGeneration
	if generation == 0 {
		generation = currentGeneration
	}
	if complete && generation != currentGeneration {
		return model.AgentMessage{}, false, fmt.Errorf("communication protocol generation %d is stale; current generation is %d", generation, currentGeneration)
	}
	var source model.AgentOperation
	if input.SourceOperation != "" {
		source, err = scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where id=?`, input.SourceOperation))
		if err != nil {
			return model.AgentMessage{}, false, err
		}
		if source.AgentID != value.SenderAgentID {
			return model.AgentMessage{}, false, sql.ErrNoRows
		}
		if !runProvided {
			value.RunID = source.CausalRunID
		}
		if !parentProvided && source.ParentMessageID != "" {
			value.ParentMessageID = source.ParentMessageID
		}
		if source.ParentMessageID != "" {
			var parentRoot, parentRun string
			var parentDepth int
			if err := tx.QueryRowContext(ctx, `select root_message_id,run_id,depth from agent_messages where id=?`, source.ParentMessageID).Scan(&parentRoot, &parentRun, &parentDepth); err != nil {
				return model.AgentMessage{}, false, err
			}
			if !rootProvided {
				value.RootMessageID = parentRoot
			}
			if value.RootMessageID != parentRoot || value.RunID != parentRun || value.Depth != 0 && value.Depth != parentDepth+1 {
				return model.AgentMessage{}, false, fmt.Errorf("message does not match the source operation causal lineage")
			}
			value.Depth = parentDepth + 1
		}
		if source.ProtocolGeneration != generation || value.RunID != source.CausalRunID {
			return model.AgentMessage{}, false, fmt.Errorf("message does not match the source operation causal run or protocol generation")
		}
		if source.ParentMessageID != "" && value.ParentMessageID != source.ParentMessageID {
			return model.AgentMessage{}, false, fmt.Errorf("message does not match the source operation lineage")
		}
	}
	hash, err := coordinationRequestHash(value, input.SourceOperation, input.TodoID, input.TodoPolicy, input.JoinDeadlineAt)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	if value.IdempotencyKey != "" {
		var oldHash, messageID string
		err := tx.QueryRowContext(ctx, `select request_hash,message_id from coordination_send_receipts where sender_agent_id=? and idempotency_key=?`, value.SenderAgentID, value.IdempotencyKey).Scan(&oldHash, &messageID)
		if err == nil {
			if oldHash != hash {
				return model.AgentMessage{}, false, fmt.Errorf("coordination idempotency key was already used for different work")
			}
			existing, err := scanAgentMessage(tx.QueryRowContext(ctx, `select `+agentMessageColumns+` from agent_messages where id=?`, messageID))
			if err != nil {
				return existing, false, err
			}
			images, err := loadMessageImages(ctx, tx, existing.ID, true)
			existing.Images = imagePointer(images)
			return existing, false, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return model.AgentMessage{}, false, err
		}
	}
	generation, err = requireObjectGeneration(ctx, tx, generation)
	if err != nil {
		return model.AgentMessage{}, false, err
	}
	if input.SourceOperation != "" {
		now := time.Now().UnixMilli()
		if source.Attempt != input.OperationAttempt || (source.State != "claimed" && source.State != "running") || source.LeaseExpiresAt <= now || source.DeadlineAt > 0 && source.DeadlineAt <= now || input.RuntimeID != "" && input.RuntimeID != source.RuntimeID {
			return model.AgentMessage{}, false, sql.ErrNoRows
		}
		if err := requireRuntimeGeneration(ctx, tx, source.AgentID, source.RuntimeID, generation); err != nil {
			return model.AgentMessage{}, false, err
		}
	} else if value.SenderAgentID != "" {
		if err := requireRuntimeGeneration(ctx, tx, value.SenderAgentID, input.RuntimeID, generation); err != nil {
			return model.AgentMessage{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into agent_messages(`+agentMessageColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, agentMessageValues(value)...); err != nil {
		return model.AgentMessage{}, false, err
	}
	if err := putMessageImages(ctx, tx, value.ID, messageImageValues(value.Images), value.CreatedAt); err != nil {
		return model.AgentMessage{}, false, err
	}
	childOperationID := "operation:" + value.ID
	childKind, childParent := "inbound", value.ID
	if value.SenderAgentID == "" {
		childKind, childParent = "direct", ""
	}
	child := model.AgentOperation{ID: childOperationID, AgentID: value.TargetAgentID, Kind: childKind, State: "ready", ParentMessageID: childParent, CausalRunID: value.RunID, DeadlineAt: value.ProcessingDeadlineAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt, ProtocolGeneration: generation}
	if _, err := tx.ExecContext(ctx, `insert into agent_operations(`+operationColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationValues(child)...); err != nil {
		return model.AgentMessage{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `insert into coordination_message_meta(message_id,source_operation_id,request_hash,created_at) values(?,?,?,?)`, value.ID, input.SourceOperation, hash, value.CreatedAt); err != nil {
		return model.AgentMessage{}, false, err
	}
	eligible := 1
	if input.TodoID > 0 {
		eligible = 0
	}
	if _, err := tx.ExecContext(ctx, `insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?,?,?,?,?,?,?)`, "request:"+value.ID, value.TargetAgentID, childOperationID, value.ID, "request", "pending", eligible, value.CreatedAt, value.UpdatedAt, generation); err != nil {
		return model.AgentMessage{}, false, err
	}
	if value.ResultMode == "join" {
		if _, err := tx.ExecContext(ctx, `insert into agent_operation_joins(id,operation_id,message_id,state,deadline_at,created_at,updated_at,protocol_generation) values(?,?,?,'open',?,?,?,?)`, "join:"+input.SourceOperation+":"+value.ID, input.SourceOperation, value.ID, input.JoinDeadlineAt, value.CreatedAt, value.UpdatedAt, generation); err != nil {
			return model.AgentMessage{}, false, err
		}
		if err := rejectStoredOperationCycles(ctx, tx); err != nil {
			return model.AgentMessage{}, false, err
		}
	}
	if input.TodoID > 0 {
		if _, err := tx.ExecContext(ctx, `insert into todo_link_intents(id,message_id,operation_id,todo_id,policy,state,created_at,protocol_generation) values(?,?,?,?,?,'pending',?,?)`, "todo:"+value.ID, value.ID, nullString(input.SourceOperation), input.TodoID, input.TodoPolicy, value.CreatedAt, generation); err != nil {
			return model.AgentMessage{}, false, err
		}
	}
	if value.IdempotencyKey != "" {
		if _, err := tx.ExecContext(ctx, `insert into coordination_send_receipts(sender_agent_id,idempotency_key,request_hash,message_id,created_at) values(?,?,?,?,?)`, value.SenderAgentID, value.IdempotencyKey, hash, value.ID, value.CreatedAt); err != nil {
			return model.AgentMessage{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.AgentMessage{}, false, err
	}
	return value, true, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) ApplyTodoLink(ctx context.Context, intentID string) error {
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, complete, _, err := protocolState(ctx, tx)
	if err != nil {
		return err
	}
	if complete {
		return fmt.Errorf("use the attempt-fenced TODO link API after protocol cutover")
	}
	result, err := tx.ExecContext(ctx, `update todo_link_intents set state='applied',applied_at=? where id=? and state='pending'`, now, intentID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var state string
		if err := tx.QueryRowContext(ctx, `select state from todo_link_intents where id=?`, intentID).Scan(&state); err != nil || state != "applied" {
			return sql.ErrNoRows
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set eligible=1,updated_at=? where message_id=(select message_id from todo_link_intents where id=?) and kind='request' and state='pending'`, now, intentID); err != nil {
		return err
	}
	return tx.Commit()
}

const receiptColumns = `id,agent_id,coalesce(operation_id,''),coalesce(message_id,''),coalesce(result_id,''),kind,state,eligible,runtime_id,claim_key,pi_tool_request_id,attempt,operation_attempt,claimed_at,lease_expires_at,presented_at,acknowledged_at,abandoned_at,created_at,updated_at,protocol_generation`

func scanAgentInboxReceipt(row rowScanner) (model.AgentInboxReceipt, error) {
	var value model.AgentInboxReceipt
	err := row.Scan(&value.ID, &value.AgentID, &value.OperationID, &value.MessageID, &value.ResultID, &value.Kind, &value.State, &value.Eligible, &value.RuntimeID, &value.ClaimKey, &value.PiToolRequestID, &value.Attempt, &value.OperationAttempt, &value.ClaimedAt, &value.LeaseExpiresAt, &value.PresentedAt, &value.AcknowledgedAt, &value.AbandonedAt, &value.CreatedAt, &value.UpdatedAt, &value.ProtocolGeneration)
	return value, err
}

func (s *Store) AgentInboxReceipt(ctx context.Context, id string) (model.AgentInboxReceipt, error) {
	return scanAgentInboxReceipt(s.db.QueryRowContext(ctx, `select `+receiptColumns+` from agent_inbox_receipts where id=?`, id))
}

// BindAgentInboxReceipt assigns an independent receipt to a ready operation. This is
// used for inbound requests and notify results before Pi sees the content.
func (s *Store) BindAgentInboxReceipt(ctx context.Context, receiptID, operationID string) error {
	_, complete, _, err := s.CommunicationProtocolState(ctx)
	if err != nil {
		return err
	}
	if complete {
		return fmt.Errorf("use atomic receipt operation claim after protocol cutover")
	}
	result, err := s.db.ExecContext(ctx, `update agent_inbox_receipts set operation_id=?,updated_at=? where id=? and operation_id is null and state='pending' and agent_id=(select agent_id from agent_operations where id=? and state='ready')`, operationID, time.Now().UnixMilli(), receiptID, operationID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

// TakeOperationReceipts binds at most four pending receipts to one stable Pi
// tool request. An exact retry returns the same immutable receipt and result.
func (s *Store) TakeOperationReceipts(ctx context.Context, operationID, agentID, runtimeID string, attempt int, toolRequestID string, byteLimit int) ([]model.AgentInboxReceipt, []model.AgentMessageResult, error) {
	if strings.TrimSpace(toolRequestID) == "" {
		return nil, nil, fmt.Errorf("Pi tool request ID is required")
	}
	if byteLimit <= 0 {
		byteLimit = 64 << 10
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := fenceOperationMutation(ctx, tx, operationID, agentID, runtimeID, attempt)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='pending',runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,updated_at=? where operation_id=? and kind in ('result','blocker','control') and state in ('claimed','presented') and lease_expires_at>0 and lease_expires_at<=?`, time.Now().UnixMilli(), operationID, time.Now().UnixMilli()); err != nil {
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `select `+receiptColumns+` from agent_inbox_receipts where operation_id=? and pi_tool_request_id=? and runtime_id=? and operation_attempt=? and protocol_generation=? and state in ('claimed','presented') order by created_at,id`, operationID, toolRequestID, runtimeID, attempt, operation.ProtocolGeneration)
	if err != nil {
		return nil, nil, err
	}
	receipts, err := collectReceipts(rows)
	if err != nil {
		return nil, nil, err
	}
	if len(receipts) == 0 {
		rows, err = tx.QueryContext(ctx, `select `+receiptColumns+` from agent_inbox_receipts where operation_id=? and kind in ('result','blocker','control') and state='pending' and eligible=1 and protocol_generation=? order by created_at,id limit 4`, operationID, operation.ProtocolGeneration)
		if err != nil {
			return nil, nil, err
		}
		receipts, err = collectReceipts(rows)
		if err != nil {
			return nil, nil, err
		}
		now := time.Now().UnixMilli()
		selected := receipts[:0]
		used := 0
		for _, receipt := range receipts {
			size := 0
			if receipt.ResultID != "" {
				var response, failure string
				if err := tx.QueryRowContext(ctx, `select response,error from agent_message_results where id=?`, receipt.ResultID).Scan(&response, &failure); err != nil {
					return nil, nil, err
				}
				size = len(response) + len(failure)
			}
			if len(selected) > 0 && used+size > byteLimit {
				break
			}
			used += size
			selected = append(selected, receipt)
		}
		receipts = selected
		for index := range receipts {
			result, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='claimed',runtime_id=?,pi_tool_request_id=?,attempt=attempt+1,operation_attempt=?,claimed_at=?,lease_expires_at=?,updated_at=? where id=? and state='pending' and protocol_generation=?`, runtimeID, toolRequestID, attempt, now, now+coordinationLease.Milliseconds(), now, receipts[index].ID, operation.ProtocolGeneration)
			if err != nil {
				return nil, nil, err
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return nil, nil, err
			}
			receipts[index].State = "claimed"
			receipts[index].RuntimeID = runtimeID
			receipts[index].PiToolRequestID = toolRequestID
			receipts[index].Attempt++
			receipts[index].OperationAttempt = attempt
			receipts[index].ClaimedAt = now
			receipts[index].LeaseExpiresAt = now + coordinationLease.Milliseconds()
			receipts[index].UpdatedAt = now
		}
	}
	results, err := receiptResults(ctx, tx, receipts)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return receipts, results, nil
}

func collectReceipts(rows *sql.Rows) ([]model.AgentInboxReceipt, error) {
	defer func() { _ = rows.Close() }()
	var out []model.AgentInboxReceipt
	for rows.Next() {
		value, err := scanAgentInboxReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func receiptResults(ctx context.Context, tx *sql.Tx, receipts []model.AgentInboxReceipt) ([]model.AgentMessageResult, error) {
	out := make([]model.AgentMessageResult, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.ResultID == "" {
			continue
		}
		value, err := scanAgentMessageResult(tx.QueryRowContext(ctx, `select id,message_id,status,response,error,terminal_reason,legacy_state,created_at,protocol_generation from agent_message_results where id=?`, receipt.ResultID))
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func scanAgentMessageResult(row rowScanner) (model.AgentMessageResult, error) {
	var value model.AgentMessageResult
	err := row.Scan(&value.ID, &value.MessageID, &value.Status, &value.Response, &value.Error, &value.TerminalReason, &value.LegacyState, &value.CreatedAt, &value.ProtocolGeneration)
	return value, err
}

func (s *Store) AgentMessageResult(ctx context.Context, messageID string) (model.AgentMessageResult, error) {
	return scanAgentMessageResult(s.db.QueryRowContext(ctx, `select id,message_id,status,response,error,terminal_reason,legacy_state,created_at,protocol_generation from agent_message_results where message_id=?`, messageID))
}

func (s *Store) MarkAgentInboxReceiptPresented(ctx context.Context, receiptID, operationID, runtimeID string, operationAttempt int, toolRequestID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var agentID string
	if err := tx.QueryRowContext(ctx, `select agent_id from agent_inbox_receipts where id=? and operation_id=?`, receiptID, operationID).Scan(&agentID); err != nil {
		return err
	}
	if _, err := fenceOperationMutation(ctx, tx, operationID, agentID, runtimeID, operationAttempt); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='presented',presented_at=?,updated_at=? where id=? and operation_id=? and runtime_id=? and operation_attempt=? and pi_tool_request_id=? and state='claimed' and lease_expires_at>?`, now, now, receiptID, operationID, runtimeID, operationAttempt, toolRequestID, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		var state string
		if err := tx.QueryRowContext(ctx, `select state from agent_inbox_receipts where id=? and operation_id=? and runtime_id=? and operation_attempt=? and pi_tool_request_id=?`, receiptID, operationID, runtimeID, operationAttempt, toolRequestID).Scan(&state); err == nil && state == "presented" {
			return tx.Commit()
		}
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) AcknowledgeAgentInboxReceipt(ctx context.Context, receiptID, operationID, runtimeID string, operationAttempt int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var agentID string
	if err := tx.QueryRowContext(ctx, `select agent_id from agent_inbox_receipts where id=? and operation_id=?`, receiptID, operationID).Scan(&agentID); err != nil {
		return err
	}
	if _, err := fenceOperationMutation(ctx, tx, operationID, agentID, runtimeID, operationAttempt); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='acknowledged',lease_expires_at=0,acknowledged_at=?,updated_at=? where id=? and operation_id=? and runtime_id=? and operation_attempt=? and state='presented'`, now, now, receiptID, operationID, runtimeID, operationAttempt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		var state string
		if err := tx.QueryRowContext(ctx, `select state from agent_inbox_receipts where id=? and operation_id=? and runtime_id=? and operation_attempt=?`, receiptID, operationID, runtimeID, operationAttempt).Scan(&state); err == nil && state == "acknowledged" {
			return nil
		}
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state='acknowledged',resolved_at=?,updated_at=? where operation_id=? and state='ready' and message_id=(select message_id from agent_inbox_receipts where id=?)`, now, now, operationID, receiptID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AbandonAgentInboxReceipt(ctx context.Context, receiptID, operationID, runtimeID string, operationAttempt int, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var agentID, messageID string
	if err := tx.QueryRowContext(ctx, `select agent_id,message_id from agent_inbox_receipts where id=? and operation_id=?`, receiptID, operationID).Scan(&agentID, &messageID); err != nil {
		return err
	}
	if _, err := fenceOperationMutation(ctx, tx, operationID, agentID, runtimeID, operationAttempt); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='abandoned',lease_expires_at=0,abandoned_at=?,updated_at=? where id=? and operation_id=? and runtime_id=? and operation_attempt=? and state in ('claimed','presented')`, now, now, receiptID, operationID, runtimeID, operationAttempt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state='detached',failure=?,resolved_at=?,updated_at=? where operation_id=? and message_id=? and state in ('open','ready')`, reason, now, now, operationID, messageID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AgentOperationJoin(ctx context.Context, operationID, messageID string) (model.AgentOperationJoin, error) {
	var value model.AgentOperationJoin
	err := s.db.QueryRowContext(ctx, `select id,operation_id,message_id,state,deadline_at,failure,created_at,updated_at,resolved_at,protocol_generation from agent_operation_joins where operation_id=? and message_id=?`, operationID, messageID).Scan(&value.ID, &value.OperationID, &value.MessageID, &value.State, &value.DeadlineAt, &value.Failure, &value.CreatedAt, &value.UpdatedAt, &value.ResolvedAt, &value.ProtocolGeneration)
	return value, err
}

func (s *Store) DetachAgentOperationJoin(ctx context.Context, operationID, messageID, agentID, runtimeID string, attempt int) error {
	return s.finishAgentOperationJoin(ctx, operationID, messageID, agentID, runtimeID, attempt, "detached")
}

func (s *Store) CancelAgentOperationJoin(ctx context.Context, operationID, messageID, agentID, runtimeID string, attempt int) error {
	return s.finishAgentOperationJoin(ctx, operationID, messageID, agentID, runtimeID, attempt, "canceled")
}

func (s *Store) finishAgentOperationJoin(ctx context.Context, operationID, messageID, agentID, runtimeID string, attempt int, state string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := fenceOperationMutation(ctx, tx, operationID, agentID, runtimeID, attempt); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agent_operation_joins set state=?,resolved_at=?,updated_at=? where operation_id=? and message_id=? and state in ('open','ready')`, state, now, now, operationID, messageID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='abandoned',abandoned_at=?,lease_expires_at=0,updated_at=? where operation_id=? and message_id=? and id like 'join-%' and state in ('pending','claimed','presented')`, now, now, operationID, messageID); err != nil {
		return err
	}
	return tx.Commit()
}

func acknowledgePresentedReceipts(ctx context.Context, tx *sql.Tx, operationID string, attempt int, now int64) error {
	if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state='acknowledged',resolved_at=?,updated_at=? where operation_id=? and state='ready' and message_id in (select message_id from agent_inbox_receipts where operation_id=? and operation_attempt=? and state='presented')`, now, now, operationID, operationID, attempt); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='acknowledged',lease_expires_at=0,acknowledged_at=?,updated_at=? where operation_id=? and operation_attempt=? and state='presented'`, now, now, operationID, attempt)
	return err
}

func releaseClaimedReceipts(ctx context.Context, tx *sql.Tx, operationID string, attempt int, detach bool, now int64) error {
	operationAssignment := "operation_id=operation_id"
	if detach {
		operationAssignment = "operation_id=null"
	}
	_, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='pending',`+operationAssignment+`,runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,updated_at=? where operation_id=? and operation_attempt=? and state='claimed'`, now, operationID, attempt)
	return err
}

// SettleOperation parks when a dependency still needs handling. Otherwise it
// atomically settles the operation, its parent message, result, joins, receipts,
// and TODO settlement event.
func (s *Store) SettleAgentOperation(ctx context.Context, id, agentID, runtimeID string, attempt int, response, failure string) (AgentOperationSettleResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentOperationSettleResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where id=? and agent_id=?`, id, agentID))
	if err != nil {
		return AgentOperationSettleResult{}, err
	}
	if operation.Attempt != attempt {
		return AgentOperationSettleResult{}, sql.ErrNoRows
	}
	if operation.State == "settled" || operation.State == "failed" {
		if operation.ParentMessageID != "" {
			stored, resultErr := scanAgentMessageResult(tx.QueryRowContext(ctx, `select id,message_id,status,response,error,terminal_reason,legacy_state,created_at,protocol_generation from agent_message_results where message_id=?`, operation.ParentMessageID))
			wantStatus := "completed"
			if strings.TrimSpace(failure) != "" {
				wantStatus = "failed"
			}
			if resultErr != nil || stored.Status != wantStatus || stored.Response != response || stored.Error != failure {
				return AgentOperationSettleResult{}, fmt.Errorf("operation was already settled with a different result")
			}
		} else {
			wantStatus := "completed"
			if strings.TrimSpace(failure) != "" {
				wantStatus = "failed"
			}
			if operation.CompletionDigest == "" || operation.CompletionDigest != operationCompletionDigest(wantStatus, response, failure) {
				return AgentOperationSettleResult{}, fmt.Errorf("operation was already settled with a different result")
			}
		}
		return AgentOperationSettleResult{Operation: operation}, nil
	}
	if operation.RuntimeID != runtimeID {
		return AgentOperationSettleResult{}, sql.ErrNoRows
	}
	if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, operation.ProtocolGeneration); err != nil {
		return AgentOperationSettleResult{}, err
	}
	if operation.State != "claimed" && operation.State != "running" {
		return AgentOperationSettleResult{}, sql.ErrNoRows
	}
	now := time.Now().UnixMilli()
	if operation.LeaseExpiresAt <= now || operation.DeadlineAt > 0 && operation.DeadlineAt <= now {
		return AgentOperationSettleResult{}, sql.ErrNoRows
	}
	if err := acknowledgePresentedReceipts(ctx, tx, id, attempt, now); err != nil {
		return AgentOperationSettleResult{}, err
	}
	var open, ready, pendingTodoLinks int
	if err := tx.QueryRowContext(ctx, `select
  (select count(*) from agent_operation_joins where operation_id=? and state='open'),
  (select count(*) from agent_operation_joins where operation_id=? and state='ready')+
  (select count(*) from agent_inbox_receipts where operation_id=? and id like 'join-%' and state in ('pending','claimed','presented')),
  (select count(*) from todo_link_intents where operation_id=? and state='pending')`, id, id, id, id).Scan(&open, &ready, &pendingTodoLinks); err != nil {
		return AgentOperationSettleResult{}, err
	}
	if pendingTodoLinks > 0 {
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,operation_id,message_id,kind,state,eligible,created_at,updated_at,protocol_generation)
select 'todo-link-receipt:'||intent.id,?,intent.operation_id,intent.message_id,'control','pending',1,?,?,intent.protocol_generation from todo_link_intents intent where intent.operation_id=? and intent.state='pending'`, operation.AgentID, now, now, id); err != nil {
			return AgentOperationSettleResult{}, err
		}
	}
	if pendingTodoLinks > 0 || strings.TrimSpace(failure) == "" && (open > 0 || ready > 0) {
		state := "waiting"
		if ready > 0 || pendingTodoLinks > 0 {
			state = "ready"
		}
		if _, err := tx.ExecContext(ctx, `update agent_operations set state=?,runtime_id='',claim_key='',lease_expires_at=0,updated_at=? where id=?`, state, now, id); err != nil {
			return AgentOperationSettleResult{}, err
		}
		if err := acknowledgePresentedReceipts(ctx, tx, id, attempt, now); err != nil {
			return AgentOperationSettleResult{}, err
		}
		if err := releaseClaimedReceipts(ctx, tx, id, attempt, false, now); err != nil {
			return AgentOperationSettleResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='parked',finished_at=?,updated_at=? where operation_id=? and attempt=? and runtime_id=? and state in ('claimed','running')`, now, now, id, attempt, runtimeID); err != nil {
			return AgentOperationSettleResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return AgentOperationSettleResult{}, err
		}
		operation.State, operation.RuntimeID, operation.ClaimKey, operation.LeaseExpiresAt, operation.UpdatedAt = state, "", "", 0, now
		return AgentOperationSettleResult{Operation: operation, Parked: true}, nil
	}
	if _, err := tx.ExecContext(ctx, `update agent_operations set state='settling',updated_at=? where id=?`, now, id); err != nil {
		return AgentOperationSettleResult{}, err
	}
	terminalState := "settled"
	resultStatus, reason := "completed", ""
	if strings.TrimSpace(failure) != "" {
		terminalState, resultStatus, reason = "failed", "failed", "failed"
		if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state='canceled',failure='parent operation failed',resolved_at=?,updated_at=? where operation_id=? and state in ('open','ready')`, now, now, id); err != nil {
			return AgentOperationSettleResult{}, err
		}
	}
	if operation.ParentMessageID != "" {
		result := model.AgentMessageResult{ID: "result:" + operation.ParentMessageID, MessageID: operation.ParentMessageID, Status: resultStatus, Response: response, Error: failure, TerminalReason: reason, CreatedAt: now, ProtocolGeneration: operation.ProtocolGeneration}
		if err := insertAgentMessageResult(ctx, tx, result); err != nil {
			return AgentOperationSettleResult{}, err
		}
		messageStatus := "completed"
		if resultStatus != "completed" {
			messageStatus = "failed"
		}
		if _, err := tx.ExecContext(ctx, `update agent_messages set status=?,response=?,error=?,terminal_reason=?,notification_state=case when act='inform' then 'suppressed' else 'pending' end,runtime_id='',lease_expires_at=0,completed_at=?,updated_at=? where id=? and status not in ('completed','failed')`, messageStatus, response, failure, reason, now, now, operation.ParentMessageID); err != nil {
			return AgentOperationSettleResult{}, err
		}
		if err := resolveMessageJoins(ctx, tx, result, now); err != nil {
			return AgentOperationSettleResult{}, err
		}
		if err := createNotifyResultReceipt(ctx, tx, result, now); err != nil {
			return AgentOperationSettleResult{}, err
		}
		if err := createTodoSettlementEvents(ctx, tx, result, now); err != nil {
			return AgentOperationSettleResult{}, err
		}
	}
	if err := acknowledgePresentedReceipts(ctx, tx, id, attempt, now); err != nil {
		return AgentOperationSettleResult{}, err
	}
	if err := releaseClaimedReceipts(ctx, tx, id, attempt, true, now); err != nil {
		return AgentOperationSettleResult{}, err
	}
	completionDigest := operationCompletionDigest(resultStatus, response, failure)
	if _, err := tx.ExecContext(ctx, `update agent_operations set state=?,runtime_id='',claim_key='',lease_expires_at=0,terminal_reason=?,last_error=?,settled_at=?,completion_digest=?,updated_at=? where id=?`, terminalState, reason, failure, now, completionDigest, now, id); err != nil {
		return AgentOperationSettleResult{}, err
	}
	attemptState := "settled"
	if terminalState == "failed" {
		attemptState = "failed"
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state=?,terminal_reason=?,finished_at=?,updated_at=? where operation_id=? and attempt=? and runtime_id=? and state in ('claimed','running')`, attemptState, reason, now, now, id, attempt, runtimeID); err != nil {
		return AgentOperationSettleResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentOperationSettleResult{}, err
	}
	operation.State, operation.RuntimeID, operation.ClaimKey, operation.LeaseExpiresAt = terminalState, "", "", 0
	operation.TerminalReason, operation.LastError, operation.SettledAt, operation.CompletionDigest, operation.UpdatedAt = reason, failure, now, completionDigest, now
	return AgentOperationSettleResult{Operation: operation}, nil
}

func insertAgentMessageResult(ctx context.Context, tx *sql.Tx, value model.AgentMessageResult) error {
	_, err := tx.ExecContext(ctx, `insert into agent_message_results(id,message_id,status,response,error,terminal_reason,legacy_state,created_at,protocol_generation) values(?,?,?,?,?,?,?,?,?)`, value.ID, value.MessageID, value.Status, value.Response, value.Error, value.TerminalReason, value.LegacyState, value.CreatedAt, value.ProtocolGeneration)
	if err == nil {
		return nil
	}
	existing, readErr := scanAgentMessageResult(tx.QueryRowContext(ctx, `select id,message_id,status,response,error,terminal_reason,legacy_state,created_at,protocol_generation from agent_message_results where message_id=?`, value.MessageID))
	if readErr == nil && existing.Status == value.Status && existing.Response == value.Response && existing.Error == value.Error && existing.TerminalReason == value.TerminalReason {
		return nil
	}
	return fmt.Errorf("message result is immutable: %w", err)
}

func resolveMessageJoins(ctx context.Context, tx *sql.Tx, result model.AgentMessageResult, now int64) error {
	rows, err := tx.QueryContext(ctx, `select dependency.id,dependency.operation_id,operation.agent_id,dependency.state,dependency.protocol_generation from agent_operation_joins dependency join agent_operations operation on operation.id=dependency.operation_id where dependency.message_id=? and dependency.state in ('open','detached','canceled','expired')`, result.MessageID)
	if err != nil {
		return err
	}
	type resolution struct {
		joinID, operationID, agentID, state string
		generation                          int
	}
	var values []resolution
	for rows.Next() {
		var value resolution
		if err := rows.Scan(&value.joinID, &value.operationID, &value.agentID, &value.state, &value.generation); err != nil {
			_ = rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		if value.state != "open" {
			if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?, 'result','pending',1,?,?,?)`, "late-result:"+value.joinID, value.agentID, result.MessageID, result.ID, now, now, value.generation); err != nil {
				return err
			}
			continue
		}
		joinState, receiptKind := "ready", "result"
		if result.Status != "completed" {
			joinState, receiptKind = "failed", "blocker"
		}
		if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state=?,failure=?,resolved_at=?,updated_at=? where id=? and state='open'`, joinState, result.Error, now, now, value.joinID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?,?,?,'pending',1,?,?,?)`, "join-receipt:"+value.joinID, value.agentID, value.operationID, result.MessageID, result.ID, receiptKind, now, now, value.generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operations set state='ready',updated_at=? where id=? and state='waiting'`, now, value.operationID); err != nil {
			return err
		}
	}
	return nil
}

func createNotifyResultReceipt(ctx context.Context, tx *sql.Tx, result model.AgentMessageResult, now int64) error {
	_, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation)
select 'result-receipt:'||message.id,message.sender_agent_id,message.id,?,'result','pending',1,?,?,?
from agent_messages message where message.id=? and message.sender_agent_id<>'' and message.act<>'inform' and message.result_mode='notify'`, result.ID, now, now, result.ProtocolGeneration, result.MessageID)
	return err
}

func createTodoSettlementEvents(ctx context.Context, tx *sql.Tx, result model.AgentMessageResult, now int64) error {
	_, err := tx.ExecContext(ctx, `insert or ignore into todo_settlement_events(id,intent_id,result_id,agent_id,operation_id,state,created_at,protocol_generation)
select 'todo-settlement:'||intent.id,intent.id,?,coalesce(operation.agent_id,message.sender_agent_id),intent.operation_id,'pending',?,?
from todo_link_intents intent
join agent_messages message on message.id=intent.message_id
left join agent_operations operation on operation.id=intent.operation_id
where intent.message_id=? and coalesce(operation.agent_id,message.sender_agent_id)<>''`, result.ID, now, result.ProtocolGeneration, result.MessageID)
	return err
}

// PutAgentMessageResult inserts one immutable result and resolves all durable
// delivery duties in the same transaction.
func (s *Store) PutAgentMessageResult(ctx context.Context, value model.AgentMessageResult) error {
	if value.ID == "" {
		value.ID = "result:" + value.MessageID
	}
	if value.CreatedAt == 0 {
		value.CreatedAt = time.Now().UnixMilli()
	}
	if value.MessageID == "" || !validResultStatus(value.Status) {
		return fmt.Errorf("message result has invalid fields")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	value.ProtocolGeneration, err = requireObjectGeneration(ctx, tx, value.ProtocolGeneration)
	if err != nil {
		return err
	}
	operation, lookupErr := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where parent_message_id=?`, value.MessageID))
	operationFound := lookupErr == nil
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	if operationFound {
		if operation.ProtocolGeneration != value.ProtocolGeneration {
			return fmt.Errorf("message result protocol generation does not match its operation")
		}
		if operation.State == "claimed" || operation.State == "running" {
			if _, err := fenceOperationMutation(ctx, tx, operation.ID, operation.AgentID, value.RuntimeID, value.OperationAttempt); err != nil {
				return err
			}
		} else if operation.State != "ready" && operation.State != "waiting" && operation.State != "settled" && operation.State != "failed" && operation.State != "canceled" && operation.State != "expired" {
			return sql.ErrNoRows
		}
	}
	if err := insertAgentMessageResult(ctx, tx, value); err != nil {
		return err
	}
	if operationFound && operation.State != "settled" && operation.State != "failed" && operation.State != "canceled" && operation.State != "expired" {
		now := value.CreatedAt
		operationState, attemptState := "settled", "settled"
		if value.Status != "completed" {
			operationState, attemptState = value.Status, "failed"
		}
		if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state='canceled',failure='operation completed by immutable result',resolved_at=?,updated_at=? where operation_id=? and state in ('open','ready')`, now, now, operation.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state=?,terminal_reason=?,finished_at=?,updated_at=? where operation_id=? and attempt=? and state in ('claimed','running')`, attemptState, value.TerminalReason, now, now, operation.ID, operation.Attempt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='acknowledged',runtime_id='',claim_key='',lease_expires_at=0,acknowledged_at=?,updated_at=? where operation_id=? and kind='request' and state in ('pending','claimed','presented')`, now, now, operation.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operations set state=?,runtime_id='',claim_key='',lease_expires_at=0,terminal_reason=?,last_error=?,settled_at=?,completion_digest=?,updated_at=? where id=? and state in ('ready','waiting','claimed','running')`, operationState, value.TerminalReason, value.Error, now, operationCompletionDigest(value.Status, value.Response, value.Error), now, operation.ID); err != nil {
			return err
		}
	}
	messageStatus := "completed"
	if value.Status != "completed" {
		messageStatus = "failed"
	}
	if _, err := tx.ExecContext(ctx, `update agent_messages set status=?,response=?,error=?,terminal_reason=?,notification_state=case when act='inform' then 'suppressed' else 'pending' end,runtime_id='',lease_expires_at=0,completed_at=?,updated_at=? where id=? and status not in ('completed','failed')`, messageStatus, value.Response, value.Error, value.TerminalReason, value.CreatedAt, value.CreatedAt, value.MessageID); err != nil {
		return err
	}
	if err := resolveMessageJoins(ctx, tx, value, value.CreatedAt); err != nil {
		return err
	}
	if err := createNotifyResultReceipt(ctx, tx, value, value.CreatedAt); err != nil {
		return err
	}
	if err := createTodoSettlementEvents(ctx, tx, value, value.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutAgentInboxReceipt(ctx context.Context, value model.AgentInboxReceipt) error {
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.State == "" {
		value.State = "pending"
	}
	if value.CreatedAt == 0 {
		value.CreatedAt = time.Now().UnixMilli()
	}
	if value.UpdatedAt == 0 {
		value.UpdatedAt = value.CreatedAt
	}
	if value.AgentID == "" || !validReceiptKind(value.Kind) || value.State != "pending" {
		return fmt.Errorf("inbox receipt has invalid initial fields")
	}
	value.Eligible = true
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	value.ProtocolGeneration, err = requireObjectGeneration(ctx, tx, value.ProtocolGeneration)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,runtime_id,claim_key,pi_tool_request_id,attempt,operation_attempt,claimed_at,lease_expires_at,presented_at,acknowledged_at,abandoned_at,created_at,updated_at,protocol_generation) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.ID, value.AgentID, nullString(value.OperationID), nullString(value.MessageID), nullString(value.ResultID), value.Kind, value.State, value.Eligible, value.RuntimeID, value.ClaimKey, value.PiToolRequestID, value.Attempt, value.OperationAttempt, value.ClaimedAt, value.LeaseExpiresAt, value.PresentedAt, value.AcknowledgedAt, value.AbandonedAt, value.CreatedAt, value.UpdatedAt, value.ProtocolGeneration)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimAgentInboxReceipt claims one unbound duty and atomically creates its
// handling operation. Exact claim-key retries return the same binding.
func (s *Store) ClaimAgentInboxReceipt(ctx context.Context, agentID, runtimeID, claimKey string) (*model.AgentInboxReceipt, error) {
	_, receipt, err := s.ClaimAgentInboxReceiptOperation(ctx, agentID, runtimeID, claimKey)
	return receipt, err
}

func (s *Store) ClaimAgentInboxReceiptOperation(ctx context.Context, agentID, runtimeID, claimKey string) (*model.AgentOperation, *model.AgentInboxReceipt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if claimKey != "" {
		receipt, lookupErr := scanAgentInboxReceipt(tx.QueryRowContext(ctx, `select `+receiptColumns+` from agent_inbox_receipts where agent_id=? and claim_key=?`, agentID, claimKey))
		if lookupErr == nil {
			operation, err := scanOperation(tx.QueryRowContext(ctx, `select `+operationColumns+` from agent_operations where id=?`, receipt.OperationID))
			if err != nil || receipt.RuntimeID != runtimeID || receipt.State != "claimed" || receipt.LeaseExpiresAt <= now || operation.RuntimeID != runtimeID || operation.Attempt != receipt.OperationAttempt {
				return nil, nil, sql.ErrNoRows
			}
			if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, operation.ProtocolGeneration); err != nil {
				return nil, nil, err
			}
			return &operation, &receipt, nil
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return nil, nil, lookupErr
		}
	}
	receipt, err := scanAgentInboxReceipt(tx.QueryRowContext(ctx, `select `+receiptColumns+` from agent_inbox_receipts where agent_id=? and operation_id is null and state='pending' and eligible=1 order by created_at,id limit 1`, agentID))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, receipt.ProtocolGeneration); err != nil {
		return nil, nil, err
	}
	operationID := "receipt-operation:" + receipt.ID
	causalRun := operationID
	if receipt.MessageID != "" {
		_ = tx.QueryRowContext(ctx, `select run_id from agent_messages where id=?`, receipt.MessageID).Scan(&causalRun)
	}
	lease := now + coordinationLease.Milliseconds()
	operation := model.AgentOperation{ID: operationID, AgentID: agentID, Kind: "direct", State: "claimed", CausalRunID: causalRun, RuntimeID: runtimeID, ClaimKey: claimKey, Attempt: 1, ClaimedAt: now, LeaseExpiresAt: lease, CreatedAt: now, UpdatedAt: now, ProtocolGeneration: receipt.ProtocolGeneration}
	if _, err := tx.ExecContext(ctx, `insert into agent_operations(`+operationColumns+`) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationValues(operation)...); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into agent_operation_attempts(id,operation_id,attempt,runtime_id,claim_key,state,started_at,updated_at) values(?,?,?,?,?,'claimed',?,?)`, operationID+":1", operationID, 1, runtimeID, claimKey, now, now); err != nil {
		return nil, nil, err
	}
	result, err := tx.ExecContext(ctx, `update agent_inbox_receipts set operation_id=?,state='claimed',runtime_id=?,claim_key=?,attempt=attempt+1,operation_attempt=1,claimed_at=?,lease_expires_at=?,updated_at=? where id=? and operation_id is null and state='pending'`, operationID, runtimeID, claimKey, now, lease, now, receipt.ID)
	if err != nil {
		return nil, nil, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return nil, nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	receipt.OperationID, receipt.State, receipt.RuntimeID, receipt.ClaimKey = operationID, "claimed", runtimeID, claimKey
	receipt.Attempt++
	receipt.OperationAttempt, receipt.ClaimedAt, receipt.LeaseExpiresAt, receipt.UpdatedAt = 1, now, lease, now
	return &operation, &receipt, nil
}

// PutAgentOperationJoin checks the durable wait graph and inserts the edge in
// one transaction.
func (s *Store) PutAgentOperationJoin(ctx context.Context, value model.AgentOperationJoin) error {
	if value.ID == "" {
		value.ID = "join:" + value.OperationID + ":" + value.MessageID
	}
	if value.State == "" {
		value.State = "open"
	}
	if value.CreatedAt == 0 {
		value.CreatedAt = time.Now().UnixMilli()
	}
	if value.UpdatedAt == 0 {
		value.UpdatedAt = value.CreatedAt
	}
	if value.OperationID == "" || value.MessageID == "" || value.State != "open" || value.DeadlineAt <= time.Now().UnixMilli() {
		return fmt.Errorf("operation join has invalid initial fields or deadline")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	operation, err := fenceOperationMutation(ctx, tx, value.OperationID, "", value.RuntimeID, value.OperationAttempt)
	if err != nil {
		var agentID string
		if readErr := tx.QueryRowContext(ctx, `select agent_id from agent_operations where id=?`, value.OperationID).Scan(&agentID); readErr != nil {
			return err
		}
		operation, err = fenceOperationMutation(ctx, tx, value.OperationID, agentID, value.RuntimeID, value.OperationAttempt)
		if err != nil {
			return err
		}
	}
	value.ProtocolGeneration = operation.ProtocolGeneration
	var targetOperationID string
	if err := tx.QueryRowContext(ctx, `select id from agent_operations where parent_message_id=? and state not in ('settled','failed','canceled','expired')`, value.MessageID).Scan(&targetOperationID); err != nil {
		return fmt.Errorf("joined message has no exact target operation: %w", err)
	}
	if targetOperationID == value.OperationID {
		return fmt.Errorf("coordination operation dependency cycle detected")
	}
	if _, err := tx.ExecContext(ctx, `insert into agent_operation_joins(id,operation_id,message_id,state,deadline_at,failure,created_at,updated_at,resolved_at,protocol_generation) values(?,?,?,?,?,?,?,?,?,?)`, value.ID, value.OperationID, value.MessageID, value.State, value.DeadlineAt, value.Failure, value.CreatedAt, value.UpdatedAt, value.ResolvedAt, value.ProtocolGeneration); err != nil {
		var existing model.AgentOperationJoin
		readErr := tx.QueryRowContext(ctx, `select id,operation_id,message_id,state,deadline_at,failure,created_at,updated_at,resolved_at,protocol_generation from agent_operation_joins where id=?`, value.ID).Scan(&existing.ID, &existing.OperationID, &existing.MessageID, &existing.State, &existing.DeadlineAt, &existing.Failure, &existing.CreatedAt, &existing.UpdatedAt, &existing.ResolvedAt, &existing.ProtocolGeneration)
		if readErr != nil || existing.OperationID != value.OperationID || existing.MessageID != value.MessageID || existing.State != value.State || existing.DeadlineAt != value.DeadlineAt {
			return err
		}
	}
	if err := rejectStoredOperationCycles(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResolveAgentOperationJoin(ctx context.Context, operationID, messageID, state, failure string) error {
	_, complete, _, err := s.CommunicationProtocolState(ctx)
	if err != nil {
		return err
	}
	if complete {
		return fmt.Errorf("use atomic result or deadline resolution after protocol cutover")
	}
	if state != "ready" && state != "failed" && state != "expired" && state != "detached" && state != "canceled" {
		return fmt.Errorf("invalid join resolution state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	var joinID, agentID string
	var generation int
	if err := tx.QueryRowContext(ctx, `select dependency.id,operation.agent_id,dependency.protocol_generation from agent_operation_joins dependency join agent_operations operation on operation.id=dependency.operation_id where dependency.operation_id=? and dependency.message_id=? and dependency.state='open'`, operationID, messageID).Scan(&joinID, &agentID, &generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state=?,failure=?,resolved_at=?,updated_at=? where id=? and state='open'`, state, failure, now, now, joinID); err != nil {
		return err
	}
	if state == "ready" || state == "failed" || state == "expired" {
		kind := "result"
		if state != "ready" {
			kind = "blocker"
		}
		var resultID sql.NullString
		if err := tx.QueryRowContext(ctx, `select id from agent_message_results where message_id=?`, messageID).Scan(&resultID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,operation_id,message_id,result_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?,?,?,'pending',1,?,?,?)`, "join-receipt:"+joinID, agentID, operationID, messageID, resultID, kind, now, now, generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operations set state='ready',updated_at=? where id=? and state='waiting'`, now, operationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PutAgentPiLocalEvent(ctx context.Context, value model.AgentPiLocalEvent) (model.AgentPiLocalEvent, bool, error) {
	if value.ID == "" {
		value.ID = uuid.NewString()
	}
	if value.State == "" {
		value.State = "pending"
	}
	if value.CreatedAt == 0 {
		value.CreatedAt = time.Now().UnixMilli()
	}
	if value.AgentID == "" || value.EventID == "" || value.Kind == "" || value.State != "pending" {
		return model.AgentPiLocalEvent{}, false, fmt.Errorf("Pi-local event has invalid initial fields")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AgentPiLocalEvent{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if value.OperationID != "" {
		operation, err := fenceOperationMutation(ctx, tx, value.OperationID, value.AgentID, value.RuntimeID, value.OperationAttempt)
		if err != nil {
			return model.AgentPiLocalEvent{}, false, err
		}
		value.ProtocolGeneration = operation.ProtocolGeneration
	} else {
		value.ProtocolGeneration, err = requireObjectGeneration(ctx, tx, value.ProtocolGeneration)
		if err != nil {
			return model.AgentPiLocalEvent{}, false, err
		}
	}
	result, err := tx.ExecContext(ctx, `insert into agent_pi_local_events(id,agent_id,operation_id,operation_attempt,event_id,kind,state,payload,created_at,acknowledged_at,protocol_generation) values(?,?,?,?,?,?,'pending',?,?,0,?) on conflict(agent_id,event_id) do nothing`, value.ID, value.AgentID, nullString(value.OperationID), value.OperationAttempt, value.EventID, value.Kind, value.Payload, value.CreatedAt, value.ProtocolGeneration)
	if err != nil {
		return model.AgentPiLocalEvent{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return model.AgentPiLocalEvent{}, false, err
	}
	if count == 1 {
		return value, true, tx.Commit()
	}
	var existing model.AgentPiLocalEvent
	err = tx.QueryRowContext(ctx, `select id,agent_id,coalesce(operation_id,''),operation_attempt,event_id,kind,state,payload,created_at,acknowledged_at,protocol_generation from agent_pi_local_events where agent_id=? and event_id=?`, value.AgentID, value.EventID).Scan(&existing.ID, &existing.AgentID, &existing.OperationID, &existing.OperationAttempt, &existing.EventID, &existing.Kind, &existing.State, &existing.Payload, &existing.CreatedAt, &existing.AcknowledgedAt, &existing.ProtocolGeneration)
	if err != nil {
		return model.AgentPiLocalEvent{}, false, err
	}
	if existing.OperationID != value.OperationID || existing.OperationAttempt != value.OperationAttempt || existing.Kind != value.Kind || existing.Payload != value.Payload {
		return model.AgentPiLocalEvent{}, false, fmt.Errorf("Pi-local event ID was already used for different work")
	}
	return existing, false, tx.Commit()
}

func (s *Store) AcknowledgeAgentPiLocalEvent(ctx context.Context, id, agentID, operationID, runtimeID string, operationAttempt int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var generation int
	if err := tx.QueryRowContext(ctx, `select protocol_generation from agent_pi_local_events where id=? and agent_id=? and coalesce(operation_id,'')=? and operation_attempt=?`, id, agentID, operationID, operationAttempt).Scan(&generation); err != nil {
		return err
	}
	if operationID != "" {
		if _, err := fenceOperationMutation(ctx, tx, operationID, agentID, runtimeID, operationAttempt); err != nil {
			return err
		}
	} else if err := requireRuntimeGeneration(ctx, tx, agentID, runtimeID, generation); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `update agent_pi_local_events set state='acknowledged',acknowledged_at=? where id=? and agent_id=? and coalesce(operation_id,'')=? and operation_attempt=? and state='pending'`, now, id, agentID, operationID, operationAttempt)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		var state string
		if err := tx.QueryRowContext(ctx, `select state from agent_pi_local_events where id=? and agent_id=? and coalesce(operation_id,'')=? and operation_attempt=?`, id, agentID, operationID, operationAttempt).Scan(&state); err == nil && state == "acknowledged" {
			return nil
		}
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// SweepCoordinationDeadlines gives every expired join a visible blocker path
// and makes its waiting operation ready.
func (s *Store) SweepCoordinationDeadlines(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	// Resolve child operation deadlines before matching join deadlines. The
	// immutable child result then creates the sender's single durable duty.
	operationRows, err := tx.QueryContext(ctx, `select `+operationColumns+` from agent_operations where state not in ('settled','failed','canceled','expired') and deadline_at>0 and deadline_at<=? order by created_at,id`, now)
	if err != nil {
		return err
	}
	var operations []model.AgentOperation
	for operationRows.Next() {
		value, err := scanOperation(operationRows)
		if err != nil {
			_ = operationRows.Close()
			return err
		}
		operations = append(operations, value)
	}
	if err := operationRows.Close(); err != nil {
		return err
	}
	for _, operation := range operations {
		if err := expireAgentOperation(ctx, tx, operation, now); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `select dependency.id,dependency.operation_id,dependency.message_id,operation.agent_id,dependency.protocol_generation from agent_operation_joins dependency join agent_operations operation on operation.id=dependency.operation_id where dependency.state='open' and dependency.deadline_at>0 and dependency.deadline_at<=?`, now)
	if err != nil {
		return err
	}
	type expiredJoin struct {
		id, operationID, messageID, agentID string
		generation                          int
	}
	var joins []expiredJoin
	for rows.Next() {
		var value expiredJoin
		if err := rows.Scan(&value.id, &value.operationID, &value.messageID, &value.agentID, &value.generation); err != nil {
			_ = rows.Close()
			return err
		}
		joins = append(joins, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, join := range joins {
		if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state='expired',failure='join deadline expired',resolved_at=?,updated_at=? where id=? and state='open'`, now, now, join.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into agent_inbox_receipts(id,agent_id,operation_id,message_id,kind,state,eligible,created_at,updated_at,protocol_generation) values(?,?,?,?, 'blocker','pending',1,?,?,?)`, "join-expired:"+join.id, join.agentID, join.operationID, join.messageID, now, now, join.generation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_operations set state='ready',updated_at=? where id=? and state='waiting'`, now, join.operationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func expireAgentOperation(ctx context.Context, tx *sql.Tx, operation model.AgentOperation, now int64) error {
	if _, err := tx.ExecContext(ctx, `update agent_operation_joins set state='expired',failure='parent operation deadline expired',resolved_at=?,updated_at=? where operation_id=? and state in ('open','ready')`, now, now, operation.ID); err != nil {
		return err
	}
	if operation.ParentMessageID != "" {
		result := model.AgentMessageResult{ID: "result:" + operation.ParentMessageID, MessageID: operation.ParentMessageID, Status: "expired", Error: "operation deadline expired", TerminalReason: "expired", CreatedAt: now, ProtocolGeneration: operation.ProtocolGeneration}
		if err := insertAgentMessageResult(ctx, tx, result); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update agent_messages set status='failed',error=?,last_error=?,terminal_reason='expired',notification_state=case when act='inform' then 'suppressed' else 'pending' end,runtime_id='',lease_expires_at=0,completed_at=?,updated_at=? where id=? and status not in ('completed','failed')`, result.Error, result.Error, now, now, operation.ParentMessageID); err != nil {
			return err
		}
		if err := resolveMessageJoins(ctx, tx, result, now); err != nil {
			return err
		}
		if err := createNotifyResultReceipt(ctx, tx, result, now); err != nil {
			return err
		}
		if err := createTodoSettlementEvents(ctx, tx, result, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='failed',terminal_reason='expired',finished_at=?,updated_at=? where operation_id=? and state in ('claimed','running')`, now, now, operation.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='abandoned',lease_expires_at=0,abandoned_at=?,updated_at=? where operation_id=? and state in ('pending','claimed','presented')`, now, now, operation.ID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `update agent_operations set state='expired',runtime_id='',claim_key='',lease_expires_at=0,terminal_reason='expired',last_error='operation deadline expired',settled_at=?,updated_at=? where id=? and state not in ('settled','failed','canceled','expired')`, now, now, operation.ID)
	return err
}

func recoverExpiredCoordinationLeases(ctx context.Context, tx *sql.Tx, now int64) error {
	expiredOperation := `operation.lease_expires_at>0 and operation.lease_expires_at<=? or exists(select 1 from agent_inbox_receipts receipt where receipt.operation_id=operation.id and receipt.state in ('claimed','presented') and receipt.lease_expires_at>0 and receipt.lease_expires_at<=?) or exists(select 1 from todo_link_intents intent where intent.operation_id=operation.id and intent.state='pending' and intent.runtime_id<>'' and intent.lease_expires_at>0 and intent.lease_expires_at<=?) or exists(select 1 from todo_settlement_events event where event.operation_id=operation.id and event.state in ('pending','applied') and event.acknowledged_at=0 and event.runtime_id<>'' and (event.lease_expires_at=0 or event.lease_expires_at<=?))`
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='recovered',terminal_reason='lease_expired',finished_at=?,updated_at=? where state in ('claimed','running') and exists(select 1 from agent_operations operation where operation.id=agent_operation_attempts.operation_id and operation.attempt=agent_operation_attempts.attempt and (`+expiredOperation+`))`, now, now, now, now, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_operations as operation set state='ready',runtime_id='',claim_key='',lease_expires_at=0,last_error='coordination lease expired',updated_at=? where operation.state in ('claimed','running') and (`+expiredOperation+`)`, now, now, now, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='pending',runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,pi_tool_request_id='',updated_at=? where state in ('claimed','presented') and (lease_expires_at>0 and lease_expires_at<=? or operation_id in (select id from agent_operations where state='ready' and last_error='coordination lease expired' and updated_at=?))`, now, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update todo_link_intents set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='TODO link lease expired' where state='pending' and runtime_id<>'' and lease_expires_at>0 and lease_expires_at<=?`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update todo_settlement_events set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='TODO settlement lease expired' where state in ('pending','applied') and acknowledged_at=0 and runtime_id<>'' and (lease_expires_at=0 or lease_expires_at<=?)`, now); err != nil {
		return err
	}
	return nil
}

// RecoverCoordinationState releases all process-owned protocol state. Waiting
// operations keep no lease. Presented receipts return to pending for replay.
func (s *Store) RecoverAgentCoordinationState(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update agent_operation_attempts set state='recovered',terminal_reason='daemon_restart',finished_at=?,updated_at=? where state in ('claimed','running')`, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_operations set state='ready',runtime_id='',claim_key='',lease_expires_at=0,last_error='daemon restarted before operation settled',updated_at=? where state in ('claimed','running','settling')`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update agent_inbox_receipts set state='pending',runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,updated_at=? where state in ('claimed','presented')`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update todo_link_intents set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='daemon restarted before TODO link settled' where state='pending' and runtime_id<>''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update todo_settlement_events set runtime_id='',claim_key='',lease_expires_at=0,operation_attempt=0,last_error='daemon restarted before TODO settlement completed' where state in ('pending','applied') and acknowledged_at=0 and runtime_id<>''`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from agent_runtime_protocol_generations`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CoordinationReadyAgentIDs(ctx context.Context) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := recoverExpiredCoordinationLeases(ctx, tx, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `select distinct agent_id from (
select agent_id from agent_operations where state='ready'
union all
select agent_id from agent_inbox_receipts where state='pending' and eligible=1 and operation_id is null
union all
select message.sender_agent_id from todo_link_intents intent join agent_messages message on message.id=intent.message_id where intent.state='pending' and intent.runtime_id='' and message.sender_agent_id<>''
union all
select agent_id from todo_settlement_events where state in ('pending','applied') and acknowledged_at=0 and runtime_id=''
) order by agent_id`)
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
